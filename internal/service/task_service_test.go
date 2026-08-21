package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

var frozenTime = time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

// setupTaskService returns a taskService wired to fresh mocks.
// testDefaultWorkspaceID is the single tenant every service test runs inside
// unless it deliberately builds a second one. The assignee tenancy guard needs a
// workspace behind every project and principal; without a shared default, each
// test would have to grow a workspace fixture it is not about.
var testDefaultWorkspaceID = uuid.MustParse("11111111-1111-4111-8111-111111111111")

// wireTenancyDeps gives a task service the directories production always wires.
// Leaving any of them nil is not a neutral test simplification: the tenancy guard
// refuses what it cannot check, so a half-wired service models a state production
// cannot reach — the same argument setupMembershipEnv already makes for userRepo.
func wireTenancyDeps(projRepo *MockProjectRepository, agentRepo *MockAgentRepository) []TaskServiceOption {
	projRepo.WithDefaultWorkspace(testDefaultWorkspaceID)
	agentRepo.WithDefaultWorkspace(testDefaultWorkspaceID)
	return []TaskServiceOption{
		WithProjectRepo(projRepo),
		WithTaskAgentRepo(agentRepo),
		WithWorkspaceMembershipReader(NewPermissiveWorkspaceMembershipReader()),
	}
}

// newTestTaskService is NewTaskService with the tenancy directories production
// always wires. The defaults are prepended, so a caller's own WithProjectRepo /
// WithTaskAgentRepo still wins — this only stops a test from accidentally
// building a service that cannot answer "which workspace is this?", which the
// assignee guard treats (correctly) as a refusal.
func newTestTaskService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	depRepo repository.TaskDependencyRepository,
	activityRepo repository.ActivityLogRepository,
	opts ...TaskServiceOption,
) TaskService {
	base := []TaskServiceOption{
		WithProjectRepo(NewMockProjectRepository().WithDefaultWorkspace(testDefaultWorkspaceID)),
		WithTaskAgentRepo(NewMockAgentRepository()),
		WithWorkspaceMembershipReader(NewPermissiveWorkspaceMembershipReader()),
	}
	return NewTaskService(taskRepo, statusRepo, depRepo, activityRepo, append(base, opts...)...)
}

// seedTestAgents registers agents in whatever directory the service is wired to.
//
// The assignee tenancy guard refuses a principal it cannot find — an id in no
// directory cannot be shown to belong to the task's workspace — so a test that
// assigns an ad-hoc uuid has to say that uuid is a real agent first. Before the
// guard, such an assignment "succeeded" and produced a task pointing at nobody.
func seedTestAgents(t *testing.T, svc *taskService, ids ...uuid.UUID) {
	t.Helper()
	repo, ok := svc.agentRepo.(*MockAgentRepository)
	require.True(t, ok, "service is not wired to a MockAgentRepository")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, id := range ids {
		repo.items[id] = &domain.Agent{ID: id, Slug: "seeded"}
	}
}

func setupTaskService() (*taskService, *MockTaskRepository, *MockTaskStatusRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	opts := wireTenancyDeps(NewMockProjectRepository(), NewMockAgentRepository())
	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo, opts...).(*taskService)

	// Freeze the clock for deterministic tests.
	timeNow = func() time.Time { return frozenTime }

	return svc, taskRepo, statusRepo
}

// ---------------------------------------------------------------------------
// TestTaskService_Create
// ---------------------------------------------------------------------------

func TestTaskService_Create(t *testing.T) {
	tests := []struct {
		name      string
		task      *domain.Task
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, task *domain.Task, repo *MockTaskRepository)
	}{
		{
			name: "success - generates ID and timestamps",
			task: &domain.Task{
				ProjectID: uuid.New(),
				StatusID:  uuid.New(),
				Title:     "Implement login page",
				Priority:  domain.PriorityHigh,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, task *domain.Task, repo *MockTaskRepository) {
				assert.NotEqual(t, uuid.Nil, task.ID, "ID should be generated")
				assert.Equal(t, frozenTime, task.CreatedAt)
				assert.Equal(t, frozenTime, task.UpdatedAt)

				// Verify persisted in repo.
				stored, err := repo.GetByID(context.Background(), task.ID)
				require.NoError(t, err)
				assert.Equal(t, task.Title, stored.Title)
			},
		},
		{
			name: "success - preserves provided ID",
			task: &domain.Task{
				ID:        uuid.New(),
				ProjectID: uuid.New(),
				StatusID:  uuid.New(),
				Title:     "With explicit ID",
			},
			wantErr: false,
			checkFunc: func(t *testing.T, task *domain.Task, repo *MockTaskRepository) {
				stored, err := repo.GetByID(context.Background(), task.ID)
				require.NoError(t, err)
				assert.NotNil(t, stored)
			},
		},
		{
			name: "error - empty title",
			task: &domain.Task{
				ProjectID: uuid.New(),
				StatusID:  uuid.New(),
				Title:     "",
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "error - whitespace-only title",
			task: &domain.Task{
				ProjectID: uuid.New(),
				StatusID:  uuid.New(),
				Title:     "   ",
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, _ := setupTaskService()
			ctx := context.Background()

			err := svc.Create(ctx, tt.task)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, tt.task, taskRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskService_GetByID
// ---------------------------------------------------------------------------

func TestTaskService_GetByID(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockTaskRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "found",
			setup: func(repo *MockTaskRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Task{ID: id, Title: "Test task"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found returns 404",
			setup: func(_ *MockTaskRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, _ := setupTaskService()
			ctx := context.Background()
			id := tt.setup(taskRepo)

			task, err := svc.GetByID(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, task)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, task)
				assert.Equal(t, id, task.ID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskService_MoveTask
// ---------------------------------------------------------------------------

func TestTaskService_MoveTask(t *testing.T) {
	projectID := uuid.New()

	tests := []struct {
		name      string
		setup     func(taskRepo *MockTaskRepository, statusRepo *MockTaskStatusRepository) (taskID uuid.UUID, input MoveTaskInput)
		wantErr   bool
		errCode   int
		errMsg    string
		checkFunc func(t *testing.T, taskRepo *MockTaskRepository, taskID uuid.UUID)
	}{
		{
			name: "success - move to in_progress",
			setup: func(taskRepo *MockTaskRepository, statusRepo *MockTaskStatusRepository) (uuid.UUID, MoveTaskInput) {
				taskID := uuid.New()
				statusID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "A task"}
				statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryInProgress}
				return taskID, MoveTaskInput{StatusID: &statusID}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, taskRepo *MockTaskRepository, taskID uuid.UUID) {
				task := taskRepo.items[taskID]
				require.NotNil(t, task)
				assert.Nil(t, task.CompletedAt, "CompletedAt should be nil for in_progress")
				assert.Equal(t, frozenTime, task.UpdatedAt)
			},
		},
		{
			name: "success - move to done sets completed_at",
			setup: func(taskRepo *MockTaskRepository, statusRepo *MockTaskStatusRepository) (uuid.UUID, MoveTaskInput) {
				taskID := uuid.New()
				statusID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "A task"}
				statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
				return taskID, MoveTaskInput{StatusID: &statusID}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, taskRepo *MockTaskRepository, taskID uuid.UUID) {
				task := taskRepo.items[taskID]
				require.NotNil(t, task)
				require.NotNil(t, task.CompletedAt, "CompletedAt should be set when moving to done")
				assert.Equal(t, frozenTime, *task.CompletedAt)
			},
		},
		{
			name: "success - move back from done clears completed_at",
			setup: func(taskRepo *MockTaskRepository, statusRepo *MockTaskStatusRepository) (uuid.UUID, MoveTaskInput) {
				taskID := uuid.New()
				statusID := uuid.New()
				completedAt := frozenTime.Add(-1 * time.Hour)
				taskRepo.items[taskID] = &domain.Task{
					ID: taskID, ProjectID: projectID, StatusID: uuid.New(),
					Title: "Previously done", CompletedAt: &completedAt,
				}
				statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryTodo}
				return taskID, MoveTaskInput{StatusID: &statusID}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, taskRepo *MockTaskRepository, taskID uuid.UUID) {
				task := taskRepo.items[taskID]
				require.NotNil(t, task)
				assert.Nil(t, task.CompletedAt, "CompletedAt should be cleared when moving out of done")
			},
		},
		{
			name: "error - invalid status (not found)",
			setup: func(taskRepo *MockTaskRepository, _ *MockTaskStatusRepository) (uuid.UUID, MoveTaskInput) {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, Title: "A task"}
				nonExistent := uuid.New()
				return taskID, MoveTaskInput{StatusID: &nonExistent}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - status from different project",
			setup: func(taskRepo *MockTaskRepository, statusRepo *MockTaskStatusRepository) (uuid.UUID, MoveTaskInput) {
				taskID := uuid.New()
				statusID := uuid.New()
				otherProject := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, Title: "A task"}
				statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: otherProject, Category: domain.StatusCategoryTodo}
				return taskID, MoveTaskInput{StatusID: &statusID}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "status does not belong to the same project",
		},
		{
			name: "error - task not found",
			setup: func(_ *MockTaskRepository, _ *MockTaskStatusRepository) (uuid.UUID, MoveTaskInput) {
				statusID := uuid.New()
				return uuid.New(), MoveTaskInput{StatusID: &statusID}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "success - update position only (no status change)",
			setup: func(taskRepo *MockTaskRepository, _ *MockTaskStatusRepository) (uuid.UUID, MoveTaskInput) {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, Title: "A task", Position: 1.0}
				pos := 5.5
				return taskID, MoveTaskInput{Position: &pos}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, taskRepo *MockTaskRepository, taskID uuid.UUID) {
				task := taskRepo.items[taskID]
				require.NotNil(t, task)
				assert.Equal(t, 5.5, task.Position)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, statusRepo := setupTaskService()
			ctx := context.Background()
			taskID, input := tt.setup(taskRepo, statusRepo)

			err := svc.MoveTask(ctx, taskID, input)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				if tt.errMsg != "" {
					assert.Contains(t, apiErr.Message, tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, taskRepo, taskID)
				}
			}
		})
	}
}

// TestTaskService_MoveTask_SupervisedGate verifies the supervised delegation gate:
// agent/system actors cannot move supervised tasks to review/done/cancelled.
func TestTaskService_MoveTask_SupervisedGate(t *testing.T) {
	projectID := uuid.New()

	makeSupervised := func(taskRepo *MockTaskRepository, statusRepo *MockTaskStatusRepository, cat domain.StatusCategory) (uuid.UUID, uuid.UUID) {
		taskID := uuid.New()
		statusID := uuid.New()
		taskRepo.items[taskID] = &domain.Task{
			ID: taskID, ProjectID: projectID,
			StatusID: uuid.New(), Title: "supervised task",
			DelegationLevel: domain.DelegationLevelSupervised,
		}
		statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: cat}
		return taskID, statusID
	}

	t.Run("agent move→review blocked 403", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID, statusID := makeSupervised(taskRepo, statusRepo, domain.StatusCategoryReview)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.Error(t, err)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusForbidden, apiErr.Code)
		assert.Equal(t, "supervised_requires_human_signoff", apiErr.Message)
	})

	t.Run("agent move→done blocked 403", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID, statusID := makeSupervised(taskRepo, statusRepo, domain.StatusCategoryDone)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.Error(t, err)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusForbidden, apiErr.Code)
	})

	t.Run("agent move→cancelled blocked 403", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID, statusID := makeSupervised(taskRepo, statusRepo, domain.StatusCategoryCancelled)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.Error(t, err)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusForbidden, apiErr.Code)
	})

	t.Run("user move→review allowed", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID, statusID := makeSupervised(taskRepo, statusRepo, domain.StatusCategoryReview)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.NoError(t, err)
	})

	t.Run("user move→done allowed", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID, statusID := makeSupervised(taskRepo, statusRepo, domain.StatusCategoryDone)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.NoError(t, err)
	})

	t.Run("agent move→in_progress allowed (non-terminal)", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID, statusID := makeSupervised(taskRepo, statusRepo, domain.StatusCategoryInProgress)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.NoError(t, err)
	})

	t.Run("delegation=auto, agent move→done allowed (regression)", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID := uuid.New()
		statusID := uuid.New()
		taskRepo.items[taskID] = &domain.Task{
			ID: taskID, ProjectID: projectID,
			StatusID: uuid.New(), Title: "auto task",
			DelegationLevel: domain.DelegationLevelAuto,
		}
		statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.NoError(t, err)
	})

	t.Run("delegation=review, agent move→done allowed (regression)", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID := uuid.New()
		statusID := uuid.New()
		taskRepo.items[taskID] = &domain.Task{
			ID: taskID, ProjectID: projectID,
			StatusID: uuid.New(), Title: "review-level task",
			DelegationLevel: domain.DelegationLevelReview,
		}
		statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})
		require.NoError(t, err)
	})

	t.Run("no actor in context (auto_transition case), supervised→review blocked", func(t *testing.T) {
		svc, taskRepo, statusRepo := setupTaskService()
		taskID, statusID := makeSupervised(taskRepo, statusRepo, domain.StatusCategoryReview)
		// Empty context = actorType "" ≠ "user" → gate fires
		err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})
		require.Error(t, err)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusForbidden, apiErr.Code)
	})
}

// ---------------------------------------------------------------------------
// TestTaskService_MoveTask_ReviewAssignee — review-reassign R1 acceptance tests
// ---------------------------------------------------------------------------

// setupTaskServiceWithWorkflow wires a task service with a MockRulesService carrying
// workflow rules, an AgentRepository, and a ProjectRepository for reviewer resolution.
func setupTaskServiceWithWorkflow(workflowResp *domain.WorkflowRulesResponse, effectiveRules *domain.EffectiveAssignmentRules) (
	*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockAgentRepository, *MockProjectRepository,
) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentRepo := NewMockAgentRepository()
	projRepo := NewMockProjectRepository()

	mockRules := NewMockRulesService(effectiveRules).WithWorkflowRules(workflowResp)

	opts := append(wireTenancyDeps(projRepo, agentRepo), WithRulesConfigService(mockRules))
	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo, opts...).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, agentRepo, projRepo
}

