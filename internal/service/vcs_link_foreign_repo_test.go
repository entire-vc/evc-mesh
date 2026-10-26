package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These tests pin the fix for the 25.10 incident: an MR of a fleet-tooling
// repository quoted a product card's id in its description; when it merged,
// the webhook moved that card from review to done although a person still had
// to accept it. A mention from a repository the card's project does not use
// is now a reference, and a card a person must close is never auto-closed.

// projectRepoLinks lets the fake link repository answer "which repositories
// does this project use", the way the Postgres repository does.
type projectRepoLinks struct {
	*fakeVCSLinkRepo
	tasks *fakeTaskRepo
}

func (p projectRepoLinks) ProjectRepoUsage(_ context.Context, projectID uuid.UUID, repo string) (uses, hasAny bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		t := p.tasks.tasks[l.TaskID]
		if t == nil || t.ProjectID != projectID || l.LinkType != domain.VCSLinkTypePR || l.IsReviewSubject() {
			continue
		}
		if r := RepoFromVCSURL(l.URL); r != "" {
			hasAny = true
			uses = uses || r == repo
		}
	}
	return uses, hasAny, nil
}

type foreignRepoHarness struct {
	*harness
	links projectRepoLinks
}

const (
	nativeRepoHost  = "https://git.example.com/entire-vc/evc-product"
	foreignRepoHost = "https://git.example.com/entire-vc/fleet-tool"
)

func newForeignRepoHarness(t *testing.T) *foreignRepoHarness {
	t.Helper()
	h := newHarness(t)
	links := projectRepoLinks{fakeVCSLinkRepo: h.repo, tasks: h.taskRepo}
	h.svc = NewVCSLinkService(links,
		WithVCSTaskRepo(h.taskRepo),
		WithVCSStatusRepo(h.statusRepo),
		WithVCSTaskService(h.taskSvc),
		WithVCSCommentService(h.commentSvc),
	)
	return &foreignRepoHarness{harness: h, links: links}
}

// seedNativeMR makes the project "use" the native repository: one other card
// of the project carries a PR link into it.
func (h *foreignRepoHarness) seedNativeMR(t *testing.T) {
	t.Helper()
	other := h.makeTask(t, domain.StatusCategoryDone)
	_, err := h.repo.Upsert(context.Background(), &domain.VCSLink{
		ID: uuid.New(), TaskID: other.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "1", URL: nativeRepoHost + "/-/merge_requests/1", Status: domain.VCSLinkStatusMerged,
		Metadata: []byte("{}"), CreatedAt: time.Now(),
	})
	require.NoError(t, err)
}

// mrEvent names the card in the description only. The full MESH- form keeps
// the reference resolvable for every random id: a short id without an a–f
// letter reads as an issue number and resolves to nothing.
func mrEvent(repoHost string, iid int, state string, task *domain.Task) GitLabWebhookEvent {
	path := repoHost[len("https://git.example.com/"):]
	return GitLabWebhookEvent{
		Action:      "update",
		MRIID:       iid,
		MRTitle:     "tooling change",
		MRBody:      "Case: see MESH-" + task.ID.String() + " for background",
		MRURL:       repoHost + "/-/merge_requests/" + uintToString(iid),
		MRState:     state,
		MergeSHA:    "57bf7d8aaaaaaaa",
		ProjectPath: path,
	}
}

// ownMREvent is mrEvent for the card's own MR: the reference sits in the title,
// which binds the card when the repository is one its project uses.
func ownMREvent(repoHost string, iid int, state string, task *domain.Task) GitLabWebhookEvent {
	ev := mrEvent(repoHost, iid, state, task)
	ev.MRTitle = "tooling change (MESH-" + task.ID.String() + ")"
	return ev
}

func (h *foreignRepoHarness) status(task *domain.Task) domain.StatusCategory {
	st, _ := h.statusRepo.GetByID(context.Background(), task.StatusID)
	if st == nil {
		return ""
	}
	return st.Category
}

func (h *foreignRepoHarness) lastComment() string {
	if len(h.commentSvc.created) == 0 {
		return ""
	}
	return h.commentSvc.created[len(h.commentSvc.created)-1].Body
}

