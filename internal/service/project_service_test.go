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
func setupProjectService() (*projectService, *MockProjectRepository, *MockTaskStatusRepository) {
	projectRepo := NewMockProjectRepository()
	statusRepo := NewMockTaskStatusRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewProjectService(projectRepo, statusRepo, activityRepo).(*projectService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, projectRepo, statusRepo
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

				// Verify the 7 default statuses: one per category, unique slugs.
				statuses, err := statusRepo.ListByProject(context.Background(), project.ID)
				require.NoError(t, err)
				assert.Len(t, statuses, 7, "should create 7 default statuses")

				// Verify the default status is "Todo" and nothing else is default.
				var defaults int
				for _, s := range statuses {
					if s.IsDefault {
						assert.Equal(t, "Todo", s.Name)
						assert.Equal(t, domain.StatusCategoryTodo, s.Category)
						defaults++
					}
				}
				assert.Equal(t, 1, defaults, "exactly one default status")

				categories := make(map[domain.StatusCategory]int)
				slugs := make(map[string]int)
				for _, s := range statuses {
					categories[s.Category]++
					slugs[s.Slug]++
				}
				for _, c := range []domain.StatusCategory{
					domain.StatusCategoryTriage,
					domain.StatusCategoryBacklog,
					domain.StatusCategoryTodo,
					domain.StatusCategoryInProgress,
					domain.StatusCategoryReview,
					domain.StatusCategoryDone,
					domain.StatusCategoryCancelled,
				} {
					assert.Equal(t, 1, categories[c], "category %s", c)
				}
				for slug, n := range slugs {
					assert.Equal(t, 1, n, "duplicate slug %s", slug)
				}
				assert.Equal(t, 1, slugs["triage"])
				assert.Equal(t, 1, slugs["cancelled"])
				assert.Zero(t, slugs["canceled"], "canonical slug is cancelled")
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
			svc, projectRepo, statusRepo := setupProjectService()
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
			svc, projectRepo, _ := setupProjectService()
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
			svc, projectRepo, _ := setupProjectService()
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
			svc, projectRepo, _ := setupProjectService()
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

// ---------------------------------------------------------------------------
// TestProjectService_Delete
// ---------------------------------------------------------------------------

func TestProjectService_Delete(t *testing.T) {
	svc, projectRepo, _ := setupProjectService()
	ctx := context.Background()
	id := uuid.New()
	projectRepo.items[id] = &domain.Project{ID: id, Name: "Doomed"}

	require.NoError(t, svc.Delete(ctx, id))
	_, stillThere := projectRepo.items[id]
	assert.False(t, stillThere, "Delete must reach the repository delete, not archive")

	projectRepo.errToReturn = apierror.NotFound("Project")
	err := svc.Delete(ctx, uuid.New())
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}
