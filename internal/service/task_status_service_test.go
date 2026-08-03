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

// setupTaskStatusService returns a taskStatusService wired to fresh mocks.
func setupTaskStatusService() (*taskStatusService, *MockTaskStatusRepository, *MockTaskRepository) {
	statusRepo := NewMockTaskStatusRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewTaskStatusService(statusRepo, taskRepo, activityRepo).(*taskStatusService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, statusRepo, taskRepo
}

// ---------------------------------------------------------------------------
// TestTaskStatusService_Create
// ---------------------------------------------------------------------------

func TestTaskStatusService_Create(t *testing.T) {
	projectID := uuid.New()

	tests := []struct {
		name      string
		setup     func(repo *MockTaskStatusRepository)
		status    *domain.TaskStatus
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, status *domain.TaskStatus, repo *MockTaskStatusRepository)
	}{
		{
			name:  "success - generates slug and position",
			setup: func(_ *MockTaskStatusRepository) {},
			status: &domain.TaskStatus{
				ProjectID: projectID,
				Name:      "In Review",
				Category:  domain.StatusCategoryReview,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, status *domain.TaskStatus, repo *MockTaskStatusRepository) {
				assert.NotEqual(t, uuid.Nil, status.ID)
				assert.Equal(t, "in-review", status.Slug)
				assert.Equal(t, 0, status.Position, "first status should have position 0")

				stored := repo.items[status.ID]
				require.NotNil(t, stored)
			},
		},
		{
			name: "success - assigns next position",
			setup: func(repo *MockTaskStatusRepository) {
				existingID := uuid.New()
				repo.items[existingID] = &domain.TaskStatus{
					ID:        existingID,
					ProjectID: projectID,
					Name:      "Existing",
					Position:  2,
				}
			},
			status: &domain.TaskStatus{
				ProjectID: projectID,
				Name:      "New Status",
				Category:  domain.StatusCategoryTodo,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, status *domain.TaskStatus, _ *MockTaskStatusRepository) {
				assert.Equal(t, 3, status.Position, "should be placed after existing max position")
			},
		},
		{
			name:  "error - empty name",
			setup: func(_ *MockTaskStatusRepository) {},
			status: &domain.TaskStatus{
				ProjectID: projectID,
				Name:      "",
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, statusRepo, _ := setupTaskStatusService()
			ctx := context.Background()
			tt.setup(statusRepo)

			err := svc.Create(ctx, tt.status)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, tt.status, statusRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskStatusService_Delete
// ---------------------------------------------------------------------------

func TestTaskStatusService_Delete(t *testing.T) {
	projectID := uuid.New()

	tests := []struct {
		name    string
		setup   func(statusRepo *MockTaskStatusRepository, taskRepo *MockTaskRepository) uuid.UUID
		wantErr bool
		errCode int
		errMsg  string
	}{
		{
			name: "success - no tasks use this status",
			setup: func(statusRepo *MockTaskStatusRepository, _ *MockTaskRepository) uuid.UUID {
				id := uuid.New()
				statusRepo.items[id] = &domain.TaskStatus{
					ID:        id,
					ProjectID: projectID,
					Name:      "Empty Status",
				}
				return id
			},
			wantErr: false,
		},
		{
			name: "error - tasks still use this status",
			setup: func(statusRepo *MockTaskStatusRepository, taskRepo *MockTaskRepository) uuid.UUID {
				statusID := uuid.New()
				statusRepo.items[statusID] = &domain.TaskStatus{
					ID:        statusID,
					ProjectID: projectID,
					Name:      "Busy Status",
				}
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{
					ID:        taskID,
					ProjectID: projectID,
					StatusID:  statusID,
					Title:     "A task using this status",
				}
				return statusID
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "cannot delete status",
		},
		{
			name: "error - status not found",
			setup: func(_ *MockTaskStatusRepository, _ *MockTaskRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, statusRepo, taskRepo := setupTaskStatusService()
			ctx := context.Background()
			id := tt.setup(statusRepo, taskRepo)

			err := svc.Delete(ctx, id)

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
				_, exists := statusRepo.items[id]
				assert.False(t, exists, "status should be deleted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskStatusService_Reorder
// ---------------------------------------------------------------------------

func TestTaskStatusService_Reorder(t *testing.T) {
	projectID := uuid.New()

	tests := []struct {
		name    string
		setup   func(repo *MockTaskStatusRepository) []uuid.UUID
		wantErr bool
		errCode int
		errMsg  string
	}{
		{
			name: "success - all IDs belong to the project",
			setup: func(repo *MockTaskStatusRepository) []uuid.UUID {
				ids := make([]uuid.UUID, 3)
				for i := range ids {
					ids[i] = uuid.New()
					repo.items[ids[i]] = &domain.TaskStatus{
						ID:        ids[i],
						ProjectID: projectID,
						Name:      "Status",
						Position:  i,
					}
				}
				return ids
			},
			wantErr: false,
		},
		{
			name: "error - ID from different project",
			setup: func(repo *MockTaskStatusRepository) []uuid.UUID {
				validID := uuid.New()
				repo.items[validID] = &domain.TaskStatus{
					ID:        validID,
					ProjectID: projectID,
					Name:      "Valid",
				}
				foreignID := uuid.New()
				repo.items[foreignID] = &domain.TaskStatus{
					ID:        foreignID,
					ProjectID: uuid.New(), // different project
					Name:      "Foreign",
				}
				return []uuid.UUID{validID, foreignID}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "does not belong",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, statusRepo, _ := setupTaskStatusService()
			ctx := context.Background()
			ids := tt.setup(statusRepo)

			err := svc.Reorder(ctx, projectID, ids)

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
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskStatusService_ListByTask
// ---------------------------------------------------------------------------

// TestTaskStatusService_ListByTask verifies the task→project hop that lets a
// workspace-gated caller resolve a status slug for a workspace-gated move.
func TestTaskStatusService_ListByTask(t *testing.T) {
	t.Run("returns the statuses of the task's own project", func(t *testing.T) {
		svc, statusRepo, taskRepo := setupTaskStatusService()

		ownProject := uuid.New()
		otherProject := uuid.New()

		wantID := uuid.New()
		statusRepo.items[wantID] = &domain.TaskStatus{
			ID: wantID, ProjectID: ownProject, Name: "Done", Slug: "done",
			Category: domain.StatusCategoryDone,
		}
		// A decoy in a different project: if the lookup ignored the task's project
		// this would leak in, and the caller would resolve a slug to a foreign status.
		decoyID := uuid.New()
		statusRepo.items[decoyID] = &domain.TaskStatus{
			ID: decoyID, ProjectID: otherProject, Name: "Done", Slug: "done",
			Category: domain.StatusCategoryDone,
		}

		taskID := uuid.New()
		taskRepo.items[taskID] = &domain.Task{
			ID: taskID, ProjectID: ownProject, StatusID: wantID, Title: "A task",
		}

		got, err := svc.ListByTask(context.Background(), taskID)
		require.NoError(t, err)
		require.Len(t, got, 1, "must return only the task's own project statuses")
		assert.Equal(t, wantID, got[0].ID)
		assert.Equal(t, ownProject, got[0].ProjectID)
	})

	t.Run("unknown task is 404, not an empty list", func(t *testing.T) {
		svc, _, _ := setupTaskStatusService()

		got, err := svc.ListByTask(context.Background(), uuid.New())
		require.Error(t, err)
		assert.Nil(t, got)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusNotFound, apiErr.Code,
			"an unknown task must not degrade into an empty status list — "+
				"a caller would read that as 'no such status' instead of 'no such task'")
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, _, taskRepo := setupTaskStatusService()
		taskRepo.errToReturn = assert.AnError

		_, err := svc.ListByTask(context.Background(), uuid.New())
		require.Error(t, err)
	})
}
