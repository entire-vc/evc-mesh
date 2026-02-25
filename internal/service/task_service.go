package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// timeNow is a package-level variable so tests can override the clock.
var timeNow = time.Now

type taskService struct {
	taskRepo       repository.TaskRepository
	statusRepo     repository.TaskStatusRepository
	depRepo        repository.TaskDependencyRepository
	activityRepo   repository.ActivityLogRepository
	customFieldSvc CustomFieldService
	projectRepo    repository.ProjectRepository
	autoTransSvc   AutoTransitionService
	ruleSvc        RuleService
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

// WithProjectRepo sets the project repository used to resolve workspace_id for activity logging.
func WithProjectRepo(pr repository.ProjectRepository) TaskServiceOption {
	return func(s *taskService) {
		s.projectRepo = pr
	}
}

// WithAutoTransitionService sets the auto-transition service that fires after status changes.
func WithAutoTransitionService(ats AutoTransitionService) TaskServiceOption {
	return func(s *taskService) {
		s.autoTransSvc = ats
	}
}

// WithRuleService sets the optional rule service for governance rule evaluation on task operations.
func WithRuleService(rs RuleService) TaskServiceOption {
	return func(s *taskService) {
		s.ruleSvc = rs
	}
}

// SetAutoTransitionService implements TaskServiceAutoTransitionConfigurable,
// allowing the auto-transition service to be wired after construction.
func (s *taskService) SetAutoTransitionService(svc AutoTransitionService) {
	s.autoTransSvc = svc
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

	if err := s.taskRepo.Create(ctx, task); err != nil {
		return err
	}
	s.logActivity(ctx, task.ProjectID, task.ID, "task.created", map[string]interface{}{
		"title":    map[string]interface{}{"old": nil, "new": task.Title},
		"priority": map[string]interface{}{"old": nil, "new": string(task.Priority)},
	})
	return nil
}

// GetDefaultStatus returns the default task status for a project.
func (s *taskService) GetDefaultStatus(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error) {
	return s.statusRepo.GetDefaultForProject(ctx, projectID)
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
	if err := s.taskRepo.Update(ctx, task); err != nil {
		return err
	}

	// Build diff between existing and updated task.
	changes := map[string]interface{}{}
	if existing.Title != task.Title {
		changes["title"] = map[string]interface{}{"old": existing.Title, "new": task.Title}
	}
	if existing.Description != task.Description {
		changes["description"] = map[string]interface{}{"old": existing.Description, "new": task.Description}
	}
	if existing.Priority != task.Priority {
		changes["priority"] = map[string]interface{}{"old": string(existing.Priority), "new": string(task.Priority)}
	}
	if existing.AssigneeID != task.AssigneeID {
		changes["assignee_id"] = map[string]interface{}{"old": existing.AssigneeID, "new": task.AssigneeID}
	}
	if existing.AssigneeType != task.AssigneeType {
		changes["assignee_type"] = map[string]interface{}{"old": string(existing.AssigneeType), "new": string(task.AssigneeType)}
	}
	if existing.DueDate != task.DueDate {
		changes["due_date"] = map[string]interface{}{"old": existing.DueDate, "new": task.DueDate}
	}
	if existing.EstimatedHours != task.EstimatedHours {
		changes["estimated_hours"] = map[string]interface{}{"old": existing.EstimatedHours, "new": task.EstimatedHours}
	}
	s.logActivity(ctx, task.ProjectID, task.ID, "task.updated", changes)
	return nil
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

	parentID := existing.ParentTaskID

	if err := s.taskRepo.Delete(ctx, id); err != nil {
		return err
	}
	s.logActivity(ctx, existing.ProjectID, id, "task.deleted", nil)

	// After deleting a subtask, re-check whether the parent's remaining
	// subtasks are all complete and an auto-transition should fire.
	if parentID != nil && s.autoTransSvc != nil {
		if atErr := s.autoTransSvc.CheckSubtaskCompletion(ctx, *parentID); atErr != nil {
			log.Printf("[auto-transition] WARNING: CheckSubtaskCompletion after delete for parent %s failed: %v", *parentID, atErr)
		}
	}

	return nil
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

	oldStatusID := task.StatusID
	oldPosition := task.Position

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

		// Evaluate governance rules before applying the move.
		if s.ruleSvc != nil {
			if violations, evalErr := s.evaluateRulesForMove(ctx, task, status, input); evalErr != nil {
				log.Printf("[rules] WARNING: rule evaluation failed for task %s: %v", taskID, evalErr)
			} else if len(violations) > 0 {
				// Find blocking violations.
				var blockingViolations []domain.RuleViolation
				for _, v := range violations {
					if v.Enforcement == domain.RuleEnforcementBlock {
						blockingViolations = append(blockingViolations, v)
					}
				}
				if len(blockingViolations) > 0 {
					return &RuleViolationError{Violations: blockingViolations}
				}
			}
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
	if err := s.taskRepo.Update(ctx, task); err != nil {
		return err
	}
	moveChanges := map[string]interface{}{}
	if input.StatusID != nil {
		moveChanges["status_id"] = map[string]interface{}{"old": oldStatusID.String(), "new": input.StatusID.String()}
	}
	if input.Position != nil {
		moveChanges["position"] = map[string]interface{}{"old": oldPosition, "new": *input.Position}
	}
	s.logActivity(ctx, task.ProjectID, taskID, "task.moved", moveChanges)

	// Fire auto-transition checks when the status changed.
	if input.StatusID != nil && s.autoTransSvc != nil {
		// Look up the new status category so EvaluateOnTaskMove can decide what to do.
		if newStatus, err := s.statusRepo.GetByID(ctx, *input.StatusID); err == nil && newStatus != nil {
			if atErr := s.autoTransSvc.EvaluateOnTaskMove(ctx, taskID, newStatus.Category); atErr != nil {
				log.Printf("[auto-transition] WARNING: EvaluateOnTaskMove for task %s failed: %v", taskID, atErr)
			}
		}
	}

	return nil
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

	oldAssigneeID := task.AssigneeID
	oldAssigneeType := task.AssigneeType

	task.AssigneeID = input.AssigneeID
	task.AssigneeType = input.AssigneeType
	task.UpdatedAt = timeNow()

	if err := s.taskRepo.Update(ctx, task); err != nil {
		return err
	}
	s.logActivity(ctx, task.ProjectID, taskID, "task.assigned", map[string]interface{}{
		"assignee_id":   map[string]interface{}{"old": oldAssigneeID, "new": input.AssigneeID},
		"assignee_type": map[string]interface{}{"old": string(oldAssigneeType), "new": string(input.AssigneeType)},
	})
	return nil
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
	s.logActivity(ctx, child.ProjectID, child.ID, "task.created", map[string]interface{}{
		"title":          map[string]interface{}{"old": nil, "new": child.Title},
		"parent_task_id": map[string]interface{}{"old": nil, "new": parentTaskID.String()},
	})
	return child, nil
}

// ListSubtasks returns all direct child tasks of the given parent.
func (s *taskService) ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	return s.taskRepo.ListSubtasks(ctx, parentTaskID)
}

// BulkUpdate applies a set of field changes to multiple tasks within a project.
// Each task is processed independently: failures are collected and returned without
// aborting the batch. Only tasks that belong to projectID are modified.
func (s *taskService) BulkUpdate(ctx context.Context, projectID uuid.UUID, input BulkUpdateTasksInput) BulkUpdateTasksResult {
	result := BulkUpdateTasksResult{}

	for _, taskID := range input.TaskIDs {
		if err := s.bulkUpdateOne(ctx, projectID, taskID, input); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("task %s: %v", taskID, err))
		} else {
			result.Updated++
		}
	}

	return result
}

