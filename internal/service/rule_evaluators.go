package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// RuleEvaluator is a function that evaluates a single rule against an action.
// Returns a violation if the rule is violated, nil otherwise.
type RuleEvaluator func(ctx context.Context, rule domain.Rule, input EvaluateInput, deps evaluatorDeps) (*domain.RuleViolation, error)

// evaluatorDeps groups read-only repositories used by evaluators.
// All fields are optional; evaluators should degrade gracefully if nil.
type evaluatorDeps struct {
	commentRepo    repository.CommentRepository
	taskRepo       repository.TaskRepository
	taskStatusRepo repository.TaskStatusRepository
	ruleRepo       repository.RuleRepository
}

// EvaluateInput holds everything an evaluator needs to know about the action being evaluated.
type EvaluateInput struct {
	// Action being performed: "move_task", "assign_task", "create_task"
	Action string

	// TaskID of the task being acted upon (may be nil for pre-creation).
	TaskID *uuid.UUID

	// Task is the current task state (fetched before evaluation).
	Task *domain.Task

	// TargetStatusID is the status the task is being moved to (for move_task).
	TargetStatusID *uuid.UUID

	// TargetStatus is the resolved status (populated if TargetStatusID is set).
	TargetStatus *domain.TaskStatus

	// ActorID is the ID of the actor performing the action.
	ActorID uuid.UUID

	// ActorType is the type of the actor (user or agent).
	ActorType domain.ActorType

	// ActorRole is the workspace role of the actor (e.g. "member", "admin").
	// Empty for agents.
	ActorRole string

	// WorkspaceID is always set.
	WorkspaceID uuid.UUID

	// ProjectID may be nil for workspace-level actions.
	ProjectID *uuid.UUID
}

// evaluatorRegistry maps rule_type to its evaluator function.
var evaluatorRegistry = map[string]RuleEvaluator{
	"transition_gate.require_comment":       evalRequireComment,
	"transition_gate.require_subtasks_done": evalRequireSubtasksDone,
	"capacity_limit.max_in_progress":        evalMaxInProgress,
	"capacity_limit.max_assigned":           evalMaxAssigned,
}

// ============================================================================
// transition_gate.require_comment
// ============================================================================

type requireCommentConfig struct {
	TargetStatusCategories []string `json:"target_status_categories"`
	MinCommentLength       int      `json:"min_comment_length"`
}

