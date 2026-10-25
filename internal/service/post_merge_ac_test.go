package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestPostMergeACMatch(t *testing.T) {
	cases := []struct {
		name  string
		task  *domain.Task
		match bool
	}{
		{"nil task", nil, false},
		{"plain code AC", &domain.Task{Description: "Приёмка: 1) DB-тест handler = 201; без объекта = 422."}, false},
		{"merge alone is not post-merge", &domain.Task{Description: "Open an MR and merge it after review."}, false},
		// AC2 of an internal ticket, the card this rule was moved three times on.
		{"live run ru", &domain.Task{Description: "2) Живой прогон agent-chat-e2e S03 и P01–P07 (seed) зелёные."}, true},
		{"after deploy ru", &domain.Task{Description: "Проверить на проде после деплоя: /version = merge SHA."}, true},
		{"after merge ru yo", &domain.Task{Description: "Пайплайн на main после мёржа зелёный."}, true},
		{"post-merge en", &domain.Task{Description: "AC3: post-merge smoke on main passes."}, true},
		{"after the deploy en", &domain.Task{Description: "Check the endpoint after the deploy."}, true},
		{"live probe en", &domain.Task{Description: "P2: live probe returns the marker."}, true},
		{"label override", &domain.Task{Description: "no phrase here", Labels: []string{"chat", "Post-Merge-AC"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := postMergeACMatch(tc.task)
			if tc.match {
				assert.NotEmpty(t, got)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

func TestHandleMR_MergedFromInProgress_PostMergeAC_StaysInProgress(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	task.Description = "Приёмка: 1) тест в CI. 2) Живой прогон agent-chat-e2e S03 зелёный."

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(1503, task.ID, "MESH-"+task.ID.String()))
	require.NoError(t, err)
	assert.False(t, res.Transitioned)
	assert.Equal(t, "post_merge_ac_pending", res.Reason)
	assert.Equal(t, "in_progress", res.OldStatus)
	assert.Equal(t, h.statusIDs[domain.StatusCategoryInProgress], h.taskRepo.tasks[task.ID].StatusID)
	assert.Empty(t, h.taskSvc.moveCalls, "MoveTask must not be called")

	require.Len(t, h.commentSvc.created, 1)
	body := h.commentSvc.created[0].Body
	assert.Contains(t, body, "MR !1503 merged")
	assert.Contains(t, body, "post-merge check")
	assert.Contains(t, body, "Живой прогон")
	assert.NotContains(t, body, "moved to review")

	links, _ := h.repo.ListByTask(context.Background(), task.ID)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
}

func TestHandlePR_MergedFromInProgress_PostMergeLabel_StaysInProgress(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	task.Labels = []string{LabelPostMergeAC}

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), mergedClosedEvent(7, task.ID, "MESH-"+task.ID.String()))
	require.NoError(t, err)
	assert.False(t, res.Transitioned)
	assert.Equal(t, "post_merge_ac_pending", res.Reason)
	assert.Empty(t, h.taskSvc.moveCalls)
}

// The rule only guards in_progress → review: a card already in review with a
// post-merge AC keeps the existing review → done path.
func TestHandleMR_MergedFromReview_PostMergeAC_StillDone(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryReview)
	task.Description = "Live run on prod after deploy."

	res, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergedMREvent(8, task.ID, "MESH-"+task.ID.String()))
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, "done", res.NewStatus)
}
