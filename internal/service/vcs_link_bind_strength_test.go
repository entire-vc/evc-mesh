package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// an internal ticket: merging an MR of an unrelated repository moved another project's
// review card to done because the MR description quoted its id. A merge moves
// only the task the MR belongs to — an existing link to it, or a title/branch
// reference from a repository the task's project works in. Everything else is
// a relates_to mention, and a card a person still has to accept is never
// closed by a merge.

const foreignMRURL = "https://git.example.com/ops/runner/-/merge_requests/178"

func lastComment(h *harness) string {
	if len(h.commentSvc.created) == 0 {
		return ""
	}
	return h.commentSvc.created[len(h.commentSvc.created)-1].Body
}

// The incident: the body of another repository's MR names a review card.
func TestHandleMR_BodyMentionOfOtherProjectCard_NoTransition(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(801), domain.StatusCategoryReview)

	ev := mergedMREvent(178, uuid.Nil, "runner: resume sessions on wake")
	ev.MRBody = "Follows up on #" + card.ID.String()[:8] + " (E3)."
	ev.MRURL, ev.ProjectPath = foreignMRURL, "ops/runner"

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, card.ID, res.TaskID)
	assert.False(t, res.Transitioned)
	assert.Equal(t, "mentioned_only", res.Reason)
	assert.Empty(t, h.taskSvc.moveCalls, "a mention must never move the card")
	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[card.ID].StatusID)
	assert.Equal(t, "🤖 Auto: MR !"+"178"+" merged — mentioned only, no status change.", lastComment(h))

	links := h.linksFor(card.ID)
	require.Len(t, links, 1)
	assert.True(t, links[0].IsRelatesTo())
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
}

// An opened delivery of a body mention stores relates_to, and the later merge
// does not read that row as an existing binding.
func TestHandleMR_BodyMention_OpenedThenMerged_StaysMention(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(802), domain.StatusCategoryInProgress)

	ev := mergedMREvent(179, uuid.Nil, "chore: tidy")
	ev.MRBody = "see #" + card.ID.String()[:8]
	ev.MRState, ev.Action = "opened", "open"
	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)

	ev.MRState, ev.Action = "merged", "merge"
	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, "mentioned_only", res.Reason)
	assert.Empty(t, h.taskSvc.moveCalls)
}

// A title reference is still only a mention when the repository is not one the
// card's project works in.
func TestHandleMR_TitleRefFromForeignRepo_NoTransition(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(803), domain.StatusCategoryReview)

	ev := mergedMREvent(180, card.ID, "fix: thing (#"+card.ID.String()[:8]+")")
	ev.MRURL, ev.ProjectPath = foreignMRURL, "ops/runner"

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, "mentioned_only", res.Reason)
	assert.Empty(t, h.taskSvc.moveCalls)
	assert.True(t, h.linksFor(card.ID)[0].IsRelatesTo())
}

// Title reference from the project's own repository: review → done as before.
func TestHandleMR_TitleRefOwnProject_ReviewToDone(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(804), domain.StatusCategoryReview)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(),
		mergedMREvent(181, card.ID, "feat: x #"+card.ID.String()[:8]))
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, "done", res.NewStatus)
	assert.False(t, h.linksFor(card.ID)[0].IsRelatesTo())
}

// Branch counts like the title.
func TestHandleMR_BranchRefOwnProject_Transitions(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(805), domain.StatusCategoryInProgress)

	ev := mergedMREvent(182, card.ID, "feat: x")
	ev.MRBranch = "liam/" + card.ID.String()[:8]
	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, "review", res.NewStatus)
}

// A link set before the event (add_vcs_link) binds the MR whatever the payload
// says and wherever the repository lives.
func TestHandleMR_ExistingLink_BodyOnlyRef_Transitions(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(806), domain.StatusCategoryReview)
	_, _, err := h.svcCreateLink(card.ID, foreignMRURL, `{}`)
	require.NoError(t, err)

	ev := mergedMREvent(178, uuid.Nil, "runner: resume")
	ev.MRBody = "Refs #" + card.ID.String()[:8]
	ev.MRURL, ev.ProjectPath = foreignMRURL, "ops/runner"

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, "done", res.NewStatus)
}

