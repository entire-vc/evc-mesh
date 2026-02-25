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

// GetByID retrieves an initiative with its linked projects.
func (s *initiativeService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Initiative, error) {
	ini, err := s.initiativeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ini == nil {
		return nil, apierror.NotFound("Initiative")
	}

	// Hydrate linked projects.
	projects, err := s.initiativeRepo.ListLinkedProjects(ctx, id)
	if err != nil {
		return nil, err
	}
	ini.LinkedProjects = projects

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
	if input.TargetDate != nil {
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

// List returns all initiatives for a workspace.
func (s *initiativeService) List(ctx context.Context, workspaceID uuid.UUID) ([]domain.Initiative, error) {
	return s.initiativeRepo.List(ctx, workspaceID)
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