func TestTaskService_MoveTask_ReviewAssignee(t *testing.T) {
	workspaceID := testDefaultWorkspaceID // single-tenant: the assignee guard resolves every principal against this one workspace
	projectID := uuid.New()
	builderID := uuid.New()
	creatorID := uuid.New()
	leadID := uuid.New()

	makeTask := func(taskID, statusID uuid.UUID) {
		// helper defined inline via closure — see subtests for usage
		_ = taskID
		_ = statusID
	}
	_ = makeTask

	inProgressStatus := func(statusID uuid.UUID) *domain.TaskStatus {
		return &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress"}
	}
	reviewStatus := func(statusID uuid.UUID) *domain.TaskStatus {
		return &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryReview, Name: "review"}
	}

	t.Run("no SetReviewer config - builder stays assigned (no creator bounce)", func(t *testing.T) {
		// Workflow config with no OnTransition.SetReviewer on the in_progress→review transition.
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {Allowed: []string{"review"}},
				},
			},
		}
		svc, taskRepo, statusRepo, _, projRepo := setupTaskServiceWithWorkflow(wfResp, nil)

		oldStatusID := uuid.New()
		newStatusID := uuid.New()
		taskID := uuid.New()

		statusRepo.items[oldStatusID] = inProgressStatus(oldStatusID)
		statusRepo.items[newStatusID] = reviewStatus(newStatusID)
		projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
		taskRepo.items[taskID] = &domain.Task{
			ID:            taskID,
			ProjectID:     projectID,
			StatusID:      oldStatusID,
			AssigneeID:    &builderID,
			AssigneeType:  domain.AssigneeTypeAgent,
			CreatedBy:     creatorID,
			CreatedByType: domain.ActorTypeAgent,
		}

		err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &newStatusID})
		require.NoError(t, err)

		task := taskRepo.items[taskID]
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, builderID, *task.AssigneeID, "builder should stay assigned — no creator bounce")
	})

	t.Run("SetReviewer=lead - assignee becomes default_assignee (lead)", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {
						Allowed:      []string{"review"},
						OnTransition: &domain.TransitionAction{SetReviewer: "lead"},
					},
				},
			},
		}
		effectiveRules := &domain.EffectiveAssignmentRules{
			DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "garfield", Source: "project"},
		}
		svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wfResp, effectiveRules)

		oldStatusID := uuid.New()
		newStatusID := uuid.New()
		taskID := uuid.New()

		statusRepo.items[oldStatusID] = inProgressStatus(oldStatusID)
		statusRepo.items[newStatusID] = reviewStatus(newStatusID)
		projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
		agentRepo.items[leadID] = &domain.Agent{ID: leadID, WorkspaceID: workspaceID, Slug: "garfield"}
		// The builder must be a real in-workspace agent too: restorePreReviewAssignee
		// re-checks the stashed principal before handing the card back, because a stash
		// can outlive the agent it names.
		agentRepo.items[builderID] = &domain.Agent{ID: builderID, WorkspaceID: workspaceID, Slug: "builder"}
		taskRepo.items[taskID] = &domain.Task{
			ID:            taskID,
			ProjectID:     projectID,
			StatusID:      oldStatusID,
			AssigneeID:    &builderID,
			AssigneeType:  domain.AssigneeTypeAgent,
			CreatedBy:     creatorID,
			CreatedByType: domain.ActorTypeAgent,
		}

		err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &newStatusID})
		require.NoError(t, err)

		task := taskRepo.items[taskID]
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, leadID, *task.AssigneeID, "should be assigned to lead (garfield)")
		assert.Equal(t, domain.AssigneeTypeAgent, task.AssigneeType)
	})

	t.Run("explicit assignee_id overrides config (regression)", func(t *testing.T) {
		explicitReviewerID := uuid.New()
		// Seeded below as a real agent: an id resolving to no principal is now refused
		// before this subtest's config-vs-explicit precedence is ever reached.
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {
						Allowed:      []string{"review"},
						OnTransition: &domain.TransitionAction{SetReviewer: "lead"},
					},
				},
			},
		}
		svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wfResp, nil)

		oldStatusID := uuid.New()
		newStatusID := uuid.New()
		taskID := uuid.New()

		statusRepo.items[oldStatusID] = inProgressStatus(oldStatusID)
		statusRepo.items[newStatusID] = reviewStatus(newStatusID)
		projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
		agentRepo.items[explicitReviewerID] = &domain.Agent{
			ID: explicitReviewerID, WorkspaceID: workspaceID, Slug: "explicit-lead",
		}
		taskRepo.items[taskID] = &domain.Task{
			ID:            taskID,
			ProjectID:     projectID,
			StatusID:      oldStatusID,
			AssigneeID:    &builderID,
			AssigneeType:  domain.AssigneeTypeAgent,
			CreatedBy:     creatorID,
			CreatedByType: domain.ActorTypeAgent,
		}

		err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{
			StatusID:   &newStatusID,
			AssigneeID: &explicitReviewerID,
		})
		require.NoError(t, err)

		task := taskRepo.items[taskID]
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, explicitReviewerID, *task.AssigneeID, "explicit assignee_id must win over set_reviewer config")
	})

	t.Run("bounce out of review restores pre-review assignee", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {
						Allowed:      []string{"review"},
						OnTransition: &domain.TransitionAction{SetReviewer: "lead"},
					},
				},
			},
		}
		effectiveRules := &domain.EffectiveAssignmentRules{
			DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "garfield", Source: "project"},
		}
		svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wfResp, effectiveRules)

		inProgressStatusID := uuid.New()
		reviewStatusID := uuid.New()
		backToTodoStatusID := uuid.New()
		taskID := uuid.New()

		statusRepo.items[inProgressStatusID] = inProgressStatus(inProgressStatusID)
		statusRepo.items[reviewStatusID] = reviewStatus(reviewStatusID)
		statusRepo.items[backToTodoStatusID] = &domain.TaskStatus{ID: backToTodoStatusID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
		projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
		agentRepo.items[leadID] = &domain.Agent{ID: leadID, WorkspaceID: workspaceID, Slug: "garfield"}
		// The builder must be a real in-workspace agent too: restorePreReviewAssignee
		// re-checks the stashed principal before handing the card back, because a stash
		// can outlive the agent it names.
		agentRepo.items[builderID] = &domain.Agent{ID: builderID, WorkspaceID: workspaceID, Slug: "builder"}
		taskRepo.items[taskID] = &domain.Task{
			ID:            taskID,
			ProjectID:     projectID,
			StatusID:      inProgressStatusID,
			AssigneeID:    &builderID,
			AssigneeType:  domain.AssigneeTypeAgent,
			CreatedBy:     creatorID,
			CreatedByType: domain.ActorTypeAgent,
		}

		// Move to review: SetReviewer=lead reassigns to the lead and stashes the builder.
		err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &reviewStatusID})
		require.NoError(t, err)
		task := taskRepo.items[taskID]
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, leadID, *task.AssigneeID, "review transition should reassign to lead")
		require.NotNil(t, task.PreReviewAssigneeID)
		assert.Equal(t, builderID, *task.PreReviewAssigneeID, "builder should be stashed as pre-review assignee")

		// Bounce back to todo without an explicit assignee_id — the normal verifier-bounce shape.
		err = svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &backToTodoStatusID})
		require.NoError(t, err)
		task = taskRepo.items[taskID]
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, builderID, *task.AssigneeID, "bounce out of review should restore the builder, not strand on the lead")
		assert.Nil(t, task.PreReviewAssigneeID, "stash should be cleared after restore")
		assert.Nil(t, task.PreReviewAssigneeType, "stash should be cleared after restore")
	})

	t.Run("bounce out of review with explicit assignee_id keeps the explicit assignee", func(t *testing.T) {
		explicitAssigneeID := uuid.New()
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {
						Allowed:      []string{"review"},
						OnTransition: &domain.TransitionAction{SetReviewer: "lead"},
					},
				},
			},
		}
		effectiveRules := &domain.EffectiveAssignmentRules{
			DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "garfield", Source: "project"},
		}
		svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wfResp, effectiveRules)

		inProgressStatusID := uuid.New()
		reviewStatusID := uuid.New()
		backToTodoStatusID := uuid.New()
		taskID := uuid.New()

		statusRepo.items[inProgressStatusID] = inProgressStatus(inProgressStatusID)
		statusRepo.items[reviewStatusID] = reviewStatus(reviewStatusID)
		statusRepo.items[backToTodoStatusID] = &domain.TaskStatus{ID: backToTodoStatusID, ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
		projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
		agentRepo.items[leadID] = &domain.Agent{ID: leadID, WorkspaceID: workspaceID, Slug: "garfield"}
		// The builder must be a real in-workspace agent too: restorePreReviewAssignee
		// re-checks the stashed principal before handing the card back, because a stash
		// can outlive the agent it names.
		agentRepo.items[builderID] = &domain.Agent{ID: builderID, WorkspaceID: workspaceID, Slug: "builder"}
		// Same reason as the subtest above: the explicit assignee handed to MoveTask
		// must name a real agent, or it is refused before "explicit wins over restore"
		// — the property under test here — is ever reached.
		agentRepo.items[explicitAssigneeID] = &domain.Agent{
			ID: explicitAssigneeID, WorkspaceID: workspaceID, Slug: "explicit-assignee",
		}
		taskRepo.items[taskID] = &domain.Task{
			ID:            taskID,
			ProjectID:     projectID,
			StatusID:      inProgressStatusID,
			AssigneeID:    &builderID,
			AssigneeType:  domain.AssigneeTypeAgent,
			CreatedBy:     creatorID,
			CreatedByType: domain.ActorTypeAgent,
		}

		err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &reviewStatusID})
		require.NoError(t, err)

		err = svc.MoveTask(context.Background(), taskID, MoveTaskInput{
			StatusID:   &backToTodoStatusID,
			AssigneeID: &explicitAssigneeID,
		})
		require.NoError(t, err)
		task := taskRepo.items[taskID]
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, explicitAssigneeID, *task.AssigneeID, "explicit assignee_id on bounce must win over restore")
	})
}

// ---------------------------------------------------------------------------
// TestTaskService_AssignTask
// ---------------------------------------------------------------------------

