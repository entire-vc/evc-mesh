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
	Name                string          `json:"name" db:"name"`
	Slug                string          `json:"slug" db:"slug"`
	AgentType           AgentType       `json:"agent_type" db:"agent_type"`
	APIKeyHash          string          `json:"-" db:"api_key_hash"`
	APIKeyPrefix        string          `json:"api_key_prefix" db:"api_key_prefix"`
	Capabilities        json.RawMessage `json:"capabilities" db:"capabilities"`
	Status              AgentStatus     `json:"status" db:"status"`
	LastHeartbeat       *time.Time      `json:"last_heartbeat" db:"last_heartbeat"`
	CurrentTaskID       *uuid.UUID      `json:"current_task_id" db:"current_task_id"`
	Settings            json.RawMessage `json:"settings" db:"settings"`
	TotalTasksCompleted int             `json:"total_tasks_completed" db:"total_tasks_completed"`
	TotalErrors         int             `json:"total_errors" db:"total_errors"`
	CreatedAt           time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at" db:"updated_at"`
}
