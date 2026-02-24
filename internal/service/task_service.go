package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// timeNow is a package-level variable so tests can override the clock.
var timeNow = time.Now

type taskService struct {
	taskRepo         repository.TaskRepository
	statusRepo       repository.TaskStatusRepository
	depRepo          repository.TaskDependencyRepository
	activityRepo     repository.ActivityLogRepository
	customFieldSvc   CustomFieldService
}

// NewTaskService returns a new TaskService backed by the given repositories.
// The optional customFieldSvc enables custom field value validation on create/update.
func NewTaskService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	depRepo repository.TaskDependencyRepository,
	activityRepo repository.ActivityLogRepository,
	opts ...TaskServiceOption,
) TaskService {
	s := &taskService{
		taskRepo:     taskRepo,
		statusRepo:   statusRepo,
		depRepo:      depRepo,
		activityRepo: activityRepo,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// TaskServiceOption configures optional dependencies for TaskService.
type TaskServiceOption func(*taskService)

// WithCustomFieldService sets the custom field service for value validation.
func WithCustomFieldService(cfs CustomFieldService) TaskServiceOption {
	return func(s *taskService) {
		s.customFieldSvc = cfs
	}
}

// Create validates and persists a new task.
func (s *taskService) Create(ctx context.Context, task *domain.Task) error {
	if strings.TrimSpace(task.Title) == "" {
		return apierror.ValidationError(map[string]string{
			"title": "title is required",
		})
	}

	// Validate custom field values if a custom field service is available.
	if s.customFieldSvc != nil && len(task.CustomFields) > 0 && string(task.CustomFields) != "{}" && string(task.CustomFields) != "null" {
		var vals map[string]interface{}
		if err := json.Unmarshal(task.CustomFields, &vals); err == nil && len(vals) > 0 {
			if err := s.customFieldSvc.ValidateValues(ctx, task.ProjectID, vals, true); err != nil {
				return err
			}
		}
	}

	if task.ID == uuid.Nil {
		task.ID = uuid.New()
	}

	now := timeNow()
	task.CreatedAt = now
	task.UpdatedAt = now

	return s.taskRepo.Create(ctx, task)
}

// GetByID retrieves a task by its ID.
func (s *taskService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	task, err := s.taskRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, apierror.NotFound("Task")
	}
	return task, nil
}

// Update validates that the task exists and persists changes.
func (s *taskService) Update(ctx context.Context, task *domain.Task) error {
	existing, err := s.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Task")
	}

	// Validate custom field values if a custom field service is available.
	if s.customFieldSvc != nil && len(task.CustomFields) > 0 && string(task.CustomFields) != "{}" && string(task.CustomFields) != "null" {
		var vals map[string]interface{}
		if err := json.Unmarshal(task.CustomFields, &vals); err == nil && len(vals) > 0 {
			if err := s.customFieldSvc.ValidateValues(ctx, task.ProjectID, vals, false); err != nil {
				return err
			}
		}
	}

	task.UpdatedAt = timeNow()
	return s.taskRepo.Update(ctx, task)
}

// Delete removes a task after verifying it exists.
func (s *taskService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.taskRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Task")
	}
	return s.taskRepo.Delete(ctx, id)
}

// List returns a paginated list of tasks for the given project.
func (s *taskService) List(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()
	return s.taskRepo.List(ctx, projectID, filter, pg)
}

// MoveTask transitions a task to a new status and/or position.
// It validates that the status exists and belongs to the same project as the task.
// If the target status category is "done", it sets CompletedAt.
func (s *taskService) MoveTask(ctx context.Context, taskID uuid.UUID, input MoveTaskInput) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	if input.StatusID != nil {
		status, err := s.statusRepo.GetByID(ctx, *input.StatusID)
		if err != nil {
			return err
		}
		if status == nil {
			return apierror.NotFound("TaskStatus")
		}
		if status.ProjectID != task.ProjectID {
			return apierror.BadRequest("status does not belong to the same project as the task")
		}

		task.StatusID = *input.StatusID

		if status.Category == domain.StatusCategoryDone {
			now := timeNow()
			task.CompletedAt = &now
		} else {
			task.CompletedAt = nil
		}
	}

	if input.Position != nil {
		task.Position = *input.Position
	}

	task.UpdatedAt = timeNow()
	return s.taskRepo.Update(ctx, task)
}

// AssignTask assigns or unassigns a task.
func (s *taskService) AssignTask(ctx context.Context, taskID uuid.UUID, input AssignTaskInput) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	task.AssigneeID = input.AssigneeID
	task.AssigneeType = input.AssigneeType
	task.UpdatedAt = timeNow()

	return s.taskRepo.Update(ctx, task)
}

// CreateSubtask creates a child task under the given parent.
func (s *taskService) CreateSubtask(ctx context.Context, parentTaskID uuid.UUID, input CreateSubtaskInput) (*domain.Task, error) {
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, apierror.NotFound("Task")
	}

	now := timeNow()
	child := &domain.Task{
		ID:           uuid.New(),
		ProjectID:    parent.ProjectID,
		StatusID:     parent.StatusID,
		Title:        input.Title,
		Priority:     input.Priority,
		Description:  input.Description,
		ParentTaskID: &parentTaskID,
		AssigneeType: domain.AssigneeTypeUnassigned,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.taskRepo.Create(ctx, child); err != nil {
		return nil, err
	}
	return child, nil
}

// ListSubtasks returns all direct child tasks of the given parent.
func (s *taskService) ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	return s.taskRepo.ListSubtasks(ctx, parentTaskID)
}

// GetMyTasks returns all tasks assigned to the given actor.
func (s *taskService) GetMyTasks(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error) {
	return s.taskRepo.ListByAssignee(ctx, assigneeID, assigneeType)
}
