package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// rulesService implements RulesService.
type rulesService struct {
	wsRuleRepo    repository.WorkspaceRuleConfigRepository
	projRuleRepo  repository.ProjectRuleConfigRepository
	violationRepo repository.RuleViolationLogRepository
	agentRepo     repository.AgentRepository
	memberRepo    repository.WorkspaceMemberRepository
	workspaceRepo repository.WorkspaceRepository
	projectRepo   repository.ProjectRepository
}

// NewRulesService creates a new rulesService.
func NewRulesService(
	wsRuleRepo repository.WorkspaceRuleConfigRepository,
	projRuleRepo repository.ProjectRuleConfigRepository,
	violationRepo repository.RuleViolationLogRepository,
	agentRepo repository.AgentRepository,
	memberRepo repository.WorkspaceMemberRepository,
	workspaceRepo repository.WorkspaceRepository,
	projectRepo repository.ProjectRepository,
) RulesService {
	return &rulesService{
		wsRuleRepo:    wsRuleRepo,
		projRuleRepo:  projRuleRepo,
		violationRepo: violationRepo,
		agentRepo:     agentRepo,
		memberRepo:    memberRepo,
		workspaceRepo: workspaceRepo,
		projectRepo:   projectRepo,
	}
}

// --------------------------------------------------------------------------
// Team Directory
// --------------------------------------------------------------------------

// GetTeamDirectory returns the team directory for a workspace, listing agents and human members.
func (s *rulesService) GetTeamDirectory(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectory, error) {
	ws, err := s.workspaceRepo.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	if ws == nil {
		return nil, fmt.Errorf("workspace not found")
	}

	// Get agents with current task counts (use max page size to get all agents).
	agentFilter := repository.AgentFilter{}
	agentPg := pagination.Params{Page: 1, PageSize: pagination.MaxPageSize}
	agentPage, err := s.agentRepo.List(ctx, workspaceID, agentFilter, agentPg)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}

	agents := make([]domain.TeamDirectoryAgent, 0, len(agentPage.Items))
	for _, a := range agentPage.Items {
		agents = append(agents, domain.TeamDirectoryAgent{
			ID:                 a.ID,
			Name:               a.Name,
			Slug:               a.Slug,
			Status:             a.Status,
			Role:               a.Role,
			Capabilities:       a.Capabilities,
			ResponsibilityZone: a.ResponsibilityZone,
			EscalationTo:       derefRawMessage(a.EscalationTo),
			AcceptsFrom:        a.AcceptsFrom,
			MaxConcurrentTasks: a.MaxConcurrentTasks,
			WorkingHours:       a.WorkingHours,
			ProfileDescription: a.ProfileDescription,
			CurrentTasks:       0, // TODO: enrich with task count if needed
		})
	}

	// Get human members with their profile data.
	members, err := s.memberRepo.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	humans := make([]domain.TeamDirectoryHuman, 0, len(members))
	for _, m := range members {
		humans = append(humans, domain.TeamDirectoryHuman{
			ID:                 m.User.ID,
			Name:               m.User.Name,
			Email:              m.User.Email,
			AvatarURL:          m.User.AvatarURL,
			Role:               m.Role,
			Capabilities:       json.RawMessage(`[]`),
			ResponsibilityZone: "",
			Availability:       "business-hours",
		})
	}

	return &domain.TeamDirectory{
		Workspace: ws.Name,
		Agents:    agents,
		Humans:    humans,
	}, nil
}

// UpdateAgentProfile updates the profile fields of an agent.
func (s *rulesService) UpdateAgentProfile(ctx context.Context, agentID uuid.UUID, profile domain.AgentProfileUpdate) error {
	agent, err := s.agentRepo.GetByID(ctx, agentID)
	if err != nil {
		return fmt.Errorf("get agent: %w", err)
	}
	if agent == nil {
		return fmt.Errorf("agent not found")
	}

	// Apply profile fields.
	agent.Role = profile.Role
	if profile.Capabilities != nil {
		agent.Capabilities = profile.Capabilities
	}
	agent.ResponsibilityZone = profile.ResponsibilityZone
	if profile.EscalationTo != nil {
		agent.EscalationTo = &profile.EscalationTo
	}
	if profile.AcceptsFrom != nil {
		agent.AcceptsFrom = profile.AcceptsFrom
	}
	agent.MaxConcurrentTasks = profile.MaxConcurrentTasks
	agent.WorkingHours = profile.WorkingHours
	agent.ProfileDescription = profile.ProfileDescription
	agent.UpdatedAt = time.Now()

	return s.agentRepo.Update(ctx, agent)
}