func TestTaskService_AssignTask(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(repo *MockTaskRepository) uuid.UUID
		input     AssignTaskInput
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, repo *MockTaskRepository, taskID uuid.UUID)
	}{
		{
			name: "assign to agent",
			setup: func(repo *MockTaskRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Task{ID: id, Title: "Task", AssigneeType: domain.AssigneeTypeUnassigned}
				return id
			},
			input: func() AssignTaskInput {
				agentID := uuid.New()
				return AssignTaskInput{AssigneeID: &agentID, AssigneeType: domain.AssigneeTypeAgent}
			}(),
			wantErr: false,
			checkFunc: func(t *testing.T, repo *MockTaskRepository, taskID uuid.UUID) {
				task := repo.items[taskID]
				require.NotNil(t, task)
				assert.NotNil(t, task.AssigneeID)
				assert.Equal(t, domain.AssigneeTypeAgent, task.AssigneeType)
			},
		},
		{
			name: "assign to user",
			setup: func(repo *MockTaskRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Task{ID: id, Title: "Task", AssigneeType: domain.AssigneeTypeUnassigned}
				return id
			},
			input: func() AssignTaskInput {
				userID := uuid.New()
				return AssignTaskInput{AssigneeID: &userID, AssigneeType: domain.AssigneeTypeUser}
			}(),
			wantErr: false,
			checkFunc: func(t *testing.T, repo *MockTaskRepository, taskID uuid.UUID) {
				task := repo.items[taskID]
				require.NotNil(t, task)
				assert.NotNil(t, task.AssigneeID)
				assert.Equal(t, domain.AssigneeTypeUser, task.AssigneeType)
			},
		},
		{
			name: "unassign",
			setup: func(repo *MockTaskRepository) uuid.UUID {
				agentID := uuid.New()
				id := uuid.New()
				repo.items[id] = &domain.Task{ID: id, Title: "Task", AssigneeID: &agentID, AssigneeType: domain.AssigneeTypeAgent}
				return id
			},
			input:   AssignTaskInput{AssigneeID: nil, AssigneeType: domain.AssigneeTypeUnassigned},
			wantErr: false,
			checkFunc: func(t *testing.T, repo *MockTaskRepository, taskID uuid.UUID) {
				task := repo.items[taskID]
				require.NotNil(t, task)
				assert.Nil(t, task.AssigneeID)
				assert.Equal(t, domain.AssigneeTypeUnassigned, task.AssigneeType)
			},
		},
		{
			name: "task not found",
			setup: func(_ *MockTaskRepository) uuid.UUID {
				return uuid.New()
			},
			input:   AssignTaskInput{AssigneeType: domain.AssigneeTypeUser},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, _ := setupTaskService()
			ctx := context.Background()
			taskID := tt.setup(taskRepo)
			// Agent assignees must exist in the directory for the tenancy guard;
			// user assignees are decided by the membership reader instead, and
			// seeding them here would make resolveAssigneeType call them agents.
			if tt.input.AssigneeID != nil && tt.input.AssigneeType == domain.AssigneeTypeAgent {
				seedTestAgents(t, svc, *tt.input.AssigneeID)
			}

			err := svc.AssignTask(ctx, taskID, tt.input)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, taskRepo, taskID)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskService_CreateSubtask
// ---------------------------------------------------------------------------

// subtaskFixture wires a project with a default `todo` status, an `in_progress`
// status, a `review` status, and a parent task sitting in in_progress — the exact
// shape that used to birth subtasks into a status no agent feed ever polls.
type subtaskFixture struct {
	projectID  uuid.UUID
	parentID   uuid.UUID
	todoID     uuid.UUID
	inProgress uuid.UUID
	reviewID   uuid.UUID
}

func newSubtaskFixture(repo *MockTaskRepository, statusRepo *MockTaskStatusRepository) subtaskFixture {
	f := subtaskFixture{
		projectID:  uuid.New(),
		parentID:   uuid.New(),
		todoID:     uuid.New(),
		inProgress: uuid.New(),
		reviewID:   uuid.New(),
	}
	statusRepo.items[f.todoID] = &domain.TaskStatus{
		ID: f.todoID, ProjectID: f.projectID, Slug: "todo",
		Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	statusRepo.items[f.inProgress] = &domain.TaskStatus{
		ID: f.inProgress, ProjectID: f.projectID, Slug: "in_progress",
		Category: domain.StatusCategoryInProgress,
	}
	statusRepo.items[f.reviewID] = &domain.TaskStatus{
		ID: f.reviewID, ProjectID: f.projectID, Slug: "review",
		Category: domain.StatusCategoryReview,
	}
	repo.items[f.parentID] = &domain.Task{
		ID:        f.parentID,
		ProjectID: f.projectID,
		StatusID:  f.inProgress,
		Title:     "Parent task",
	}
	return f
}

func TestTaskService_CreateSubtask(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(f subtaskFixture, statusRepo *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput)
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, f subtaskFixture, child *domain.Task, repo *MockTaskRepository)
	}{
		{
			name: "born in project default status, never the parent's status",
			setup: func(f subtaskFixture, _ *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				return f.parentID, CreateSubtaskInput{
					Title:       "Child task",
					Description: "Sub-task description",
					Priority:    domain.PriorityMedium,
				}
			},
			checkFunc: func(t *testing.T, f subtaskFixture, child *domain.Task, repo *MockTaskRepository) {
				assert.NotEqual(t, uuid.Nil, child.ID)
				assert.Equal(t, "Child task", child.Title)
				assert.Equal(t, "Sub-task description", child.Description)
				assert.Equal(t, domain.PriorityMedium, child.Priority)
				require.NotNil(t, child.ParentTaskID)
				assert.Equal(t, f.parentID, *child.ParentTaskID)
				assert.Equal(t, domain.AssigneeTypeUnassigned, child.AssigneeType)
				assert.Equal(t, frozenTime, child.CreatedAt)

				// Project is inherited from the parent; status is NOT.
				assert.Equal(t, f.projectID, child.ProjectID)
				assert.Equal(t, f.todoID, child.StatusID, "subtask must be born in the project default status")
				assert.NotEqual(t, f.inProgress, child.StatusID, "parent status must not leak into the child")

				// Verify persisted with the same status.
				stored := repo.items[child.ID]
				require.NotNil(t, stored)
				assert.Equal(t, f.todoID, stored.StatusID)
			},
		},
		{
			// Regression test for #d13fe920: create_subtask used to silently
			// drop assignee_id/assignee_type/labels/custom_fields/due_date/
			// estimated_hours — the child was always born unassigned to the
			// creator with empty labels regardless of what was passed. This
			// mirrors CreateTask's field-level contract.
			name: "assignee/labels/custom_fields/due_date/estimated_hours are honoured",
			setup: func(f subtaskFixture, _ *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				assigneeID := uuid.New()
				due := frozenTime.Add(48 * time.Hour)
				hours := 3.5
				return f.parentID, CreateSubtaskInput{
					Title:          "Delegated child",
					Priority:       domain.PriorityMedium,
					AssigneeID:     &assigneeID,
					AssigneeType:   domain.AssigneeTypeAgent,
					Labels:         []string{"a", "b"},
					CustomFields:   json.RawMessage(`{"k":"v"}`),
					DueDate:        &due,
					EstimatedHours: &hours,
				}
			},
			checkFunc: func(t *testing.T, f subtaskFixture, child *domain.Task, _ *MockTaskRepository) {
				require.NotNil(t, child.AssigneeID)
				assert.Equal(t, domain.AssigneeTypeAgent, child.AssigneeType)
				assert.Equal(t, pq.StringArray{"a", "b"}, child.Labels)
				assert.JSONEq(t, `{"k":"v"}`, string(child.CustomFields))
				require.NotNil(t, child.DueDate)
				assert.Equal(t, frozenTime.Add(48*time.Hour), *child.DueDate)
				require.NotNil(t, child.EstimatedHours)
				assert.Equal(t, 3.5, *child.EstimatedHours)
			},
		},
		{
			// Regression guard the other direction: omitting assignee_id must
			// keep today's behavior (unassigned, subject to applyAutoAssign) —
			// the fix must not force every subtask to require an assignee.
			name: "omitted assignee_id still defaults to unassigned",
			setup: func(f subtaskFixture, _ *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				return f.parentID, CreateSubtaskInput{
					Title:    "Undelegated child",
					Priority: domain.PriorityMedium,
				}
			},
			checkFunc: func(t *testing.T, f subtaskFixture, child *domain.Task, _ *MockTaskRepository) {
				assert.Nil(t, child.AssigneeID)
				assert.Equal(t, domain.AssigneeTypeUnassigned, child.AssigneeType)
				assert.Empty(t, child.Labels)
			},
		},
		{
			name: "explicit status_id is honoured",
			setup: func(f subtaskFixture, _ *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				return f.parentID, CreateSubtaskInput{
					Title:    "Child task",
					Priority: domain.PriorityMedium,
					StatusID: &f.inProgress,
				}
			},
			checkFunc: func(t *testing.T, f subtaskFixture, child *domain.Task, _ *MockTaskRepository) {
				assert.Equal(t, f.inProgress, child.StatusID)
			},
		},
		{
			name: "explicit review status is rejected",
			setup: func(f subtaskFixture, _ *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				return f.parentID, CreateSubtaskInput{
					Title:    "Child task",
					Priority: domain.PriorityMedium,
					StatusID: &f.reviewID,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "status_id from another project is rejected",
			setup: func(f subtaskFixture, statusRepo *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				foreign := uuid.New()
				statusRepo.items[foreign] = &domain.TaskStatus{
					ID: foreign, ProjectID: uuid.New(), Slug: "todo",
					Category: domain.StatusCategoryTodo,
				}
				return f.parentID, CreateSubtaskInput{
					Title:    "Child task",
					Priority: domain.PriorityMedium,
					StatusID: &foreign,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "project without a default status is rejected",
			setup: func(f subtaskFixture, statusRepo *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				delete(statusRepo.items, f.todoID)
				return f.parentID, CreateSubtaskInput{
					Title:    "Child task",
					Priority: domain.PriorityMedium,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "parent not found",
			setup: func(_ subtaskFixture, _ *MockTaskStatusRepository) (uuid.UUID, CreateSubtaskInput) {
				return uuid.New(), CreateSubtaskInput{
					Title:    "Orphan child",
					Priority: domain.PriorityLow,
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, statusRepo := setupTaskService()
			ctx := context.Background()
			f := newSubtaskFixture(taskRepo, statusRepo)
			parentID, input := tt.setup(f, statusRepo)
			// An agent-typed assignee must exist in the directory before the
			// tenancy guard will let it hold a task.
			if input.AssigneeID != nil && input.AssigneeType != domain.AssigneeTypeUser {
				seedTestAgents(t, svc, *input.AssigneeID)
			}

			child, err := svc.CreateSubtask(ctx, parentID, input)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, child)
			} else {
				require.NoError(t, err)
				require.NotNil(t, child)
				if tt.checkFunc != nil {
					tt.checkFunc(t, f, child, taskRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskService_CreateSubtask_Notify — task #9304f2cc: CreateSubtask used to
// skip notifyAssignee/notifyAssignedAgent entirely, so a subtask's assignee
// never heard about it. These mirror the Create() notification tests above
// one-for-one so the subtask code path can't silently diverge again.
// ---------------------------------------------------------------------------

// setupTaskServiceForSubtaskNotify wires a task service with agent-push +
// in-app notification fakes plus the tenancy directories CreateSubtask's
// assignee enrolment needs, and returns the statusRepo subtaskFixture requires.
func setupTaskServiceForSubtaskNotify() (*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockNotificationService, *MockAgentNotifyService) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	notifySvc := NewMockNotificationService()
	agentNotify := NewMockAgentNotifyService()
	opts := append(wireTenancyDeps(NewMockProjectRepository(), NewMockAgentRepository()),
		WithNotificationService(notifySvc),
		WithAgentNotifyService(agentNotify),
	)
	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo, opts...).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, notifySvc, agentNotify
}

func TestTaskService_CreateSubtask_UserAssignee_DispatchesTargetedNotification(t *testing.T) {
	svc, taskRepo, statusRepo, notifySvc, agentNotify := setupTaskServiceForSubtaskNotify()
	ctx := context.Background()
	f := newSubtaskFixture(taskRepo, statusRepo)

	assignee := uuid.New()
	child, err := svc.CreateSubtask(ctx, f.parentID, CreateSubtaskInput{
		Title:        "Child task",
		Priority:     domain.PriorityMedium,
		AssigneeID:   &assignee,
		AssigneeType: domain.AssigneeTypeUser,
	})
	require.NoError(t, err)
	require.NotNil(t, child)

	require.Len(t, notifySvc.Calls(), 1)
	call := notifySvc.Calls()[0]
	assert.Equal(t, "task.assigned", call.EventType)
	require.NotNil(t, call.TargetUserID, "subtask assignment must be targeted, not broadcast")
	assert.Equal(t, assignee, *call.TargetUserID)
	assert.Empty(t, agentNotify.Calls())
}

func TestTaskService_CreateSubtask_AgentAssignee_NotifiesViaPush(t *testing.T) {
	svc, taskRepo, statusRepo, notifySvc, agentNotify := setupTaskServiceForSubtaskNotify()
	ctx := context.Background()
	f := newSubtaskFixture(taskRepo, statusRepo)

	assignee := uuid.New()
	seedTestAgents(t, svc, assignee)
	child, err := svc.CreateSubtask(ctx, f.parentID, CreateSubtaskInput{
		Title:        "Child task",
		Priority:     domain.PriorityMedium,
		AssigneeID:   &assignee,
		AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)
	require.NotNil(t, child)

	var found bool
	for _, call := range agentNotify.Calls() {
		if call.EventType == "task.assigned" && call.AgentID == assignee {
			found = true
		}
	}
	assert.True(t, found, "expected task.assigned push to the agent assignee")
	assert.Empty(t, notifySvc.Calls(), "an agent assignee must go through the push path, not the in-app table")
}

func TestTaskService_CreateSubtask_SelfAssign_NoNotification(t *testing.T) {
	svc, taskRepo, statusRepo, notifySvc, agentNotify := setupTaskServiceForSubtaskNotify()
	self := uuid.New()
	ctx := actorctx.WithActor(context.Background(), self, domain.ActorTypeUser)
	f := newSubtaskFixture(taskRepo, statusRepo)

	child, err := svc.CreateSubtask(ctx, f.parentID, CreateSubtaskInput{
		Title:        "Child task",
		Priority:     domain.PriorityMedium,
		AssigneeID:   &self,
		AssigneeType: domain.AssigneeTypeUser,
	})
	require.NoError(t, err)
	require.NotNil(t, child)

	assert.Empty(t, notifySvc.Calls(), "must not notify the actor about assigning a subtask to themselves")
	assert.Empty(t, agentNotify.Calls())
}

func TestTaskService_CreateSubtask_NoAssignee_NoNotification(t *testing.T) {
	svc, taskRepo, statusRepo, notifySvc, agentNotify := setupTaskServiceForSubtaskNotify()
	ctx := context.Background()
	f := newSubtaskFixture(taskRepo, statusRepo)

	child, err := svc.CreateSubtask(ctx, f.parentID, CreateSubtaskInput{
		Title:    "Unassigned child",
		Priority: domain.PriorityMedium,
	})
	require.NoError(t, err)
	require.NotNil(t, child)

	assert.Empty(t, notifySvc.Calls())
	assert.Empty(t, agentNotify.Calls())
}

// ---------------------------------------------------------------------------
// TestTaskService_Delete
// ---------------------------------------------------------------------------

func TestTaskService_Delete(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockTaskRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "success",
			setup: func(repo *MockTaskRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Task{ID: id, Title: "To be deleted"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found returns error",
			setup: func(_ *MockTaskRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, _ := setupTaskService()
			ctx := context.Background()
			id := tt.setup(taskRepo)

			err := svc.Delete(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				// Verify removed from repo.
				_, exists := taskRepo.items[id]
				assert.False(t, exists, "task should be deleted from repo")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskService_applyAutoAssign
// ---------------------------------------------------------------------------

// setupTaskServiceWithRules returns a taskService wired to a MockRulesService.
func setupTaskServiceWithRules(rules *domain.EffectiveAssignmentRules) (*taskService, *MockTaskRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	mockRules := NewMockRulesService(rules)

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithRulesConfigService(mockRules),
	).(*taskService)

	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo
}

func TestTaskService_applyAutoAssign(t *testing.T) {
	agentIDStr := uuid.New()
	agentIDStr2 := uuid.New()

	tests := []struct {
		name             string
		rules            *domain.EffectiveAssignmentRules
		task             *domain.Task
		wantAssigned     bool
		wantAssigneeID   *uuid.UUID
		wantAssigneeType domain.AssigneeType
	}{
		{
			name:  "no rules configured - task stays unassigned",
			rules: nil,
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Task without rules",
				Priority:     domain.PriorityHigh,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned: false,
		},
		{
			name: "by_priority match - assigns the mapped agent",
			rules: &domain.EffectiveAssignmentRules{
				ByPriority: map[string]domain.EffectiveAssignmentRule{
					"high": {Value: agentIDStr.String(), Source: "workspace"},
				},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "High priority task",
				Priority:     domain.PriorityHigh,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "by_priority no match - falls back to default_assignee",
			rules: &domain.EffectiveAssignmentRules{
				ByPriority: map[string]domain.EffectiveAssignmentRule{
					"critical": {Value: agentIDStr.String(), Source: "workspace"},
				},
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: agentIDStr2.String(), Source: "workspace"},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Medium priority task",
				Priority:     domain.PriorityMedium,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr2,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "no priority match and no default - falls back to fallback_chain",
			rules: &domain.EffectiveAssignmentRules{
				FallbackChain: []string{agentIDStr.String()},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Low priority task",
				Priority:     domain.PriorityLow,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "by_priority takes precedence over default_assignee and fallback_chain",
			rules: &domain.EffectiveAssignmentRules{
				ByPriority: map[string]domain.EffectiveAssignmentRule{
					"high": {Value: agentIDStr.String(), Source: "project"},
				},
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: agentIDStr2.String(), Source: "workspace"},
				FallbackChain:   []string{agentIDStr2.String()},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "High priority with all rules",
				Priority:     domain.PriorityHigh,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "rules with invalid UUID in by_priority - falls back to default_assignee",
			rules: &domain.EffectiveAssignmentRules{
				ByPriority: map[string]domain.EffectiveAssignmentRule{
					"high": {Value: "not-a-uuid", Source: "workspace"},
				},
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: agentIDStr2.String(), Source: "workspace"},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Invalid UUID in by_priority",
				Priority:     domain.PriorityHigh,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr2,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "task already has assignee - skipped by Create guard",
			rules: &domain.EffectiveAssignmentRules{
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: agentIDStr.String(), Source: "workspace"},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Already assigned task",
				Priority:     domain.PriorityMedium,
				AssigneeID:   &agentIDStr2,
				AssigneeType: domain.AssigneeTypeAgent,
			},
			// applyAutoAssign is not called when AssigneeType != unassigned.
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr2,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "empty fallback_chain - task stays unassigned",
			rules: &domain.EffectiveAssignmentRules{
				FallbackChain: []string{},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Empty fallback chain",
				Priority:     domain.PriorityLow,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned: false,
		},
		{
			name: "by_type match via label - assigns the mapped agent",
			rules: &domain.EffectiveAssignmentRules{
				ByType: map[string]domain.EffectiveAssignmentRule{
					"bug": {Value: agentIDStr.String(), Source: "project"},
				},
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: agentIDStr2.String(), Source: "workspace"},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Bug task with label",
				Priority:     domain.PriorityMedium,
				Labels:       pq.StringArray{"bug", "frontend"},
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "by_type no label match - falls back to by_priority",
			rules: &domain.EffectiveAssignmentRules{
				ByType: map[string]domain.EffectiveAssignmentRule{
					"bug": {Value: agentIDStr.String(), Source: "project"},
				},
				ByPriority: map[string]domain.EffectiveAssignmentRule{
					"high": {Value: agentIDStr2.String(), Source: "workspace"},
				},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Feature task with no matching label",
				Priority:     domain.PriorityHigh,
				Labels:       pq.StringArray{"feature"},
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr2,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "by_type takes precedence over by_priority when label matches",
			rules: &domain.EffectiveAssignmentRules{
				ByType: map[string]domain.EffectiveAssignmentRule{
					"bug": {Value: agentIDStr.String(), Source: "project"},
				},
				ByPriority: map[string]domain.EffectiveAssignmentRule{
					"high": {Value: agentIDStr2.String(), Source: "workspace"},
				},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "High priority bug",
				Priority:     domain.PriorityHigh,
				Labels:       pq.StringArray{"bug"},
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "by_type with prefixed agent:<uuid> format",
			rules: &domain.EffectiveAssignmentRules{
				ByType: map[string]domain.EffectiveAssignmentRule{
					"security": {Value: "agent:" + agentIDStr.String(), Source: "project"},
				},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Security task",
				Priority:     domain.PriorityMedium,
				Labels:       pq.StringArray{"security"},
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "prefixed agent:<uuid> format - assigns agent correctly",
			rules: &domain.EffectiveAssignmentRules{
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "agent:" + agentIDStr.String(), Source: "project"},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Prefixed agent format",
				Priority:     domain.PriorityMedium,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "prefixed user:<uuid> format - assigns user correctly",
			rules: &domain.EffectiveAssignmentRules{
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "user:" + agentIDStr.String(), Source: "project"},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Prefixed user format",
				Priority:     domain.PriorityMedium,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeUser,
		},
		{
			name: "prefixed agent:<uuid> in by_priority - assigns correctly",
			rules: &domain.EffectiveAssignmentRules{
				ByPriority: map[string]domain.EffectiveAssignmentRule{
					"urgent": {Value: "agent:" + agentIDStr.String(), Source: "project"},
				},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Prefixed by_priority",
				Priority:     domain.PriorityUrgent,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr,
			wantAssigneeType: domain.AssigneeTypeAgent,
		},
		{
			name: "prefixed user:<uuid> in fallback_chain - assigns user correctly",
			rules: &domain.EffectiveAssignmentRules{
				FallbackChain: []string{"user:" + agentIDStr2.String()},
			},
			task: &domain.Task{
				ProjectID:    uuid.New(),
				StatusID:     uuid.New(),
				Title:        "Prefixed fallback chain",
				Priority:     domain.PriorityLow,
				AssigneeType: domain.AssigneeTypeUnassigned,
			},
			wantAssigned:     true,
			wantAssigneeID:   &agentIDStr2,
			wantAssigneeType: domain.AssigneeTypeUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo := setupTaskServiceWithRules(tt.rules)
			ctx := context.Background()
			// The rule engine hands back principal ids; the tenancy guard then
			// insists an AGENT id is really in the agent directory. Seeding the
			// user-typed cases too would make resolveAssigneeType call them agents,
			// so seed only where the expectation is an agent. User-typed ids are
			// cleared by the workspace membership reader instead.
			if tt.wantAssigneeType == domain.AssigneeTypeAgent {
				seedTestAgents(t, svc, agentIDStr, agentIDStr2)
			}

			err := svc.Create(ctx, tt.task)
			require.NoError(t, err, "Create should never fail due to rules errors")

			stored, err := taskRepo.GetByID(ctx, tt.task.ID)
			require.NoError(t, err)
			require.NotNil(t, stored)

			if tt.wantAssigned {
				require.NotNil(t, stored.AssigneeID, "expected assignee to be set")
				assert.Equal(t, *tt.wantAssigneeID, *stored.AssigneeID)
				assert.Equal(t, tt.wantAssigneeType, stored.AssigneeType)
			} else if tt.task.AssigneeType == domain.AssigneeTypeUnassigned || tt.task.AssigneeType == "" {
				assert.Nil(t, stored.AssigneeID, "expected task to remain unassigned")
				assert.Equal(t, domain.AssigneeTypeUnassigned, stored.AssigneeType)
			}
		})
	}
}

// TestTaskService_Create_ExplicitAssigneeIDNotClobbered is a regression test for the
// $59 incident where create_task(assignee_id=X, assignee_type="") silently assigned
// to the auto-assign default instead of X. The service guard must skip applyAutoAssign
// when AssigneeID is already non-nil, even if AssigneeType is "unassigned".
func TestTaskService_Create_ExplicitAssigneeIDNotClobbered(t *testing.T) {
	explicitAssignee := uuid.New()
	autoAssignDefault := uuid.New()

	rules := &domain.EffectiveAssignmentRules{
		DefaultAssignee: &domain.EffectiveAssignmentRule{Value: autoAssignDefault.String(), Source: "workspace"},
	}
	svc, taskRepo := setupTaskServiceWithRules(rules)
	ctx := context.Background()

	// The explicit assignee must be a REAL agent: an id naming no principal is now
	// refused outright (AssigneeUnresolvedError), so an unseeded uuid would fail this
	// test on a completely different rule and stop exercising auto-assign at all.
	//
	// Seeding does NOT weaken what is under test. Create's auto-assign guard reads
	// `(type == unassigned || type == "") && AssigneeID == nil`, and it runs BEFORE
	// resolveAssigneeType corrects the type — so at that moment the type below is
	// still "unassigned" and the ONLY thing stopping the clobber is the AssigneeID
	// clause, exactly as before. Proven by mutation: delete `&& task.AssigneeID == nil`
	// and this test fails.
	// autoAssignDefault is seeded too, deliberately. If it were not a real agent, a
	// regression that let auto-assign clobber the explicit id would be caught by the
	// unresolved-assignee guard instead of by this test's own assertion — the test
	// would still go red, but for the wrong reason, and it would stop proving
	// anything about clobbering the moment that guard changed.
	seedTestAgents(t, svc, explicitAssignee, autoAssignDefault)

	task := &domain.Task{
		ProjectID: uuid.New(),
		StatusID:  uuid.New(),
		Title:     "Explicit assignee should not be clobbered",
		Priority:  domain.PriorityMedium,
		// assignee_id provided but assignee_type left as "unassigned" — the old
		// handler default when type was omitted from the request.
		AssigneeID:   &explicitAssignee,
		AssigneeType: domain.AssigneeTypeUnassigned,
	}

	require.NoError(t, svc.Create(ctx, task))

	stored, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)

	assert.Equal(t, explicitAssignee, *stored.AssigneeID,
		"explicit assignee_id must be preserved; auto-assign must not clobber it")
	assert.NotEqual(t, autoAssignDefault, *stored.AssigneeID,
		"auto-assign default must not override an explicit assignee_id")
}

func TestTaskService_applyAutoAssign_RulesServiceError(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()

	mockRules := NewMockRulesService(nil)
	mockRules.errToReturn = fmt.Errorf("database unavailable")

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithRulesConfigService(mockRules),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }

	task := &domain.Task{
		ProjectID:    uuid.New(),
		StatusID:     uuid.New(),
		Title:        "Task with broken rules svc",
		Priority:     domain.PriorityHigh,
		AssigneeType: domain.AssigneeTypeUnassigned,
	}

	// Create must succeed even when rules service returns an error.
	err := svc.Create(context.Background(), task)
	require.NoError(t, err, "Create must not fail when rules service errors")

	stored, err := taskRepo.GetByID(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Nil(t, stored.AssigneeID, "task should remain unassigned when rules lookup fails")
}

// ---------------------------------------------------------------------------
// TestTaskService_List
// ---------------------------------------------------------------------------

func TestTaskService_List(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockTaskRepository) uuid.UUID
		filter  repository.TaskFilter
		pg      pagination.Params
		wantLen int
	}{
		{
			name: "with matching tasks",
			setup: func(repo *MockTaskRepository) uuid.UUID {
				projID := uuid.New()
				for i := 0; i < 3; i++ {
					id := uuid.New()
					repo.items[id] = &domain.Task{ID: id, ProjectID: projID, Title: "Task"}
				}
				// Task in another project — should not be returned.
				other := uuid.New()
				repo.items[other] = &domain.Task{ID: other, ProjectID: uuid.New(), Title: "Other project"}
				return projID
			},
			filter:  repository.TaskFilter{},
			pg:      pagination.Params{Page: 1, PageSize: 50},
			wantLen: 3,
		},
		{
			name: "empty result",
			setup: func(_ *MockTaskRepository) uuid.UUID {
				return uuid.New()
			},
			filter:  repository.TaskFilter{},
			pg:      pagination.Params{Page: 1, PageSize: 50},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, _ := setupTaskService()
			ctx := context.Background()
			projID := tt.setup(taskRepo)

			page, err := svc.List(ctx, projID, tt.filter, tt.pg)

			require.NoError(t, err)
			require.NotNil(t, page)
			assert.Len(t, page.Items, tt.wantLen)
			assert.Equal(t, tt.wantLen, page.TotalCount)
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskService_List_RevisionValidation — ADR-0004 stale-cursor rejection
// ---------------------------------------------------------------------------

// stubTaskListRevisionRepo is a scripted repository.TaskListRevisionRepository:
// GetRevision always returns the configured value, ignoring projectID (these
// tests only ever exercise one project at a time).
type stubTaskListRevisionRepo struct {
	revision int64
	err      error
	calls    int
}

func (r *stubTaskListRevisionRepo) GetRevision(_ context.Context, _ uuid.UUID) (int64, error) {
	r.calls++
	return r.revision, r.err
}

func setupTaskServiceWithListRevisionRepo(revision int64) (*taskService, *MockTaskRepository, *stubTaskListRevisionRepo) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	revRepo := &stubTaskListRevisionRepo{revision: revision}

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithTaskListRevisionRepo(revRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, revRepo
}

// TestTaskService_List_RevisionValidation_FreshWalk_StampsCurrentRevision is
// page 1 of a new walk: no list_revision sent, no staleness check possible or
// needed (ADR-0004 Decision 3) — the response is stamped with whatever the
// project's current revision actually is, for the caller to echo back later.
func TestTaskService_List_RevisionValidation_FreshWalk_StampsCurrentRevision(t *testing.T) {
	svc, taskRepo, _ := setupTaskServiceWithListRevisionRepo(47)
	ctx := context.Background()
	projID := uuid.New()
	taskRepo.items[uuid.New()] = &domain.Task{ID: uuid.New(), ProjectID: projID}

	page, err := svc.List(ctx, projID, repository.TaskFilter{}, pagination.Params{Page: 1, PageSize: 50})

	require.NoError(t, err)
	assert.Equal(t, int64(47), page.ListRevision)
}

// TestTaskService_List_RevisionValidation_MatchingRevision_Succeeds is page 2+
// of a walk where nothing changed: the caller's list_revision matches the
// project's current revision exactly, so the page proceeds normally and is
// re-stamped with the same value.
func TestTaskService_List_RevisionValidation_MatchingRevision_Succeeds(t *testing.T) {
	svc, taskRepo, _ := setupTaskServiceWithListRevisionRepo(47)
	ctx := context.Background()
	projID := uuid.New()
	taskRepo.items[uuid.New()] = &domain.Task{ID: uuid.New(), ProjectID: projID}

	page, err := svc.List(ctx, projID, repository.TaskFilter{}, pagination.Params{Page: 2, PageSize: 50, ListRevision: 47})

	require.NoError(t, err)
	assert.Equal(t, int64(47), page.ListRevision)
}

// TestTaskService_List_RevisionValidation_StaleRevision_Rejected is the core
// acceptance case: a cursor issued before a mutation is presented on a later
// page, after tasks/artifacts/vcs_links changed for this project (current
// revision has moved from 47 to 52). List must reject outright — no silent
// fallback to page 1, no silently serving the requested offset against the
// new state (ADR-0004 Decision 4's explicit "what must NOT happen").
func TestTaskService_List_RevisionValidation_StaleRevision_Rejected(t *testing.T) {
	svc, taskRepo, revRepo := setupTaskServiceWithListRevisionRepo(52)
	ctx := context.Background()
	projID := uuid.New()
	taskRepo.items[uuid.New()] = &domain.Task{ID: uuid.New(), ProjectID: projID}

	page, err := svc.List(ctx, projID, repository.TaskFilter{}, pagination.Params{Page: 2, PageSize: 50, ListRevision: 47})

	require.Error(t, err)
	assert.Nil(t, page)
	var staleErr *ListRevisionStaleError
	require.ErrorAs(t, err, &staleErr)
	assert.Equal(t, int64(47), staleErr.Requested)
	assert.Equal(t, int64(52), staleErr.Current)
	// The repo must actually have been consulted — a rejection based on a
	// zero-value default would be indistinguishable from a real check.
	assert.Equal(t, 1, revRepo.calls)
}

// TestTaskService_List_RevisionValidation_GetRevisionError_Propagates ensures
// a failure reading the revision (e.g. a DB error) surfaces as a real error
// rather than being swallowed into "no check" or a false positive/negative.
func TestTaskService_List_RevisionValidation_GetRevisionError_Propagates(t *testing.T) {
	svc, _, revRepo := setupTaskServiceWithListRevisionRepo(0)
	revRepo.err = errors.New("connection reset")
	ctx := context.Background()

	page, err := svc.List(ctx, uuid.New(), repository.TaskFilter{}, pagination.Params{Page: 1, PageSize: 50})

	require.Error(t, err)
	assert.Nil(t, page)
	assert.Contains(t, err.Error(), "connection reset")
}

func TestListRevisionStaleError_Error_MessageNamesBothRevisions(t *testing.T) {
	err := &ListRevisionStaleError{Requested: 47, Current: 52}
	assert.Contains(t, err.Error(), "47")
	assert.Contains(t, err.Error(), "52")
}

// TestTaskService_List_RevisionValidation_NoRepoWired_BehavesAsBeforeThisFeature
// is the backward-compat guarantee: an existing caller of NewTaskService that
// never adds WithTaskListRevisionRepo gets exactly the pre-ADR-0004 behavior
// — no check, no error, ListRevision left at its zero value — regardless of
// what the caller sends in list_revision.
func TestTaskService_List_RevisionValidation_NoRepoWired_BehavesAsBeforeThisFeature(t *testing.T) {
	svc, taskRepo, _ := setupTaskService()
	ctx := context.Background()
	projID := uuid.New()
	taskRepo.items[uuid.New()] = &domain.Task{ID: uuid.New(), ProjectID: projID}

	page, err := svc.List(ctx, projID, repository.TaskFilter{}, pagination.Params{Page: 2, PageSize: 50, ListRevision: 999})

	require.NoError(t, err)
	assert.Equal(t, int64(0), page.ListRevision)
}

// ---------------------------------------------------------------------------
// TestTaskService_MoveTask_TransitionGate — R-D-C acceptance tests
// 5 required cases from the task spec.
// ---------------------------------------------------------------------------

// setupTransitionGateService builds a taskService wired with the given workflow response
// and a project repo so applyTransitionGate can fetch the workflow config.
func setupTransitionGateService(wfResp *domain.WorkflowRulesResponse) (
	*taskService, *MockTaskRepository, *MockTaskStatusRepository,
) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	projRepo := NewMockProjectRepository()

	mockRules := NewMockRulesService(nil).WithWorkflowRules(wfResp)

	projectID := uuid.New()
	workspaceID := testDefaultWorkspaceID // single-tenant: the assignee guard resolves every principal against this one workspace
	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}

	// Pre-register a task so tests can set ProjectID via the shared projectID.
	// Tests override this in their own setup.
	_ = projectID

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithRulesConfigService(mockRules),
		WithProjectRepo(projRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo
}

func TestTaskService_MoveTask_TransitionGate(t *testing.T) {
	projectID := uuid.New()
	workspaceID := testDefaultWorkspaceID // single-tenant: the assignee guard resolves every principal against this one workspace

	// makeTask creates a task in a given from-status and registers the target status.
	// fromStatusName and toStatusName are set as status.Name for config lookup.
	makeSetup := func(
		taskRepo *MockTaskRepository,
		statusRepo *MockTaskStatusRepository,
		projRepo *MockProjectRepository,
		fromCat domain.StatusCategory,
		fromName string,
		toCat domain.StatusCategory,
		toName string,
	) (taskID, toStatusID uuid.UUID) {
		fromStatusID := uuid.New()
		toStatusID = uuid.New()
		taskID = uuid.New()

		statusRepo.items[fromStatusID] = &domain.TaskStatus{ID: fromStatusID, ProjectID: projectID, Category: fromCat, Name: fromName, Slug: fromName}
		statusRepo.items[toStatusID] = &domain.TaskStatus{ID: toStatusID, ProjectID: projectID, Category: toCat, Name: toName, Slug: toName}
		taskRepo.items[taskID] = &domain.Task{
			ID: taskID, ProjectID: projectID,
			StatusID: fromStatusID, Title: "test task",
		}
		projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
		return taskID, toStatusID
	}

	// Case 1: project without Transitions config → MoveTask OK (allow-all, no behavior change).
	t.Run("case1: empty config → allow-all (no behavior change)", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				EnforcementMode: domain.RuleConfigEnforcementStrict,
				// Transitions intentionally empty
			},
		}
		svc, taskRepo, statusRepo := setupTransitionGateService(wfResp)
		projRepo := svc.projectRepo.(*MockProjectRepository)
		taskID, toStatusID := makeSetup(taskRepo, statusRepo, projRepo,
			domain.StatusCategoryInProgress, "in_progress",
			domain.StatusCategoryDone, "done",
		)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toStatusID})
		require.NoError(t, err, "empty Transitions config must allow all moves")
	})

	// Case 2: advisory + allowed transition → OK, no violation in activity log.
	t.Run("case2: advisory + allowed transition → OK", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				EnforcementMode: domain.RuleConfigEnforcementAdvisory,
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {Allowed: []string{"review", "done"}},
				},
			},
		}
		svc, taskRepo, statusRepo := setupTransitionGateService(wfResp)
		projRepo := svc.projectRepo.(*MockProjectRepository)
		taskID, toStatusID := makeSetup(taskRepo, statusRepo, projRepo,
			domain.StatusCategoryInProgress, "in_progress",
			domain.StatusCategoryDone, "done",
		)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toStatusID})
		require.NoError(t, err)
	})

	// Case 3: advisory + forbidden transition → move allowed + violation in activity log.
	t.Run("case3: advisory + forbidden transition → OK + violation logged", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				EnforcementMode: domain.RuleConfigEnforcementAdvisory,
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {Allowed: []string{"review"}}, // "done" is NOT allowed
				},
			},
		}
		svc, taskRepo, statusRepo := setupTransitionGateService(wfResp)
		projRepo := svc.projectRepo.(*MockProjectRepository)
		taskID, toStatusID := makeSetup(taskRepo, statusRepo, projRepo,
			domain.StatusCategoryInProgress, "in_progress",
			domain.StatusCategoryDone, "done",
		)
		activityRepo := svc.activityRepo.(*MockActivityLogRepository)

		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toStatusID})
		require.NoError(t, err, "advisory violation must allow the move")

		// Verify violation was logged in activity.
		activityRepo.mu.Lock()
		defer activityRepo.mu.Unlock()
		var found bool
		for _, entry := range activityRepo.items {
			if entry.Action == "task.transition_violation" {
				found = true
				break
			}
		}
		assert.True(t, found, "advisory violation must be recorded in activity log")
	})

	// Case 4: strict + forbidden transition → 403 blocked.
	t.Run("case4: strict + forbidden transition → 403 blocked", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				EnforcementMode: domain.RuleConfigEnforcementStrict,
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {Allowed: []string{"review"}}, // "done" is NOT allowed
				},
			},
		}
		svc, taskRepo, statusRepo := setupTransitionGateService(wfResp)
		projRepo := svc.projectRepo.(*MockProjectRepository)
		taskID, toStatusID := makeSetup(taskRepo, statusRepo, projRepo,
			domain.StatusCategoryInProgress, "in_progress",
			domain.StatusCategoryDone, "done",
		)
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toStatusID})
		require.Error(t, err)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusForbidden, apiErr.Code)
		assert.Equal(t, "workflow_transition_blocked", apiErr.Message)
	})

	// Case 5: system actor in strict mode → exempt (move succeeds).
	t.Run("case5: system actor in strict mode → exempt", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				EnforcementMode: domain.RuleConfigEnforcementStrict,
				Transitions: map[string]domain.TransitionRule{
					"in_progress": {Allowed: []string{"review"}}, // "done" not in allowed
				},
				// EnforceSystemActors defaults to false → system is exempt
			},
		}
		svc, taskRepo, statusRepo := setupTransitionGateService(wfResp)
		projRepo := svc.projectRepo.(*MockProjectRepository)
		taskID, toStatusID := makeSetup(taskRepo, statusRepo, projRepo,
			domain.StatusCategoryInProgress, "in_progress",
			domain.StatusCategoryDone, "done",
		)
		// Simulate auto_transition context: system actor
		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeSystem)
		err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toStatusID})
		require.NoError(t, err, "system actor must be exempt from strict enforcement by default")
	})

	// Case 6: regression — capitalized display Name vs lowercase Slug.
	// Config keys are slugs (e.g. "todo"); TaskStatus.Name is the display name ("Todo").
	// Before the fix, cfg.Transitions["Todo"] always missed → gate never fired.
	// After the fix, cfg.Transitions["todo"] (via .Slug) must hit and enforce correctly.
	t.Run("case6: capitalized Name with lowercase Slug → slug lookup enforces correctly", func(t *testing.T) {
		wfResp := &domain.WorkflowRulesResponse{
			WorkflowRulesConfig: domain.WorkflowRulesConfig{
				EnforcementMode: domain.RuleConfigEnforcementStrict,
				Transitions: map[string]domain.TransitionRule{
					"todo": {Allowed: []string{"in_progress", "backlog", "triage"}},
				},
			},
		}
		svc, taskRepo, statusRepo := setupTransitionGateService(wfResp)
		projRepo := svc.projectRepo.(*MockProjectRepository)

		fromStatusID := uuid.New()
		toStatusID := uuid.New()
		taskID6 := uuid.New()
		// Name = display name (capitalised), Slug = config key (lowercase).
		statusRepo.items[fromStatusID] = &domain.TaskStatus{
			ID: fromStatusID, ProjectID: projectID, Category: domain.StatusCategoryTodo,
			Name: "Todo", Slug: "todo",
		}
		statusRepo.items[toStatusID] = &domain.TaskStatus{
			ID: toStatusID, ProjectID: projectID, Category: domain.StatusCategoryDone,
			Name: "Done", Slug: "done", // "done" not in allowed → should be blocked
		}
		taskRepo.items[taskID6] = &domain.Task{
			ID: taskID6, ProjectID: projectID,
			StatusID: fromStatusID, Title: "slug regression task",
		}
		projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}

		ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
		err := svc.MoveTask(ctx, taskID6, MoveTaskInput{StatusID: &toStatusID})
		require.Error(t, err, "todo→done must be blocked in strict mode via slug lookup")
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusForbidden, apiErr.Code)
		assert.Equal(t, "workflow_transition_blocked", apiErr.Message)
	})
}