func TestForeignRepoMention_MergeDoesNotMoveReviewCard(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryReview)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mrEvent(foreignRepoHost, 73, "merged", card))
	require.NoError(t, err)

	assert.Equal(t, "foreign_repo_reference", res.Reason)
	assert.False(t, res.Transitioned)
	assert.Equal(t, domain.StatusCategoryReview, h.status(card), "a mention from a foreign repo must not close the card")
	assert.Empty(t, h.taskSvc.moveCalls)

	links := h.linksFor(card.ID)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkTypeReference, links[0].LinkType, "the link is kept, as a reference")
	assert.Contains(t, string(links[0].Metadata), `"reference_only":true`)
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)

	assert.Contains(t, h.lastComment(), "NOT moved")
	assert.Contains(t, h.lastComment(), "entire-vc/fleet-tool")
}

func TestForeignRepoMention_NativeRepoStillClosesReviewCard(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryReview)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ownMREvent(nativeRepoHost, 74, "merged", card))
	require.NoError(t, err)

	assert.True(t, res.Transitioned, "positive control: a native MR keeps its auto-done")
	assert.Equal(t, domain.StatusCategoryDone, h.status(card))
	assert.Equal(t, domain.VCSLinkTypePR, h.linksFor(card.ID)[0].LinkType)
}

// The same group/project on another host is another repository: a title
// reference from it is a reference, not the card's own MR.
func TestForeignRepoMention_SamePathOtherHostIsForeign(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryReview)

	ev := ownMREvent(nativeRepoHost, 75, "merged", card)
	ev.MRURL = "https://git.other.example/entire-vc/evc-product/-/merge_requests/75"

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, "foreign_repo_reference", res.Reason)
	assert.Equal(t, domain.StatusCategoryReview, h.status(card))
	assert.Equal(t, domain.VCSLinkTypeReference, h.linksFor(card.ID)[0].LinkType)
}

func TestForeignRepoMention_ForeignOpenThenMergeStaysReference(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryReview)

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mrEvent(foreignRepoHost, 75, "opened", card))
	require.NoError(t, err)
	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mrEvent(foreignRepoHost, 75, "merged", card))
	require.NoError(t, err)

	assert.Equal(t, "foreign_repo_reference", res.Reason)
	assert.Equal(t, domain.StatusCategoryReview, h.status(card))
	assert.Len(t, h.linksFor(card.ID), 1, "the second delivery updates the reference link, it does not add a PR link")
}

func TestForeignRepoMention_ForeignOpenMRDoesNotMoveInProgressCard(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryInProgress)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mrEvent(foreignRepoHost, 76, "merged", card))
	require.NoError(t, err)

	assert.Equal(t, "foreign_repo_reference", res.Reason)
	assert.Equal(t, domain.StatusCategoryInProgress, h.status(card), "a foreign merge must not push the card into review either")
}

func TestForeignRepoMention_ExplicitPRLinkIsTrusted(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryReview)
	// An agent attached this MR to the card on purpose (add_vcs_link).
	_, err := h.repo.Upsert(context.Background(), &domain.VCSLink{
		ID: uuid.New(), TaskID: card.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "77", URL: foreignRepoHost + "/-/merge_requests/77", Status: domain.VCSLinkStatusOpen,
		Metadata: []byte("{}"), CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mrEvent(foreignRepoHost, 77, "merged", card))
	require.NoError(t, err)

	assert.True(t, res.Transitioned, "a card that deliberately owns the MR link keeps its auto-done")
	assert.Equal(t, domain.StatusCategoryDone, h.status(card))
}

func TestForeignRepoMention_ProjectWithoutAnyRepoBodyMentionStays(t *testing.T) {
	h := newForeignRepoHarness(t)                  // no seedNativeMR: nothing to compare against
	h.repo.links = map[uuid.UUID]*domain.VCSLink{} // nor the repositories every harness seeds
	card := h.makeTask(t, domain.StatusCategoryReview)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mrEvent(foreignRepoHost, 78, "merged", card))
	require.NoError(t, err)

	// The repository rule cannot judge without a known project repository, but
	// a description-only mention never binds: the card stays, the link is a
	// relates_to mention rather than a reference.
	assert.Equal(t, "mentioned_only", res.Reason)
	assert.False(t, res.Transitioned)
	assert.Equal(t, domain.StatusCategoryReview, h.status(card))
}

