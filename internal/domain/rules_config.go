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
	Statuses            []string                  `json:"statuses,omitempty"`
	Transitions         map[string]TransitionRule `json:"transitions,omitempty"`
	EnforcementMode     string                    `json:"enforcement_mode,omitempty"`
	Policies            map[string]PolicyRule     `json:"policies,omitempty"`
	EnforceSystemActors bool                      `json:"enforce_system_actors,omitempty"` // if true, system actors are not exempt
	MidPipeline         *MidPipelineConfig        `json:"mid_pipeline,omitempty"`
}

// MidPipelineConfig configures the mid-pipeline guard: the two transitions that
// had no writer other than an agent following a prompt (todo→in_progress is
// already written by CheckoutTask; these are the two that remain).
//
// Both flags default to OFF and both are additive: with the block absent — which
// is every project until someone opts in — the server behaves exactly as it did
// before this type existed. That is deliberate. Each flag tightens a gate that
// the whole fleet moves tasks through, so the blast radius of getting one wrong
// is every lane at once; the safe default is the behaviour already in production,
// and enabling is a per-project data change rather than a release.
//
// Note the asymmetry with the progress signal used by the auto-park sweep: that
// is a CORRECTNESS fix (updated_at alone cannot see a comment) and is therefore
// unconditional, not flagged. A flag guards a change of policy; it must not
// guard a change from wrong to right.
type MidPipelineConfig struct {
	// ReviewEvidenceStrict tightens the existing review-evidence gate from
	// "has any comment" to "has evidence": an artifact, a VCS link, a passing
	// dod_check, or a comment that actually carries a URL.
	//
	// The loose form is close to vacuous in practice — fleet convention already
	// requires a comment on every status change, so the third arm is true on
	// essentially every card by the time it reaches review, and the gate only
	// ever refuses a card nothing has happened on. Strict mode is what makes
	// "70% of closes bypassed review" distinguishable from "the gate works".
	ReviewEvidenceStrict bool `json:"review_evidence_strict,omitempty"`

	// AutoParkStalled changes where a stalled in_progress task is sent by the
	// lease reaper's unleased sweep: backlog with a due_date and a kind:monitor
	// label, instead of straight back to todo.
	//
	// Sending it to todo returns it to the feed immediately, which is how a
	// stalled card gets re-fed to the same lane over and over (measured: 320
	// cards fed >=3 times, 109 >=5). Parking breaks that loop, and the
	// kind:monitor label is what lets MonitorPromotionService pick it back up
	// when the due_date arrives — without the label the park has no wake-up
	// path at all and the card sleeps indefinitely.
	AutoParkStalled bool `json:"auto_park_stalled,omitempty"`

	// AutoParkDueHours is how far ahead the park sets due_date. 0 = default (24).
	AutoParkDueHours int `json:"auto_park_due_hours,omitempty"`

	// TriageEntryStrict gates entry into the triage status category: a move is
	// allowed only when the task's human_gate metadata says a human genuinely
	// needs to look at it — the gate was authored by a human directly
	// (GateAuthorType == user), or its class is hard (the fail-closed default
	// for a "❓ Blocking @user" marker, regardless of who posted it).
	//
	// Off by default for the same reason as the other two flags: triage today
	// has four inputs (the comment-marker path this gate protects, an explicit
	// PATCH arm, and two dispatcher-side paths outside this repo — a stale-
	// redispatch auto-triage and a manual escalation, neither of which ever
	// sets human_gate at all) and one exit that only reliably fires for the
	// gated ones. Turning this on for every project at once would 422 the
	// dispatcher's un-gated triage moves fleet-wide before its callers have
	// been adapted to the new refusal; per-project opt-in lets Lab and Mesh
	// dev absorb that first.
	TriageEntryStrict bool `json:"triage_entry_strict,omitempty"`

	// TriageParkDueHours is how far ahead the triage-entry fallback (see
	// enforceBlockingTriage) sets due_date when it parks a disqualified gate to
	// backlog instead of moving it to triage. 0 = default (48).
	TriageParkDueHours int `json:"triage_park_due_hours,omitempty"`
}

// DefaultAutoParkDueHours is the wake-up delay applied to an auto-parked task
// when the project config does not name one.
const DefaultAutoParkDueHours = 24

// DefaultTriageParkDueHours is the wake-up delay applied when a disqualified
// gate is parked to backlog instead of triage.
const DefaultTriageParkDueHours = 48

