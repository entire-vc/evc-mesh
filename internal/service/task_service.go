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
	taskRepo        repository.TaskRepository
	statusRepo      repository.TaskStatusRepository
	depRepo         repository.TaskDependencyRepository
	activityRepo    repository.ActivityLogRepository
	customFieldSvc  CustomFieldService
	projectRepo     repository.ProjectRepository
	autoTransSvc    AutoTransitionService
	ruleSvc         RuleService
	rulesConfigSvc  RulesService
	eventBusSvc     EventBusService
	webhookSvc      WebhookService
	agentNotifySvc  AgentNotifyService
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

// WithEventBusService sets the optional event bus service.
// When set, task mutations (create/update/move/delete) automatically publish events.
func WithEventBusService(ebs EventBusService) TaskServiceOption {
	return func(s *taskService) {
		s.eventBusSvc = ebs
	}
}

// WithWebhookService sets the optional webhook service.
// When set, task lifecycle events (created, assigned, status changed) dispatch outbound webhooks.
func WithWebhookService(ws WebhookService) TaskServiceOption {
	return func(s *taskService) {
		s.webhookSvc = ws
	}
}

// WithAgentNotifyService sets the optional agent notification service.
// When set, task assignments fire push notifications to the assigned agent via
// callback URL and Redis pub/sub (for SSE and long-poll consumers).
func WithAgentNotifyService(ans AgentNotifyService) TaskServiceOption {
	return func(s *taskService) {
		s.agentNotifySvc = ans
	}
}

// WithRulesConfigService sets the optional rules configuration service.
// When set, task creation will apply auto-assign rules from project/workspace config.
func WithRulesConfigService(rcs RulesService) TaskServiceOption {
	return func(s *taskService) {
		s.rulesConfigSvc = rcs
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

	// Apply auto-assign rules if the task has no assignee.
	if task.AssigneeType == domain.AssigneeTypeUnassigned || task.AssigneeType == "" {
		s.applyAutoAssign(ctx, task)
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

	// Dispatch webhook for task.created (agent wakeup pipeline).
	if s.webhookSvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			go s.webhookSvc.Dispatch(ctx, proj.WorkspaceID, "task.created", map[string]interface{}{
				"task_id":     task.ID,
				"project_id":  task.ProjectID,
				"title":       task.Title,
				"priority":    string(task.Priority),
				"assignee_id": task.AssigneeID,
				"status_id":   task.StatusID,
			})
		}
	}

	// Notify assigned agent via push mechanisms (callback_url, SSE, long-poll).
	s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
		"assignee_id": map[string]any{"old": nil, "new": task.AssigneeID},
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
	assigneeChanged := existing.AssigneeID != task.AssigneeID
	if assigneeChanged {
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

	// Dispatch webhook for task.assigned when the assignee changes (agent wakeup pipeline).
	if assigneeChanged && s.webhookSvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			go s.webhookSvc.Dispatch(ctx, proj.WorkspaceID, "task.assigned", map[string]interface{}{
				"task_id":      task.ID,
				"project_id":   task.ProjectID,
				"assignee_id":  task.AssigneeID,
				"assignee_type": string(task.AssigneeType),
			})
		}
	}

	// Notify newly assigned agent via push mechanisms (callback_url, SSE, long-poll).
	if assigneeChanged {
		s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
			"assignee_id": map[string]any{"old": existing.AssigneeID, "new": task.AssigneeID},
		})
	}

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

	// Dispatch webhook for task.status_changed (agent wakeup pipeline).
	if input.StatusID != nil && s.webhookSvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			go s.webhookSvc.Dispatch(ctx, proj.WorkspaceID, "task.status_changed", map[string]interface{}{
				"task_id":       task.ID,
				"project_id":    task.ProjectID,
				"old_status_id": oldStatusID,
				"new_status_id": *input.StatusID,
			})
		}
	}

	// Notify assigned agent about status change via push mechanisms (SSE, long-poll, callback).
	if input.StatusID != nil {
		s.notifyAssignedAgent(ctx, task, "task.status_changed", map[string]any{
			"status_id": map[string]any{"old": oldStatusID.String(), "new": input.StatusID.String()},
		})
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

	// Notify newly assigned agent via push mechanisms (callback_url, SSE, long-poll).
	s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
		"assignee_id":   map[string]any{"old": oldAssigneeID, "new": input.AssigneeID},
		"assignee_type": map[string]any{"old": string(oldAssigneeType), "new": string(input.AssigneeType)},
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

// applyAutoAssign applies assignment rules to a task if no assignee is set.
// It checks by_priority, default_assignee, then fallback_chain in order.
// Failures are logged but never block task creation.
func (s *taskService) applyAutoAssign(ctx context.Context, task *domain.Task) {
	if s.rulesConfigSvc == nil {
		return
	}

	effective, err := s.rulesConfigSvc.GetEffectiveAssignmentRules(ctx, task.ProjectID)
	if err != nil {
		log.Printf("[auto-assign] WARNING: failed to get assignment rules for project %s: %v", task.ProjectID, err)
		return
	}

	var assigneeID string

	// 1. Check by_priority[task.priority]
	if effective.ByPriority != nil {
		if rule, ok := effective.ByPriority[string(task.Priority)]; ok && rule.Value != "" {
			assigneeID = rule.Value
		}
	}

	// 2. Fallback to default_assignee
	if assigneeID == "" && effective.DefaultAssignee != nil && effective.DefaultAssignee.Value != "" {
		assigneeID = effective.DefaultAssignee.Value
	}

	// 3. Fallback to first in fallback_chain
	if assigneeID == "" && len(effective.FallbackChain) > 0 {
		assigneeID = effective.FallbackChain[0]
	}

	if assigneeID == "" {
		return
	}

	parsed, err := uuid.Parse(assigneeID)
	if err != nil {
		log.Printf("[auto-assign] WARNING: invalid assignee UUID %q in rules: %v", assigneeID, err)
		return
	}

	task.AssigneeID = &parsed
	task.AssigneeType = domain.AssigneeTypeAgent
	log.Printf("[auto-assign] assigned task %q to agent %s via rules", task.Title, assigneeID)
}

// buildTaskSnapshot creates a map representation of a task for webhook payloads.
// Description is truncated to 500 characters per spec.
func (s *taskService) buildTaskSnapshot(ctx context.Context, task *domain.Task) map[string]any {
	desc := task.Description
	if len(desc) > 500 {
		desc = desc[:500]
	}

	snap := map[string]any{
		"id":            task.ID,
		"project_id":    task.ProjectID,
		"title":         task.Title,
		"priority":      string(task.Priority),
		"description":   desc,
		"assignee_id":   task.AssigneeID,
		"assignee_type": string(task.AssigneeType),
		"labels":        task.Labels,
	}

	// Resolve status info.
	if status, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && status != nil {
		snap["status"] = map[string]any{
			"id":       status.ID,
			"name":     status.Name,
			"category": string(status.Category),
		}
	}

	// Include assignee_name if available from enriched query.
	if task.AssigneeName != nil {
		snap["assignee_name"] = *task.AssigneeName
	}

	return snap
}

// notifyAssignedAgent sends a push notification to the assigned agent if it's an agent type.
func (s *taskService) notifyAssignedAgent(ctx context.Context, task *domain.Task, eventType string, changes map[string]any) {
	if s.agentNotifySvc == nil || task.AssigneeType != domain.AssigneeTypeAgent || task.AssigneeID == nil {
		return
	}

	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}

	// Extract actor info from request context (set by auth middleware).
	actorID, actorType := actorctx.FromContext(ctx)
	actorName := actorctx.NameFromContext(ctx)

	s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
		EventType:   eventType,
		Timestamp:   timeNow(),
		WorkspaceID: wsID,
		Task:        s.buildTaskSnapshot(ctx, task),
		AgentID:     *task.AssigneeID,
		ActorID:     actorID,
		ActorType:   string(actorType),
		ActorName:   actorName,
		Changes:     changes,
		TaskID:      task.ID,
		ProjectID:   task.ProjectID,
	})
}