// bulkUpdateOne applies updates to a single task, verifying project membership.
func (s *taskService) bulkUpdateOne(ctx context.Context, projectID, taskID uuid.UUID, input BulkUpdateTasksInput) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}
	if task.ProjectID != projectID {
		return apierror.BadRequest("task does not belong to the project")
	}

	// If status_id is provided, delegate to MoveTask which handles CompletedAt,
	// activity logging, and auto-transitions.
	if input.StatusID != nil {
		if err := s.MoveTask(ctx, taskID, MoveTaskInput{StatusID: input.StatusID}); err != nil {
			return err
		}
		// Re-fetch so the subsequent Update call works on the latest state.
		task, err = s.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return err
		}
		if task == nil {
			return apierror.NotFound("Task")
		}
	}

	// Apply remaining scalar fields (priority, assignee, labels).
	changed := false
	if input.Priority != nil {
		task.Priority = *input.Priority
		changed = true
	}
	if input.AssigneeID != nil {
		task.AssigneeID = input.AssigneeID
		changed = true
	}
	if input.AssigneeType != nil {
		task.AssigneeType = *input.AssigneeType
		changed = true
	}
	if input.Labels != nil {
		task.Labels = *input.Labels
		changed = true
	}

	if changed {
		if err := s.Update(ctx, task); err != nil {
			return err
		}
	}

	return nil
}

