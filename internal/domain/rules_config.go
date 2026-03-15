package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// WorkspaceRuleConfig stores workspace-level configuration (assignment defaults, policies, workflow templates).
// Stored in the workspace_rules table, one row per rule_type per workspace.
type WorkspaceRuleConfig struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	WorkspaceID uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	RuleType    string          `json:"rule_type" db:"rule_type"`
	Config      json.RawMessage `json:"config" db:"config"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at" db:"updated_at"`
}

// ProjectRuleConfig stores project-level configuration (workflow transitions, assignment overrides).
// Stored in the project_rules table, one row per rule_type per project.
type ProjectRuleConfig struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	ProjectID       uuid.UUID       `json:"project_id" db:"project_id"`
	RuleType        string          `json:"rule_type" db:"rule_type"`
	Config          json.RawMessage `json:"config" db:"config"`
	EnforcementMode string          `json:"enforcement_mode" db:"enforcement_mode"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at" db:"updated_at"`
}

// RuleViolationLog records a rule breach (advisory: allowed, strict: blocked).
// Named with Log suffix to distinguish from the in-memory RuleViolation used during evaluation.
type RuleViolationLog struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	WorkspaceID     uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	ProjectID       *uuid.UUID      `json:"project_id,omitempty" db:"project_id"`
	ActorID         uuid.UUID       `json:"actor_id" db:"actor_id"`
	ActorType       string          `json:"actor_type" db:"actor_type"`
	RuleType        string          `json:"rule_type" db:"rule_type"`
	ViolationDetail json.RawMessage `json:"violation_detail" db:"violation_detail"`
	ActionTaken     string          `json:"action_taken" db:"action_taken"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
}

// Rule type constants for workspace/project rule configs.
const (
	RuleConfigTypeAssignment       = "assignment"
	RuleConfigTypePolicy           = "policy"
	RuleConfigTypeWorkflowTemplate = "workflow_template"
	RuleConfigTypeWorkflow         = "workflow"
)

// Enforcement modes for project rules.
const (
	RuleConfigEnforcementAdvisory = "advisory"
	RuleConfigEnforcementStrict   = "strict"
)

// AssignmentRulesConfig is the typed structure stored in workspace_rules/project_rules config JSONB
// for rule_type = "assignment".
type AssignmentRulesConfig struct {
	DefaultAssignee string            `json:"default_assignee,omitempty"`
	ByType          map[string]string `json:"by_type,omitempty"`
	ByPriority      map[string]string `json:"by_priority,omitempty"`
	FallbackChain   []string          `json:"fallback_chain,omitempty"`
}

// WorkflowRulesConfig is the typed structure for project workflow rules (rule_type = "workflow").
type WorkflowRulesConfig struct {
	Statuses        []string                    `json:"statuses,omitempty"`
	Transitions     map[string]TransitionRule   `json:"transitions,omitempty"`
	EnforcementMode string                      `json:"enforcement_mode,omitempty"`
	Policies        map[string]PolicyRule       `json:"policies,omitempty"`
}

// TransitionRule defines allowed transitions from a given status.
type TransitionRule struct {
	Allowed      []string          `json:"allowed"`
	Description  string            `json:"description,omitempty"`
	OnTransition *TransitionAction `json:"on_transition,omitempty"`
	Requires     *TransitionReq    `json:"requires,omitempty"`
}

// TransitionAction defines automatic actions when a transition occurs.
type TransitionAction struct {
	AutoAssign  bool     `json:"auto_assign,omitempty"`
	SetReviewer string   `json:"set_reviewer,omitempty"`
	Notify      []string `json:"notify,omitempty"`
}

// TransitionReq defines requirements before a transition is allowed.
type TransitionReq struct {
	Approval bool `json:"approval,omitempty"`
}

// PolicyRule defines actor permissions within the workflow.
type PolicyRule struct {
	Allowed []string `json:"allowed"`
}

// AgentProfileUpdate represents the updatable profile fields for an agent (team directory).
type AgentProfileUpdate struct {
	Role               string          `json:"role"`
	Capabilities       json.RawMessage `json:"capabilities"`
	ResponsibilityZone string          `json:"responsibility_zone"`
	EscalationTo       json.RawMessage `json:"escalation_to,omitempty"`
	AcceptsFrom        json.RawMessage `json:"accepts_from"`
	MaxConcurrentTasks int             `json:"max_concurrent_tasks"`
	WorkingHours       string          `json:"working_hours"`
	ProfileDescription string          `json:"profile_description"`
}

// TeamDirectoryAgent is the full agent info for team directory API.
type TeamDirectoryAgent struct {
	ID                 uuid.UUID       `json:"id"`
	Name               string          `json:"name"`
	Slug               string          `json:"slug"`
	Status             AgentStatus     `json:"status"`
	AgentType          AgentType       `json:"agent_type"`
	ParentAgentID      *uuid.UUID      `json:"parent_agent_id,omitempty"`
	SupervisorUserID   *uuid.UUID      `json:"supervisor_user_id,omitempty"`
	Role               string          `json:"role"`
	Capabilities       json.RawMessage `json:"capabilities"`
	ResponsibilityZone string          `json:"responsibility_zone"`
	EscalationTo       json.RawMessage `json:"escalation_to,omitempty"`
	AcceptsFrom        json.RawMessage `json:"accepts_from"`
	MaxConcurrentTasks int             `json:"max_concurrent_tasks"`
	WorkingHours       string          `json:"working_hours"`
	ProfileDescription string          `json:"profile_description"`
	CurrentTasks       int             `json:"current_tasks"`
	Projects           []string        `json:"projects"`
	// Heartbeat monitoring fields
	LastHeartbeat    *time.Time `json:"last_heartbeat,omitempty"`
	HeartbeatStatus  string     `json:"heartbeat_status,omitempty"`
	HeartbeatMessage string     `json:"heartbeat_message,omitempty"`
	IsStale          bool       `json:"is_stale"`
}

// TeamDirectoryHuman is the human member profile for team directory API.
type TeamDirectoryHuman struct {
	ID                 uuid.UUID       `json:"id"`
	Name               string          `json:"name"`
	Email              string          `json:"email"`
	AvatarURL          string          `json:"avatar_url"`
	Role               string          `json:"role"`
	Capabilities       json.RawMessage `json:"capabilities"`
	ResponsibilityZone string          `json:"responsibility_zone"`
	Availability       string          `json:"availability"`
	Projects           []string        `json:"projects"`
}

// TeamDirectory is the response for GET /workspaces/:ws_id/team (flat format).
type TeamDirectory struct {
	Workspace string               `json:"workspace"`
	Agents    []TeamDirectoryAgent `json:"agents"`
	Humans    []TeamDirectoryHuman `json:"humans"`
}

// TeamDirectoryAgentNode is a tree node for the hierarchical org chart view.
type TeamDirectoryAgentNode struct {
	TeamDirectoryAgent
	Children []TeamDirectoryAgentNode `json:"children"`
}

// TeamDirectoryTree is the response for GET /workspaces/:ws_id/team?format=tree.
type TeamDirectoryTree struct {
	Workspace string                   `json:"workspace"`
	AgentTree []TeamDirectoryAgentNode `json:"agent_tree"`
	Humans    []TeamDirectoryHuman     `json:"humans"`
}

// EffectiveAssignmentRule is a single assignment rule value annotated with its source.
type EffectiveAssignmentRule struct {
	Value  string `json:"value"`
	Source string `json:"source"` // "project" or "workspace"
}

// EffectiveAssignmentRules is the merged response for assignment rules (workspace + project).
type EffectiveAssignmentRules struct {
	DefaultAssignee *EffectiveAssignmentRule           `json:"default_assignee,omitempty"`
	ByType          map[string]EffectiveAssignmentRule `json:"by_type,omitempty"`
	ByPriority      map[string]EffectiveAssignmentRule `json:"by_priority,omitempty"`
	FallbackChain   []string                          `json:"fallback_chain,omitempty"`
}

// AutoAssignTestResult is the response for the auto-assign diagnostic endpoint.
type AutoAssignTestResult struct {
	WouldAssign  bool                     `json:"would_assign"`
	AssigneeID   string                   `json:"assignee_id,omitempty"`
	AssigneeType string                   `json:"assignee_type,omitempty"`
	MatchedRule  string                   `json:"matched_rule,omitempty"` // "by_type", "by_priority", "default_assignee", "fallback_chain"
	Candidates   []AutoAssignCandidate    `json:"candidates"`
	Effective    *EffectiveAssignmentRules `json:"effective_rules"`
}

// AutoAssignCandidate is a candidate evaluated during auto-assign simulation.
type AutoAssignCandidate struct {
	Value  string `json:"value"`
	Source string `json:"source"` // "by_type", "by_priority", "default_assignee", "fallback_chain"
	Valid  bool   `json:"valid"`
	Reason string `json:"reason,omitempty"` // why invalid
}

// WorkflowRulesResponse is the response for GET /projects/:proj_id/rules/workflow.
type WorkflowRulesResponse struct {
	WorkflowRulesConfig
	MyPermissions *MyPermissions `json:"my_permissions,omitempty"`
}

// MyPermissions holds the computed permissions for the authenticated caller.
type MyPermissions struct {
	MyRole         string          `json:"my_role"`
	MyName         string          `json:"my_name"`
	CanTransition  map[string]bool `json:"can_transition"`
	CanCreateTasks bool            `json:"can_create_tasks"`
	CanDeleteTasks bool            `json:"can_delete_tasks"`
	CanReassign    bool            `json:"can_reassign"`
}

// --------------------------------------------------------------------------
// Sprint 21 — Config Import/Export + Workflow Templates
// --------------------------------------------------------------------------

// MeshConfig is the unified workspace configuration format for YAML import/export.
type MeshConfig struct {
	Workspace         string                        `yaml:"workspace" json:"workspace"`
	Version           int                           `yaml:"version" json:"version"`
	Team              *TeamConfig                   `yaml:"team,omitempty" json:"team,omitempty"`
	AssignmentRules   *AssignmentRulesConfig        `yaml:"assignment_rules,omitempty" json:"assignment_rules,omitempty"`
	WorkflowTemplates map[string]WorkflowRulesConfig `yaml:"workflow_templates,omitempty" json:"workflow_templates,omitempty"`
}

// TeamConfig holds the agent and human member lists for import/export.
type TeamConfig struct {
	Agents []TeamAgentConfig `yaml:"agents,omitempty" json:"agents,omitempty"`
	Humans []TeamHumanConfig `yaml:"humans,omitempty" json:"humans,omitempty"`
}

// TeamAgentConfig is the YAML/JSON representation of an agent in a team config.
type TeamAgentConfig struct {
	Name               string   `yaml:"name" json:"name"`
	DisplayName        string   `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Role               string   `yaml:"role" json:"role"`
	Capabilities       []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	ResponsibilityZone string   `yaml:"responsibility_zone,omitempty" json:"responsibility_zone,omitempty"`
	EscalationTo       string   `yaml:"escalation_to,omitempty" json:"escalation_to,omitempty"`
	AcceptsFrom        []string `yaml:"accepts_from,omitempty" json:"accepts_from,omitempty"`
	MaxConcurrentTasks int      `yaml:"max_concurrent_tasks,omitempty" json:"max_concurrent_tasks,omitempty"`
	WorkingHours       string   `yaml:"working_hours,omitempty" json:"working_hours,omitempty"`
	Description        string   `yaml:"description,omitempty" json:"description,omitempty"`
}

