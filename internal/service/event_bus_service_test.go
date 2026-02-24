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

// setupEventBusService returns an eventBusService wired to fresh mocks.
func setupEventBusService() (*eventBusService, *MockEventBusMessageRepository, *MockActivityLogRepository) {
	eventRepo := NewMockEventBusMessageRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewEventBusService(eventRepo, activityRepo).(*eventBusService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, eventRepo, activityRepo
}

// ---------------------------------------------------------------------------
// TestEventBusService_Publish
// ---------------------------------------------------------------------------

func TestEventBusService_Publish(t *testing.T) {
	tests := []struct {
		name      string
		input     PublishEventInput
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, msg *domain.EventBusMessage, repo *MockEventBusMessageRepository)
	}{
		{
			name: "success - with explicit TTL",
			input: PublishEventInput{
				WorkspaceID: uuid.New(),
				ProjectID:   uuid.New(),
				EventType:   domain.EventTypeSummary,
				Subject:     "task.completed",
				Payload:     map[string]any{"task_id": "abc"},
				Tags:        []string{"important"},
				TTLSeconds:  3600,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, msg *domain.EventBusMessage, repo *MockEventBusMessageRepository) {
				assert.NotEqual(t, uuid.Nil, msg.ID)
				assert.Equal(t, "task.completed", msg.Subject)
				assert.Equal(t, domain.EventTypeSummary, msg.EventType)
				assert.Equal(t, frozenTime, msg.CreatedAt)
				require.NotNil(t, msg.ExpiresAt)

				// TTL of 3600 seconds = 1 hour after frozenTime.
				expectedExpiry := frozenTime.Add(3600 * time.Second)
				assert.Equal(t, expectedExpiry, *msg.ExpiresAt)

				assert.Equal(t, "3600 seconds", msg.TTL)

				// Verify persisted.
				stored := repo.items[msg.ID]
				require.NotNil(t, stored)
			},
		},
		{
			name: "success - uses default TTL when zero",
			input: PublishEventInput{
				WorkspaceID: uuid.New(),
				ProjectID:   uuid.New(),
				EventType:   domain.EventTypeContextUpdate,
				Subject:     "context.update",
				Payload:     map[string]any{},
				TTLSeconds:  0, // should use default (86400)
			},
			wantErr: false,
			checkFunc: func(t *testing.T, msg *domain.EventBusMessage, _ *MockEventBusMessageRepository) {
				require.NotNil(t, msg.ExpiresAt)
				expectedExpiry := frozenTime.Add(86400 * time.Second)
				assert.Equal(t, expectedExpiry, *msg.ExpiresAt)
				assert.Equal(t, "86400 seconds", msg.TTL)
			},
		},
		{
			name: "error - empty subject",
			input: PublishEventInput{
				WorkspaceID: uuid.New(),
				ProjectID:   uuid.New(),
				EventType:   domain.EventTypeSummary,
				Subject:     "",
				Payload:     map[string]any{},
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, eventRepo, _ := setupEventBusService()
			ctx := context.Background()

			msg, err := svc.Publish(ctx, tt.input)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, msg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, msg)
				if tt.checkFunc != nil {
					tt.checkFunc(t, msg, eventRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestEventBusService_CleanupExpired
// ---------------------------------------------------------------------------

func TestEventBusService_CleanupExpired(t *testing.T) {
	svc, _, _ := setupEventBusService()
	ctx := context.Background()

	count, err := svc.CleanupExpired(ctx)

	require.NoError(t, err)
	// Mock returns 0 by default.
	assert.Equal(t, int64(0), count)
}

// ---------------------------------------------------------------------------
// TestEventBusService_GetContext
// ---------------------------------------------------------------------------

func TestEventBusService_GetContext(t *testing.T) {
	svc, eventRepo, _ := setupEventBusService()
	ctx := context.Background()

	projectID := uuid.New()

	// Add some events.
	for i := 0; i < 3; i++ {
		id := uuid.New()
		eventRepo.items[id] = &domain.EventBusMessage{
			ID:        id,
			ProjectID: projectID,
			EventType: domain.EventTypeSummary,
			Subject:   "test",
			CreatedAt: frozenTime,
		}
	}

	msgs, err := svc.GetContext(ctx, projectID, GetContextOptions{Limit: 10})

	require.NoError(t, err)
	assert.Len(t, msgs, 3)
}