// --------------------------------------------------------------------------
// Assignment Rules
// --------------------------------------------------------------------------

// GetWorkspaceAssignmentRules returns the workspace-level assignment rules config.
func (s *rulesService) GetWorkspaceAssignmentRules(ctx context.Context, workspaceID uuid.UUID) (*domain.AssignmentRulesConfig, error) {
	rule, err := s.wsRuleRepo.GetByType(ctx, workspaceID, domain.RuleConfigTypeAssignment)
	if err != nil {
		return nil, fmt.Errorf("get workspace assignment rules: %w", err)
	}
	if rule == nil {
		return &domain.AssignmentRulesConfig{}, nil
	}
	var cfg domain.AssignmentRulesConfig
	if err := json.Unmarshal(rule.Config, &cfg); err != nil {
		return nil, fmt.Errorf("parse assignment rules config: %w", err)
	}
	return &cfg, nil
}

// SetWorkspaceAssignmentRules upserts the workspace-level assignment rules config.
func (s *rulesService) SetWorkspaceAssignmentRules(ctx context.Context, workspaceID uuid.UUID, config domain.AssignmentRulesConfig) error {
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal assignment rules config: %w", err)
	}
	rule := &domain.WorkspaceRuleConfig{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		RuleType:    domain.RuleConfigTypeAssignment,
		Config:      data,
	}
	return s.wsRuleRepo.Upsert(ctx, rule)
}

// GetEffectiveAssignmentRules returns merged assignment rules for a project
// (project overrides workspace, with source annotations).
func (s *rulesService) GetEffectiveAssignmentRules(ctx context.Context, projectID uuid.UUID) (*domain.EffectiveAssignmentRules, error) {
	proj, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if proj == nil {
		return nil, fmt.Errorf("project not found")
	}

	// Get workspace rules.
	wsRule, err := s.wsRuleRepo.GetByType(ctx, proj.WorkspaceID, domain.RuleConfigTypeAssignment)
	if err != nil {
		return nil, fmt.Errorf("get workspace assignment rules: %w", err)
	}

	// Get project rules.
	projRule, err := s.projRuleRepo.GetByType(ctx, projectID, domain.RuleConfigTypeAssignment)
	if err != nil {
		return nil, fmt.Errorf("get project assignment rules: %w", err)
	}

	effective := &domain.EffectiveAssignmentRules{}

	// Start with workspace config.
	if wsRule != nil {
		var wsCfg domain.AssignmentRulesConfig
		if err := json.Unmarshal(wsRule.Config, &wsCfg); err == nil {
			if wsCfg.DefaultAssignee != "" {
				effective.DefaultAssignee = &domain.EffectiveAssignmentRule{Value: wsCfg.DefaultAssignee, Source: "workspace"}
			}
			if len(wsCfg.ByType) > 0 {
				effective.ByType = make(map[string]domain.EffectiveAssignmentRule)
				for k, v := range wsCfg.ByType {
					effective.ByType[k] = domain.EffectiveAssignmentRule{Value: v, Source: "workspace"}
				}
			}
			if len(wsCfg.ByPriority) > 0 {
				effective.ByPriority = make(map[string]domain.EffectiveAssignmentRule)
				for k, v := range wsCfg.ByPriority {
					effective.ByPriority[k] = domain.EffectiveAssignmentRule{Value: v, Source: "workspace"}
				}
			}
			if len(wsCfg.FallbackChain) > 0 {
				effective.FallbackChain = wsCfg.FallbackChain
			}
		}
	}

	// Project overrides workspace.
	if projRule != nil {
		var projCfg domain.AssignmentRulesConfig
		if err := json.Unmarshal(projRule.Config, &projCfg); err == nil {
			if projCfg.DefaultAssignee != "" {
				effective.DefaultAssignee = &domain.EffectiveAssignmentRule{Value: projCfg.DefaultAssignee, Source: "project"}
			}
			if len(projCfg.ByType) > 0 {
				if effective.ByType == nil {
					effective.ByType = make(map[string]domain.EffectiveAssignmentRule)
				}
				for k, v := range projCfg.ByType {
					effective.ByType[k] = domain.EffectiveAssignmentRule{Value: v, Source: "project"}
				}
			}
			if len(projCfg.ByPriority) > 0 {
				if effective.ByPriority == nil {
					effective.ByPriority = make(map[string]domain.EffectiveAssignmentRule)
				}
				for k, v := range projCfg.ByPriority {
					effective.ByPriority[k] = domain.EffectiveAssignmentRule{Value: v, Source: "project"}
				}
			}
			if len(projCfg.FallbackChain) > 0 {
				effective.FallbackChain = projCfg.FallbackChain
			}
		}
	}

	return effective, nil
}

