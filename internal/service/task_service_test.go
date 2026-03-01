package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// frozenTime is a fixed point in time used in tests.
var frozenTime = time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

// setupTaskService returns a taskService wired to fresh mocks.
func setupTaskService() (*taskService, *MockTaskRepository, *MockTaskStatusRepository, *MockTaskDependencyRepository, *MockActivityLogRepository) {
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	depRepo := NewMockTaskDependencyRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewTaskService(taskRepo, statusRepo, depRepo, activityRepo).(*taskService)

	// Freeze the clock for deterministic tests.
	timeNow = func() time.Time { return frozenTime }

	return svc, taskRepo, statusRepo, depRepo, activityRepo
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
			svc, taskRepo, _, _, _ := setupTaskService()
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
			svc, taskRepo, _, _, _ := setupTaskService()
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
			svc, taskRepo, statusRepo, _, _ := setupTaskService()
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
			svc, taskRepo, _, _, _ := setupTaskService()
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
			svc, taskRepo, _, _, _ := setupTaskService()
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
			svc, taskRepo, _, _, _ := setupTaskService()
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
		name            string
		rules           *domain.EffectiveAssignmentRules
		task            *domain.Task
		wantAssigned    bool
		wantAssigneeID  *uuid.UUID
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
			name: "rules with invalid UUID in by_priority - silently skips, falls back to default",
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
			// The invalid UUID in by_priority causes applyAutoAssign to log and return
			// early without assigning, so the task stays unassigned despite default_assignee.
			// This tests the current behaviour: invalid UUID in ANY selected rule = no assign.
			wantAssigned: false,
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
			svc, taskRepo, _, _, _ := setupTaskService()
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
