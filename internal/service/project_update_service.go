package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type projectUpdateService struct {
	updateRepo  repository.ProjectUpdateRepository
	projectRepo repository.ProjectRepository
	taskRepo    repository.TaskRepository
	statusRepo  repository.TaskStatusRepository
}

// NewProjectUpdateService creates a new ProjectUpdateService.
func NewProjectUpdateService(
	updateRepo repository.ProjectUpdateRepository,
	projectRepo repository.ProjectRepository,
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
) ProjectUpdateService {
	return &projectUpdateService{
		updateRepo:  updateRepo,
		projectRepo: projectRepo,
		taskRepo:    taskRepo,
		statusRepo:  statusRepo,
	}
}

// Create validates and persists a new project update, auto-populating metrics.
func (s *projectUpdateService) Create(ctx context.Context, input CreateProjectUpdateInput) (*domain.ProjectUpdate, error) {
	if strings.TrimSpace(input.Title) == "" {
		return nil, apierror.ValidationError(map[string]string{
			"title": "title is required",
		})
	}
	if strings.TrimSpace(input.Summary) == "" {
		return nil, apierror.ValidationError(map[string]string{
			"summary": "summary is required",
		})
	}

	// Validate the project exists.
	project, err := s.projectRepo.GetByID(ctx, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, apierror.NotFound("Project")
	}

	// Validate status value.
	switch input.Status {
	case domain.UpdateStatusOnTrack, domain.UpdateStatusAtRisk,
		domain.UpdateStatusOffTrack, domain.UpdateStatusCompleted:
		// valid
	default:
		if input.Status == "" {
			input.Status = domain.UpdateStatusOnTrack
		} else {
			return nil, apierror.ValidationError(map[string]string{
				"status": "status must be one of: on_track, at_risk, off_track, completed",
			})
		}
	}

	// Auto-populate metrics from task counts.
	metrics, err := s.buildMetrics(ctx, input.ProjectID)
	if err != nil {
		// Non-fatal: continue with empty metrics.
		metrics = &domain.ProjectUpdateMetrics{}
	}
	metricsJSON, err := json.Marshal(metrics)
	if err != nil {
		metricsJSON = json.RawMessage(`{}`)
	}

	// Encode JSONB list fields.
	highlightsJSON, err := json.Marshal(input.Highlights)
	if err != nil {
		highlightsJSON = json.RawMessage(`[]`)
	}
	blockersJSON, err := json.Marshal(input.Blockers)
	if err != nil {
		blockersJSON = json.RawMessage(`[]`)
	}
	nextStepsJSON, err := json.Marshal(input.NextSteps)
	if err != nil {
		nextStepsJSON = json.RawMessage(`[]`)
	}

	update := &domain.ProjectUpdate{
		ID:         uuid.New(),
		ProjectID:  input.ProjectID,
		Title:      input.Title,
		Status:     input.Status,
		Summary:    input.Summary,
		Highlights: highlightsJSON,
		Blockers:   blockersJSON,
		NextSteps:  nextStepsJSON,
		Metrics:    metricsJSON,
		CreatedBy:  input.CreatedBy,
		CreatedAt:  timeNow(),
	}

	if err := s.updateRepo.Create(ctx, update); err != nil {
		return nil, err
	}

	return update, nil
}

// List returns paginated project updates, most recent first.
func (s *projectUpdateService) List(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ProjectUpdate], error) {
	pg.Normalize()
	return s.updateRepo.List(ctx, projectID, pg)
}

// GetLatest returns the most recent update for a project, or nil if none exists.
func (s *projectUpdateService) GetLatest(ctx context.Context, projectID uuid.UUID) (*domain.ProjectUpdate, error) {
	return s.updateRepo.GetLatest(ctx, projectID)
}

// buildMetrics queries task counts by status category for the project.
func (s *projectUpdateService) buildMetrics(ctx context.Context, projectID uuid.UUID) (*domain.ProjectUpdateMetrics, error) {
	counts, err := s.taskRepo.CountByStatusCategory(ctx, projectID)
	if err != nil {
		return nil, err
	}

	m := &domain.ProjectUpdateMetrics{}
	for cat, cnt := range counts {
		m.TasksTotal += cnt
		switch cat {
		case domain.StatusCategoryDone:
			m.TasksCompleted += cnt
		case domain.StatusCategoryInProgress:
			m.TasksInProgress += cnt
		}
	}
	return m, nil
}