// SetProjectAssignmentRules upserts the project-level assignment rules config.
func (s *rulesService) SetProjectAssignmentRules(ctx context.Context, projectID uuid.UUID, config domain.AssignmentRulesConfig) error {
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal assignment rules config: %w", err)
	}
	rule := &domain.ProjectRuleConfig{
		ID:              uuid.New(),
		ProjectID:       projectID,
		RuleType:        domain.RuleConfigTypeAssignment,
		Config:          data,
		EnforcementMode: domain.RuleConfigEnforcementAdvisory,
	}
	return s.projRuleRepo.Upsert(ctx, rule)
}

// --------------------------------------------------------------------------
// Workflow Rules
// --------------------------------------------------------------------------

// GetProjectWorkflowRules returns the project workflow rules, optionally enriched
// with the caller's computed permissions.
func (s *rulesService) GetProjectWorkflowRules(ctx context.Context, projectID uuid.UUID, callerAgentID *uuid.UUID) (*domain.WorkflowRulesResponse, error) {
	projRule, err := s.projRuleRepo.GetByType(ctx, projectID, domain.RuleConfigTypeWorkflow)
	if err != nil {
		return nil, fmt.Errorf("get project workflow rules: %w", err)
	}

	resp := &domain.WorkflowRulesResponse{}

	if projRule != nil {
		var cfg domain.WorkflowRulesConfig
		if err := json.Unmarshal(projRule.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parse workflow rules config: %w", err)
		}
		resp.WorkflowRulesConfig = cfg
	}

	// Compute my_permissions if a caller agent was provided.
	if callerAgentID != nil {
		agent, err := s.agentRepo.GetByID(ctx, *callerAgentID)
		if err == nil && agent != nil {
			resp.MyPermissions = computeAgentPermissions(agent, resp.WorkflowRulesConfig)
		}
	}

	return resp, nil
}

// SetProjectWorkflowRules upserts the project workflow rules config.
func (s *rulesService) SetProjectWorkflowRules(ctx context.Context, projectID uuid.UUID, config domain.WorkflowRulesConfig) error {
	enforcementMode := config.EnforcementMode
	if enforcementMode == "" {
		enforcementMode = domain.RuleConfigEnforcementAdvisory
	}

	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal workflow rules config: %w", err)
	}
	rule := &domain.ProjectRuleConfig{
		ID:              uuid.New(),
		ProjectID:       projectID,
		RuleType:        domain.RuleConfigTypeWorkflow,
		Config:          data,
		EnforcementMode: enforcementMode,
	}
	return s.projRuleRepo.Upsert(ctx, rule)
}

// --------------------------------------------------------------------------
// Violations
// --------------------------------------------------------------------------

