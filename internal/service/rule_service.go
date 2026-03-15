package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ruleTypePattern validates rule_type format: two dot-separated lowercase snake_case segments.
var ruleTypePattern = regexp.MustCompile(`^[a-z_]+\.[a-z_]+$`)

// CreateRuleInput holds fields for creating a new rule.
type CreateRuleInput struct {
	WorkspaceID         uuid.UUID
	ProjectID           *uuid.UUID
	AgentID             *uuid.UUID
	Scope               domain.RuleScope
	RuleType            string
	Name                string
	Description         string
	Config              json.RawMessage
	AppliesToActorTypes []string
	AppliesToRoles      []string
	Enforcement         domain.RuleEnforcement
	Priority            int
}

// UpdateRuleInput holds fields for partially updating a rule.
type UpdateRuleInput struct {
	Name                *string
	Description         *string
	Config              json.RawMessage
	AppliesToActorTypes []string
	AppliesToRoles      []string
	Enforcement         *domain.RuleEnforcement
	Priority            *int
	IsEnabled           *bool
}

// RuleContext describes the actor context for effective rule resolution.
type RuleContext struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	AgentID     *uuid.UUID
	ActorID     uuid.UUID
	ActorType   domain.ActorType
	ActorRole   string
}

// ruleService implements RuleService.
type ruleService struct {
	ruleRepo       repository.RuleRepository
	activityRepo   repository.ActivityLogRepository
	commentRepo    repository.CommentRepository
	taskRepo       repository.TaskRepository
	taskStatusRepo repository.TaskStatusRepository
}

// NewRuleService returns a new RuleService.
func NewRuleService(
	ruleRepo repository.RuleRepository,
	activityRepo repository.ActivityLogRepository,
	opts ...RuleServiceOption,
) RuleService {
	s := &ruleService{
		ruleRepo:     ruleRepo,
		activityRepo: activityRepo,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// RuleServiceOption configures optional dependencies for RuleService.
type RuleServiceOption func(*ruleService)

// WithRuleCommentRepo sets the comment repository for evaluators.
func WithRuleCommentRepo(cr repository.CommentRepository) RuleServiceOption {
	return func(s *ruleService) {
		s.commentRepo = cr
	}
}

// WithRuleTaskRepo sets the task repository for evaluators.
func WithRuleTaskRepo(tr repository.TaskRepository) RuleServiceOption {
	return func(s *ruleService) {
		s.taskRepo = tr
	}
}

// WithRuleTaskStatusRepo sets the task status repository for evaluators.
func WithRuleTaskStatusRepo(tsr repository.TaskStatusRepository) RuleServiceOption {
	return func(s *ruleService) {
		s.taskStatusRepo = tsr
	}
}

// Create validates and persists a new rule.
func (s *ruleService) Create(ctx context.Context, input CreateRuleInput) (*domain.Rule, error) {
	if err := validateRuleInput(input); err != nil {
		return nil, err
	}

	now := time.Now()
	actorID, actorType := actorctx.FromContext(ctx)

	config := input.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}

	actorTypes := input.AppliesToActorTypes
	if actorTypes == nil {
		actorTypes = []string{}
	}
	roles := input.AppliesToRoles
	if roles == nil {
		roles = []string{}
	}

	enforcement := input.Enforcement
	if enforcement == "" {
		enforcement = domain.RuleEnforcementBlock
	}

	priority := input.Priority
	if priority == 0 {
		priority = 100
	}

	rule := &domain.Rule{
		ID:                  uuid.New(),
		WorkspaceID:         input.WorkspaceID,
		ProjectID:           input.ProjectID,
		AgentID:             input.AgentID,
		Scope:               input.Scope,
		RuleType:            input.RuleType,
		Name:                input.Name,
		Description:         input.Description,
		Config:              config,
		AppliesToActorTypes: actorTypes,
		AppliesToRoles:      roles,
		Enforcement:         enforcement,
		Priority:            priority,
		IsEnabled:           true,
		CreatedBy:           actorID,
		CreatedByType:       actorType,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	if err := s.ruleRepo.Create(ctx, rule); err != nil {
		return nil, fmt.Errorf("create rule: %w", err)
	}

	s.logActivity(ctx, input.WorkspaceID, rule.ID, "rule.created", nil)
	return rule, nil
}

// GetByID retrieves a rule by ID.
func (s *ruleService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Rule, error) {
	rule, err := s.ruleRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return nil, apierror.NotFound("Rule")
	}
	return rule, nil
}

// Update partially updates a rule.
func (s *ruleService) Update(ctx context.Context, id uuid.UUID, input UpdateRuleInput) (*domain.Rule, error) {
	rule, err := s.ruleRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return nil, apierror.NotFound("Rule")
	}

	if input.Name != nil {
		rule.Name = *input.Name
	}
	if input.Description != nil {
		rule.Description = *input.Description
	}
	if len(input.Config) > 0 {
		rule.Config = input.Config
	}
	if input.AppliesToActorTypes != nil {
		rule.AppliesToActorTypes = input.AppliesToActorTypes
	}
	if input.AppliesToRoles != nil {
		rule.AppliesToRoles = input.AppliesToRoles
	}
	if input.Enforcement != nil {
		rule.Enforcement = *input.Enforcement
	}
	if input.Priority != nil {
		rule.Priority = *input.Priority
	}
	if input.IsEnabled != nil {
		rule.IsEnabled = *input.IsEnabled
	}
	rule.UpdatedAt = time.Now()

	if err := s.ruleRepo.Update(ctx, rule); err != nil {
		return nil, fmt.Errorf("update rule: %w", err)
	}

	s.logActivity(ctx, rule.WorkspaceID, rule.ID, "rule.updated", nil)
	return rule, nil
}