func TestForeignRepoMention_GitHubPullRequest(t *testing.T) {
	h := newForeignRepoHarness(t)
	other := h.makeTask(t, domain.StatusCategoryDone)
	_, err := h.repo.Upsert(context.Background(), &domain.VCSLink{
		ID: uuid.New(), TaskID: other.ID, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
		ExternalID: "5", URL: "https://github.com/entire-vc/evc-product/pull/5", Status: domain.VCSLinkStatusMerged,
		Metadata: []byte("{}"), CreatedAt: time.Now(),
	})
	require.NoError(t, err)
	card := h.makeTask(t, domain.StatusCategoryReview)

	ev := mergedClosedEvent(9, card.ID, "tooling")
	ev.PRBody = "Case: see MESH-" + card.ID.String()
	ev.PRHTMLURL = "https://github.com/entire-vc/fleet-tool/pull/9"
	ev.Repository = "entire-vc/fleet-tool"
	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)

	assert.Equal(t, "foreign_repo_reference", res.Reason)
	assert.Equal(t, domain.StatusCategoryReview, h.status(card))
}

func TestAutoDone_HumanGateKeepsCardInReview(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryReview)
	card.HumanGate = true

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ownMREvent(nativeRepoHost, 80, "merged", card))
	require.NoError(t, err)

	assert.Equal(t, "human_gate_active", res.Reason)
	assert.False(t, res.Transitioned)
	assert.Equal(t, domain.StatusCategoryReview, h.status(card))
	assert.Contains(t, h.lastComment(), "NOT moved to done")
	assert.Contains(t, h.lastComment(), "human gate")
}

func TestAutoDone_CompletionSignalKeepsCardInReview(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryReview)
	card.CompletionSignal = true

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ownMREvent(nativeRepoHost, 81, "merged", card))
	require.NoError(t, err)

	assert.Equal(t, "completion_signal_pending", res.Reason)
	assert.Equal(t, domain.StatusCategoryReview, h.status(card))
	assert.Contains(t, h.lastComment(), "NOT moved to done")
}

func TestAutoDone_HumanGateDoesNotBlockInProgressToReview(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t)
	card := h.makeTask(t, domain.StatusCategoryInProgress)
	card.HumanGate = true

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ownMREvent(nativeRepoHost, 82, "merged", card))
	require.NoError(t, err)

	assert.True(t, res.Transitioned, "only the move to done is guarded; in_progress -> review is unchanged")
	assert.Equal(t, domain.StatusCategoryReview, h.status(card))
}

// A repository the project still uses must stay native however many newer links
// another repository has: the lookup is bounded by repositories, not by links.
func TestForeignRepoMention_OldNativeRepoSurvivesManyNewerLinksOfAnotherRepo(t *testing.T) {
	h := newForeignRepoHarness(t)
	h.seedNativeMR(t) // the only link into the native repository
	busy := h.makeTask(t, domain.StatusCategoryDone)
	for i := 1; i <= 600; i++ {
		_, err := h.repo.Upsert(context.Background(), &domain.VCSLink{
			ID: uuid.New(), TaskID: busy.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
			ExternalID: uintToString(i), URL: "https://git.example.com/entire-vc/busy-repo/-/merge_requests/" + uintToString(i),
			Status: domain.VCSLinkStatusMerged, Metadata: []byte("{}"), CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		})
		require.NoError(t, err)
	}
	card := h.makeTask(t, domain.StatusCategoryReview)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ownMREvent(nativeRepoHost, 90, "merged", card))
	require.NoError(t, err)

	assert.True(t, res.Transitioned, "an old native repository is still the project's")
	assert.Equal(t, domain.StatusCategoryDone, h.status(card))
}
