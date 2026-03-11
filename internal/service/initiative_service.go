package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type initiativeService struct {
	initiativeRepo repository.InitiativeRepository
	projectRepo    repository.ProjectRepository
	db             *sqlx.DB
}

// NewInitiativeService creates a new InitiativeService.
func NewInitiativeService(
	initiativeRepo repository.InitiativeRepository,
	projectRepo repository.ProjectRepository,
	db *sqlx.DB,
) InitiativeService {
	return &initiativeService{
		initiativeRepo: initiativeRepo,
		projectRepo:    projectRepo,
		db:             db,
	}
}

// enrichProgress populates TotalTasks and CompletedTasks on the initiative
// by counting tasks across all linked projects using a single JOIN query.
func (s *initiativeService) enrichProgress(ctx context.Context, ini *domain.Initiative) {
	if s.db == nil {
		return
	}
	const q = `
		SELECT
			COUNT(*) FILTER (WHERE ts.category NOT IN ('cancelled')) AS total,
			COUNT(*) FILTER (WHERE ts.category = 'done') AS completed
		FROM tasks t
		JOIN task_statuses ts ON ts.id = t.status_id
		JOIN initiative_projects ip ON ip.project_id = t.project_id
		WHERE ip.initiative_id = $1 AND t.deleted_at IS NULL
	`
	var total, completed int
	if err := s.db.QueryRowContext(ctx, q, ini.ID).Scan(&total, &completed); err == nil {
		ini.TotalTasks = total
		ini.CompletedTasks = completed
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

	s.enrichProgress(ctx, ini)

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

// List returns all initiatives for a workspace with progress counts.
func (s *initiativeService) List(ctx context.Context, workspaceID uuid.UUID) ([]domain.Initiative, error) {
	items, err := s.initiativeRepo.List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		s.enrichProgress(ctx, &items[i])
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
