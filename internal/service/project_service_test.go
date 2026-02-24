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

// setupProjectService returns a projectService wired to fresh mocks.
func setupProjectService() (*projectService, *MockProjectRepository, *MockTaskStatusRepository, *MockActivityLogRepository) {
	projectRepo := NewMockProjectRepository()
	statusRepo := NewMockTaskStatusRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewProjectService(projectRepo, statusRepo, activityRepo).(*projectService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, projectRepo, statusRepo, activityRepo
}

// ---------------------------------------------------------------------------
// TestProjectService_Create
// ---------------------------------------------------------------------------

func TestProjectService_Create(t *testing.T) {
	tests := []struct {
		name      string
		project   *domain.Project
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, project *domain.Project, projectRepo *MockProjectRepository, statusRepo *MockTaskStatusRepository)
	}{
		{
			name: "success - creates project with default statuses",
			project: &domain.Project{
				WorkspaceID: uuid.New(),
				Name:        "My Project",
			},
			wantErr: false,
			checkFunc: func(t *testing.T, project *domain.Project, projectRepo *MockProjectRepository, statusRepo *MockTaskStatusRepository) {
				assert.NotEqual(t, uuid.Nil, project.ID)
				assert.Equal(t, "my-project", project.Slug)
				assert.Equal(t, frozenTime, project.CreatedAt)
				assert.Equal(t, frozenTime, project.UpdatedAt)

				// Verify project persisted.
				stored, err := projectRepo.GetByID(context.Background(), project.ID)
				require.NoError(t, err)
				assert.Equal(t, project.Name, stored.Name)

				// Verify 5 default statuses were created.
				statuses, err := statusRepo.ListByProject(context.Background(), project.ID)
				require.NoError(t, err)
				assert.Len(t, statuses, 5, "should create 5 default statuses")

				// Verify the default status is "Todo".
				var hasDefault bool
				for _, s := range statuses {
					if s.IsDefault {
						assert.Equal(t, "Todo", s.Name)
						assert.Equal(t, domain.StatusCategoryTodo, s.Category)
						hasDefault = true
					}
				}
				assert.True(t, hasDefault, "should have a default status")

				// Verify all categories are present.
				categories := make(map[domain.StatusCategory]bool)
				for _, s := range statuses {
					categories[s.Category] = true
				}
				assert.True(t, categories[domain.StatusCategoryBacklog])
				assert.True(t, categories[domain.StatusCategoryTodo])
				assert.True(t, categories[domain.StatusCategoryInProgress])
				assert.True(t, categories[domain.StatusCategoryReview])
				assert.True(t, categories[domain.StatusCategoryDone])
			},
		},
		{
			name: "error - empty name",
			project: &domain.Project{
				WorkspaceID: uuid.New(),
				Name:        "",
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "error - invalid slug",
			project: &domain.Project{
				WorkspaceID: uuid.New(),
				Name:        "My Project",
				Slug:        "INVALID!!",
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, projectRepo, statusRepo, _ := setupProjectService()
			ctx := context.Background()

			err := svc.Create(ctx, tt.project)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, tt.project, projectRepo, statusRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestProjectService_Archive
// ---------------------------------------------------------------------------

func TestProjectService_Archive(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockProjectRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "success - sets is_archived to true",
			setup: func(repo *MockProjectRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Project{
					ID:         id,
					Name:       "Archivable Project",
					IsArchived: false,
				}
				return id
			},
			wantErr: false,
		},
		{
			name: "error - project not found",
			setup: func(_ *MockProjectRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, projectRepo, _, _ := setupProjectService()
			ctx := context.Background()
			id := tt.setup(projectRepo)

			err := svc.Archive(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				project := projectRepo.items[id]
				require.NotNil(t, project)
				assert.True(t, project.IsArchived, "project should be archived")
				assert.Equal(t, frozenTime, project.UpdatedAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestProjectService_Unarchive
// ---------------------------------------------------------------------------

func TestProjectService_Unarchive(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockProjectRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "success - sets is_archived to false",
			setup: func(repo *MockProjectRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Project{
					ID:         id,
					Name:       "Archived Project",
					IsArchived: true,
				}
				return id
			},
			wantErr: false,
		},
		{
			name: "error - project not found",
			setup: func(_ *MockProjectRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, projectRepo, _, _ := setupProjectService()
			ctx := context.Background()
			id := tt.setup(projectRepo)

			err := svc.Unarchive(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				project := projectRepo.items[id]
				require.NotNil(t, project)
				assert.False(t, project.IsArchived, "project should be unarchived")
				assert.Equal(t, frozenTime, project.UpdatedAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestProjectService_GetByID
// ---------------------------------------------------------------------------

func TestProjectService_GetByID(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockProjectRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "found",
			setup: func(repo *MockProjectRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Project{ID: id, Name: "Test Project"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found returns 404",
			setup: func(_ *MockProjectRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, projectRepo, _, _ := setupProjectService()
			ctx := context.Background()
			id := tt.setup(projectRepo)

			project, err := svc.GetByID(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, project)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, project)
				assert.Equal(t, id, project.ID)
			}
		})
	}
}
