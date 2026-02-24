package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// setupActivityLogService returns an activityLogService wired to fresh mocks.
func setupActivityLogService() (*activityLogService, *MockActivityLogRepository) {
	activityRepo := NewMockActivityLogRepository()
	svc := NewActivityLogService(activityRepo).(*activityLogService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, activityRepo
}

// ---------------------------------------------------------------------------
// TestActivityLogService_Log
// ---------------------------------------------------------------------------

func TestActivityLogService_Log(t *testing.T) {
	tests := []struct {
		name      string
		entry     *domain.ActivityLog
		checkFunc func(t *testing.T, entry *domain.ActivityLog, repo *MockActivityLogRepository)
	}{
		{
			name: "success - generates ID and timestamp",
			entry: &domain.ActivityLog{
				WorkspaceID: uuid.New(),
				EntityType:  "task",
				EntityID:    uuid.New(),
				Action:      "created",
				ActorID:     uuid.New(),
				ActorType:   domain.ActorTypeUser,
			},
			checkFunc: func(t *testing.T, entry *domain.ActivityLog, repo *MockActivityLogRepository) {
				assert.NotEqual(t, uuid.Nil, entry.ID)
				assert.Equal(t, frozenTime, entry.CreatedAt)

				stored := repo.items[entry.ID]
				require.NotNil(t, stored)
				assert.Equal(t, "task", stored.EntityType)
				assert.Equal(t, "created", stored.Action)
			},
		},
		{
			name: "success - preserves provided ID",
			entry: &domain.ActivityLog{
				ID:          uuid.New(),
				WorkspaceID: uuid.New(),
				EntityType:  "project",
				EntityID:    uuid.New(),
				Action:      "archived",
				ActorID:     uuid.New(),
				ActorType:   domain.ActorTypeSystem,
			},
			checkFunc: func(t *testing.T, entry *domain.ActivityLog, repo *MockActivityLogRepository) {
				stored := repo.items[entry.ID]
				require.NotNil(t, stored)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, activityRepo := setupActivityLogService()
			ctx := context.Background()

			err := svc.Log(ctx, tt.entry)

			require.NoError(t, err)
			if tt.checkFunc != nil {
				tt.checkFunc(t, tt.entry, activityRepo)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestActivityLogService_List
// ---------------------------------------------------------------------------

func TestActivityLogService_List(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockActivityLogRepository) uuid.UUID
		wantLen int
	}{
		{
			name: "with matching entries",
			setup: func(repo *MockActivityLogRepository) uuid.UUID {
				wsID := uuid.New()
				for i := 0; i < 3; i++ {
					id := uuid.New()
					repo.items[id] = &domain.ActivityLog{
						ID:          id,
						WorkspaceID: wsID,
						EntityType:  "task",
						Action:      "updated",
					}
				}
				// Entry in another workspace.
				otherID := uuid.New()
				repo.items[otherID] = &domain.ActivityLog{
					ID:          otherID,
					WorkspaceID: uuid.New(),
					EntityType:  "task",
					Action:      "created",
				}
				return wsID
			},
			wantLen: 3,
		},
		{
			name: "empty result",
			setup: func(_ *MockActivityLogRepository) uuid.UUID {
				return uuid.New()
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, activityRepo := setupActivityLogService()
			ctx := context.Background()
			wsID := tt.setup(activityRepo)

			page, err := svc.List(ctx, wsID, repository.ActivityLogFilter{}, pagination.Params{Page: 1, PageSize: 50})

			require.NoError(t, err)
			require.NotNil(t, page)
			assert.Len(t, page.Items, tt.wantLen)
			assert.Equal(t, tt.wantLen, page.TotalCount)
		})
	}
}