// logActivity writes an activity log entry and publishes an event bus message.
// Failures are logged but not propagated.
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

	// Also publish to the event bus so the Events page shows task activity.
	s.publishTaskEvent(ctx, wsID, projectID, entityID, actorID, actorType, action, changes)
}

// publishTaskEvent publishes a task mutation as an event bus message.
// This bridges the gap between activity_log (audit) and event_bus_messages (feed).
func (s *taskService) publishTaskEvent(ctx context.Context, wsID, projectID, taskID, actorID uuid.UUID, actorType domain.ActorType, action string, changes map[string]interface{}) {
	if s.eventBusSvc == nil {
		return
	}

	// Map activity actions to event types.
	eventType := domain.EventTypeCustom
	switch action {
	case "task.created", "task.deleted":
		eventType = domain.EventTypeStatusChange
	case "task.moved", "task.assigned":
		eventType = domain.EventTypeStatusChange
	case "task.updated":
		eventType = domain.EventTypeContextUpdate
	}

	payload := map[string]any{
		"task_id":    taskID.String(),
		"action":     action,
		"actor_id":   actorID,
		"actor_type": actorType,
	}
	// Merge changes into payload.
	for k, v := range changes {
		payload[k] = v
	}

	taskIDPtr := &taskID
	input := PublishEventInput{
		WorkspaceID: wsID,
		ProjectID:   projectID,
		TaskID:      taskIDPtr,
		EventType:   eventType,
		Subject:     action,
		Payload:     payload,
		Tags:        []string{"auto", "task"},
		TTLSeconds:  86400, // 24h
	}

	if _, err := s.eventBusSvc.Publish(ctx, input); err != nil {
		log.Printf("[event_bus] WARNING: failed to publish %s event for task %s: %v", action, taskID, err)
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
