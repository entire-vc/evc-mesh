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
func setupWorkspaceService() (*workspaceService, *MockWorkspaceRepository) {
	wsRepo := NewMockWorkspaceRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewWorkspaceService(wsRepo, activityRepo).(*workspaceService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, wsRepo
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
			svc, wsRepo := setupWorkspaceService()
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
			svc, wsRepo := setupWorkspaceService()
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
			svc, wsRepo := setupWorkspaceService()
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
			svc, wsRepo := setupWorkspaceService()
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
			svc, wsRepo := setupWorkspaceService()
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

// ---------------------------------------------------------------------------
// TestWorkspaceService_ListForUser
// ---------------------------------------------------------------------------

// newWS is a small helper for building workspaces in list tests.
func newWS(name string, ownerID uuid.UUID, createdAt time.Time) *domain.Workspace {
	return &domain.Workspace{
		ID:        uuid.New(),
		Name:      name,
		Slug:      slugify(name),
		OwnerID:   ownerID,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

func TestWorkspaceService_ListForUser(t *testing.T) {
	ctx := context.Background()

	t.Run("member who is not the owner sees the workspace", func(t *testing.T) {
		svc, repo := setupWorkspaceService()
		owner, member := uuid.New(), uuid.New()

		ws := newWS("Owned By Someone Else", owner, frozenTime)
		require.NoError(t, repo.Create(ctx, ws))
		repo.AddMember(ws.ID, owner)
		repo.AddMember(ws.ID, member)

		got, err := svc.ListForUser(ctx, member)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, ws.ID, got[0].ID)
	})

	t.Run("non-member sees nothing", func(t *testing.T) {
		svc, repo := setupWorkspaceService()
		owner, outsider := uuid.New(), uuid.New()

		ws := newWS("Private", owner, frozenTime)
		require.NoError(t, repo.Create(ctx, ws))
		repo.AddMember(ws.ID, owner)

		got, err := svc.ListForUser(ctx, outsider)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("owner with no membership row still sees the workspace", func(t *testing.T) {
		svc, repo := setupWorkspaceService()
		owner := uuid.New()

		// Legacy row: workspace created before the auto-membership insert,
		// or the best-effort insert failed.
		ws := newWS("Legacy", owner, frozenTime)
		require.NoError(t, repo.Create(ctx, ws))

		got, err := svc.ListForUser(ctx, owner)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, ws.ID, got[0].ID)
	})

	t.Run("owner who is also a member appears exactly once", func(t *testing.T) {
		svc, repo := setupWorkspaceService()
		owner := uuid.New()

		ws := newWS("Both", owner, frozenTime)
		require.NoError(t, repo.Create(ctx, ws))
		repo.AddMember(ws.ID, owner)

		got, err := svc.ListForUser(ctx, owner)
		require.NoError(t, err)
		assert.Len(t, got, 1)
	})

	t.Run("removing the membership removes the workspace from the list", func(t *testing.T) {
		svc, repo := setupWorkspaceService()
		owner, member := uuid.New(), uuid.New()

		ws := newWS("Revocable", owner, frozenTime)
		require.NoError(t, repo.Create(ctx, ws))
		repo.AddMember(ws.ID, member)

		got, err := svc.ListForUser(ctx, member)
		require.NoError(t, err)
		require.Len(t, got, 1)

		repo.RemoveMember(ws.ID, member)

		got, err = svc.ListForUser(ctx, member)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("owned and member workspaces are combined and ordered by created_at", func(t *testing.T) {
		svc, repo := setupWorkspaceService()
		user, other := uuid.New(), uuid.New()

		older := newWS("Older Owned", user, frozenTime.Add(-time.Hour))
		newer := newWS("Newer Member", other, frozenTime)
		require.NoError(t, repo.Create(ctx, older))
		require.NoError(t, repo.Create(ctx, newer))
		repo.AddMember(newer.ID, user)

		got, err := svc.ListForUser(ctx, user)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, older.ID, got[0].ID, "oldest first")
		assert.Equal(t, newer.ID, got[1].ID)
	})

	t.Run("repository error is propagated", func(t *testing.T) {
		svc, repo := setupWorkspaceService()
		repo.errToReturn = apierror.InternalError("db down")

		got, err := svc.ListForUser(ctx, uuid.New())
		require.Error(t, err)
		assert.Nil(t, got)
	})
}
