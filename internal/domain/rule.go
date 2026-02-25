package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// RuleScope defines the level at which a rule applies.
type RuleScope string

const (
	RuleScopeWorkspace RuleScope = "workspace"
	RuleScopeProject   RuleScope = "project"
	RuleScopeAgent     RuleScope = "agent"
)

// RuleEnforcement defines the consequence when a rule is violated.
type RuleEnforcement string

const (
	RuleEnforcementBlock RuleEnforcement = "block"
	RuleEnforcementWarn  RuleEnforcement = "warn"
	RuleEnforcementLog   RuleEnforcement = "log"
)

// Rule is a governance policy that constrains actor behavior within a workspace or project.
type Rule struct {
	ID                  uuid.UUID       `json:"id" db:"id"`
	WorkspaceID         uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	ProjectID           *uuid.UUID      `json:"project_id,omitempty" db:"project_id"`
	AgentID             *uuid.UUID      `json:"agent_id,omitempty" db:"agent_id"`
	Scope               RuleScope       `json:"scope" db:"scope"`
	RuleType            string          `json:"rule_type" db:"rule_type"`
	Name                string          `json:"name" db:"name"`
	Description         string          `json:"description" db:"description"`
	Config              json.RawMessage `json:"config" db:"config"`
	AppliesToActorTypes pq.StringArray  `json:"applies_to_actor_types" db:"applies_to_actor_types"`
	AppliesToRoles      pq.StringArray  `json:"applies_to_roles" db:"applies_to_roles"`
	Enforcement         RuleEnforcement `json:"enforcement" db:"enforcement"`
	Priority            int             `json:"priority" db:"priority"`
	IsEnabled           bool            `json:"is_enabled" db:"is_enabled"`
	CreatedBy           uuid.UUID       `json:"created_by" db:"created_by"`
	CreatedByType       ActorType       `json:"created_by_type" db:"created_by_type"`
	CreatedAt           time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at" db:"updated_at"`
}

// RuleViolation describes a single rule that was violated during evaluation.
type RuleViolation struct {
	RuleID      uuid.UUID       `json:"rule_id"`
	RuleName    string          `json:"rule_name"`
	RuleType    string          `json:"rule_type"`
	Enforcement RuleEnforcement `json:"enforcement"`
	Message     string          `json:"message"`
}
