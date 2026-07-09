package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

var frozenTime = time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

// setupTaskService returns a taskService wired to fresh mocks.
func setupTaskService() (*taskService, *MockTaskRepository, *MockTaskStatusRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo).(*taskService)

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

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithRulesConfigService(mockRules),
		WithTaskAgentRepo(agentRepo),
		WithProjectRepo(projRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, agentRepo, projRepo
}

func TestTaskService_MoveTask_ReviewAssignee(t *testing.T) {
	workspaceID := uuid.New()
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

		err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{
			StatusID:   &newStatusID,
			AssigneeID: &explicitReviewerID,
		})
		require.NoError(t, err)

		task := taskRepo.items[taskID]
		require.NotNil(t, task.AssigneeID)
		assert.Equal(t, explicitReviewerID, *task.AssigneeID, "explicit assignee_id must win over set_reviewer config")
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

func TestTaskService_CreateSubtask(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(repo *MockTaskRepository) uuid.UUID
		input     CreateSubtaskInput
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, child *domain.Task, parentID uuid.UUID, repo *MockTaskRepository)
	}{
		{
			name: "success",
			setup: func(repo *MockTaskRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Task{
					ID:        id,
					ProjectID: uuid.New(),
					StatusID:  uuid.New(),
					Title:     "Parent task",
				}
				return id
			},
			input: CreateSubtaskInput{
				Title:       "Child task",
				Description: "Sub-task description",
				Priority:    domain.PriorityMedium,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, child *domain.Task, parentID uuid.UUID, repo *MockTaskRepository) {
				assert.NotEqual(t, uuid.Nil, child.ID)
				assert.Equal(t, "Child task", child.Title)
				assert.Equal(t, "Sub-task description", child.Description)
				assert.Equal(t, domain.PriorityMedium, child.Priority)
				require.NotNil(t, child.ParentTaskID)
				assert.Equal(t, parentID, *child.ParentTaskID)
				assert.Equal(t, domain.AssigneeTypeUnassigned, child.AssigneeType)
				assert.Equal(t, frozenTime, child.CreatedAt)

				// Verify the child inherits project and status from parent.
				parent := repo.items[parentID]
				assert.Equal(t, parent.ProjectID, child.ProjectID)
				assert.Equal(t, parent.StatusID, child.StatusID)

				// Verify persisted.
				stored := repo.items[child.ID]
				assert.NotNil(t, stored)
			},
		},
		{
			name: "parent not found",
			setup: func(_ *MockTaskRepository) uuid.UUID {
				return uuid.New()
			},
			input: CreateSubtaskInput{
				Title:    "Orphan child",
				Priority: domain.PriorityLow,
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, taskRepo, _ := setupTaskService()
			ctx := context.Background()
			parentID := tt.setup(taskRepo)

			child, err := svc.CreateSubtask(ctx, parentID, tt.input)

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
					tt.checkFunc(t, child, parentID, taskRepo)
				}
			}
		})
	}
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

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
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

	task := &domain.Task{
		ProjectID: uuid.New(),
		StatusID:  uuid.New(),
		Title:     "Explicit assignee should not be clobbered",
		Priority:  domain.PriorityMedium,
		// Simulate the bug: assignee_id provided but assignee_type left as "unassigned"
		// (the old handler default when type was omitted from the request).
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

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
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
	workspaceID := uuid.New()
	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}

	// Pre-register a task so tests can set ProjectID via the shared projectID.
	// Tests override this in their own setup.
	_ = projectID

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithRulesConfigService(mockRules),
		WithProjectRepo(projRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo
}

func TestTaskService_MoveTask_TransitionGate(t *testing.T) {
	projectID := uuid.New()
	workspaceID := uuid.New()

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

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
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

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
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

	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo,
		WithVCSLinkRepoTask(vcsRepo),
		WithCommentRepoTask(commentRepo),
	).(*taskService)
	timeNow = func() time.Time { return frozenTime }
	return svc, taskRepo, statusRepo, vcsRepo, commentRepo
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
	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo).(*taskService)

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
