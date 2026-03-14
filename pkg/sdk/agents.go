package sdk

import (
	"context"
	"encoding/json"
	"fmt"
)

// Agent represents a registered AI agent in the workspace.
type Agent struct {
	ID                  string          `json:"id"`
	WorkspaceID         string          `json:"workspace_id"`
	ParentAgentID       *string         `json:"parent_agent_id,omitempty"`
	SupervisorUserID    *string         `json:"supervisor_user_id,omitempty"`
	Name                string          `json:"name"`
	Slug                string          `json:"slug"`
	AgentType           string          `json:"agent_type"` // claude_code|openclaw|cline|aider|custom
	APIKeyPrefix        string          `json:"api_key_prefix"`
	Status              string          `json:"status"` // online|offline|busy|error
	LastHeartbeat       *string         `json:"last_heartbeat,omitempty"`
	HeartbeatStatus     string          `json:"heartbeat_status,omitempty"`
	HeartbeatMessage    string          `json:"heartbeat_message,omitempty"`
	HeartbeatMetadata   json.RawMessage `json:"heartbeat_metadata,omitempty"`
	CurrentTaskID       *string         `json:"current_task_id,omitempty"`
	TotalTasksCompleted int             `json:"total_tasks_completed"`
	TotalErrors         int             `json:"total_errors"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

// HeartbeatOptions provides optional parameters for HeartbeatWithOptions.
type HeartbeatOptions struct {
	Status        string         `json:"status,omitempty"`
	Message       string         `json:"message,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CurrentTaskID string         `json:"current_task_id,omitempty"`
}

// Me returns the current agent's profile (identified by the agent key used in New()).
func (c *Client) Me(ctx context.Context) (*Agent, error) {
	var agent Agent
	if err := c.get(ctx, "/api/v1/agents/me", &agent); err != nil {
		return nil, fmt.Errorf("Me: %w", err)
	}
	return &agent, nil
}

// Heartbeat signals that the current agent is alive. The server updates
// last_heartbeat and marks the agent as online.
func (c *Client) Heartbeat(ctx context.Context) error {
	return c.HeartbeatWithOptions(ctx, nil)
}

// HeartbeatWithOptions sends a heartbeat with optional status, message, and metadata.
func (c *Client) HeartbeatWithOptions(ctx context.Context, opts *HeartbeatOptions) error {
	body := map[string]any{}
	if opts != nil {
		if opts.Status != "" {
			body["status"] = opts.Status
		}
		if opts.Message != "" {
			body["message"] = opts.Message
		}
		if opts.Metadata != nil {
			body["metadata"] = opts.Metadata
		}
		if opts.CurrentTaskID != "" {
			body["current_task_id"] = opts.CurrentTaskID
		}
	}
	if err := c.post(ctx, "/api/v1/agents/heartbeat", body, nil); err != nil {
		return fmt.Errorf("HeartbeatWithOptions: %w", err)
	}
	return nil
}

// GetAgent returns any agent by ID.
func (c *Client) GetAgent(ctx context.Context, agentID string) (*Agent, error) {
	var agent Agent
	if err := c.get(ctx, "/api/v1/agents/"+agentID, &agent); err != nil {
		return nil, fmt.Errorf("GetAgent: %w", err)
	}
	return &agent, nil
}

// RegisterSubAgentInput is the request body for creating a sub-agent.
type RegisterSubAgentInput struct {
	Name          string         `json:"name"`
	AgentType     string         `json:"agent_type,omitempty"` // claude_code|openclaw|cline|aider|custom
	ParentAgentID string         `json:"parent_agent_id,omitempty"`
	Capabilities  map[string]any `json:"capabilities,omitempty"`
}

// RegisterSubAgentOutput extends Agent with the plain-text API key returned once at creation.
type RegisterSubAgentOutput struct {
	Agent
	APIKey string `json:"api_key,omitempty"`
}

// RegisterSubAgent creates a child agent under the calling agent.
// The returned RegisterSubAgentOutput includes the plain-text api_key (shown only once).
func (c *Client) RegisterSubAgent(ctx context.Context, input RegisterSubAgentInput) (*RegisterSubAgentOutput, error) {
	if input.ParentAgentID == "" {
		input.ParentAgentID = c.agentID
	}

	var out RegisterSubAgentOutput
	if err := c.post(ctx, "/api/v1/workspaces/"+c.wsID+"/agents", input, &out); err != nil {
		return nil, fmt.Errorf("RegisterSubAgent: %w", err)
	}
	return &out, nil
}

// subAgentsResponse mirrors GET /agents/:id/sub-agents.
type subAgentsResponse struct {
	Agents []Agent `json:"agents"`
	Count  int     `json:"count"`
}

// ListSubAgents returns the direct child agents of the given agent.
// Set recursive=true to retrieve all descendants (up to 10 levels deep).
func (c *Client) ListSubAgents(ctx context.Context, agentID string, recursive bool) ([]Agent, error) {
	path := "/api/v1/agents/" + agentID + "/sub-agents"
	if recursive {
		path += "?recursive=true"
	}

	var resp subAgentsResponse
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("ListSubAgents: %w", err)
	}
	return resp.Agents, nil
}

// UpdateAgentInput is the partial-update body for PATCH /agents/:id.
type UpdateAgentInput struct {
	Name         *string        `json:"name,omitempty"`
	AgentType    *string        `json:"agent_type,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
}

// UpdateAgent partially updates an agent by ID.
func (c *Client) UpdateAgent(ctx context.Context, agentID string, input UpdateAgentInput) (*Agent, error) {
	var agent Agent
	if err := c.patch(ctx, "/api/v1/agents/"+agentID, input, &agent); err != nil {
		return nil, fmt.Errorf("UpdateAgent: %w", err)
	}
	return &agent, nil
}

// DeleteAgent removes an agent by ID.
func (c *Client) DeleteAgent(ctx context.Context, agentID string) error {
	if err := c.delete(ctx, "/api/v1/agents/"+agentID); err != nil {
		return fmt.Errorf("DeleteAgent: %w", err)
	}
	return nil
}
