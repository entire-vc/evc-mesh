package sdk

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Memory represents a persistent knowledge entry stored in the Mesh memory layer.
type Memory struct {
	ID            string   `json:"id"`
	WorkspaceID   string   `json:"workspace_id"`
	ProjectID     *string  `json:"project_id,omitempty"`
	AgentID       *string  `json:"agent_id,omitempty"`
	Key           string   `json:"key"`
	Content       string   `json:"content"`
	Scope         string   `json:"scope"` // workspace|project|agent
	Tags          []string `json:"tags,omitempty"`
	SourceType    string   `json:"source_type"` // agent|human|system
	SourceEventID *string  `json:"source_event_id,omitempty"`
	Relevance     float64  `json:"relevance"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	ExpiresAt     *string  `json:"expires_at,omitempty"`
}

// ScoredMemory is a Memory with a search relevance score.
type ScoredMemory struct {
	Memory
	Score float64 `json:"score"`
}

// RememberInput is the request body for creating/updating a memory.
type RememberInput struct {
	WorkspaceID string   `json:"workspace_id"`
	ProjectID   *string  `json:"project_id,omitempty"`
	Key         string   `json:"key"`
	Content     string   `json:"content"`
	Scope       string   `json:"scope,omitempty"` // default: project
	Tags        []string `json:"tags,omitempty"`
	SourceType  string   `json:"source_type,omitempty"` // default: agent
}

// RememberResult is the response from creating/updating a memory.
type RememberResult struct {
	Memory  Memory `json:"memory"`
	Outcome string `json:"outcome"` // "created" or "updated"
}

// ProjectKnowledge is the response from GetProjectKnowledge.
type ProjectKnowledge struct {
	WorkspaceMemories []Memory `json:"workspace_memories"`
	ProjectMemories   []Memory `json:"project_memories"`
	TotalCount        int      `json:"total_count"`
}

// Remember creates or updates a memory entry (UPSERT by key).
func (c *Client) Remember(ctx context.Context, input RememberInput) (*RememberResult, error) {
	var result RememberResult
	if err := c.post(ctx, "/api/v1/memories", input, &result); err != nil {
		return nil, fmt.Errorf("Remember: %w", err)
	}
	return &result, nil
}

// RecallOption configures the memory search query.
type RecallOption func(q url.Values)

// WithRecallScope filters by scope (workspace|project|agent).
func WithRecallScope(scope string) RecallOption {
	return func(q url.Values) { q.Set("scope", scope) }
}

// WithRecallProjectID filters to a specific project.
func WithRecallProjectID(id string) RecallOption {
	return func(q url.Values) { q.Set("project_id", id) }
}

// WithRecallTags filters by tags.
func WithRecallTags(tags ...string) RecallOption {
	return func(q url.Values) { q.Set("tags", strings.Join(tags, ",")) }
}

// WithRecallLimit sets the max results (default 10).
func WithRecallLimit(limit int) RecallOption {
	return func(q url.Values) { q.Set("limit", strconv.Itoa(limit)) }
}

// recallResult mirrors the search response.
type recallResult struct {
	Items []ScoredMemory `json:"items"`
}

// Recall searches the memory layer using full-text search.
func (c *Client) Recall(ctx context.Context, query string, opts ...RecallOption) ([]ScoredMemory, error) {
	q := url.Values{}
	q.Set("q", query)
	for _, opt := range opts {
		opt(q)
	}

	var result recallResult
	if err := c.get(ctx, "/api/v1/memories/search?"+q.Encode(), &result); err != nil {
		return nil, fmt.Errorf("Recall: %w", err)
	}
	return result.Items, nil
}

// GetProjectKnowledge returns all accumulated knowledge for a project.
func (c *Client) GetProjectKnowledge(ctx context.Context, projectID string) (*ProjectKnowledge, error) {
	var result ProjectKnowledge
	if err := c.get(ctx, "/api/v1/projects/"+projectID+"/knowledge", &result); err != nil {
		return nil, fmt.Errorf("GetProjectKnowledge: %w", err)
	}
	return &result, nil
}

// ForgetMemory deletes a memory entry by ID.
func (c *Client) ForgetMemory(ctx context.Context, memoryID string) error {
	if err := c.delete(ctx, "/api/v1/memories/"+memoryID); err != nil {
		return fmt.Errorf("ForgetMemory: %w", err)
	}
	return nil
}
