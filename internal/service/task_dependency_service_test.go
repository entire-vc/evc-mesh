package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// setupTaskDependencyService returns a taskDependencyService wired to fresh mocks.
func setupTaskDependencyService() (*taskDependencyService, *MockTaskDependencyRepository, *MockTaskRepository) {
	depRepo := NewMockTaskDependencyRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewTaskDependencyService(depRepo, taskRepo, activityRepo).(*taskDependencyService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, depRepo, taskRepo
}

// helper to add a task to the mock repo.
func addTask(repo *MockTaskRepository, id uuid.UUID) {
	repo.items[id] = &domain.Task{ID: id, Title: "Task " + id.String()}
}

// ---------------------------------------------------------------------------
// TestTaskDependencyService_Create
// ---------------------------------------------------------------------------

func TestTaskDependencyService_Create(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency
		wantErr   bool
		errCode   int
		errMsg    string
		checkFunc func(t *testing.T, dep *domain.TaskDependency, depRepo *MockTaskDependencyRepository)
	}{
		{
			name: "success",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: taskB,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, dep *domain.TaskDependency, depRepo *MockTaskDependencyRepository) {
				assert.NotEqual(t, uuid.Nil, dep.ID)
				assert.Equal(t, frozenTime, dep.CreatedAt)
				stored := depRepo.items[dep.ID]
				require.NotNil(t, stored)
			},
		},
		{
			name: "error - self-reference",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				addTask(taskRepo, taskA)
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: taskA,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "cannot depend on itself",
		},
		{
			name: "error - task not found",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				addTask(taskRepo, taskA)
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: uuid.New(), // does not exist
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - duplicate dependency",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				// Add existing dependency.
				existingID := uuid.New()
				depRepo.items[existingID] = &domain.TaskDependency{
					ID:              existingID,
					TaskID:          taskA,
					DependsOnTaskID: taskB,
				}
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: taskB,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusConflict,
			errMsg:  "already exists",
		},
		{
			name: "error - cycle detection (A->B->C, adding C->A)",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				taskB := uuid.New()
				taskC := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				addTask(taskRepo, taskC)

				// A depends on B.
				depAB := uuid.New()
				depRepo.items[depAB] = &domain.TaskDependency{
					ID:              depAB,
					TaskID:          taskA,
					DependsOnTaskID: taskB,
				}
				// B depends on C.
				depBC := uuid.New()
				depRepo.items[depBC] = &domain.TaskDependency{
					ID:              depBC,
					TaskID:          taskB,
					DependsOnTaskID: taskC,
				}

				// Try to add C -> A which would create a cycle: C -> A -> B -> C.
				return &domain.TaskDependency{
					TaskID:          taskC,
					DependsOnTaskID: taskA,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "cycle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, depRepo, taskRepo := setupTaskDependencyService()
			ctx := context.Background()
			dep := tt.setup(depRepo, taskRepo)

			err := svc.Create(ctx, dep)

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
					tt.checkFunc(t, dep, depRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskDependencyService_CheckCycle
// ---------------------------------------------------------------------------

func TestTaskDependencyService_CheckCycle(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (taskID, dependsOnTaskID uuid.UUID)
		wantCycle bool
	}{
		{
			name: "no cycle - independent tasks",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (uuid.UUID, uuid.UUID) {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				return taskA, taskB
			},
			wantCycle: false,
		},
		{
			name: "no cycle - linear chain A->B->C, adding D->A",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (uuid.UUID, uuid.UUID) {
				taskA := uuid.New()
				taskB := uuid.New()
				taskC := uuid.New()
				taskD := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				addTask(taskRepo, taskC)
				addTask(taskRepo, taskD)

				depAB := uuid.New()
				depRepo.items[depAB] = &domain.TaskDependency{ID: depAB, TaskID: taskA, DependsOnTaskID: taskB}
				depBC := uuid.New()
				depRepo.items[depBC] = &domain.TaskDependency{ID: depBC, TaskID: taskB, DependsOnTaskID: taskC}

				// Adding D -> A: from A, can we reach D? No.
				return taskD, taskA
			},
			wantCycle: false,
		},
		{
			name: "cycle - A->B, adding B->A",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (uuid.UUID, uuid.UUID) {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)

				depAB := uuid.New()
				depRepo.items[depAB] = &domain.TaskDependency{ID: depAB, TaskID: taskA, DependsOnTaskID: taskB}

				// Adding B -> A: from A, can we reach B? Yes (A -> B).
				return taskB, taskA
			},
			wantCycle: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, depRepo, taskRepo := setupTaskDependencyService()
			ctx := context.Background()
			taskID, dependsOnTaskID := tt.setup(depRepo, taskRepo)

			hasCycle, err := svc.CheckCycle(ctx, taskID, dependsOnTaskID)

			require.NoError(t, err)
			assert.Equal(t, tt.wantCycle, hasCycle)
		})
	}
}
