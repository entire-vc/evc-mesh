package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// slugPattern matches a valid slug: lowercase alphanumeric and hyphens,
// must start and end with an alphanumeric character.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type workspaceService struct {
	workspaceRepo repository.WorkspaceRepository
	activityRepo  repository.ActivityLogRepository
}

// NewWorkspaceService returns a new WorkspaceService backed by the given repositories.
func NewWorkspaceService(
	workspaceRepo repository.WorkspaceRepository,
	activityRepo repository.ActivityLogRepository,
) WorkspaceService {
	return &workspaceService{
		workspaceRepo: workspaceRepo,
		activityRepo:  activityRepo,
	}
}

// Create validates and persists a new workspace.
func (s *workspaceService) Create(ctx context.Context, workspace *domain.Workspace) error {
	if strings.TrimSpace(workspace.Name) == "" {
		return apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}

	if workspace.Slug == "" {
		workspace.Slug = slugify(workspace.Name)
	}

	if !slugPattern.MatchString(workspace.Slug) {
		return apierror.ValidationError(map[string]string{
			"slug": "slug must be lowercase alphanumeric with hyphens only",
		})
	}

	if workspace.ID == uuid.Nil {
		workspace.ID = uuid.New()
	}

	now := timeNow()
	workspace.CreatedAt = now
	workspace.UpdatedAt = now

	return s.workspaceRepo.Create(ctx, workspace)
}

// GetByID retrieves a workspace by its ID.
func (s *workspaceService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
	ws, err := s.workspaceRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, apierror.NotFound("Workspace")
	}
	return ws, nil
}

// GetBySlug retrieves a workspace by its slug.
func (s *workspaceService) GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error) {
	ws, err := s.workspaceRepo.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, apierror.NotFound("Workspace")
	}
	return ws, nil
}

// Update validates that the workspace exists and persists changes.
func (s *workspaceService) Update(ctx context.Context, workspace *domain.Workspace) error {
	existing, err := s.workspaceRepo.GetByID(ctx, workspace.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Workspace")
	}

	workspace.UpdatedAt = timeNow()
	return s.workspaceRepo.Update(ctx, workspace)
}

// Delete removes a workspace after verifying it exists.
func (s *workspaceService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.workspaceRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Workspace")
	}
	return s.workspaceRepo.Delete(ctx, id)
}

// ListByOwner returns all workspaces owned by the given user.
func (s *workspaceService) ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
	return s.workspaceRepo.ListByOwner(ctx, ownerID)
}