// Delete removes a rule.
func (s *ruleService) Delete(ctx context.Context, id uuid.UUID) error {
	rule, err := s.ruleRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if rule == nil {
		return apierror.NotFound("Rule")
	}

	if err := s.ruleRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}

	s.logActivity(ctx, rule.WorkspaceID, id, "rule.deleted", nil)
	return nil
}

// ListByWorkspace returns rules scoped to a workspace.
func (s *ruleService) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, includeDisabled bool) ([]domain.Rule, error) {
	return s.ruleRepo.ListByWorkspace(ctx, workspaceID, includeDisabled)
}

// ListByProject returns rules scoped to a project.
func (s *ruleService) ListByProject(ctx context.Context, projectID uuid.UUID, includeDisabled bool) ([]domain.Rule, error) {
	return s.ruleRepo.ListByProject(ctx, projectID, includeDisabled)
}

// ListByAgent returns rules scoped to an agent.
func (s *ruleService) ListByAgent(ctx context.Context, agentID uuid.UUID, includeDisabled bool) ([]domain.Rule, error) {
	return s.ruleRepo.ListByAgent(ctx, agentID, includeDisabled)
}

// GetEffective fetches all candidate rules and resolves inheritance.
// Most specific scope wins per rule_type; rules are additive across types.
func (s *ruleService) GetEffective(ctx context.Context, ruleCtx RuleContext) ([]domain.Rule, error) {
	candidates, err := s.ruleRepo.GetEffective(ctx, ruleCtx.WorkspaceID, ruleCtx.ProjectID, ruleCtx.AgentID)
	if err != nil {
		return nil, fmt.Errorf("get effective rules candidates: %w", err)
	}

	return resolveInheritance(candidates), nil
}

// Evaluate runs effective rules through evaluators and returns violations.
func (s *ruleService) Evaluate(ctx context.Context, input EvaluateInput) ([]domain.RuleViolation, error) {
	ruleCtx := RuleContext{
		WorkspaceID: input.WorkspaceID,
		ActorID:     input.ActorID,
		ActorType:   input.ActorType,
		ActorRole:   input.ActorRole,
	}
	if input.ProjectID != nil {
		ruleCtx.ProjectID = input.ProjectID
	}
	if input.ActorType == domain.ActorTypeAgent {
		ruleCtx.AgentID = &input.ActorID
	}

	effectiveRules, err := s.GetEffective(ctx, ruleCtx)
	if err != nil {
		return nil, err
	}

	deps := evaluatorDeps{
		commentRepo:    s.commentRepo,
		taskRepo:       s.taskRepo,
		taskStatusRepo: s.taskStatusRepo,
		ruleRepo:       s.ruleRepo,
	}

	var violations []domain.RuleViolation
	for _, rule := range effectiveRules {
		if !ruleAppliesTo(rule, input) {
			continue
		}

		evaluator, ok := evaluatorRegistry[rule.RuleType]
		if !ok {
			// Unknown rule type — skip silently (forward compat).
			continue
		}

		violation, err := evaluator(ctx, rule, input, deps)
		if err != nil {
			log.Printf("[rules] WARNING: evaluator %s failed for rule %s: %v", rule.RuleType, rule.ID, err)
			continue
		}
		if violation != nil {
			violations = append(violations, *violation)
		}
	}

	return violations, nil
}

