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

const (
	sameIIDURLA = "https://gitlab.example.invalid/group/a/-/merge_requests/88"
	sameIIDURLB = "https://gitlab.example.invalid/group/b/-/merge_requests/88"
)

func seedIIDLink(h *harness, taskID uuid.UUID, url string) uuid.UUID {
	l := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "88", URL: url,
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	_, _ = h.repo.Upsert(context.Background(), l)
	return l.ID
}

// Two projects' !88 are different merge requests. A delivery that names no
// task must fall back to the link of ITS project, not any link numbered 88.
// The fake repo hands the candidates back in map order, so the loop makes the
// red run (no url filter) fail with overwhelming probability.
func TestHandleMR_FallbackLookupIsScopedToProject(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := newHarness(t)
		taskA := h.makeTask(t, domain.StatusCategoryInProgress)
		taskB := h.makeTask(t, domain.StatusCategoryInProgress)
		linkA := seedIIDLink(h, taskA.ID, sameIIDURLA)
		linkB := seedIIDLink(h, taskB.ID, sameIIDURLB)

		res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), GitLabWebhookEvent{
			Action: "merge", MRIID: 88, MRTitle: "no ref here", MRURL: sameIIDURLB,
			MRState: "merged", MergeSHA: "deadbeefcafe", ProjectPath: "group/b",
		})
		require.NoError(t, err)
		assert.Equal(t, taskB.ID, res.TaskID, "!88 of group/b must resolve to the task linked to group/b!88")
		gotB, _ := h.repo.GetByID(context.Background(), linkB)
		require.NotNil(t, gotB)
		assert.Equal(t, domain.VCSLinkStatusMerged, gotB.Status, "b!88 delivery updates b's link")
		gotA, _ := h.repo.GetByID(context.Background(), linkA)
		require.NotNil(t, gotA)
		assert.Equal(t, domain.VCSLinkStatusOpen, gotA.Status, "a!88 is a different MR and stays open")
	}
}

func TestFilterLinksByURL(t *testing.T) {
	links := []domain.VCSLink{{URL: sameIIDURLA}, {URL: sameIIDURLB}}
	got := filterLinksByURL(links, sameIIDURLB+"/")
	require.Len(t, got, 1)
	assert.Equal(t, sameIIDURLB, got[0].URL)
	assert.Len(t, filterLinksByURL(links, ""), 2, "no url in the payload filters nothing")
}

// The card's acceptance scenario: ONE task holds group/a!88 and group/b!88; a
// webhook for b!88 updates b's status, a's is untouched, and nothing errors.
func TestHandleMR_SameTaskTwoProjectsSameIID_UpdatesOnlyOwnLink(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	linkA := seedIIDLink(h, task.ID, sameIIDURLA)
	linkB := seedIIDLink(h, task.ID, sameIIDURLB)
	require.NotEqual(t, linkA, linkB, "both links must coexist on the one task")

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), GitLabWebhookEvent{
		Action: "merge", MRIID: 88, MRTitle: "MESH-" + task.ID.String(), MRURL: sameIIDURLB,
		MRState: "merged", MergeSHA: "deadbeefcafe", ProjectPath: "group/b",
	})
	require.NoError(t, err)

	gotA, _ := h.repo.GetByID(context.Background(), linkA)
	gotB, _ := h.repo.GetByID(context.Background(), linkB)
	require.NotNil(t, gotA)
	require.NotNil(t, gotB)
	assert.Equal(t, domain.VCSLinkStatusMerged, gotB.Status)
	assert.Equal(t, domain.VCSLinkStatusOpen, gotA.Status)
	assert.Len(t, h.linksFor(task.ID), 2)
}

