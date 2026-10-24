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

// A link added without a status for a GitLab MR that merged before any webhook
// could see it used to be stored "open" for ever. Create now asks GitLab once;
// these tests pin both what it learns and that GitLab trouble never costs the link.

const liveStatusMRURL = "https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/501"

type fakeMRChecker struct {
	state gitlabapi.MergeRequestState
	err   error
	calls int
	path  string
	iid   int
}

func (f *fakeMRChecker) GetMergeRequestState(_ context.Context, projectPath string, iid int) (gitlabapi.MergeRequestState, error) {
	f.calls++
	f.path, f.iid = projectPath, iid
	return f.state, f.err
}

// liveStatusService is the harness service plus the pieces Create needs to find
// the task's workspace and a GitLab client.
func liveStatusService(h *harness, src func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool)) VCSLinkService {
	return NewVCSLinkService(h.repo,
		WithVCSTaskRepo(h.taskRepo),
		WithVCSProjectRepo(NewMockProjectRepository().WithDefaultWorkspace(uuid.New())),
		WithVCSMRStateSource(src),
	)
}

func createGitLabLink(t *testing.T, svc VCSLinkService, taskID uuid.UUID) *domain.VCSLink {
	t.Helper()
	link, created, err := svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "501", URL: liveStatusMRURL,
	})
	require.NoError(t, err, "GitLab trouble must never fail link creation")
	require.True(t, created)
	return link
}

func TestVCSLinkService_Create_NoStatus_GitLabMergedIsStored(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	checker := &fakeMRChecker{state: gitlabapi.MergeRequestState{Merged: true, State: "merged"}}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })

	link := createGitLabLink(t, svc, task.ID)

	assert.Equal(t, domain.VCSLinkStatusMerged, link.Status)
	assert.Equal(t, 1, checker.calls)
	assert.Equal(t, "entire-vc/evc-mesh", checker.path)
	assert.Equal(t, 501, checker.iid)
	stored, _ := h.repo.GetByID(context.Background(), link.ID)
	require.NotNil(t, stored)
	assert.Equal(t, domain.VCSLinkStatusMerged, stored.Status)
	assert.NotContains(t, string(stored.Metadata), "status_declared_manually",
		"a measured status is not a self-declaration")
}

func TestVCSLinkService_Create_NoStatus_GitLabClosedIsStored(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	checker := &fakeMRChecker{state: gitlabapi.MergeRequestState{State: "closed"}}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })

	assert.Equal(t, domain.VCSLinkStatusClosed, createGitLabLink(t, svc, task.ID).Status)
}

func TestVCSLinkService_Create_NoStatus_GitLabOpenStaysOpen(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	checker := &fakeMRChecker{state: gitlabapi.MergeRequestState{State: "opened"}}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })

	assert.Equal(t, domain.VCSLinkStatusOpen, createGitLabLink(t, svc, task.ID).Status)
	assert.Equal(t, 1, checker.calls)
}

func TestVCSLinkService_Create_NoStatus_GitLabErrorKeepsOpen(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	checker := &fakeMRChecker{err: errors.New("context deadline exceeded")}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })

	assert.Equal(t, domain.VCSLinkStatusOpen, createGitLabLink(t, svc, task.ID).Status)
}

func TestVCSLinkService_Create_NoStatus_NoTokenKeepsOpen(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return nil, false })

	assert.Equal(t, domain.VCSLinkStatusOpen, createGitLabLink(t, svc, task.ID).Status)
}

func TestVCSLinkService_Create_NoStatus_SourceNotWiredKeepsOpen(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	assert.Equal(t, domain.VCSLinkStatusOpen, createGitLabLink(t, h.svc, task.ID).Status)
}

func TestVCSLinkService_Create_ExplicitStatus_DoesNotAskGitLab(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	checker := &fakeMRChecker{state: gitlabapi.MergeRequestState{Merged: true, State: "merged"}}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })

	link, _, err := svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "501", URL: liveStatusMRURL, Status: domain.VCSLinkStatusOpen,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatusOpen, link.Status, "a caller's explicit status is not overridden")
	assert.Equal(t, 0, checker.calls)
}

func TestVCSLinkService_Create_NoStatus_GitHubLinkDoesNotAskGitLab(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	checker := &fakeMRChecker{state: gitlabapi.MergeRequestState{Merged: true, State: "merged"}}
	svc := liveStatusService(h, func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return checker, true })

	link, _, err := svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR,
		ExternalID: "42", URL: "https://github.com/entire-vc/evc-mesh/pull/42",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatusOpen, link.Status)
	assert.Equal(t, 0, checker.calls)
}
