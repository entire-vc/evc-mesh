package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AgentType identifies the kind of AI agent.
type AgentType string

const (
	AgentTypeClaudeCode AgentType = "claude_code"
	AgentTypeOpenClaw   AgentType = "openclaw"
	AgentTypeCline      AgentType = "cline"
	AgentTypeAider      AgentType = "aider"
	AgentTypeCustom     AgentType = "custom"
)

// AgentStatus represents the current operational state of an agent.
type AgentStatus string

const (
	AgentStatusOnline  AgentStatus = "online"
	AgentStatusOffline AgentStatus = "offline"
	AgentStatusBusy    AgentStatus = "busy"
	AgentStatusError   AgentStatus = "error"
)

// Agent represents a registered AI agent within a workspace.
// Agents authenticate via API keys and interact through MCP or REST API.
type Agent struct {
	ID                  uuid.UUID       `json:"id" db:"id"`
	WorkspaceID         uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	ParentAgentID       *uuid.UUID      `json:"parent_agent_id,omitempty" db:"parent_agent_id"`
	SupervisorUserID    *uuid.UUID      `json:"supervisor_user_id,omitempty" db:"supervisor_user_id"`
	Name                string          `json:"name" db:"name"`
	Slug                string          `json:"slug" db:"slug"`
	AgentType           AgentType       `json:"agent_type" db:"agent_type"`
	APIKeyHash          string          `json:"-" db:"api_key_hash"`
	APIKeyPrefix        string          `json:"api_key_prefix" db:"api_key_prefix"`
	Capabilities        json.RawMessage `json:"capabilities" db:"capabilities"`
	Status              AgentStatus     `json:"status" db:"status"`
	LastHeartbeat       *time.Time      `json:"last_heartbeat" db:"last_heartbeat"`
	HeartbeatStatus     string          `json:"heartbeat_status" db:"heartbeat_status"`
	HeartbeatMessage    string          `json:"heartbeat_message" db:"heartbeat_message"`
	HeartbeatMetadata   json.RawMessage `json:"heartbeat_metadata" db:"heartbeat_metadata"`
	CurrentTaskID       *uuid.UUID      `json:"current_task_id" db:"current_task_id"`
	Settings            json.RawMessage `json:"settings" db:"settings"`
	TotalTasksCompleted int             `json:"total_tasks_completed" db:"total_tasks_completed"`
	TotalErrors         int             `json:"total_errors" db:"total_errors"`
	ExternalAgentID     *string         `json:"external_agent_id,omitempty" db:"external_agent_id"`
	// Profile fields (Sprint 20 — team directory & assignment rules)
	// Note: AgentType above is the automation category (claude_code/openclaw/etc).
	// Role is the team role (developer/lead/reviewer/etc).
	Role               string          `json:"role" db:"role"`
	ResponsibilityZone string          `json:"responsibility_zone" db:"responsibility_zone"`
	EscalationTo       *json.RawMessage `json:"escalation_to,omitempty" db:"escalation_to"`
	AcceptsFrom        json.RawMessage `json:"accepts_from" db:"accepts_from"`
	MaxConcurrentTasks int             `json:"max_concurrent_tasks" db:"max_concurrent_tasks"`
	WorkingHours       string          `json:"working_hours" db:"working_hours"`
	ProfileDescription string          `json:"profile_description" db:"profile_description"`
	CallbackURL        string          `json:"callback_url" db:"callback_url"`
	CreatedAt           time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at" db:"updated_at"`
}

// DefaultHeartbeatStaleThreshold is the default time after which an agent's heartbeat is considered stale.
const DefaultHeartbeatStaleThreshold = 15 * time.Minute

// IsHeartbeatStale returns true if the agent's last heartbeat is older than the threshold.
func (a *Agent) IsHeartbeatStale() bool {
	if a.LastHeartbeat == nil {
		return false
	}
	return time.Since(*a.LastHeartbeat) > DefaultHeartbeatStaleThreshold
}

// SecondsSinceHeartbeat returns seconds since last heartbeat, or nil if never sent.
func (a *Agent) SecondsSinceHeartbeat() *int {
	if a.LastHeartbeat == nil {
		return nil
	}
	secs := int(time.Since(*a.LastHeartbeat).Seconds())
	return &secs
}

// AgentActivityLog represents a single event in the agent monitoring timeline.
type AgentActivityLog struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	AgentID     uuid.UUID       `json:"agent_id" db:"agent_id"`
	WorkspaceID uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	EventType   string          `json:"event_type" db:"event_type"`
	TaskID      *uuid.UUID      `json:"task_id,omitempty" db:"task_id"`
	Message     string          `json:"message" db:"message"`
	Metadata    json.RawMessage `json:"metadata,omitempty" db:"metadata"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
}