// ---------------------------------------------------------------------------
// TestTaskService_Update_DelegationLevel*
// ---------------------------------------------------------------------------

// setupTaskServiceForDelegation returns a taskService wired with notify + project repo for Update delegation tests.
func setupTaskServiceForDelegation() (*taskService, *MockTaskRepository, *MockActivityLogRepository, *MockAgentNotifyService, *MockProjectRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	notifySvc := NewMockAgentNotifyService()
	projRepo := NewMockProjectRepository()

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithAgentNotifyService(notifySvc),
		WithProjectRepo(projRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, activityRepo, notifySvc, projRepo
}

func TestTaskService_Update_DelegationLevelLogged(t *testing.T) {
	svc, taskRepo, activityRepo, _, projRepo := setupTaskServiceForDelegation()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		DelegationLevel: domain.DelegationLevelAuto,
	}

	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		DelegationLevel: domain.DelegationLevelSupervised,
	})
	require.NoError(t, err)

	activityRepo.mu.RLock()
	var found *domain.ActivityLog
	for _, entry := range activityRepo.items {
		if entry.Action == "task.updated" {
			found = entry
			break
		}
	}
	activityRepo.mu.RUnlock()

	require.NotNil(t, found, "expected task.updated activity log entry")
	assert.Contains(t, string(found.Changes), `"delegation_level"`)
	assert.Contains(t, string(found.Changes), `"auto"`)
	assert.Contains(t, string(found.Changes), `"supervised"`)
}