// ============================================================================
// Helpers
// ============================================================================

// resolveInheritance implements "most specific wins per rule_type, additive across types."
func resolveInheritance(candidates []domain.Rule) []domain.Rule {
	// scopeRank: lower = more specific. Agent > Project > Workspace.
	scopeRank := map[domain.RuleScope]int{
		domain.RuleScopeWorkspace: 0,
		domain.RuleScopeProject:   1,
		domain.RuleScopeAgent:     2,
	}

	// For each rule_type, keep only the most specific enabled rule.
	// disabled project rule with same type disables workspace rule for that type.
	type best struct {
		rule domain.Rule
		rank int
	}
	byType := map[string]best{}

	for _, r := range candidates {
		rank := scopeRank[r.Scope]
		existing, found := byType[r.RuleType]
		if !found || rank > existing.rank {
			byType[r.RuleType] = best{rule: r, rank: rank}
		} else if rank == existing.rank && r.Priority < existing.rule.Priority {
			// Same scope level, lower priority number wins (higher priority).
			byType[r.RuleType] = best{rule: r, rank: rank}
		}
	}

	var result []domain.Rule
	for _, b := range byType {
		if b.rule.IsEnabled {
			result = append(result, b.rule)
		}
	}
	return result
}

// ruleAppliesTo returns true if the rule applies to the actor in the given input.
func ruleAppliesTo(rule domain.Rule, input EvaluateInput) bool {
	// Filter by actor type if specified.
	if len(rule.AppliesToActorTypes) > 0 {
		if !containsString(rule.AppliesToActorTypes, string(input.ActorType)) {
			return false
		}
	}
	// Filter by role if specified.
	if len(rule.AppliesToRoles) > 0 && input.ActorRole != "" {
		if !containsString(rule.AppliesToRoles, input.ActorRole) {
			return false
		}
	}
	return true
}

// validateRuleInput validates the fields for rule creation.
func validateRuleInput(input CreateRuleInput) error {
	errs := map[string]string{}

	if input.Name == "" {
		errs["name"] = "name is required"
	}
	if input.RuleType == "" {
		errs["rule_type"] = "rule_type is required"
	} else if !ruleTypePattern.MatchString(input.RuleType) {
		errs["rule_type"] = "rule_type must match pattern 'category.action' (e.g. transition_gate.require_comment)"
	}
	if input.Scope == "" {
		errs["scope"] = "scope is required"
	}

	// Scope constraints.
	switch input.Scope {
	case domain.RuleScopeWorkspace:
		if input.ProjectID != nil || input.AgentID != nil {
			errs["scope"] = "workspace-scoped rules must not have project_id or agent_id"
		}
	case domain.RuleScopeProject:
		if input.ProjectID == nil {
			errs["project_id"] = "project_id is required for project-scoped rules"
		}
		if input.AgentID != nil {
			errs["agent_id"] = "project-scoped rules must not have agent_id"
		}
	case domain.RuleScopeAgent:
		if input.AgentID == nil {
			errs["agent_id"] = "agent_id is required for agent-scoped rules"
		}
	}

	if len(errs) > 0 {
		return apierror.ValidationError(errs)
	}
	return nil
}

// logActivity writes a rule activity log entry. Failures are logged and not propagated.
func (s *ruleService) logActivity(ctx context.Context, workspaceID, entityID uuid.UUID, action string, changes map[string]interface{}) {
	if s.activityRepo == nil {
		return
	}

	actorID, actorType := actorctx.FromContext(ctx)
	changesJSON, _ := json.Marshal(changes)

	entry := &domain.ActivityLog{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		EntityType:  "rule",
		EntityID:    entityID,
		Action:      action,
		ActorID:     actorID,
		ActorType:   actorType,
		Changes:     changesJSON,
		CreatedAt:   time.Now(),
	}
	if err := s.activityRepo.Create(ctx, entry); err != nil {
		log.Printf("[activity] WARNING: failed to log %s for rule %s: %v", action, entityID, err)
	}
}