// A mention upgraded by a later binding delivery loses the relates_to mark.
func TestHandleMR_MentionThenTitleRef_UpgradesLink(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(807), domain.StatusCategoryInProgress)

	ev := mergedMREvent(183, uuid.Nil, "wip")
	ev.MRBody = "#" + card.ID.String()[:8]
	ev.MRState, ev.Action = "opened", "open"
	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	require.True(t, h.linksFor(card.ID)[0].IsRelatesTo())

	ev.MRTitle, ev.MRState, ev.Action = "feat: done #"+card.ID.String()[:8], "merged", "merge"
	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	links := h.linksFor(card.ID)
	require.Len(t, links, 1)
	assert.False(t, links[0].IsRelatesTo())
}

// A relates_to link elsewhere on the task does not hold the transition back.
func TestHandleMR_OpenMentionLink_NotPending(t *testing.T) {
	h := newHarness(t)
	card := h.makeTaskWithID(t, fixedTaskID(808), domain.StatusCategoryInProgress)
	_, _, err := h.svcCreateLink(card.ID, foreignMRURL, `{"relation":"relates_to"}`)
	require.NoError(t, err)

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(),
		mergedMREvent(184, card.ID, "MESH-"+card.ID.String()))
	require.NoError(t, err)
	assert.True(t, res.Transitioned, "an open mention must not count as a pending PR")
}

// A card in review that a person still has to accept stays in review.
func TestHandleMR_ReviewAwaitingHuman_NoDone(t *testing.T) {
	for reason, arm := range map[string]func(*domain.Task){
		"human_gate_active":         func(tk *domain.Task) { tk.HumanGate = true },
		"completion_signal_pending": func(tk *domain.Task) { tk.CompletionSignal = true },
	} {
		t.Run(reason, func(t *testing.T) {
			h := newHarness(t)
			card := h.makeTaskWithID(t, fixedTaskID(809), domain.StatusCategoryReview)
			arm(card)

			res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(),
				mergedMREvent(185, card.ID, "MESH-"+card.ID.String()))
			require.NoError(t, err)
			assert.False(t, res.Transitioned)
			assert.Equal(t, reason, res.Reason)
			assert.Empty(t, h.taskSvc.moveCalls)
			assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[card.ID].StatusID)
			assert.Contains(t, lastComment(h), "NOT moved to done")
		})
	}
}

// Same for a review card bound to the MR by role=review.
func TestHandleMR_BoundReviewCardAwaitingHuman_StaysOpen(t *testing.T) {
	h := newHarness(t)
	work := h.makeTaskWithID(t, fixedTaskID(810), domain.StatusCategoryInProgress)
	review := h.makeTaskWithID(t, fixedTaskID(811), domain.StatusCategoryReview)
	review.HumanGate = true
	h.bindReview(review, reviewSubjectURL, `{"role":"review"}`)

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(101, work.ID, "MESH-"+work.ID.String()))
	require.NoError(t, err)
	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[review.ID].StatusID)
	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[work.ID].StatusID, "the work card itself still moves")
}

func TestVCSRepoKey(t *testing.T) {
	assert.Equal(t, "gitlab.example.invalid/entire-vc/evc-mesh", vcsRepoKey("https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/7"))
	assert.Equal(t, "github.com/entire-vc/repo", vcsRepoKey("https://GitHub.com/Entire-VC/Repo/pull/3"))
	assert.Equal(t, "", vcsRepoKey("https://gitlab.example.invalid/entire-vc/evc-mesh"))
	assert.NotEqual(t, vcsRepoKey("https://github.com/entire-vc/evc-mesh/pull/1"), vcsRepoKey("https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/1"))
}
