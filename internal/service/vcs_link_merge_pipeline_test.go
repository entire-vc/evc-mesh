package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
)

type fakePipelineSource struct {
	hp    gitlabapi.HeadPipeline
	found bool
	err   error
	calls int
}

func (f *fakePipelineSource) GetMergeRequestHeadPipeline(context.Context, string, int) (gitlabapi.HeadPipeline, bool, error) {
	f.calls++
	return f.hp, f.found, f.err
}

// inProgressTask fixes the id: the task-ref extractor ignores an all-digit
// 8-hex prefix, which a random uuid occasionally produces.
func inProgressTask(t *testing.T, h *harness) *domain.Task {
	t.Helper()
	tk := h.makeTask(t, domain.StatusCategoryInProgress)
	delete(h.taskRepo.tasks, tk.ID)
	tk.ID = uuid.MustParse("abcdef12-3456-4789-8abc-def012345678")
	h.taskRepo.tasks[tk.ID] = tk
	return tk
}

const mergePipelineHead = "7371544b0000000000000000000000000000aaaa"

func mergeEventWithHead(taskID uuid.UUID) GitLabWebhookEvent {
	return GitLabWebhookEvent{
		Action: "merge", MRIID: 1285, MRURL: pipelineMRURL, MRState: "merged",
		MRTitle: "fix: x (#" + taskID.String()[:8] + ")", HeadSHA: mergePipelineHead,
		ProjectPath: "entire-vc/evc-mesh",
	}
}

func withPipelineSource(h *harness, src MRHeadPipelineSource) {
	h.svc.(*vcsLinkService).pipelineSource = func(context.Context, uuid.UUID) (MRHeadPipelineSource, bool) {
		return src, src != nil
	}
}

func mergedLinkOf(t *testing.T, h *harness, taskID uuid.UUID) domain.VCSLink {
	t.Helper()
	links, err := h.repo.ListByTask(context.Background(), taskID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	return links[0]
}

// A merge event creates the link itself, so no Pipeline Hook can have reached
// it earlier: the head pipeline must be read at merge time.
func TestMergeEvent_StoresHeadPipelineOnTheLink(t *testing.T) {
	h := newHarness(t)
	task := inProgressTask(t, h)
	withPipelineSource(h, &fakePipelineSource{found: true,
		hp: gitlabapi.HeadPipeline{ID: 77, Status: "success", SHA: mergePipelineHead}})

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergeEventWithHead(task.ID))
	require.NoError(t, err)

	l := mergedLinkOf(t, h, task.ID)
	require.NotNil(t, l.PipelineStatus)
	assert.Equal(t, "success", *l.PipelineStatus)
	assert.Equal(t, mergePipelineHead, l.PipelineSHA())
}

// Red control: without a source the link has no pipeline status, which is the
// defect, and the gate stays closed even with a SHIP on the same head.
func TestMergeEvent_WithoutPipelineSource_GateStaysClosed(t *testing.T) {
	h := newHarness(t)
	task := inProgressTask(t, h)

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergeEventWithHead(task.ID))
	require.NoError(t, err)

	l := mergedLinkOf(t, h, task.ID)
	assert.Nil(t, l.PipelineStatus)
	l.Metadata = []byte(`{"head_sha":"` + mergePipelineHead + `","ship_sha":"` + mergePipelineHead + `"}`)
	assert.False(t, l.IsMergedShippedGreen())
}

// The synced status feeds the unchanged gate: SHIP bound to the head plus a
// green pipeline on that head proves delivery.
func TestMergeEvent_SyncedPipelineSatisfiesTheGate(t *testing.T) {
	h := newHarness(t)
	task := inProgressTask(t, h)
	withPipelineSource(h, &fakePipelineSource{found: true,
		hp: gitlabapi.HeadPipeline{ID: 77, Status: "success", SHA: mergePipelineHead}})

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergeEventWithHead(task.ID))
	require.NoError(t, err)

	l := mergedLinkOf(t, h, task.ID)
	l.Metadata = []byte(`{"head_sha":"` + mergePipelineHead + `","pipeline_sha":"` + mergePipelineHead + `","ship_sha":"` + mergePipelineHead + `"}`)
	assert.True(t, l.IsMergedShippedGreen())
}

// A pipeline that ran on another commit, a failed one, no pipeline and a
// GitLab error never produce a green link.
func TestMergeEvent_NoGreenFromOtherPipelines(t *testing.T) {
	cases := map[string]*fakePipelineSource{
		"failed":      {found: true, hp: gitlabapi.HeadPipeline{ID: 5, Status: "failed", SHA: mergePipelineHead}},
		"no-pipeline": {found: false},
		"gitlab-down": {err: errors.New("boom")},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			task := inProgressTask(t, h)
			withPipelineSource(h, src)

			_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), mergeEventWithHead(task.ID))
			require.NoError(t, err, "a sync failure must not fail the delivery")

			l := mergedLinkOf(t, h, task.ID)
			if l.PipelineStatus != nil {
				assert.NotEqual(t, "success", *l.PipelineStatus)
			}
			assert.Equal(t, 1, src.calls)
		})
	}
}

// Only a merge triggers the lookup.
func TestOpenedEvent_DoesNotQueryGitLab(t *testing.T) {
	h := newHarness(t)
	task := inProgressTask(t, h)
	src := &fakePipelineSource{found: true, hp: gitlabapi.HeadPipeline{ID: 1, Status: "success"}}
	withPipelineSource(h, src)
	ev := mergeEventWithHead(task.ID)
	ev.MRState, ev.Action = "opened", "open"

	_, err := h.svc.HandleGitLabMergeRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Zero(t, src.calls)
}