// AutoParkDue returns the configured wake-up delay, falling back to the default.
// Safe on a nil receiver: a project with no mid_pipeline block still answers.
func (c *MidPipelineConfig) AutoParkDue() int {
	if c == nil || c.AutoParkDueHours <= 0 {
		return DefaultAutoParkDueHours
	}
	return c.AutoParkDueHours
}

// ReviewStrict reports whether strict review evidence is enabled. Nil-safe so
// callers do not have to nil-check a block that is absent on most projects.
func (c *MidPipelineConfig) ReviewStrict() bool {
	return c != nil && c.ReviewEvidenceStrict
}

// ParkStalled reports whether stalled in_progress tasks should be parked rather
// than returned to todo. Nil-safe, same reason as ReviewStrict.
func (c *MidPipelineConfig) ParkStalled() bool {
	return c != nil && c.AutoParkStalled
}

// TriageStrict reports whether the triage-entry gate is enabled. Nil-safe,
// same reason as ReviewStrict.
func (c *MidPipelineConfig) TriageStrict() bool {
	return c != nil && c.TriageEntryStrict
}

// TriageParkDue returns the configured triage-fallback wake-up delay, falling
// back to the default. Nil-safe, same reason as AutoParkDue.
func (c *MidPipelineConfig) TriageParkDue() int {
	if c == nil || c.TriageParkDueHours <= 0 {
		return DefaultTriageParkDueHours
	}
	return c.TriageParkDueHours
}

// TransitionRule defines allowed transitions from a given status.
type TransitionRule struct {
	Allowed       []string          `json:"allowed"`                  // allowed target status names (empty = any)
	AllowedActors []string          `json:"allowed_actors,omitempty"` // actor patterns (empty = any)
	Description   string            `json:"description,omitempty"`
	OnTransition  *TransitionAction `json:"on_transition,omitempty"`
	Requires      *TransitionReq    `json:"requires,omitempty"`
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
//
// This is a PATCH-semantics partial update, despite the route being PUT
// /agents/:agent_id/profile: every field is a pointer (or a nil-able
// json.RawMessage) and a field omitted from the request body is left
// untouched on the agent rather than being zeroed out. A caller that wants to
// clear a field must send it explicitly (e.g. "working_hours": "").
type AgentProfileUpdate struct {
	Role               *string         `json:"role,omitempty"`
	Capabilities       json.RawMessage `json:"capabilities"`
	ResponsibilityZone *string         `json:"responsibility_zone,omitempty"`
	// EscalationTo is a single name/handle (e.g. "Garfield"), not a list —
	// matches the shape every other write path already assumes: the MCP
	// tool (mcpsdk.ParseString), and TeamAgentConfig.EscalationTo (plain
	// string) used by YAML config import/export.
	EscalationTo       *string         `json:"escalation_to,omitempty"`
	AcceptsFrom        json.RawMessage `json:"accepts_from"`
	MaxConcurrentTasks *int            `json:"max_concurrent_tasks,omitempty"`
	WorkingHours       *string         `json:"working_hours,omitempty"`
	ProfileDescription *string         `json:"profile_description,omitempty"`
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
	// Computed presence fields (derived, not stored in DB)
	ComputedStatus ComputedAgentStatus `json:"computed_status"`
	LastSeenAt     *time.Time          `json:"last_seen_at,omitempty"`
}

// TeamDirectoryHuman is the human member profile for team directory API.
type TeamDirectoryHuman struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// Username is the @-handle, present for the same reason an agent carries
	// Slug: the directory is what a renderer consults to decide whether an
	// `@word` in a comment is a mention. Without it agents highlighted and
	// people did not, so "@pavel — @daedalus" rendered one as a mention and the
	// other as prose even though both had been notified.
	Username           string          `json:"username"`
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
	FallbackChain   []string                           `json:"fallback_chain,omitempty"`
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
	Workspace         string                         `yaml:"workspace" json:"workspace"`
	Version           int                            `yaml:"version" json:"version"`
	Team              *TeamConfig                    `yaml:"team,omitempty" json:"team,omitempty"`
	AssignmentRules   *AssignmentRulesConfig         `yaml:"assignment_rules,omitempty" json:"assignment_rules,omitempty"`
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
	Warnings          []string               `json:"warnings"`
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