// ---------------------------------------------------------------------------
// Ambiguous task ref on a merge event: refresh the status of a link that
// already names this exact MR/PR, nothing more.
//
// Ten of eighteen stale links came from MR bodies that quoted a second task id:
// the payload was ambiguous, the handler (rightly) refused to pick a task, and
// the refusal also threw away the one fact that was never in doubt — the MR
// merged. The guard against the earlier change (no fallback to a stored link, no new link)
// must survive, so every assertion below is about what is NOT written as much
// as what is.
// ---------------------------------------------------------------------------

// ambiguousMRBody names both tasks and the title names none, which is exactly
// the shape resolveTaskRef answers with refAmbiguous.
func ambiguousMRBody(a, b *domain.Task) string {
	return "Implements MESH-" + a.ID.String() + "\nFollow-up of MESH-" + b.ID.String()
}

func ambiguousMREvent(url, state string, a, b *domain.Task) GitLabWebhookEvent {
	return GitLabWebhookEvent{
		Action: "merge", MRIID: 88, MRTitle: "fix: something without a card id",
		MRBody: ambiguousMRBody(a, b), MRURL: url, MRState: state,
		MergeSHA: "deadbeefcafe", HeadSHA: "0123456789abcdef", ProjectPath: "group/a",
	}
}

func TestHandleMR_AmbiguousRef_RefreshesOnlyTheExistingLinkAndMovesNothing(t *testing.T) {
	h := newHarness(t)
	linked := h.makeTask(t, domain.StatusCategoryInProgress)
	other := h.makeTask(t, domain.StatusCategoryInProgress)
	unmentioned := h.makeTask(t, domain.StatusCategoryInProgress)
	linkID := seedIIDLink(h, linked.ID, sameIIDURLA)
	sameIIDOtherProject := seedIIDLink(h, linked.ID, sameIIDURLB)
	unmentionedLink := seedIIDLink(h, unmentioned.ID, sameIIDURLA)
	before := h.linkCount()

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(),
		ambiguousMREvent(sameIIDURLA, "merged", linked, other))
	require.NoError(t, err)

	assert.Equal(t, "ambiguous_task_ref", res.Reason)
	assert.Equal(t, uuid.Nil, res.TaskID, "the delivery still chooses no task (the earlier change guard)")
	assert.False(t, res.Transitioned)
	assert.Equal(t, 1, res.LinksSynced)

	got, _ := h.repo.GetByID(context.Background(), linkID)
	require.NotNil(t, got)
	assert.Equal(t, domain.VCSLinkStatusMerged, got.Status, "the existing link learns the MR merged")
	assert.Equal(t, sameIIDURLA, got.URL)
	assert.Contains(t, string(got.Metadata), "0123456789abcdef", "head_sha is refreshed with the status")

	gotB, _ := h.repo.GetByID(context.Background(), sameIIDOtherProject)
	assert.Equal(t, domain.VCSLinkStatusOpen, gotB.Status, "group/b!88 is another MR")
	gotU, _ := h.repo.GetByID(context.Background(), unmentionedLink)
	assert.Equal(t, domain.VCSLinkStatusOpen, gotU.Status, "a task the payload did not mention is not touched")

	assert.Equal(t, before, h.linkCount(), "no link may be created")
	assert.Empty(t, h.linksFor(other.ID), "the other mentioned task gets no link")
	assert.Empty(t, h.taskSvc.moveCalls, "no task transition from the ambiguous path")
	assert.Empty(t, h.commentSvc.created, "no auto-comment from the ambiguous path")
	assert.Equal(t, h.statusIDs[domain.StatusCategoryInProgress], h.taskRepo.tasks[linked.ID].StatusID)
}

func TestHandleMR_AmbiguousRef_ClosedWithoutMergeIsRecordedToo(t *testing.T) {
	h := newHarness(t)
	linked := h.makeTask(t, domain.StatusCategoryInProgress)
	other := h.makeTask(t, domain.StatusCategoryInProgress)
	linkID := seedIIDLink(h, linked.ID, sameIIDURLA)

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(),
		ambiguousMREvent(sameIIDURLA, "closed", linked, other))
	require.NoError(t, err)

	got, _ := h.repo.GetByID(context.Background(), linkID)
	assert.Equal(t, domain.VCSLinkStatusClosed, got.Status)
	assert.Empty(t, h.commentSvc.created, "the closed-without-merge comment belongs to the resolved path only")
}