// GetMyTasks returns all tasks assigned to the given actor.
func (s *taskService) GetMyTasks(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error) {
	return s.taskRepo.ListByAssignee(ctx, assigneeID, assigneeType)
}

// logActivity writes an activity log entry. Failures are logged but not propagated.
func (s *taskService) logActivity(ctx context.Context, projectID, entityID uuid.UUID, action string, changes map[string]interface{}) {
	if s.activityRepo == nil {
		return
	}

	// Resolve workspace_id from project.
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, projectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}
	if wsID == uuid.Nil {
		log.Printf("[activity] WARNING: could not resolve workspace_id for project %s, skipping", projectID)
		return
	}

	// Extract actor from Go context (set by auth middleware).
	actorID, actorType := actorctx.FromContext(ctx)

	changesJSON, _ := json.Marshal(changes)
	entry := &domain.ActivityLog{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		EntityType:  "task",
		EntityID:    entityID,
		Action:      action,
		ActorID:     actorID,
		ActorType:   actorType,
		Changes:     changesJSON,
		CreatedAt:   timeNow(),
	}
	if err := s.activityRepo.Create(ctx, entry); err != nil {
		log.Printf("[activity] WARNING: failed to log %s for task %s: %v", action, entityID, err)
	}
}

// RuleViolationError is returned when a governance rule blocks an action.
type RuleViolationError struct {
	Violations []domain.RuleViolation
}

func (e *RuleViolationError) Error() string {
	return fmt.Sprintf("action blocked by %d governance rule(s)", len(e.Violations))
}

// evaluateRulesForMove evaluates governance rules before a MoveTask operation.
// Returns violations; the caller decides whether to block.
func (s *taskService) evaluateRulesForMove(ctx context.Context, task *domain.Task, targetStatus *domain.TaskStatus, _ MoveTaskInput) ([]domain.RuleViolation, error) {
	if s.ruleSvc == nil {
		return nil, nil
	}

	actorID, actorType := actorctx.FromContext(ctx)

	// Resolve workspace_id.
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}
	if wsID == uuid.Nil {
		return nil, nil
	}

	taskID := task.ID
	projID := task.ProjectID
	statusID := targetStatus.ID

	input := EvaluateInput{
		Action:         "move_task",
		TaskID:         &taskID,
		Task:           task,
		TargetStatusID: &statusID,
		TargetStatus:   targetStatus,
		ActorID:        actorID,
		ActorType:      actorType,
		WorkspaceID:    wsID,
		ProjectID:      &projID,
	}

	return s.ruleSvc.Evaluate(ctx, input)
}