// ListViolations returns recent violation log entries for the workspace.
func (s *rulesService) ListViolations(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.RuleViolationLog, error) {
	return s.violationRepo.ListByWorkspace(ctx, workspaceID, limit)
}

// LogViolation persists a rule violation log entry.
func (s *rulesService) LogViolation(ctx context.Context, v *domain.RuleViolationLog) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	return s.violationRepo.Create(ctx, v)
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// computeAgentPermissions calculates what a given agent is allowed to do
// based on workflow rules policies.
func computeAgentPermissions(agent *domain.Agent, cfg domain.WorkflowRulesConfig) *domain.MyPermissions {
	perms := &domain.MyPermissions{
		MyRole:         agent.Role,
		MyName:         agent.Name,
		CanTransition:  make(map[string]bool),
		CanCreateTasks: false,
		CanDeleteTasks: false,
		CanReassign:    false,
	}

	// Check each status transition for this agent.
	for status, transition := range cfg.Transitions {
		allowed := isActorAllowed(agent, transition.Allowed)
		perms.CanTransition[status] = allowed
	}

	// Check policies.
	if cfg.Policies != nil {
		if policy, ok := cfg.Policies["create_tasks"]; ok {
			perms.CanCreateTasks = isActorAllowed(agent, policy.Allowed)
		}
		if policy, ok := cfg.Policies["delete_tasks"]; ok {
			perms.CanDeleteTasks = isActorAllowed(agent, policy.Allowed)
		}
		if policy, ok := cfg.Policies["reassign"]; ok {
			perms.CanReassign = isActorAllowed(agent, policy.Allowed)
		}
	}

	return perms
}

// isActorAllowed checks if an agent matches any of the allowed actor patterns.
// Patterns: "*" (any), "role:developer", "agent:name", "assigned".
func isActorAllowed(agent *domain.Agent, allowed []string) bool {
	for _, pattern := range allowed {
		switch pattern {
		case "*":
			return true
		}
		if len(pattern) > 5 && pattern[:5] == "role:" {
			if agent.Role == pattern[5:] {
				return true
			}
		}
		if len(pattern) > 6 && pattern[:6] == "agent:" {
			if agent.Name == pattern[6:] || agent.Slug == pattern[6:] {
				return true
			}
		}
	}
	return false
}

// --------------------------------------------------------------------------
// Sprint 21 — Workflow Templates
// --------------------------------------------------------------------------

// GetWorkflowTemplates returns all workspace-level workflow templates.
func (s *rulesService) GetWorkflowTemplates(ctx context.Context, workspaceID uuid.UUID) (map[string]domain.WorkflowRulesConfig, error) {
	rule, err := s.wsRuleRepo.GetByType(ctx, workspaceID, domain.RuleConfigTypeWorkflowTemplate)
	if err != nil {
		return nil, fmt.Errorf("get workflow templates rule: %w", err)
	}
	if rule == nil {
		return map[string]domain.WorkflowRulesConfig{}, nil
	}
	var templates map[string]domain.WorkflowRulesConfig
	if err := json.Unmarshal(rule.Config, &templates); err != nil {
		return nil, fmt.Errorf("parse workflow templates config: %w", err)
	}
	return templates, nil
}

// SetWorkflowTemplates upserts the workspace-level workflow templates config.
func (s *rulesService) SetWorkflowTemplates(ctx context.Context, workspaceID uuid.UUID, templates map[string]domain.WorkflowRulesConfig) error {
	data, err := json.Marshal(templates)
	if err != nil {
		return fmt.Errorf("marshal workflow templates config: %w", err)
	}
	rule := &domain.WorkspaceRuleConfig{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		RuleType:    domain.RuleConfigTypeWorkflowTemplate,
		Config:      data,
	}
	return s.wsRuleRepo.Upsert(ctx, rule)
}

// --------------------------------------------------------------------------
// Sprint 21 — Config Import/Export
// --------------------------------------------------------------------------