func TestHandleMR_AmbiguousRef_NoExistingLink_WritesNothing(t *testing.T) {
	h := newHarness(t)
	a := h.makeTask(t, domain.StatusCategoryInProgress)
	b := h.makeTask(t, domain.StatusCategoryInProgress)
	// A same-numbered MR of another project, on a mentioned task, must not be adopted.
	otherProject := seedIIDLink(h, a.ID, sameIIDURLB)
	before := h.linkCount()

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(),
		ambiguousMREvent(sameIIDURLA, "merged", a, b))
	require.NoError(t, err)

	assert.Equal(t, "ambiguous_task_ref", res.Reason)
	assert.Equal(t, 0, res.LinksSynced)
	assert.Equal(t, before, h.linkCount(), "no link may be created")
	got, _ := h.repo.GetByID(context.Background(), otherProject)
	assert.Equal(t, domain.VCSLinkStatusOpen, got.Status)
	assert.Empty(t, h.taskSvc.moveCalls)
}

func TestHandleMR_AmbiguousRef_NoURLInPayload_WritesNothing(t *testing.T) {
	h := newHarness(t)
	a := h.makeTask(t, domain.StatusCategoryInProgress)
	b := h.makeTask(t, domain.StatusCategoryInProgress)
	linkID := seedIIDLink(h, a.ID, sameIIDURLA)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(),
		ambiguousMREvent("", "merged", a, b))
	require.NoError(t, err)

	assert.Equal(t, 0, res.LinksSynced, "an iid alone cannot tell two projects' MRs apart")
	got, _ := h.repo.GetByID(context.Background(), linkID)
	assert.Equal(t, domain.VCSLinkStatusOpen, got.Status)
}

func TestHandlePR_AmbiguousRef_RefreshesExistingLinkStatusOnly(t *testing.T) {
	h := newHarness(t)
	linked := h.makeTask(t, domain.StatusCategoryInProgress)
	other := h.makeTask(t, domain.StatusCategoryInProgress)
	const prURL = "https://github.com/entire-vc/evc-mesh/pull/433"
	l := &domain.VCSLink{
		ID: uuid.New(), TaskID: linked.ID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "433", URL: prURL,
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	_, err := h.repo.Upsert(context.Background(), l)
	require.NoError(t, err)
	before := h.linkCount()

	ev := mergedClosedEvent(433, linked.ID, "no card id here")
	ev.PRBody = ambiguousMRBody(linked, other)
	ev.PRHTMLURL = prURL
	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)

	assert.Equal(t, "ambiguous_task_ref", res.Reason)
	assert.Equal(t, uuid.Nil, res.TaskID)
	assert.Equal(t, 1, res.LinksSynced)
	got, _ := h.repo.GetByID(context.Background(), l.ID)
	assert.Equal(t, domain.VCSLinkStatusMerged, got.Status)
	assert.Equal(t, before, h.linkCount())
	assert.Empty(t, h.taskSvc.moveCalls)
	assert.Empty(t, h.commentSvc.created)
}

func TestHandlePR_AmbiguousRef_NoExistingLink_WritesNothing(t *testing.T) {
	h := newHarness(t)
	a := h.makeTask(t, domain.StatusCategoryInProgress)
	b := h.makeTask(t, domain.StatusCategoryInProgress)

	ev := mergedClosedEvent(434, a.ID, "no card id here")
	ev.PRBody = ambiguousMRBody(a, b)
	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)

	assert.Equal(t, "ambiguous_task_ref", res.Reason)
	assert.Equal(t, 0, res.LinksSynced)
	assert.Equal(t, 0, h.linkCount())
}