func TestTaskService_Update_DelegationLevelNotifiesAgent(t *testing.T) {
	svc, taskRepo, _, notifySvc, projRepo := setupTaskServiceForDelegation()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	agentID := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		DelegationLevel: domain.DelegationLevelAuto,
		AssigneeID:      &agentID,
		AssigneeType:    domain.AssigneeTypeAgent,
	}

	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		DelegationLevel: domain.DelegationLevelSupervised,
		AssigneeID:      &agentID,
		AssigneeType:    domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	var found bool
	for _, call := range notifySvc.Calls() {
		if call.EventType == "task.delegation_changed" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected task.delegation_changed notification to agent")
}

func TestTaskService_Update_DelegationLevelNoNotifyIfUnchanged(t *testing.T) {
	svc, taskRepo, _, notifySvc, projRepo := setupTaskServiceForDelegation()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	agentID := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		DelegationLevel: domain.DelegationLevelAuto,
		AssigneeID:      &agentID,
		AssigneeType:    domain.AssigneeTypeAgent,
	}

	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		DelegationLevel: domain.DelegationLevelAuto,
		AssigneeID:      &agentID,
		AssigneeType:    domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	for _, call := range notifySvc.Calls() {
		assert.NotEqual(t, "task.delegation_changed", call.EventType,
			"must not notify task.delegation_changed when delegation_level is unchanged")
	}
}

// setupTaskServiceForAssigneeNotify wires a task service with both the
// agent-notify mock and the user-facing notification mock, so Update's
// notifyAssignee("task.assigned") call is observable.
func setupTaskServiceForAssigneeNotify() (*taskService, *MockTaskRepository, *MockNotificationService, *MockProjectRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	notifySvc := NewMockNotificationService()
	projRepo := NewMockProjectRepository()

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithNotificationService(notifySvc),
		WithProjectRepo(projRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, notifySvc, projRepo
}

func TestTaskService_Update_AssigneeChanged_DispatchesUserNotification(t *testing.T) {
	svc, taskRepo, notifySvc, projRepo := setupTaskServiceForAssigneeNotify()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	oldAssignee := uuid.New()
	newAssignee := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		AssigneeID: &oldAssignee, AssigneeType: domain.AssigneeTypeUser,
	}

	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		AssigneeID: &newAssignee, AssigneeType: domain.AssigneeTypeUser,
	})
	require.NoError(t, err)

	var call *domain.NotificationEvent
	for i, c := range notifySvc.Calls() {
		if c.EventType == "task.assigned" {
			call = &notifySvc.Calls()[i]
		}
	}
	require.NotNil(t, call, "expected task.assigned in-app notification when assignee changes via Update")
	assert.Contains(t, call.Title, "Task assigned:")
	require.NotNil(t, call.TargetUserID, "task.assigned notification must be targeted, not broadcast")
	assert.Equal(t, newAssignee, *call.TargetUserID)
	assert.Equal(t, newAssignee, call.Metadata["assignee_id"], "metadata must carry the assignee's identifier")
}

// TestTaskService_Update_AssigneeChangedToAgent_NoInAppNotification is the
// AC5 regression test: an agent assignee is woken via notifyAssignedAgent's
// push/SSE/callback path, not the in-app notifications table (which only
// holds user rows) — so Update must not also fire a targeted in-app event
// for an agent assignee.
func TestTaskService_Update_AssigneeChangedToAgent_NoInAppNotification(t *testing.T) {
	svc, taskRepo, notifySvc, projRepo := setupTaskServiceForAssigneeNotify()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	oldAssignee := uuid.New()
	newAssignee := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		AssigneeID: &oldAssignee, AssigneeType: domain.AssigneeTypeAgent,
	}
	// The new assignee must be a real agent of this project's workspace, or the
	// tenancy guard refuses the reassignment before any notification is dispatched.
	seedTestAgents(t, svc, oldAssignee, newAssignee)
	agentRepo, ok := svc.agentRepo.(*MockAgentRepository)
	require.True(t, ok)
	agentRepo.items[newAssignee].WorkspaceID = wsID
	agentRepo.items[oldAssignee].WorkspaceID = wsID

	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		AssigneeID: &newAssignee, AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	for _, call := range notifySvc.Calls() {
		assert.NotEqual(t, "task.assigned", call.EventType,
			"an agent assignee must go through notifyAssignedAgent's push path, not the in-app table")
	}
}

