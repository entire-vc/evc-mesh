package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// taskTemplateService implements TaskTemplateService.
type taskTemplateService struct {
	repo    repository.TaskTemplateRepository
	taskSvc TaskService
}

// NewTaskTemplateService creates a new TaskTemplateService.
func NewTaskTemplateService(repo repository.TaskTemplateRepository, taskSvc TaskService) TaskTemplateService {
	return &taskTemplateService{repo: repo, taskSvc: taskSvc}
}

// Create inserts a new task template for the given project.
func (s *taskTemplateService) Create(ctx context.Context, input domain.CreateTemplateInput) (*domain.TaskTemplate, error) {
	priority := input.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}

	cf := input.CustomFields
	if cf == nil {
		cf = json.RawMessage("{}")
	}

	tmpl := &domain.TaskTemplate{
		ID:                  uuid.New(),
		ProjectID:           input.ProjectID,
		Name:                input.Name,
		Description:         input.Description,
		TitleTemplate:       input.TitleTemplate,
		DescriptionTemplate: input.DescriptionTemplate,
		Priority:            priority,
		Labels:              pq.StringArray(input.Labels),
		EstimatedHours:      input.EstimatedHours,
		CustomFields:        cf,
		AssigneeID:          input.AssigneeID,
		AssigneeType:        input.AssigneeType,
		StatusID:            input.StatusID,
		CreatedBy:           input.CreatedBy,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	if err := s.repo.Create(ctx, tmpl); err != nil {
		return nil, fmt.Errorf("taskTemplateService.Create: %w", err)
	}
	return tmpl, nil
}

// GetByID fetches a task template by its ID.
func (s *taskTemplateService) GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskTemplate, error) {
	tmpl, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("taskTemplateService.GetByID: %w", err)
	}
	return tmpl, nil
}

// List returns all templates for a project.
func (s *taskTemplateService) List(ctx context.Context, projectID uuid.UUID) ([]domain.TaskTemplate, error) {
	tmpls, err := s.repo.List(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("taskTemplateService.List: %w", err)
	}
	return tmpls, nil
}

// Update partially updates an existing task template.
func (s *taskTemplateService) Update(ctx context.Context, id uuid.UUID, input domain.UpdateTemplateInput) (*domain.TaskTemplate, error) {
	tmpl, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return nil, fmt.Errorf("taskTemplateService.Update: %w", err)
	}
	return tmpl, nil
}

// Delete removes a task template by ID.
func (s *taskTemplateService) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("taskTemplateService.Delete: %w", err)
	}
	return nil
}

// CreateTaskFromTemplate creates a new task based on the given template, applying any
// overrides supplied by the caller.  Overrides are a map[string]any where keys
// correspond to task fields: title, description, priority, labels, assignee_id,
// assignee_type, status_id, estimated_hours.
func (s *taskTemplateService) CreateTaskFromTemplate(ctx context.Context, templateID uuid.UUID, createdBy uuid.UUID, createdByType domain.ActorType, overrides map[string]any) (*domain.Task, error) {
	tmpl, err := s.repo.GetByID(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("taskTemplateService.CreateTaskFromTemplate: fetch template: %w", err)
	}

	// Start with template defaults.
	title := tmpl.TitleTemplate
	description := tmpl.DescriptionTemplate
	priority := tmpl.Priority
	labels := []string(tmpl.Labels)
	assigneeID := tmpl.AssigneeID
	var assigneeType domain.AssigneeType
	if tmpl.AssigneeType != nil {
		assigneeType = *tmpl.AssigneeType
	} else {
		assigneeType = domain.AssigneeTypeUnassigned
	}
	var statusID *uuid.UUID
	if tmpl.StatusID != nil {
		id := *tmpl.StatusID
		statusID = &id
	}
	estimatedHours := tmpl.EstimatedHours
	customFields := tmpl.CustomFields

	// Apply overrides.
	if v, ok := overrides["title"].(string); ok && v != "" {
		title = v
	}
	if v, ok := overrides["description"].(string); ok {
		description = v
	}
	if v, ok := overrides["priority"].(string); ok && v != "" {
		priority = domain.Priority(v)
	}
	if v, ok := overrides["labels"].([]string); ok {
		labels = v
	}
	if v, ok := overrides["assignee_id"].(string); ok && v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			assigneeID = &id
		}
	}
	if v, ok := overrides["assignee_type"].(string); ok && v != "" {
		assigneeType = domain.AssigneeType(v)
	}
	if v, ok := overrides["status_id"].(string); ok && v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			statusID = &id
		}
	}
	if v, ok := overrides["estimated_hours"].(float64); ok {
		estimatedHours = &v
	}

	// Resolve status: use override/template value or fall back to project default.
	var finalStatusID uuid.UUID
	if statusID != nil {
		finalStatusID = *statusID
	} else {
		defaultStatus, err := s.taskSvc.GetDefaultStatus(ctx, tmpl.ProjectID)
		if err != nil || defaultStatus == nil {
			return nil, fmt.Errorf("taskTemplateService.CreateTaskFromTemplate: no status_id provided and project has no default status")
		}
		finalStatusID = defaultStatus.ID
	}

	task := &domain.Task{
		ID:             uuid.New(),
		ProjectID:      tmpl.ProjectID,
		StatusID:       finalStatusID,
		Title:          title,
		Description:    description,
		Priority:       priority,
		Labels:         pq.StringArray(labels),
		AssigneeID:     assigneeID,
		AssigneeType:   assigneeType,
		EstimatedHours: estimatedHours,
		CustomFields:   customFields,
		CreatedBy:      createdBy,
		CreatedByType:  createdByType,
	}

	if err := s.taskSvc.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("taskTemplateService.CreateTaskFromTemplate: create task: %w", err)
	}

	// Re-fetch to populate computed fields (assignee_name, etc.) after auto-assign.
	enriched, err := s.taskSvc.GetByID(ctx, task.ID)
	if err == nil && enriched != nil {
		return enriched, nil
	}
	return task, nil
}