// TeamHumanConfig is the YAML/JSON representation of a human member in a team config.
type TeamHumanConfig struct {
	Name               string   `yaml:"name" json:"name"`
	Role               string   `yaml:"role,omitempty" json:"role,omitempty"`
	ResponsibilityZone string   `yaml:"responsibility_zone,omitempty" json:"responsibility_zone,omitempty"`
	Capabilities       []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Availability       string   `yaml:"availability,omitempty" json:"availability,omitempty"`
}

// ImportResult is the response returned after a config import operation.
type ImportResult struct {
	Team              *TeamImportResult      `json:"team,omitempty"`
	AssignmentRules   *ImportRulesResult     `json:"assignment_rules,omitempty"`
	WorkflowTemplates *ImportTemplatesResult `json:"workflow_templates,omitempty"`
	Warnings          []string              `json:"warnings"`
}

// TeamImportResult holds counts and errors from a team import operation.
type TeamImportResult struct {
	AgentsUpdated int      `json:"agents_updated"`
	HumansUpdated int      `json:"humans_updated"`
	Errors        []string `json:"errors,omitempty"`
}

// ImportRulesResult indicates whether assignment rules were updated.
type ImportRulesResult struct {
	Updated bool `json:"updated"`
}

// ImportTemplatesResult holds counts from a workflow templates import.
type ImportTemplatesResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
}