// ExportConfig assembles the full workspace config and returns it as YAML bytes.
func (s *rulesService) ExportConfig(ctx context.Context, workspaceID uuid.UUID) ([]byte, error) {
	ws, err := s.workspaceRepo.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	if ws == nil {
		return nil, fmt.Errorf("workspace not found")
	}

	cfg := domain.MeshConfig{
		Workspace: ws.Slug,
		Version:   1,
	}

	// Team.
	dir, err := s.GetTeamDirectory(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("get team directory: %w", err)
	}
	teamCfg := domain.TeamConfig{}
	for _, a := range dir.Agents {
		ac := domain.TeamAgentConfig{
			Name:               a.Name,
			Role:               a.Role,
			ResponsibilityZone: a.ResponsibilityZone,
			MaxConcurrentTasks: a.MaxConcurrentTasks,
			WorkingHours:       a.WorkingHours,
			Description:        a.ProfileDescription,
		}
		// Unmarshal capabilities and accepts_from into string slices.
		if len(a.Capabilities) > 0 {
			_ = json.Unmarshal(a.Capabilities, &ac.Capabilities)
		}
		if len(a.AcceptsFrom) > 0 {
			_ = json.Unmarshal(a.AcceptsFrom, &ac.AcceptsFrom)
		}
		// EscalationTo stored as a JSON string or array.
		if len(a.EscalationTo) > 0 {
			var escalation string
			if jsonErr := json.Unmarshal(a.EscalationTo, &escalation); jsonErr == nil {
				ac.EscalationTo = escalation
			}
		}
		teamCfg.Agents = append(teamCfg.Agents, ac)
	}
	for _, h := range dir.Humans {
		hc := domain.TeamHumanConfig{
			Name: h.Name,
			Role: h.Role,
		}
		if len(h.Capabilities) > 0 {
			_ = json.Unmarshal(h.Capabilities, &hc.Capabilities)
		}
		teamCfg.Humans = append(teamCfg.Humans, hc)
	}
	if len(teamCfg.Agents) > 0 || len(teamCfg.Humans) > 0 {
		cfg.Team = &teamCfg
	}

	// Assignment rules.
	assignmentCfg, err := s.GetWorkspaceAssignmentRules(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("get assignment rules: %w", err)
	}
	if assignmentCfg != nil && (assignmentCfg.DefaultAssignee != "" || len(assignmentCfg.ByType) > 0 || len(assignmentCfg.ByPriority) > 0 || len(assignmentCfg.FallbackChain) > 0) {
		cfg.AssignmentRules = assignmentCfg
	}

	// Workflow templates.
	templates, err := s.GetWorkflowTemplates(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("get workflow templates: %w", err)
	}
	if len(templates) > 0 {
		cfg.WorkflowTemplates = templates
	}

	return yaml.Marshal(cfg)
}

// ImportConfig parses a YAML MeshConfig and applies team, assignment rules, and workflow templates.
func (s *rulesService) ImportConfig(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.ImportResult, error) {
	var meshCfg domain.MeshConfig
	if err := yaml.Unmarshal(yamlData, &meshCfg); err != nil {
		return nil, fmt.Errorf("parse mesh config YAML: %w", err)
	}

	result := &domain.ImportResult{
		Warnings: []string{},
	}

	// Apply team section.
	if meshCfg.Team != nil {
		teamResult, err := s.importTeamConfig(ctx, workspaceID, meshCfg.Team)
		if err != nil {
			return nil, fmt.Errorf("import team: %w", err)
		}
		result.Team = teamResult
	}

	// Apply assignment rules section.
	if meshCfg.AssignmentRules != nil {
		if err := s.SetWorkspaceAssignmentRules(ctx, workspaceID, *meshCfg.AssignmentRules); err != nil {
			return nil, fmt.Errorf("set assignment rules: %w", err)
		}
		result.AssignmentRules = &domain.ImportRulesResult{Updated: true}
	}

	// Apply workflow templates section.
	if len(meshCfg.WorkflowTemplates) > 0 {
		existing, err := s.GetWorkflowTemplates(ctx, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("get existing workflow templates: %w", err)
		}
		created, updated := 0, 0
		for name, tpl := range meshCfg.WorkflowTemplates {
			if _, exists := existing[name]; exists {
				updated++
			} else {
				created++
			}
			existing[name] = tpl
		}
		if err := s.SetWorkflowTemplates(ctx, workspaceID, existing); err != nil {
			return nil, fmt.Errorf("set workflow templates: %w", err)
		}
		result.WorkflowTemplates = &domain.ImportTemplatesResult{Created: created, Updated: updated}
	}

	return result, nil
}

