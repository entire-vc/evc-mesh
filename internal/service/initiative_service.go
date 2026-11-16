package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type initiativeService struct {
	initiativeRepo repository.InitiativeRepository
	projectRepo    repository.ProjectRepository
}

// NewInitiativeService creates a new InitiativeService.
func NewInitiativeService(
	initiativeRepo repository.InitiativeRepository,
	projectRepo repository.ProjectRepository,
) InitiativeService {
	return &initiativeService{
		initiativeRepo: initiativeRepo,
		projectRepo:    projectRepo,
	}
}

// enrichProgress populates TotalTasks and CompletedTasks on the initiative
// from the linked projects the viewer may open (PRD dashboards §6.3): a member
// outside one of the projects must not read its task counts through the bar.
func (s *initiativeService) enrichProgress(ctx context.Context, ini *domain.Initiative, viewer domain.ProjectViewer) error {
	total, completed, err := s.initiativeRepo.Progress(ctx, ini.ID, viewer)
	if err != nil {
		return err
	}
	ini.TotalTasks = total
	ini.CompletedTasks = completed
	return nil
}

// Create validates and persists a new initiative.
func (s *initiativeService) Create(ctx context.Context, input CreateInitiativeInput) (*domain.Initiative, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}

	if input.Status == "" {
		input.Status = domain.InitiativeStatusActive
	}
	switch input.Status {
	case domain.InitiativeStatusActive, domain.InitiativeStatusCompleted, domain.InitiativeStatusArchived:
		// valid
	default:
		return nil, apierror.ValidationError(map[string]string{
			"status": "status must be one of: active, completed, archived",
		})
	}

	now := timeNow()
	ini := &domain.Initiative{
		ID:          uuid.New(),
		WorkspaceID: input.WorkspaceID,
		Name:        input.Name,
		Description: input.Description,
		Status:      input.Status,
		TargetDate:  input.TargetDate,
		CreatedBy:   input.CreatedBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.initiativeRepo.Create(ctx, ini); err != nil {
		return nil, err
	}
	return ini, nil
}

// GetByID retrieves an initiative with the linked projects the viewer may open
// and the progress over them.
func (s *initiativeService) GetByID(ctx context.Context, id uuid.UUID, viewer domain.ProjectViewer) (*domain.Initiative, error) {
	ini, err := s.initiativeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ini == nil {
		return nil, apierror.NotFound("Initiative")
	}

	// Hydrate linked projects.
	projects, err := s.initiativeRepo.ListLinkedProjects(ctx, id, viewer)
	if err != nil {
		return nil, err
	}
	ini.LinkedProjects = projects

	if err := s.enrichProgress(ctx, ini, viewer); err != nil {
		return nil, err
	}

	return ini, nil
}

// Update applies partial changes to an existing initiative.
func (s *initiativeService) Update(ctx context.Context, id uuid.UUID, input UpdateInitiativeInput) (*domain.Initiative, error) {
	ini, err := s.initiativeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ini == nil {
		return nil, apierror.NotFound("Initiative")
	}

	if input.Name != nil {
		if strings.TrimSpace(*input.Name) == "" {
			return nil, apierror.ValidationError(map[string]string{
				"name": "name cannot be empty",
			})
		}
		ini.Name = *input.Name
	}
	if input.Description != nil {
		ini.Description = *input.Description
	}
	if input.Status != nil {
		switch *input.Status {
		case domain.InitiativeStatusActive, domain.InitiativeStatusCompleted, domain.InitiativeStatusArchived:
			// valid
		default:
			return nil, apierror.ValidationError(map[string]string{
				"status": "status must be one of: active, completed, archived",
			})
		}
		ini.Status = *input.Status
	}
	if input.ClearTargetDate {
		ini.TargetDate = nil
	} else if input.TargetDate != nil {
		ini.TargetDate = input.TargetDate
	}

	ini.UpdatedAt = timeNow()

	if err := s.initiativeRepo.Update(ctx, ini); err != nil {
		return nil, err
	}
	return ini, nil
}

// Delete removes an initiative and its project links (cascade).
func (s *initiativeService) Delete(ctx context.Context, id uuid.UUID) error {
	ini, err := s.initiativeRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if ini == nil {
		return apierror.NotFound("Initiative")
	}
	return s.initiativeRepo.Delete(ctx, id)
}

// List returns all initiatives for a workspace with progress counted over the
// linked projects the viewer may open.
func (s *initiativeService) List(ctx context.Context, workspaceID uuid.UUID, viewer domain.ProjectViewer) ([]domain.Initiative, error) {
	items, err := s.initiativeRepo.List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err := s.enrichProgress(ctx, &items[i], viewer); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// LinkProject associates a project with an initiative.
func (s *initiativeService) LinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error {
	ini, err := s.initiativeRepo.GetByID(ctx, initiativeID)
	if err != nil {
		return err
	}
	if ini == nil {
		return apierror.NotFound("Initiative")
	}

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return apierror.NotFound("Project")
	}

	// The project has to be in the initiative's own workspace.
	//
	// project_id arrives in the request body, so no route parameter names it and
	// the workspace guard cannot see it — only :init_id is checked, and that is
	// the caller's own initiative. Until this check existed, linking was allowed
	// on "both exist", so a member of any workspace could graft a stranger's
	// project onto their own initiative and read its name and summary back out of
	// every initiative view.
	if project.WorkspaceID != ini.WorkspaceID {
		return apierror.BadRequest("project belongs to a different workspace")
	}

	return s.initiativeRepo.LinkProject(ctx, initiativeID, projectID)
}

// UnlinkProject removes a project association from an initiative.
func (s *initiativeService) UnlinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error {
	ini, err := s.initiativeRepo.GetByID(ctx, initiativeID)
	if err != nil {
		return err
	}
	if ini == nil {
		return apierror.NotFound("Initiative")
	}
	return s.initiativeRepo.UnlinkProject(ctx, initiativeID, projectID)
}
