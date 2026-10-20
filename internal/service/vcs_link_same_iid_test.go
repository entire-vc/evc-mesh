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
