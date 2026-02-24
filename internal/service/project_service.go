package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// defaultStatuses defines the task statuses created automatically for every new project.
var defaultStatuses = []struct {
	Name      string
	Slug      string
	Category  domain.StatusCategory
	IsDefault bool
	Position  int
}{
	{Name: "Backlog", Slug: "backlog", Category: domain.StatusCategoryBacklog, IsDefault: false, Position: 0},
	{Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Position: 1},
	{Name: "In Progress", Slug: "in_progress", Category: domain.StatusCategoryInProgress, IsDefault: false, Position: 2},
	{Name: "Review", Slug: "review", Category: domain.StatusCategoryReview, IsDefault: false, Position: 3},
	{Name: "Done", Slug: "done", Category: domain.StatusCategoryDone, IsDefault: false, Position: 4},
}

type projectService struct {
	projectRepo  repository.ProjectRepository
	statusRepo   repository.TaskStatusRepository
	activityRepo repository.ActivityLogRepository
}

// NewProjectService returns a new ProjectService backed by the given repositories.
func NewProjectService(
	projectRepo repository.ProjectRepository,
	statusRepo repository.TaskStatusRepository,
	activityRepo repository.ActivityLogRepository,
) ProjectService {
	return &projectService{
		projectRepo:  projectRepo,
		statusRepo:   statusRepo,
		activityRepo: activityRepo,
	}
}

// Create validates and persists a new project, then creates the default task statuses.
func (s *projectService) Create(ctx context.Context, project *domain.Project) error {
	if strings.TrimSpace(project.Name) == "" {
		return apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}

	if project.Slug == "" {
		project.Slug = slugify(project.Name)
	}

	if !slugPattern.MatchString(project.Slug) {
		return apierror.ValidationError(map[string]string{
			"slug": "slug must be lowercase alphanumeric with hyphens only",
		})
	}

	if project.ID == uuid.Nil {
		project.ID = uuid.New()
	}

	now := timeNow()
	project.CreatedAt = now
	project.UpdatedAt = now

	if err := s.projectRepo.Create(ctx, project); err != nil {
		return err
	}

	// Create default task statuses for the new project.
	for _, ds := range defaultStatuses {
		status := &domain.TaskStatus{
			ID:        uuid.New(),
			ProjectID: project.ID,
			Name:      ds.Name,
			Slug:      ds.Slug,
			Category:  ds.Category,
			IsDefault: ds.IsDefault,
			Position:  ds.Position,
		}
		if err := s.statusRepo.Create(ctx, status); err != nil {
			return err
		}
	}

	return nil
}

// GetByID retrieves a project by its ID.
func (s *projectService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	project, err := s.projectRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, apierror.NotFound("Project")
	}
	return project, nil
}

// Update validates that the project exists and persists changes.
func (s *projectService) Update(ctx context.Context, project *domain.Project) error {
	existing, err := s.projectRepo.GetByID(ctx, project.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Project")
	}

	project.UpdatedAt = timeNow()
	return s.projectRepo.Update(ctx, project)
}

// Archive sets is_archived=true on the project.
func (s *projectService) Archive(ctx context.Context, id uuid.UUID) error {
	project, err := s.projectRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if project == nil {
		return apierror.NotFound("Project")
	}

	project.IsArchived = true
	project.UpdatedAt = timeNow()
	return s.projectRepo.Update(ctx, project)
}

// Unarchive sets is_archived=false on the project.
func (s *projectService) Unarchive(ctx context.Context, id uuid.UUID) error {
	project, err := s.projectRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if project == nil {
		return apierror.NotFound("Project")
	}

	project.IsArchived = false
	project.UpdatedAt = timeNow()
	return s.projectRepo.Update(ctx, project)
}

// List returns a paginated list of projects for the given workspace.
func (s *projectService) List(ctx context.Context, workspaceID uuid.UUID, filter repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error) {
	pg.Normalize()
	return s.projectRepo.List(ctx, workspaceID, filter, pg)
}