// TestTaskService_Update_SelfAssign_NoInAppNotification is the AC2 regression
// test: assigning a task to yourself must not notify you about your own action.
func TestTaskService_Update_SelfAssign_NoInAppNotification(t *testing.T) {
	svc, taskRepo, notifySvc, projRepo := setupTaskServiceForAssigneeNotify()

	self := uuid.New()
	ctx := actorctx.WithActor(context.Background(), self, domain.ActorTypeUser)

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	oldAssignee := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		AssigneeID: &oldAssignee, AssigneeType: domain.AssigneeTypeUser,
	}

	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		AssigneeID: &self, AssigneeType: domain.AssigneeTypeUser,
	})
	require.NoError(t, err)

	for _, call := range notifySvc.Calls() {
		assert.NotEqual(t, "task.assigned", call.EventType,
			"must not notify the actor about assigning a task to themselves")
	}
}

// TestTaskService_Update_AssigneeUnchanged_DoesNotDispatchNotification is a
// regression test for a pointer-vs-value comparison bug: existing and the
// incoming task come from two independent reads, so their AssigneeID
// pointers differ even when the UUID they point to is identical. Using two
// separately allocated pointers to the SAME value (not literally sharing one
// &var, which would mask the bug) reproduces that.
func TestTaskService_Update_AssigneeUnchanged_DoesNotDispatchNotification(t *testing.T) {
	svc, taskRepo, notifySvc, projRepo := setupTaskServiceForAssigneeNotify()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	assignee := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeAgent,
	}

	sameValueDifferentPointer := assignee
	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T changed",
		AssigneeID: &sameValueDifferentPointer, AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err)

	for _, call := range notifySvc.Calls() {
		assert.NotEqual(t, "task.assigned", call.EventType,
			"must not notify task.assigned when assignee value is unchanged, even via a different pointer")
	}
}

// TestTaskService_Create_AssigneeSet_DispatchesTargetedUserNotification covers
// the Create() call site — the same targeted contract as Update, checked
// separately since Create builds the task object rather than mutating a
// fetched one.
func TestTaskService_Create_AssigneeSet_DispatchesTargetedUserNotification(t *testing.T) {
	svc, _, notifySvc, projRepo := setupTaskServiceForAssigneeNotify()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	assignee := uuid.New()
	task := &domain.Task{
		ProjectID: projID, Title: "T",
		AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser,
	}
	require.NoError(t, svc.Create(ctx, task))

	require.Len(t, notifySvc.Calls(), 1)
	call := notifySvc.Calls()[0]
	assert.Equal(t, "task.assigned", call.EventType)
	require.NotNil(t, call.TargetUserID, "task.assigned on create must be targeted, not broadcast")
	assert.Equal(t, assignee, *call.TargetUserID)
}

// TestTaskService_AssignTask_DispatchesTargetedUserNotification covers the
// third call site — POST /tasks/{id}/assign.
func TestTaskService_AssignTask_DispatchesTargetedUserNotification(t *testing.T) {
	svc, taskRepo, notifySvc, projRepo := setupTaskServiceForAssigneeNotify()
	ctx := context.Background()

	projID := uuid.New()
	wsID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projID, Title: "T"}

	assignee := uuid.New()
	err := svc.AssignTask(ctx, taskID, AssignTaskInput{AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser})
	require.NoError(t, err)

	require.Len(t, notifySvc.Calls(), 1)
	call := notifySvc.Calls()[0]
	assert.Equal(t, "task.assigned", call.EventType)
	require.NotNil(t, call.TargetUserID, "task.assigned via AssignTask must be targeted, not broadcast")
	assert.Equal(t, assignee, *call.TargetUserID)
}

// ---------------------------------------------------------------------------
// TestTaskService_*Reviewer* — Reviewer field notification tests
// ---------------------------------------------------------------------------

// fakeUserNotifyService captures Notify() calls so tests can assert on
// EventType and TargetUserID without a real notificationService/DB.
type fakeUserNotifyService struct {
	mu    sync.Mutex
	calls []domain.NotificationEvent
}

func (f *fakeUserNotifyService) Notify(_ context.Context, event domain.NotificationEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, event)
}
func (f *fakeUserNotifyService) GetPreferences(context.Context, uuid.UUID) ([]domain.NotificationPreference, error) {
	return nil, nil
}
func (f *fakeUserNotifyService) UpsertPreferences(_ context.Context, p *domain.NotificationPreference) (*domain.NotificationPreference, error) {
	return p, nil
}
func (f *fakeUserNotifyService) DeletePreference(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (f *fakeUserNotifyService) ListUnread(context.Context, uuid.UUID) ([]domain.Notification, error) {
	return nil, nil
}
func (f *fakeUserNotifyService) CountUnread(context.Context, uuid.UUID) (int, error) { return 0, nil }
func (f *fakeUserNotifyService) MarkRead(context.Context, uuid.UUID, []uuid.UUID) error {
	return nil
}
func (f *fakeUserNotifyService) MarkAllRead(context.Context, uuid.UUID) error { return nil }
func (f *fakeUserNotifyService) EmailAvailable() bool                         { return false }
func (f *fakeUserNotifyService) TelegramBotInfo(context.Context, uuid.UUID) (string, bool) {
	return "", false
}

func (f *fakeUserNotifyService) TelegramReachable(context.Context, uuid.UUID) (reachable bool, reason string) {
	return false, ""
}

func (f *fakeUserNotifyService) Calls() []domain.NotificationEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.NotificationEvent(nil), f.calls...)
}

var _ NotificationService = (*fakeUserNotifyService)(nil)

// reviewerEnv bundles the repos and notification fakes a reviewer-field test
// seeds and asserts on. A struct rather than six return values: the tuple form
// tripped gocritic's tooManyResultsChecker, and every call site was already
// blanking at least one position. Mirrors membershipEnv in
// task_assignee_membership_test.go.
type reviewerEnv struct {
	svc         *taskService
	tasks       *MockTaskRepository
	statuses    *MockTaskStatusRepository
	agentNotify *MockAgentNotifyService
	userNotify  *fakeUserNotifyService
	projects    *MockProjectRepository
}

// setupTaskServiceForReviewer wires a task service with agent-push + in-app
// notification fakes for reviewer-field tests.
func setupTaskServiceForReviewer() reviewerEnv {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	agentNotify := NewMockAgentNotifyService()
	userNotify := &fakeUserNotifyService{}
	projRepo := NewMockProjectRepository()

	// newTestTaskService, not NewTaskService: the reviewer field now carries the
	// same tenancy check as the assignee, and an unwired directory refuses.
	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithAgentNotifyService(agentNotify),
		WithNotificationService(userNotify),
		WithProjectRepo(projRepo.WithDefaultWorkspace(testDefaultWorkspaceID)),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return reviewerEnv{
		svc: svc, tasks: taskRepo, statuses: statusRepo,
		agentNotify: agentNotify, userNotify: userNotify, projects: projRepo,
	}
}

func TestTaskService_Update_ReviewerAssigned_AgentReviewerNotified(t *testing.T) {
	env := setupTaskServiceForReviewer()
	svc, taskRepo, agentNotify, userNotify, projRepo := env.svc, env.tasks, env.agentNotify, env.userNotify, env.projects
	ctx := context.Background()

	projID := uuid.New()
	// One tenant: the reviewer below must belong to the project's workspace.
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: testDefaultWorkspaceID}

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projID, Title: "T"}

	reviewerID := uuid.New()
	reviewerType := domain.AssigneeTypeAgent
	// An agent reviewer must be a real agent of this workspace, same as an assignee.
	seedTestAgents(t, svc, reviewerID)
	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		ReviewerID: &reviewerID, ReviewerType: &reviewerType,
	})
	require.NoError(t, err)

	var found bool
	for _, call := range agentNotify.Calls() {
		if call.EventType == "task.reviewer_assigned" && call.AgentID == reviewerID {
			found = true
		}
	}
	assert.True(t, found, "expected task.reviewer_assigned push to agent reviewer")
	assert.Empty(t, userNotify.Calls(), "agent reviewer must not also go through the in-app user path")
}

func TestTaskService_Update_ReviewerAssigned_UserReviewerNotifiedTargeted(t *testing.T) {
	env := setupTaskServiceForReviewer()
	svc, taskRepo, agentNotify, userNotify, projRepo := env.svc, env.tasks, env.agentNotify, env.userNotify, env.projects
	ctx := context.Background()

	projID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: uuid.New()}

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projID, Title: "T"}

	reviewerID := uuid.New()
	reviewerType := domain.AssigneeTypeUser
	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		ReviewerID: &reviewerID, ReviewerType: &reviewerType,
	})
	require.NoError(t, err)

	require.Len(t, userNotify.Calls(), 1)
	call := userNotify.Calls()[0]
	assert.Equal(t, "task.reviewer_assigned", call.EventType)
	require.NotNil(t, call.TargetUserID, "reviewer notification must be targeted, not broadcast")
	assert.Equal(t, reviewerID, *call.TargetUserID)
	assert.Empty(t, agentNotify.Calls())
}

// TestTaskService_Update_ReviewerNoNotifyIfUnchanged is the regression test for
// the pointer-vs-value bug: existing and task come from two separate GetByID
// calls, so a raw pointer comparison (existing.ReviewerID != task.ReviewerID)
// would report "changed" on every update even when the reviewer stayed the same,
// firing a spurious "Review requested" notification on every unrelated edit.
func TestTaskService_Update_ReviewerNoNotifyIfUnchanged(t *testing.T) {
	env := setupTaskServiceForReviewer()
	svc, taskRepo, agentNotify, userNotify, projRepo := env.svc, env.tasks, env.agentNotify, env.userNotify, env.projects
	ctx := context.Background()

	projID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: uuid.New()}

	reviewerID := uuid.New()
	reviewerType := domain.AssigneeTypeUser
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T",
		ReviewerID: &reviewerID, ReviewerType: &reviewerType,
	}

	// Same reviewer UUID *value*, but a fresh pointer allocation — as would
	// happen after a plain title edit that leaves reviewer_id untouched.
	sameReviewerID := reviewerID
	sameReviewerType := reviewerType
	err := svc.Update(ctx, &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T (edited)",
		ReviewerID: &sameReviewerID, ReviewerType: &sameReviewerType,
	})
	require.NoError(t, err)

	assert.Empty(t, userNotify.Calls(), "must not notify when the reviewer value did not actually change")
	assert.Empty(t, agentNotify.Calls())
}

// ---------------------------------------------------------------------------
// Create — the reviewer-on-create path (task #6bf97281). Reviewer used to be
// settable only via a follow-up PATCH; these mirror the Update reviewer tests
// above one-for-one so Create can't silently diverge from Update's contract.
// ---------------------------------------------------------------------------

func TestTaskService_Create_ReviewerAssigned_AgentReviewerNotified(t *testing.T) {
	env := setupTaskServiceForReviewer()
	svc, agentNotify, userNotify, projRepo := env.svc, env.agentNotify, env.userNotify, env.projects
	ctx := context.Background()

	projID := uuid.New()
	// One tenant: the reviewer below must belong to the project's workspace —
	// same contract as the Update twin above. Reviewer-at-creation is a write
	// path that names a principal by id, so it carries the assignee tenancy
	// guard, and an agent reviewer must be a real agent of this workspace.
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: testDefaultWorkspaceID}

	reviewerID := uuid.New()
	reviewerType := domain.AssigneeTypeAgent
	seedTestAgents(t, svc, reviewerID)
	task := &domain.Task{
		ProjectID: projID, Title: "T",
		ReviewerID: &reviewerID, ReviewerType: &reviewerType,
	}
	require.NoError(t, svc.Create(ctx, task))

	var found bool
	for _, call := range agentNotify.Calls() {
		if call.EventType == "task.reviewer_assigned" && call.AgentID == reviewerID {
			found = true
		}
	}
	assert.True(t, found, "expected task.reviewer_assigned push to agent reviewer on create")
	// No assignee was set on this task, so notifyAssignee no-ops and Calls()
	// holds only the reviewer path — assert nothing leaked from it.
	for _, call := range userNotify.Calls() {
		assert.NotEqual(t, "task.reviewer_assigned", call.EventType,
			"an agent reviewer must go through the agent push path, not the in-app broadcast")
	}
}

func TestTaskService_Create_ReviewerAssigned_UserReviewerNotifiedTargeted(t *testing.T) {
	env := setupTaskServiceForReviewer()
	svc, agentNotify, userNotify, projRepo := env.svc, env.agentNotify, env.userNotify, env.projects
	ctx := context.Background()

	projID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: uuid.New()}

	reviewerID := uuid.New()
	reviewerType := domain.AssigneeTypeUser
	task := &domain.Task{
		ProjectID: projID, Title: "T",
		ReviewerID: &reviewerID, ReviewerType: &reviewerType,
	}
	require.NoError(t, svc.Create(ctx, task))

	// No assignee was set on this task, so notifyAssignee no-ops and the
	// reviewer notification is the only in-app call.
	require.Len(t, userNotify.Calls(), 1)
	call := userNotify.Calls()[0]
	assert.Equal(t, "task.reviewer_assigned", call.EventType)
	require.NotNil(t, call.TargetUserID, "reviewer notification on create must be targeted, not broadcast")
	assert.Equal(t, reviewerID, *call.TargetUserID)
	assert.Empty(t, agentNotify.Calls())
}

// TestTaskService_Create_NoReviewer_SendsNoReviewerNotification is the AC6
// regression test: creating a task without a reviewer must not fire
// task.reviewer_assigned to anyone. This is exactly the class of bug the task
// description warns against repeating — task.assigned used to fire
// unconditionally on Create even when no assignee was set.
func TestTaskService_Create_NoReviewer_SendsNoReviewerNotification(t *testing.T) {
	env := setupTaskServiceForReviewer()
	svc, agentNotify, userNotify, projRepo := env.svc, env.agentNotify, env.userNotify, env.projects
	ctx := context.Background()

	projID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: uuid.New()}

	task := &domain.Task{ProjectID: projID, Title: "T, no reviewer"}
	require.NoError(t, svc.Create(ctx, task))

	for _, call := range agentNotify.Calls() {
		assert.NotEqual(t, "task.reviewer_assigned", call.EventType,
			"no reviewer set — must not fire task.reviewer_assigned via agent push")
	}
	for _, call := range userNotify.Calls() {
		assert.NotEqual(t, "task.reviewer_assigned", call.EventType,
			"no reviewer set — must not fire task.reviewer_assigned via in-app notification")
	}
}

