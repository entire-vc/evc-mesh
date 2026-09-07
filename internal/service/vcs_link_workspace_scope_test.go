package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// These tests are the acceptance criteria of #839b9897: the GitHub/GitLab
// webhook receiver authenticates an inbound delivery against the UNION of
// every workspace's configured secret (deliberate — see
// VCSIntegrationResolver's doc comment, no single workspace is known until
// the payload is read), but before this fix threw away WHICH workspace's
// secret actually matched once validation succeeded. Task-ref resolution
// then searched the whole instance, so a workspace B secret could create a
// vcs_link on — and, via applyPRTransitionPolicy, transition the status of —
// a task belonging to workspace A or C. Reproduced live against prod
// 2026-09-07 (throwaway resources, all deleted after) before this fix
// landed; these are the same shape run against the fake stack.

// fakeProjectRepo is a minimal repository.ProjectRepository stub keyed by
// project id, so a test can put two projects in two different workspaces
// and assert task-ref resolution honours the boundary between them.
type fakeProjectRepo struct {
	projects map[uuid.UUID]*domain.Project
}

func newFakeProjectRepo() *fakeProjectRepo {
	return &fakeProjectRepo{projects: map[uuid.UUID]*domain.Project{}}
}

func (r *fakeProjectRepo) put(projectID, workspaceID uuid.UUID) {
	r.projects[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
}

func (r *fakeProjectRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Project, error) {
	return r.projects[id], nil
}
func (r *fakeProjectRepo) GetBySlug(context.Context, uuid.UUID, string) (*domain.Project, error) {
	return nil, nil
}
func (r *fakeProjectRepo) Create(context.Context, *domain.Project) error { return nil }
func (r *fakeProjectRepo) Update(context.Context, *domain.Project) error { return nil }
func (r *fakeProjectRepo) Delete(context.Context, uuid.UUID) error       { return nil }
func (r *fakeProjectRepo) List(context.Context, uuid.UUID, repository.ProjectFilter, pagination.Params) (*pagination.Page[domain.Project], error) {
	return nil, nil
}

var _ repository.ProjectRepository = (*fakeProjectRepo)(nil)

// workspaceScopeHarness wires two projects in two different workspaces, one
// task per project, and a full in_progress→review→done status ladder per
// project (applyPRTransitionPolicy resolves the target status within the
// task's own project).
type workspaceScopeHarness struct {
	repo        *fakeVCSLinkRepo
	taskRepo    *fakeTaskRepo
	statusRepo  *fakeStatusRepo
	taskSvc     *fakeTaskService
	commentSvc  *fakeCommentService
	projectRepo *fakeProjectRepo
	svc         VCSLinkService

	wsA, wsB     uuid.UUID
	projA, projB uuid.UUID
	taskA        *domain.Task // in_progress, belongs to projA/wsA
}

func newWorkspaceScopeHarness(t *testing.T) *workspaceScopeHarness {
	t.Helper()
	wsA, wsB := uuid.New(), uuid.New()
	projA, projB := uuid.New(), uuid.New()

	projectRepo := newFakeProjectRepo()
	projectRepo.put(projA, wsA)
	projectRepo.put(projB, wsB)

	statusRepo := newFakeStatusRepo()
	inProgressA := statusRepo.addStatus(projA, "in_progress", domain.StatusCategoryInProgress)
	statusRepo.addStatus(projA, "review", domain.StatusCategoryReview)
	statusRepo.addStatus(projB, "in_progress", domain.StatusCategoryInProgress)
	statusRepo.addStatus(projB, "review", domain.StatusCategoryReview)

	taskRepo := &fakeTaskRepo{tasks: map[uuid.UUID]*domain.Task{}}
	taskA := &domain.Task{ID: fixedTaskID(101), ProjectID: projA, StatusID: inProgressA.ID}
	taskRepo.tasks[taskA.ID] = taskA

	taskSvc := &fakeTaskService{taskRepo: taskRepo, statusRepo: statusRepo}
	commentSvc := &fakeCommentService{}
	repo := newFakeVCSLinkRepo()

	svc := NewVCSLinkService(repo,
		WithVCSTaskRepo(taskRepo),
		WithVCSStatusRepo(statusRepo),
		WithVCSTaskService(taskSvc),
		WithVCSCommentService(commentSvc),
		WithVCSProjectRepo(projectRepo),
	)

	return &workspaceScopeHarness{
		repo: repo, taskRepo: taskRepo, statusRepo: statusRepo,
		taskSvc: taskSvc, commentSvc: commentSvc, projectRepo: projectRepo, svc: svc,
		wsA: wsA, wsB: wsB, projA: projA, projB: projB, taskA: taskA,
	}
}

func (h *workspaceScopeHarness) mergedMREvent(workspaceID uuid.UUID, title string) GitLabWebhookEvent {
	return GitLabWebhookEvent{
		Action:      "merge",
		MRIID:       42,
		MRTitle:     title,
		MRState:     "merged",
		ProjectPath: "throwaway/repo",
		WorkspaceID: workspaceID,
	}
}

// TestHandleGitLabMergeRequestEvent_CrossWorkspaceSecret_RefusesForeignTask
// is the negative control: a secret belonging to workspace B validates a
// delivery whose title names a task that exists but belongs to workspace A.
// Before the fix this created a vcs_link on taskA and — because the task
// was in_progress and the MR reports merged — transitioned it straight to
// review, entirely bypassing workspace B's ownership boundary. It must now
// resolve to no_task_ref, exactly as if the id in the title didn't exist.
func TestHandleGitLabMergeRequestEvent_CrossWorkspaceSecret_RefusesForeignTask(t *testing.T) {
	h := newWorkspaceScopeHarness(t)
	ctx := context.Background()

	title := "Cross-workspace probe MR referencing #" + h.taskA.ID.String()[:8]
	result, err := h.svc.HandleGitLabMergeRequestEvent(ctx, h.mergedMREvent(h.wsB, title))
	require.NoError(t, err)

	assert.Equal(t, "no_task_ref", result.Reason, "a task named only by a DIFFERENT workspace's secret must resolve like no reference was present at all")
	assert.False(t, result.Transitioned)
	assert.Equal(t, uuid.Nil, result.TaskID)

	// The task itself must be untouched: still in_progress, no vcs_link.
	assert.Equal(t, h.statusRepo.byProject[h.projA][0].ID, h.taskA.StatusID, "task status must not move")
	links, err := h.repo.ListByTask(ctx, h.taskA.ID)
	require.NoError(t, err)
	assert.Empty(t, links, "no vcs_link may be created on a task outside the validating secret's workspace")
}

// TestHandleGitLabMergeRequestEvent_SameWorkspaceSecret_StillLinksAndTransitions
// is the positive control: the SAME payload shape, but the secret belongs to
// the task's own workspace (A). The existing in_progress→review transition
// must still fire — this is the "AC3 re-enable" of this fix: proving AC1's
// refusal isn't just "nothing works at all any more".
func TestHandleGitLabMergeRequestEvent_SameWorkspaceSecret_StillLinksAndTransitions(t *testing.T) {
	h := newWorkspaceScopeHarness(t)
	ctx := context.Background()

	title := "fix: gate (#" + h.taskA.ID.String()[:8] + ")"
	result, err := h.svc.HandleGitLabMergeRequestEvent(ctx, h.mergedMREvent(h.wsA, title))
	require.NoError(t, err)

	assert.Equal(t, h.taskA.ID, result.TaskID)
	assert.True(t, result.Transitioned)
	assert.Equal(t, "review", result.NewStatus)

	reviewStatusID := h.statusRepo.byProject[h.projA][1].ID
	assert.Equal(t, reviewStatusID, h.taskA.StatusID)

	links, err := h.repo.ListByTask(ctx, h.taskA.ID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
}

// TestHandleGitLabMergeRequestEvent_UnscopedEnvFallback_PreservesLegacyBehavior
// pins the DELIBERATE exception named in WebhookSecret's doc comment: when
// the matching secret is the instance-wide env fallback (WorkspaceID ==
// uuid.Nil — no workspace has configured its own secret), resolution stays
// unscoped exactly as it always has. This is not a residual hole this fix
// missed; it's the documented single-tenant/self-host trust model, pinned so
// a future change can't narrow it by accident without a test noticing.
func TestHandleGitLabMergeRequestEvent_UnscopedEnvFallback_PreservesLegacyBehavior(t *testing.T) {
	h := newWorkspaceScopeHarness(t)
	ctx := context.Background()

	title := "fix: gate (#" + h.taskA.ID.String()[:8] + ")"
	result, err := h.svc.HandleGitLabMergeRequestEvent(ctx, h.mergedMREvent(uuid.Nil, title))
	require.NoError(t, err)

	assert.Equal(t, h.taskA.ID, result.TaskID)
	assert.True(t, result.Transitioned)
}

// TestHandleGitLabMergeRequestEvent_CrossWorkspaceSecret_IgnoresStoredLinkFallback
// covers the second exposure Garfield's triage plan didn't name explicitly:
// when the payload's title/body/branch name nothing, HandleGitLabMergeRequestEvent
// falls back to whatever vcs_link is already stored for this (provider,
// link_type, external_id) — and an MR iid is just as guessable as a task id.
// A workspace B secret must not be able to reuse a link that was legitimately
// created for a task in workspace A just by naming the same MR number.
func TestHandleGitLabMergeRequestEvent_CrossWorkspaceSecret_IgnoresStoredLinkFallback(t *testing.T) {
	h := newWorkspaceScopeHarness(t)
	ctx := context.Background()

	// Seed a pre-existing link for MR !42 pointing at taskA (workspace A) —
	// as if an earlier, correctly-scoped delivery created it.
	_, err := h.repo.Upsert(ctx, &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     h.taskA.ID,
		Provider:   domain.VCSProviderGitLab,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "42",
		URL:        "https://git.entire.host/throwaway/repo/-/merge_requests/42",
		Status:     domain.VCSLinkStatusOpen,
		Metadata:   []byte("{}"),
	})
	require.NoError(t, err)

	// A workspace B secret sends a redelivery naming no task at all — must
	// NOT fall back to the workspace-A link just because the MR number matches.
	result, err := h.svc.HandleGitLabMergeRequestEvent(ctx, h.mergedMREvent(h.wsB, "chore: bump deps"))
	require.NoError(t, err)

	assert.Equal(t, "no_task_ref", result.Reason)
	assert.Equal(t, uuid.Nil, result.TaskID)
	assert.Equal(t, h.statusRepo.byProject[h.projA][0].ID, h.taskA.StatusID, "task status must not move")
}
