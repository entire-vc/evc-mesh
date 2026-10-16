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

const pipelineMRURL = "https://gitlab.example.com/entire-vc/evc-mesh/-/merge_requests/1285"

func pipelineEvent(id int64, status string) GitLabPipelineEvent {
	return GitLabPipelineEvent{
		PipelineID: id, Status: status, MRIID: 1285, MRURL: pipelineMRURL,
		ProjectPath: "entire-vc/evc-mesh",
	}
}

func seedMRLink(h *harness, taskID uuid.UUID, url string) *domain.VCSLink {
	l := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "1285", URL: url,
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	_, _ = h.repo.Upsert(context.Background(), l)
	return l
}

func pipelineStatusOf(t *testing.T, h *harness, id uuid.UUID) *string {
	t.Helper()
	l, err := h.repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, l)
	return l.PipelineStatus
}

func TestHandlePipeline_StoresStatusOnTheMRLink(t *testing.T) {
	h := newHarness(t)
	link := seedMRLink(h, uuid.New(), pipelineMRURL)

	res, err := h.svc.HandleGitLabPipelineEvent(context.Background(), pipelineEvent(10, "running"))
	require.NoError(t, err)
	assert.Equal(t, 1, res.Updated)
	got := pipelineStatusOf(t, h, link.ID)
	require.NotNil(t, got)
	assert.Equal(t, "running", *got)

	_, err = h.svc.HandleGitLabPipelineEvent(context.Background(), pipelineEvent(10, "success"))
	require.NoError(t, err)
	assert.Equal(t, "success", *pipelineStatusOf(t, h, link.ID), "a later status of the same pipeline overwrites")
}

// Red control: without the url comparison, a pipeline of another project's !1285
// would paint this link.
func TestHandlePipeline_OtherProjectsSameIIDIsNotTouched(t *testing.T) {
	h := newHarness(t)
	link := seedMRLink(h, uuid.New(), "https://gitlab.example.com/entire-vc/evc-spark/-/merge_requests/1285")

	res, err := h.svc.HandleGitLabPipelineEvent(context.Background(), pipelineEvent(10, "failed"))
	require.NoError(t, err)
	assert.Equal(t, 0, res.Updated)
	assert.Equal(t, "no_link", res.Reason)
	assert.Nil(t, pipelineStatusOf(t, h, link.ID))
}

func TestHandlePipeline_OlderPipelineDoesNotOverwriteNewer(t *testing.T) {
	h := newHarness(t)
	link := seedMRLink(h, uuid.New(), pipelineMRURL)

	_, err := h.svc.HandleGitLabPipelineEvent(context.Background(), pipelineEvent(20, "success"))
	require.NoError(t, err)
	res, err := h.svc.HandleGitLabPipelineEvent(context.Background(), pipelineEvent(19, "failed"))
	require.NoError(t, err)
	assert.Equal(t, 0, res.Updated)
	assert.Equal(t, "success", *pipelineStatusOf(t, h, link.ID))
}

func TestHandlePipeline_UnknownStatusAndNoMRAreDropped(t *testing.T) {
	h := newHarness(t)
	link := seedMRLink(h, uuid.New(), pipelineMRURL)

	res, err := h.svc.HandleGitLabPipelineEvent(context.Background(), pipelineEvent(10, "exploded"))
	require.NoError(t, err)
	assert.Equal(t, "unknown_status", res.Reason)

	noMR := pipelineEvent(10, "success")
	noMR.MRIID, noMR.MRURL = 0, ""
	res, err = h.svc.HandleGitLabPipelineEvent(context.Background(), noMR)
	require.NoError(t, err)
	assert.Equal(t, "no_merge_request", res.Reason)
	assert.Nil(t, pipelineStatusOf(t, h, link.ID))
}

func TestHandlePipeline_NeverCreatesALink(t *testing.T) {
	h := newHarness(t)
	res, err := h.svc.HandleGitLabPipelineEvent(context.Background(), pipelineEvent(10, "success"))
	require.NoError(t, err)
	assert.Equal(t, "no_link", res.Reason)
	links, _ := h.repo.ListByExternalID(context.Background(), domain.VCSProviderGitLab, domain.VCSLinkTypePR, "1285")
	assert.Empty(t, links)
}