func TestTaskService_MoveTask_ReadyForReview_NotifiesReviewer(t *testing.T) {
	env := setupTaskServiceForReviewer()
	svc, taskRepo, statusRepo, agentNotify, userNotify, projRepo := env.svc, env.tasks, env.statuses, env.agentNotify, env.userNotify, env.projects
	ctx := context.Background()

	projID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: uuid.New()}

	fromStatusID := uuid.New()
	toStatusID := uuid.New()
	statusRepo.items[fromStatusID] = &domain.TaskStatus{ID: fromStatusID, ProjectID: projID, Category: domain.StatusCategoryInProgress, Name: "In Progress"}
	statusRepo.items[toStatusID] = &domain.TaskStatus{ID: toStatusID, ProjectID: projID, Category: domain.StatusCategoryReview, Name: "Review"}

	reviewerID := uuid.New()
	reviewerType := domain.AssigneeTypeUser
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, Title: "T", StatusID: fromStatusID,
		ReviewerID: &reviewerID, ReviewerType: &reviewerType,
	}

	err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toStatusID})
	require.NoError(t, err)

	var found bool
	for _, call := range userNotify.Calls() {
		if call.EventType == "task.ready_for_review" {
			found = true
			require.NotNil(t, call.TargetUserID)
			assert.Equal(t, reviewerID, *call.TargetUserID)
		}
	}
	assert.True(t, found, "expected task.ready_for_review targeted notification when task with a reviewer enters review")
	for _, call := range agentNotify.Calls() {
		assert.NotEqual(t, "task.ready_for_review", call.EventType,
			"a user reviewer must not also receive the agent-push path")
	}
}

// ---------------------------------------------------------------------------
// TestTaskService_MoveTask_ReviewGate — evidence gate tests
// ---------------------------------------------------------------------------

// setupTaskServiceWithCommentRepo wires a task service with a comment repo for the review-evidence gate.
func setupTaskServiceWithCommentRepo() (*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockCommentRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	commentRepo := NewMockCommentRepository()

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithCommentRepoTask(commentRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, commentRepo
}

func TestTaskService_MoveTask_ReviewGate_BlockedWhenNoEvidence(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, _ := setupTaskServiceWithCommentRepo()

	// Task has no artifact, no VCS link.
	taskRepo.items[taskID] = &domain.Task{
		ID:            taskID,
		ProjectID:     projectID,
		StatusID:      uuid.New(),
		Title:         "Evidence-less task",
		ArtifactCount: 0,
		VCSLinkCount:  0,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryReview,
	}

	// No comments added — gate should block.
	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.Error(t, err)
	var evidenceErr *ReviewEvidenceError
	require.ErrorAs(t, err, &evidenceErr)
}

func TestTaskService_MoveTask_ReviewGate_PassesWithArtifact(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, _ := setupTaskServiceWithCommentRepo()

	// Task has one artifact — gate should pass.
	taskRepo.items[taskID] = &domain.Task{
		ID:            taskID,
		ProjectID:     projectID,
		StatusID:      uuid.New(),
		Title:         "Task with artifact",
		ArtifactCount: 1,
		VCSLinkCount:  0,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryReview,
	}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

func TestTaskService_MoveTask_ReviewGate_PassesWithVCSLink(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, _ := setupTaskServiceWithCommentRepo()

	// Task has a VCS link — gate should pass.
	taskRepo.items[taskID] = &domain.Task{
		ID:            taskID,
		ProjectID:     projectID,
		StatusID:      uuid.New(),
		Title:         "Task with VCS link",
		ArtifactCount: 0,
		VCSLinkCount:  1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryReview,
	}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

func TestTaskService_MoveTask_ReviewGate_PassesWithComment(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, commentRepo := setupTaskServiceWithCommentRepo()

	// Task has no artifact or VCS link, but has a comment — gate should pass.
	taskRepo.items[taskID] = &domain.Task{
		ID:            taskID,
		ProjectID:     projectID,
		StatusID:      uuid.New(),
		Title:         "Task with comment only",
		ArtifactCount: 0,
		VCSLinkCount:  0,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryReview,
	}

	// Add a comment for this task.
	commentID := uuid.New()
	commentRepo.items[commentID] = &domain.Comment{
		ID:         commentID,
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "CI passed: go test ./... ✓",
	}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Done-evidence gate tests
// ---------------------------------------------------------------------------

// setupTaskServiceWithDoneGate wires a task service with both the VCS link repo
// (done-evidence gate) and comment repo (no-VCS fallback path).
func setupTaskServiceWithDoneGate() (*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockVCSLinkRepository, *MockCommentRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	vcsRepo := NewMockVCSLinkRepository()
	commentRepo := NewMockCommentRepository()

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithVCSLinkRepoTask(vcsRepo),
		WithCommentRepoTask(commentRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, vcsRepo, commentRepo
}

// fakeGitHubPRChecker is a scripted githubapi.PullRequestChecker for the
// done-evidence gate's live-check branch (#5f7f8c6e). It records every call
// so tests can assert the gate actually consulted it rather than passing/
// failing for an unrelated reason.
type fakeGitHubPRChecker struct {
	mu                  sync.Mutex
	state               githubapi.PullRequestState
	err                 error
	calls               int
	lastOwner, lastRepo string
	lastNumber          int
}

func (f *fakeGitHubPRChecker) GetPullRequestState(_ context.Context, owner, repo string, number int) (githubapi.PullRequestState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastOwner, f.lastRepo, f.lastNumber = owner, repo, number
	if f.err != nil {
		return githubapi.PullRequestState{}, f.err
	}
	return f.state, nil
}

func (f *fakeGitHubPRChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func setupTaskServiceWithDoneGateAndGitHubChecker(checker githubapi.PullRequestChecker) (*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockVCSLinkRepository, *MockCommentRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	vcsRepo := NewMockVCSLinkRepository()
	commentRepo := NewMockCommentRepository()

	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithVCSLinkRepoTask(vcsRepo),
		WithCommentRepoTask(commentRepo),
		WithGitHubPRChecker(checker),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, vcsRepo, commentRepo
}

// The reported incident: PR merged on GitHub, but the cached vcs_links
// record still says "open" (the webhook that would have updated it never
// arrived for this link). The gate must ask GitHub directly and let the
// move through — without any manual add_vcs_link intervention.
func TestTaskService_MoveTask_DoneGate_PassesWhenGitHubReportsLiveMerged(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()
	linkID := uuid.New()

	checker := &fakeGitHubPRChecker{state: githubapi.PullRequestState{Merged: true, State: "closed"}}
	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGateAndGitHubChecker(checker)

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with a PR merged 9.5h ago on GitHub",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:         linkID,
		TaskID:     taskID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		Status:     domain.VCSLinkStatusOpen, // stale cached record
		URL:        "https://github.com/entire-vc/evc-mesh/pull/427",
		Title:      "feat: something already merged",
		ExternalID: "427",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err, "a GitHub-merged PR must not block move_task->done without manual intervention")
	assert.Equal(t, 1, checker.callCount())
	assert.Equal(t, "entire-vc", checker.lastOwner)
	assert.Equal(t, "evc-mesh", checker.lastRepo)
	assert.Equal(t, 427, checker.lastNumber)

	// Self-heal: the cache should now reflect what GitHub said, so a
	// follow-up read (or another agent hitting the same gate) doesn't need
	// another round trip and doesn't see a misleading "open" anywhere.
	links, lerr := vcsRepo.ListByTask(context.Background(), taskID)
	require.NoError(t, lerr)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status, "the cached status should be healed to merged after a live-verified merge")
}

// The other direction: a PR that is genuinely still open on GitHub must
// continue to block the move — the live check is not a way to route around
// a real "not merged yet".
func TestTaskService_MoveTask_DoneGate_BlockedWhenGenuinelyStillOpenOnGitHub(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	checker := &fakeGitHubPRChecker{state: githubapi.PullRequestState{Merged: false, State: "open"}}
	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGateAndGitHubChecker(checker)

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with a genuinely open PR",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     taskID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		Status:     domain.VCSLinkStatusOpen,
		URL:        "https://github.com/entire-vc/evc-mesh/pull/500",
		Title:      "feat: still in review",
		ExternalID: "500",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.Error(t, err)
	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
	assert.True(t, doneErr.PRStatusCheckedLive, "the block must reflect a live GitHub read, not the cache")
	assert.Contains(t, doneErr.Error(), "verified live against GitHub")
	assert.Equal(t, 1, checker.callCount())

	// Cache must NOT be healed — GitHub said it's still open.
	links, lerr := vcsRepo.ListByTask(context.Background(), taskID)
	require.NoError(t, lerr)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusOpen, links[0].Status)
}

// When GitHub is unreachable, the gate must fall back to the cached status
// (never treat "couldn't verify" as either "merged" or "not merged" — this
// is the pre-existing fail-closed behavior) and the error message must say
// the live check couldn't be performed, so the reader understands this is
// possibly a stale cache rather than a definitive "still open" read.
func TestTaskService_MoveTask_DoneGate_FallsBackToCachedStatusWhenGitHubUnreachable(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()
	linkedAt := frozenTime.Add(-9*time.Hour - 30*time.Minute)

	checker := &fakeGitHubPRChecker{err: fmt.Errorf("dial tcp: connection refused")}
	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGateAndGitHubChecker(checker)

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with a PR and GitHub unreachable",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     taskID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		Status:     domain.VCSLinkStatusOpen,
		URL:        "https://github.com/entire-vc/evc-mesh/pull/600",
		Title:      "feat: unknown live state",
		ExternalID: "600",
		CreatedAt:  linkedAt,
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.Error(t, err)
	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
	assert.False(t, doneErr.PRStatusCheckedLive, "an unreachable GitHub must not be reported as a live verdict")
	assert.Contains(t, doneErr.Error(), "could not verify live against GitHub")
	assert.Contains(t, doneErr.Error(), "open")
	assert.Contains(t, doneErr.Error(), linkedAt.UTC().Format(time.RFC3339), "the message should say when the stale record was made")
}

// No GitHub checker wired at all (e.g. GITHUB_TOKEN-less deployments that
// choose not to enable it) — must behave exactly as before this fix: fall
// back to the cached status without erroring or panicking on a nil checker.
func TestTaskService_MoveTask_DoneGate_NoCheckerWired_FallsBackToCachedStatus(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate() // no checker wired

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with a PR, no checker wired",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     taskID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		Status:     domain.VCSLinkStatusOpen,
		URL:        "https://github.com/entire-vc/evc-mesh/pull/601",
		ExternalID: "601",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.Error(t, err)
	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
	assert.False(t, doneErr.PRStatusCheckedLive)
}

func TestTaskService_MoveTask_DoneGate_BlockedWhenPROpen(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate()

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with open PR",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:       uuid.New(),
		TaskID:   taskID,
		LinkType: domain.VCSLinkTypePR,
		Status:   domain.VCSLinkStatusOpen,
		URL:      "https://github.com/entire-vc/evc-mesh/pull/99",
		Title:    "feat: my feature",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.Error(t, err)
	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
	assert.Contains(t, doneErr.Error(), "feat: my feature")
	assert.Contains(t, doneErr.Error(), "open")
	// #df734dd9: this branch has no comment-based escape hatch — a
	// justification comment is only consulted when VCSLinkCount == 0. The
	// message must not promise one that doesn't exist on this path.
	assert.NotContains(t, doneErr.Error(), "justification comment")
}

// #df734dd9: an empty recorded status (a link created before status
// tracking existed, or one add_vcs_link left blank) gets a message that
// names the real cause — no webhook can ever arrive for a PR that was
// merged before the link existed — instead of a generic "not merged" that
// reads as if the block is just waiting on a merge that hasn't happened.
func TestTaskService_MoveTask_DoneGate_BlockedWhenPRStatusEmpty_NamesRealCause(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate()

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with unrecorded PR status",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:       uuid.New(),
		TaskID:   taskID,
		LinkType: domain.VCSLinkTypePR,
		Status:   "",
		URL:      "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Title:    "feat: add_vcs_link",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.Error(t, err)
	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
	assert.Contains(t, doneErr.Error(), "no recorded merge status")
	assert.Contains(t, doneErr.Error(), "status=merged")
	assert.NotContains(t, doneErr.Error(), "justification comment")
}

func TestTaskService_MoveTask_DoneGate_PassesWhenPRMerged(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate()

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with merged PR",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:       uuid.New(),
		TaskID:   taskID,
		LinkType: domain.VCSLinkTypePR,
		Status:   domain.VCSLinkStatusMerged,
		URL:      "https://github.com/entire-vc/evc-mesh/pull/247",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

func TestTaskService_MoveTask_DoneGate_PassesWhenPRClosed(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate()

	// Closed (without merge) PR is considered resolved — gate passes.
	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with closed PR",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:       uuid.New(),
		TaskID:   taskID,
		LinkType: domain.VCSLinkTypePR,
		Status:   domain.VCSLinkStatusClosed,
		URL:      "https://github.com/entire-vc/evc-mesh/pull/55",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

func TestTaskService_MoveTask_DoneGate_CommitLinkDoesNotBlock(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate()

	// Commit-type link with "open" status — gate should NOT block (only PR links matter).
	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Task with commit link",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:       uuid.New(),
		TaskID:   taskID,
		LinkType: domain.VCSLinkTypeCommit,
		Status:   domain.VCSLinkStatusOpen,
		URL:      "https://github.com/entire-vc/evc-mesh/commit/abc123",
	})

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

func TestTaskService_MoveTask_DoneGate_SystemActorExempt(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate()

	taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    projectID,
		StatusID:     uuid.New(),
		Title:        "Auto-transition task",
		VCSLinkCount: 1,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
		ID:       uuid.New(),
		TaskID:   taskID,
		LinkType: domain.VCSLinkTypePR,
		Status:   domain.VCSLinkStatusOpen,
		URL:      "https://github.com/entire-vc/evc-mesh/pull/42",
	})

	// System actor (auto_transition) must bypass the gate.
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

func TestTaskService_MoveTask_DoneGate_NoVCSLinks_BlockedWhenNoEvidence(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, _, _ := setupTaskServiceWithDoneGate()

	taskRepo.items[taskID] = &domain.Task{
		ID:            taskID,
		ProjectID:     projectID,
		StatusID:      uuid.New(),
		Title:         "No-evidence task",
		ArtifactCount: 0,
		VCSLinkCount:  0,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.Error(t, err)
	var doneErr *DoneEvidenceError
	require.ErrorAs(t, err, &doneErr)
}

func TestTaskService_MoveTask_DoneGate_NoVCSLinks_PassesWithArtifact(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, _, _ := setupTaskServiceWithDoneGate()

	taskRepo.items[taskID] = &domain.Task{
		ID:            taskID,
		ProjectID:     projectID,
		StatusID:      uuid.New(),
		Title:         "Task with artifact",
		ArtifactCount: 1,
		VCSLinkCount:  0,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

func TestTaskService_MoveTask_DoneGate_NoVCSLinks_PassesWithComment(t *testing.T) {
	projectID := uuid.New()
	taskID := uuid.New()
	statusID := uuid.New()

	svc, taskRepo, statusRepo, _, commentRepo := setupTaskServiceWithDoneGate()

	taskRepo.items[taskID] = &domain.Task{
		ID:            taskID,
		ProjectID:     projectID,
		StatusID:      uuid.New(),
		Title:         "Task with comment",
		ArtifactCount: 0,
		VCSLinkCount:  0,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{
		ID:        statusID,
		ProjectID: projectID,
		Category:  domain.StatusCategoryDone,
	}
	commentID := uuid.New()
	commentRepo.items[commentID] = &domain.Comment{
		ID:         commentID,
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "All tests passed; deploying to prod.",
	}

	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// CAS precondition tests
// ---------------------------------------------------------------------------

func TestTaskService_MoveTask_CAS_RejectsStaleExpectedStatus(t *testing.T) {
	projectID := uuid.New()
	svc, taskRepo, statusRepo := setupTaskService()

	todoID := uuid.New()
	inProgressID := uuid.New()
	doneID := uuid.New()
	taskID := uuid.New()

	taskRepo.items[taskID] = &domain.Task{
		ID:        taskID,
		ProjectID: projectID,
		StatusID:  todoID,
		Title:     "CAS test task",
	}
	statusRepo.items[inProgressID] = &domain.TaskStatus{ID: inProgressID, ProjectID: projectID, Category: domain.StatusCategoryInProgress}
	statusRepo.items[doneID] = &domain.TaskStatus{ID: doneID, ProjectID: projectID, Category: domain.StatusCategoryDone}

	// Move 1: expected=todo (correct) → in_progress: should succeed.
	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{
		StatusID:         &inProgressID,
		ExpectedStatusID: &todoID,
	})
	require.NoError(t, err)

	// Move 2: expected=todo (stale) → done: should return CASConflictError.
	err = svc.MoveTask(context.Background(), taskID, MoveTaskInput{
		StatusID:         &doneID,
		ExpectedStatusID: &todoID,
	})
	require.Error(t, err)
	var casErr *CASConflictError
	require.ErrorAs(t, err, &casErr)
	assert.Equal(t, inProgressID, casErr.CurrentStatusID)
}

func TestTaskService_MoveTask_CAS_NilExpected_PassesThrough(t *testing.T) {
	projectID := uuid.New()
	svc, taskRepo, statusRepo := setupTaskService()

	statusID := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "Compat task"}
	statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryInProgress}

	// No CAS precondition — backward-compatible, move passes as before.
	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})
	require.NoError(t, err)
}

func TestTaskService_MoveTask_CAS_ExpectedUpdatedAt_Conflict(t *testing.T) {
	projectID := uuid.New()
	svc, taskRepo, statusRepo := setupTaskService()

	statusID := uuid.New()
	taskID := uuid.New()
	taskUpdatedAt := frozenTime.Add(-time.Hour)
	taskRepo.items[taskID] = &domain.Task{
		ID:        taskID,
		ProjectID: projectID,
		StatusID:  uuid.New(),
		Title:     "Updated-at CAS test",
		UpdatedAt: taskUpdatedAt,
	}
	statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryInProgress}

	// Caller passes stale updated_at — should fail with CASConflictError.
	stale := frozenTime.Add(-2 * time.Hour)
	err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{
		StatusID:          &statusID,
		ExpectedUpdatedAt: &stale,
	})
	require.Error(t, err)
	var casErr *CASConflictError
	require.ErrorAs(t, err, &casErr)
}

// ---------------------------------------------------------------------------
// MoveToProject cascade tests
// ---------------------------------------------------------------------------

type moveToProjectFixture struct {
	svc         *taskService
	taskRepo    *MockTaskRepository
	statusRepo  *MockTaskStatusRepository
	projectRepo *MockProjectRepository
	workspaceID uuid.UUID
	projectA    uuid.UUID
	projectB    uuid.UUID
	todoA       uuid.UUID
	inProgressA uuid.UUID
	doneA       uuid.UUID
	todoB       uuid.UUID
	inProgressB uuid.UUID
	doneB       uuid.UUID
}

func setupMoveToProjectFixture() *moveToProjectFixture {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	svc := newTestTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithProjectRepo(projectRepo)).(*taskService)

	f := &moveToProjectFixture{
		svc:         svc,
		taskRepo:    taskRepo,
		statusRepo:  statusRepo,
		projectA:    uuid.New(),
		projectB:    uuid.New(),
		todoA:       uuid.New(),
		inProgressA: uuid.New(),
		doneA:       uuid.New(),
		todoB:       uuid.New(),
		inProgressB: uuid.New(),
		doneB:       uuid.New(),
	}

	// Both projects are in one workspace: MoveToProject refuses a move that would
	// cross a tenant boundary, which is a different test (see
	// TestMoveToProject_RefusesAnotherWorkspacesProject).
	f.workspaceID = uuid.New()
	projectRepo.items[f.projectA] = &domain.Project{ID: f.projectA, WorkspaceID: f.workspaceID}
	projectRepo.items[f.projectB] = &domain.Project{ID: f.projectB, WorkspaceID: f.workspaceID}
	f.projectRepo = projectRepo

	// Source project statuses.
	statusRepo.items[f.todoA] = &domain.TaskStatus{ID: f.todoA, ProjectID: f.projectA, Category: domain.StatusCategoryTodo, Position: 1}
	statusRepo.items[f.inProgressA] = &domain.TaskStatus{ID: f.inProgressA, ProjectID: f.projectA, Category: domain.StatusCategoryInProgress, Position: 2}
	statusRepo.items[f.doneA] = &domain.TaskStatus{ID: f.doneA, ProjectID: f.projectA, Category: domain.StatusCategoryDone, Position: 3}

	// Target project statuses.
	statusRepo.items[f.todoB] = &domain.TaskStatus{ID: f.todoB, ProjectID: f.projectB, Category: domain.StatusCategoryTodo, Position: 1}
	statusRepo.items[f.inProgressB] = &domain.TaskStatus{ID: f.inProgressB, ProjectID: f.projectB, Category: domain.StatusCategoryInProgress, Position: 2}
	statusRepo.items[f.doneB] = &domain.TaskStatus{ID: f.doneB, ProjectID: f.projectB, Category: domain.StatusCategoryDone, Position: 3}

	return f
}