// ImportTeam parses a YAML TeamConfig and applies it to the workspace.
func (s *rulesService) ImportTeam(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.TeamImportResult, error) {
	var teamCfg domain.TeamConfig
	if err := yaml.Unmarshal(yamlData, &teamCfg); err != nil {
		return nil, fmt.Errorf("parse team YAML: %w", err)
	}
	return s.importTeamConfig(ctx, workspaceID, &teamCfg)
}

// importTeamConfig applies a TeamConfig to the workspace by looking up agents/humans by name
// and updating their profile fields.
func (s *rulesService) importTeamConfig(ctx context.Context, workspaceID uuid.UUID, teamCfg *domain.TeamConfig) (*domain.TeamImportResult, error) {
	result := &domain.TeamImportResult{}

	// Process agents: look up each agent by name within the workspace, then update profile.
	for _, ac := range teamCfg.Agents {
		filter := repository.AgentFilter{Search: ac.Name}
		pg := pagination.Params{Page: 1, PageSize: 50}
		page, err := s.agentRepo.List(ctx, workspaceID, filter, pg)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("agent %q: list error: %v", ac.Name, err))
			continue
		}
		// Find the exact name match from the search results.
		var matchedAgent *domain.Agent
		for i := range page.Items {
			if page.Items[i].Name == ac.Name || page.Items[i].Slug == ac.Name {
				matchedAgent = &page.Items[i]
				break
			}
		}
		if matchedAgent == nil {
			result.Errors = append(result.Errors, fmt.Sprintf("agent %q: not found in workspace", ac.Name))
			continue
		}

		// Marshal capabilities and accepts_from slices back to JSON.
		capJSON, _ := json.Marshal(ac.Capabilities)
		acceptsJSON, _ := json.Marshal(ac.AcceptsFrom)
		escalationJSON, _ := json.Marshal(ac.EscalationTo)

		profile := domain.AgentProfileUpdate{
			Role:               ac.Role,
			Capabilities:       capJSON,
			ResponsibilityZone: ac.ResponsibilityZone,
			EscalationTo:       escalationJSON,
			AcceptsFrom:        acceptsJSON,
			MaxConcurrentTasks: ac.MaxConcurrentTasks,
			WorkingHours:       ac.WorkingHours,
			ProfileDescription: ac.Description,
		}
		if err := s.UpdateAgentProfile(ctx, matchedAgent.ID, profile); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("agent %q: update error: %v", ac.Name, err))
			continue
		}
		result.AgentsUpdated++
	}

	// Process humans: look up each human by display name in the workspace members list.
	members, err := s.memberRepo.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	// Build a name→member index.
	memberByName := make(map[string]domain.WorkspaceMemberWithUser, len(members))
	for _, m := range members {
		memberByName[m.User.Name] = m
	}

	for _, hc := range teamCfg.Humans {
		m, ok := memberByName[hc.Name]
		if !ok {
			result.Errors = append(result.Errors, fmt.Sprintf("human %q: not found in workspace members", hc.Name))
			continue
		}
		// Update the workspace member role if provided.
		if hc.Role != "" && hc.Role != m.Role {
			if err := s.memberRepo.UpdateRole(ctx, workspaceID, m.User.ID, hc.Role); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("human %q: update role error: %v", hc.Name, err))
				continue
			}
		}
		result.HumansUpdated++
	}

	return result, nil
}

// derefRawMessage safely dereferences a *json.RawMessage, returning nil if the pointer is nil.
func derefRawMessage(p *json.RawMessage) json.RawMessage {
	if p == nil {
		return nil
	}
	return *p
}

