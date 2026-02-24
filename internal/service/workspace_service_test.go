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

// setupWorkspaceService returns a workspaceService wired to fresh mocks.
func setupWorkspaceService() (*workspaceService, *MockWorkspaceRepository, *MockActivityLogRepository) {
	wsRepo := NewMockWorkspaceRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewWorkspaceService(wsRepo, activityRepo).(*workspaceService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, wsRepo, activityRepo
}

// ---------------------------------------------------------------------------
// TestWorkspaceService_Create
// ---------------------------------------------------------------------------

func TestWorkspaceService_Create(t *testing.T) {
	tests := []struct {
		name      string
		workspace *domain.Workspace
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, ws *domain.Workspace, repo *MockWorkspaceRepository)
	}{
		{
			name: "success - generates ID, slug, and timestamps",
			workspace: &domain.Workspace{
				Name:    "Acme Corp",
				OwnerID: uuid.New(),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, ws *domain.Workspace, repo *MockWorkspaceRepository) {
				assert.NotEqual(t, uuid.Nil, ws.ID, "ID should be generated")
				assert.Equal(t, "acme-corp", ws.Slug, "slug should be generated from name")
				assert.Equal(t, frozenTime, ws.CreatedAt)
				assert.Equal(t, frozenTime, ws.UpdatedAt)

				// Verify persisted.
				stored, err := repo.GetByID(context.Background(), ws.ID)
				require.NoError(t, err)
				assert.Equal(t, ws.Name, stored.Name)
			},
		},
		{
			name: "success - preserves provided slug",
			workspace: &domain.Workspace{
				Name:    "Acme Corp",
				Slug:    "custom-slug",
				OwnerID: uuid.New(),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, ws *domain.Workspace, _ *MockWorkspaceRepository) {
				assert.Equal(t, "custom-slug", ws.Slug)
			},
		},
		{
			name: "error - empty name",
			workspace: &domain.Workspace{
				Name:    "",
				OwnerID: uuid.New(),
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "error - whitespace-only name",
			workspace: &domain.Workspace{
				Name:    "   ",
				OwnerID: uuid.New(),
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "error - invalid slug",
			workspace: &domain.Workspace{
				Name:    "Acme Corp",
				Slug:    "INVALID_SLUG!",
				OwnerID: uuid.New(),
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, wsRepo, _ := setupWorkspaceService()
			ctx := context.Background()

			err := svc.Create(ctx, tt.workspace)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, tt.workspace, wsRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestWorkspaceService_GetByID
// ---------------------------------------------------------------------------

func TestWorkspaceService_GetByID(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockWorkspaceRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "found",
			setup: func(repo *MockWorkspaceRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Workspace{ID: id, Name: "Test WS", Slug: "test-ws"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found returns 404",
			setup: func(_ *MockWorkspaceRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, wsRepo, _ := setupWorkspaceService()
			ctx := context.Background()
			id := tt.setup(wsRepo)

			ws, err := svc.GetByID(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, ws)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, ws)
				assert.Equal(t, id, ws.ID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestWorkspaceService_GetBySlug
// ---------------------------------------------------------------------------

func TestWorkspaceService_GetBySlug(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockWorkspaceRepository) string
		wantErr bool
		errCode int
	}{
		{
			name: "found",
			setup: func(repo *MockWorkspaceRepository) string {
				id := uuid.New()
				repo.items[id] = &domain.Workspace{ID: id, Name: "Test WS", Slug: "test-ws"}
				return "test-ws"
			},
			wantErr: false,
		},
		{
			name: "not found returns 404",
			setup: func(_ *MockWorkspaceRepository) string {
				return "nonexistent"
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, wsRepo, _ := setupWorkspaceService()
			ctx := context.Background()
			slug := tt.setup(wsRepo)

			ws, err := svc.GetBySlug(ctx, slug)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, ws)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, ws)
				assert.Equal(t, slug, ws.Slug)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestWorkspaceService_Update
// ---------------------------------------------------------------------------

func TestWorkspaceService_Update(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockWorkspaceRepository) *domain.Workspace
		wantErr bool
		errCode int
	}{
		{
			name: "success",
			setup: func(repo *MockWorkspaceRepository) *domain.Workspace {
				id := uuid.New()
				repo.items[id] = &domain.Workspace{ID: id, Name: "Old Name", Slug: "old-name"}
				return &domain.Workspace{ID: id, Name: "New Name", Slug: "new-name"}
			},
			wantErr: false,
		},
		{
			name: "not found",
			setup: func(_ *MockWorkspaceRepository) *domain.Workspace {
				return &domain.Workspace{ID: uuid.New(), Name: "Ghost", Slug: "ghost"}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, wsRepo, _ := setupWorkspaceService()
			ctx := context.Background()
			ws := tt.setup(wsRepo)

			err := svc.Update(ctx, ws)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				assert.Equal(t, frozenTime, ws.UpdatedAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestWorkspaceService_Delete
// ---------------------------------------------------------------------------

func TestWorkspaceService_Delete(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockWorkspaceRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "success",
			setup: func(repo *MockWorkspaceRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Workspace{ID: id, Name: "To Delete", Slug: "to-delete"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found returns error",
			setup: func(_ *MockWorkspaceRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, wsRepo, _ := setupWorkspaceService()
			ctx := context.Background()
			id := tt.setup(wsRepo)

			err := svc.Delete(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				_, exists := wsRepo.items[id]
				assert.False(t, exists, "workspace should be deleted from repo")
			}
		})
	}
}