// TestMoveToProject_RefusesAnotherWorkspacesProject is the cross-tenant repro at
// the service. project_id comes from the request body, where the workspace guard —
// which reads route parameters — cannot see it; :task_id is the caller's own task
// and resolves to their own workspace, so nothing upstream had an opinion about
// where the task was going. "Both projects exist" was the whole of the check, and
// a member of any workspace could push their task and its whole subtree onto a
// stranger's board.
func TestMoveToProject_RefusesAnotherWorkspacesProject(t *testing.T) {
	f := setupMoveToProjectFixture()

	foreignProject := uuid.New()
	f.projectRepo.items[foreignProject] = &domain.Project{ID: foreignProject, WorkspaceID: uuid.New()}
	f.statusRepo.items[uuid.New()] = &domain.TaskStatus{ID: uuid.New(), ProjectID: foreignProject, Category: domain.StatusCategoryTodo, Position: 1}

	taskID := uuid.New()
	f.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Mine"}

	_, err := f.svc.MoveToProject(context.Background(), taskID, foreignProject)
	require.Error(t, err, "a task was moved into another workspace's project")
	assert.Equal(t, f.projectA, f.taskRepo.items[taskID].ProjectID, "the task moved anyway")
}

// TestMoveToProject_WithoutProjectRepoFailsClosed: the project repository is an
// option on this service, so it can be absent. "Cannot check the workspace" must
// refuse, not allow — the alternative is a wiring mistake reopening the hole in
// silence.
func TestMoveToProject_WithoutProjectRepoFailsClosed(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	// Deliberately NOT newTestTaskService: this test is about the absent project
	// repo, which that helper always supplies.
	svc := NewTaskService(taskRepo, statusRepo, NewMockTaskDependencyRepository(), NewMockActivityLogRepository())

	projectA, projectB := uuid.New(), uuid.New()
	todoA, todoB := uuid.New(), uuid.New()
	statusRepo.items[todoA] = &domain.TaskStatus{ID: todoA, ProjectID: projectA, Category: domain.StatusCategoryTodo, Position: 1}
	statusRepo.items[todoB] = &domain.TaskStatus{ID: todoB, ProjectID: projectB, Category: domain.StatusCategoryTodo, Position: 1}

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectA, StatusID: todoA}

	_, err := svc.MoveToProject(context.Background(), taskID, projectB)
	require.Error(t, err)
	assert.Equal(t, projectA, taskRepo.items[taskID].ProjectID)
}

func TestMoveToProject_CascadesDirectSubtasks(t *testing.T) {
	f := setupMoveToProjectFixture()

	parentID := uuid.New()
	child1ID := uuid.New()
	child2ID := uuid.New()

	f.taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Parent"}
	f.taskRepo.items[child1ID] = &domain.Task{ID: child1ID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Child 1", ParentTaskID: &parentID}
	f.taskRepo.items[child2ID] = &domain.Task{ID: child2ID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Child 2", ParentTaskID: &parentID}

	_, err := f.svc.MoveToProject(context.Background(), parentID, f.projectB)
	require.NoError(t, err)

	assert.Equal(t, f.projectB, f.taskRepo.items[parentID].ProjectID, "parent must move to projectB")
	assert.Equal(t, f.projectB, f.taskRepo.items[child1ID].ProjectID, "child1 must move to projectB")
	assert.Equal(t, f.projectB, f.taskRepo.items[child2ID].ProjectID, "child2 must move to projectB")

	// Subtask statuses must be valid target-project status IDs.
	assert.Equal(t, f.todoB, f.taskRepo.items[child1ID].StatusID)
	assert.Equal(t, f.todoB, f.taskRepo.items[child2ID].StatusID)
}

func TestMoveToProject_CascadesNested(t *testing.T) {
	f := setupMoveToProjectFixture()

	parentID := uuid.New()
	childID := uuid.New()
	grandchildID := uuid.New()

	f.taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Parent"}
	f.taskRepo.items[childID] = &domain.Task{ID: childID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Child", ParentTaskID: &parentID}
	f.taskRepo.items[grandchildID] = &domain.Task{ID: grandchildID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Grandchild", ParentTaskID: &childID}

	_, err := f.svc.MoveToProject(context.Background(), parentID, f.projectB)
	require.NoError(t, err)

	assert.Equal(t, f.projectB, f.taskRepo.items[parentID].ProjectID)
	assert.Equal(t, f.projectB, f.taskRepo.items[childID].ProjectID)
	assert.Equal(t, f.projectB, f.taskRepo.items[grandchildID].ProjectID)

	assert.Equal(t, f.todoB, f.taskRepo.items[grandchildID].StatusID, "grandchild status must be remapped to target project")
}

func TestMoveToProject_StatusCategoryMapping(t *testing.T) {
	f := setupMoveToProjectFixture()

	parentID := uuid.New()
	childID := uuid.New()

	f.taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: f.projectA, StatusID: f.inProgressA, Title: "Parent"}
	f.taskRepo.items[childID] = &domain.Task{ID: childID, ProjectID: f.projectA, StatusID: f.inProgressA, Title: "Child in_progress", ParentTaskID: &parentID}

	_, err := f.svc.MoveToProject(context.Background(), parentID, f.projectB)
	require.NoError(t, err)

	// Child was in_progress → must map to in_progress in target, not the default todo.
	assert.Equal(t, f.inProgressB, f.taskRepo.items[childID].StatusID, "in_progress subtask must map to in_progress in target project")
}

func TestMoveToProject_StatusCategoryFallback(t *testing.T) {
	// Target project has no "review" status — subtask must fall back to defaultStatus (todoB).
	f := setupMoveToProjectFixture()

	// Add a "review" status to source only.
	reviewA := uuid.New()
	f.statusRepo.items[reviewA] = &domain.TaskStatus{ID: reviewA, ProjectID: f.projectA, Category: domain.StatusCategoryReview, Position: 4}

	parentID := uuid.New()
	childID := uuid.New()

	f.taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Parent"}
	f.taskRepo.items[childID] = &domain.Task{ID: childID, ProjectID: f.projectA, StatusID: reviewA, Title: "Child in review", ParentTaskID: &parentID}

	_, err := f.svc.MoveToProject(context.Background(), parentID, f.projectB)
	require.NoError(t, err)

	// No review in target → falls back to default (lowest position = todoB).
	assert.Equal(t, f.todoB, f.taskRepo.items[childID].StatusID, "subtask with unmapped category must fall back to default status")
}

func TestMoveToProject_NoSubtasks(t *testing.T) {
	f := setupMoveToProjectFixture()

	parentID := uuid.New()
	f.taskRepo.items[parentID] = &domain.Task{ID: parentID, ProjectID: f.projectA, StatusID: f.todoA, Title: "Lone task"}

	updated, err := f.svc.MoveToProject(context.Background(), parentID, f.projectB)
	require.NoError(t, err)

	assert.Equal(t, f.projectB, updated.ProjectID, "task must be moved to projectB")
	assert.Equal(t, f.todoB, f.taskRepo.items[parentID].StatusID, "status must be remapped to target project default")
}

// Update's two early exits. They are trivial branches, but Update is the busiest
// write path in the service and "task vanished between read and write" is a real
// concurrent-delete shape — a silent nil-deref here would surface as a 500 on an
// ordinary PATCH.
func TestTaskService_Update_MissingTaskReturnsNotFound(t *testing.T) {
	svc, _, _ := setupTaskService()

	err := svc.Update(context.Background(), &domain.Task{ID: uuid.New(), Title: "T"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Task", "a missing task must surface as NotFound, not a nil-deref")
}

func TestTaskService_Update_RepoErrorPropagates(t *testing.T) {
	svc, taskRepo, _ := setupTaskService()
	taskRepo.errToReturn = assert.AnError

	err := svc.Update(context.Background(), &domain.Task{ID: uuid.New(), Title: "T"})

	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError, "a read failure must propagate, not be reported as NotFound")
}