func evalRequireComment(ctx context.Context, rule domain.Rule, input EvaluateInput, deps evaluatorDeps) (*domain.RuleViolation, error) {
	if input.Action != "move_task" || input.TargetStatus == nil {
		return nil, nil
	}

	var cfg requireCommentConfig
	if err := json.Unmarshal(rule.Config, &cfg); err != nil {
		return nil, fmt.Errorf("parse require_comment config: %w", err)
	}

	if !containsString(cfg.TargetStatusCategories, string(input.TargetStatus.Category)) {
		return nil, nil
	}

	if deps.commentRepo == nil || input.TaskID == nil {
		// Cannot check without comment repo — skip enforcement.
		return nil, nil
	}

	// Look for a recent comment by this actor on the task (within 24 hours).
	minLen := cfg.MinCommentLength
	if minLen <= 0 {
		minLen = 1
	}

	pg := pagination.Params{Page: 1, PageSize: 10}
	comments, err := deps.commentRepo.ListByTask(ctx, *input.TaskID, repository.CommentFilter{}, pg)
	if err != nil {
		return nil, fmt.Errorf("list comments for rule check: %w", err)
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, c := range comments.Items {
		if c.AuthorID != input.ActorID {
			continue
		}
		if c.CreatedAt.Before(cutoff) {
			continue
		}
		if len(c.Body) >= minLen {
			return nil, nil // found a valid comment
		}
	}

	targetCat := string(input.TargetStatus.Category)
	msg := fmt.Sprintf("A comment of at least %d character(s) is required before moving to %s", minLen, targetCat)
	return &domain.RuleViolation{
		RuleID:      rule.ID,
		RuleName:    rule.Name,
		RuleType:    rule.RuleType,
		Enforcement: rule.Enforcement,
		Message:     msg,
	}, nil
}

// ============================================================================
// transition_gate.require_subtasks_done
// ============================================================================

type requireSubtasksDoneConfig struct {
	TargetStatusCategories []string `json:"target_status_categories"`
	AllowCancelled         bool     `json:"allow_cancelled"`
}

func evalRequireSubtasksDone(ctx context.Context, rule domain.Rule, input EvaluateInput, deps evaluatorDeps) (*domain.RuleViolation, error) {
	if input.Action != "move_task" || input.TargetStatus == nil {
		return nil, nil
	}

	var cfg requireSubtasksDoneConfig
	if err := json.Unmarshal(rule.Config, &cfg); err != nil {
		return nil, fmt.Errorf("parse require_subtasks_done config: %w", err)
	}

	if !containsString(cfg.TargetStatusCategories, string(input.TargetStatus.Category)) {
		return nil, nil
	}

	if deps.taskRepo == nil || input.TaskID == nil {
		return nil, nil
	}

	subtasks, err := deps.taskRepo.ListSubtasks(ctx, *input.TaskID)
	if err != nil {
		return nil, fmt.Errorf("list subtasks for rule check: %w", err)
	}

	// No subtasks — rule trivially satisfied.
	if len(subtasks) == 0 {
		return nil, nil
	}

	for _, st := range subtasks {
		if st.CompletedAt != nil {
			continue // done
		}

		// If allow_cancelled is enabled, check whether this subtask's status category
		// is "cancelled" before treating it as a blocker. This requires a status lookup.
		if cfg.AllowCancelled && deps.taskStatusRepo != nil {
			status, err := deps.taskStatusRepo.GetByID(ctx, st.StatusID)
			if err == nil && status != nil && status.Category == domain.StatusCategoryCancelled {
				continue // cancelled subtasks are acceptable when allow_cancelled is set
			}
		}

		return &domain.RuleViolation{
			RuleID:      rule.ID,
			RuleName:    rule.Name,
			RuleType:    rule.RuleType,
			Enforcement: rule.Enforcement,
			Message:     "All subtasks must be completed before moving to " + string(input.TargetStatus.Category),
		}, nil
	}

	return nil, nil
}

// ============================================================================
// capacity_limit.max_in_progress
// ============================================================================

type maxInProgressConfig struct {
	Limit              int      `json:"limit"`
	StatusCategories   []string `json:"status_categories"`
}

func evalMaxInProgress(ctx context.Context, rule domain.Rule, input EvaluateInput, deps evaluatorDeps) (*domain.RuleViolation, error) {
	if input.Action != "move_task" || input.TargetStatus == nil {
		return nil, nil
	}

	var cfg maxInProgressConfig
	if err := json.Unmarshal(rule.Config, &cfg); err != nil {
		return nil, fmt.Errorf("parse max_in_progress config: %w", err)
	}

	categories := cfg.StatusCategories
	if len(categories) == 0 {
		categories = []string{"in_progress"}
	}

	// Only check if the actor is moving into one of the limited categories.
	if !containsString(categories, string(input.TargetStatus.Category)) {
		return nil, nil
	}

	if deps.ruleRepo == nil {
		return nil, nil
	}

	count, err := deps.ruleRepo.CountTasksByAssigneeAndCategory(ctx, input.WorkspaceID, input.ActorID, string(input.ActorType), categories)
	if err != nil {
		return nil, fmt.Errorf("count in-progress tasks: %w", err)
	}

	if count >= cfg.Limit {
		return &domain.RuleViolation{
			RuleID:      rule.ID,
			RuleName:    rule.Name,
			RuleType:    rule.RuleType,
			Enforcement: rule.Enforcement,
			Message:     fmt.Sprintf("You have reached the maximum of %d task(s) in progress", cfg.Limit),
		}, nil
	}

	return nil, nil
}

// ============================================================================
// capacity_limit.max_assigned
// ============================================================================

type maxAssignedConfig struct {
	Limit int `json:"limit"`
}

func evalMaxAssigned(ctx context.Context, rule domain.Rule, input EvaluateInput, deps evaluatorDeps) (*domain.RuleViolation, error) {
	if input.Action != "assign_task" && input.Action != "create_task" {
		return nil, nil
	}

	var cfg maxAssignedConfig
	if err := json.Unmarshal(rule.Config, &cfg); err != nil {
		return nil, fmt.Errorf("parse max_assigned config: %w", err)
	}

	if deps.ruleRepo == nil {
		return nil, nil
	}

	// Count all non-done, non-cancelled tasks.
	openCategories := []string{"backlog", "todo", "in_progress", "review"}
	count, err := deps.ruleRepo.CountTasksByAssigneeAndCategory(ctx, input.WorkspaceID, input.ActorID, string(input.ActorType), openCategories)
	if err != nil {
		return nil, fmt.Errorf("count assigned tasks: %w", err)
	}

	if count >= cfg.Limit {
		return &domain.RuleViolation{
			RuleID:      rule.ID,
			RuleName:    rule.Name,
			RuleType:    rule.RuleType,
			Enforcement: rule.Enforcement,
			Message:     fmt.Sprintf("You have reached the maximum of %d assigned task(s)", cfg.Limit),
		}, nil
	}

	return nil, nil
}

// ============================================================================
// Helpers
// ============================================================================

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
