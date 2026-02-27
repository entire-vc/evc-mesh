package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type taskStatusService struct {
	statusRepo   repository.TaskStatusRepository
	taskRepo     repository.TaskRepository
	activityRepo repository.ActivityLogRepository
}

// NewTaskStatusService returns a new TaskStatusService backed by the given repositories.
func NewTaskStatusService(
	statusRepo repository.TaskStatusRepository,
	taskRepo repository.TaskRepository,
	activityRepo repository.ActivityLogRepository,
) TaskStatusService {
	return &taskStatusService{
		statusRepo:   statusRepo,
		taskRepo:     taskRepo,
		activityRepo: activityRepo,
	}
}

// Create validates and persists a new task status.
// It generates a slug from the name and assigns the next available position.
func (s *taskStatusService) Create(ctx context.Context, status *domain.TaskStatus) error {
	if strings.TrimSpace(status.Name) == "" {
		return apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}

	if status.Slug == "" {
		status.Slug = slugify(status.Name)
	}

	if status.ID == uuid.Nil {
		status.ID = uuid.New()
	}

	// Assign the next position by counting existing statuses in the project.
	existing, err := s.statusRepo.ListByProject(ctx, status.ProjectID)
	if err != nil {
		return err
	}
	maxPos := -1
	for _, es := range existing {
		if es.Position > maxPos {
			maxPos = es.Position
		}
	}
	status.Position = maxPos + 1

	return s.statusRepo.Create(ctx, status)
}

// Update validates that the status exists and persists changes.
// It merges only the non-zero fields from the incoming status onto the existing record.
func (s *taskStatusService) Update(ctx context.Context, status *domain.TaskStatus) error {
	existing, err := s.statusRepo.GetByID(ctx, status.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("TaskStatus")
	}

	// Merge: apply non-zero incoming fields onto existing.
	if status.Name != "" {
		existing.Name = status.Name
		// Auto-regenerate slug when name changes (unless slug is explicitly set).
		if status.Slug == "" {
			existing.Slug = slugify(status.Name)
		}
	}
	if status.Slug != "" {
		existing.Slug = status.Slug
	}
	if status.Color != "" {
		existing.Color = status.Color
	}
	if status.Category != "" {
		existing.Category = status.Category
	}
	if status.AutoTransition != nil {
		existing.AutoTransition = status.AutoTransition
	}

	if err := s.statusRepo.Update(ctx, existing); err != nil {
		return err
	}

	// Copy merged state back so the handler can return the full record.
	*status = *existing
	return nil
}

// Delete removes a task status after verifying it exists and no tasks reference it.
func (s *taskStatusService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.statusRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("TaskStatus")
	}

	// Check that no tasks use this status.
	counts, err := s.taskRepo.CountByStatus(ctx, existing.ProjectID)
	if err != nil {
		return err
	}
	if counts[id] > 0 {
		return apierror.BadRequest("cannot delete status that is still used by tasks")
	}

	return s.statusRepo.Delete(ctx, id)
}

// ListByProject returns all task statuses for the given project.
func (s *taskStatusService) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error) {
	return s.statusRepo.ListByProject(ctx, projectID)
}

// Reorder sets the order of statuses within a project.
// All provided IDs must belong to the same project.
func (s *taskStatusService) Reorder(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error {
	// Validate all IDs belong to the given project.
	existing, err := s.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		return err
	}
	existingSet := make(map[uuid.UUID]bool, len(existing))
	for _, es := range existing {
		existingSet[es.ID] = true
	}

	for _, sid := range statusIDs {
		if !existingSet[sid] {
			return apierror.BadRequest("status ID does not belong to the specified project")
		}
	}

	return s.statusRepo.Reorder(ctx, projectID, statusIDs)
}
