package sdk

import (
	"context"
	"fmt"
	"net/url"
)

// Event represents a message published to the Mesh event bus.
type Event struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	ProjectID   string         `json:"project_id"`
	TaskID      *string        `json:"task_id,omitempty"`
	AgentID     *string        `json:"agent_id,omitempty"`
	EventType   string         `json:"event_type"` // summary|status_change|context_update|error|dependency_resolved|custom
	Subject     string         `json:"subject"`
	Payload     map[string]any `json:"payload,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	TTL         string         `json:"ttl,omitempty"`
	CreatedAt   string         `json:"created_at"`
	ExpiresAt   *string        `json:"expires_at,omitempty"`
}

// PublishEventInput is the request body for POST /projects/:id/events.
type PublishEventInput struct {
	EventType  string         `json:"event_type"`           // summary|status_change|context_update|error|dependency_resolved|custom
	Subject    string         `json:"subject"`              // required human-readable subject line
	Payload    map[string]any `json:"payload,omitempty"`    // arbitrary JSON payload
	TaskID     *string        `json:"task_id,omitempty"`   // optional task context
	Tags       []string       `json:"tags,omitempty"`       // searchable tags
	TTLSeconds int            `json:"ttl_seconds,omitempty"` // 0 = no expiry
}

// eventPage mirrors pagination.Page[Event] from the server.
type eventPage struct {
	Items      []Event `json:"items"`
	TotalCount int     `json:"total_count"`
	Page       int     `json:"page"`
	PageSize   int     `json:"page_size"`
	TotalPages int     `json:"total_pages"`
	HasMore    bool    `json:"has_more"`
}

// PublishEvent publishes a structured event to the event bus for a project.
// Other agents subscribed to the project can read these events via GetContext.
func (c *Client) PublishEvent(ctx context.Context, projectID string, input PublishEventInput) (*Event, error) {
	var event Event
	if err := c.post(ctx, "/api/v1/projects/"+projectID+"/events", input, &event); err != nil {
		return nil, fmt.Errorf("PublishEvent: %w", err)
	}
	return &event, nil
}

// GetContextOption configures the event context query.
type GetContextOption func(q url.Values)

// WithEventType filters events by type (summary|status_change|context_update|error|dependency_resolved|custom).
func WithEventType(t string) GetContextOption {
	return func(q url.Values) { q.Set("event_type", t) }
}

// WithEventTaskID filters events that reference a specific task.
func WithEventTaskID(taskID string) GetContextOption {
	return func(q url.Values) { q.Set("task_id", taskID) }
}

// WithEventAgentID filters events published by a specific agent.
func WithEventAgentID(agentID string) GetContextOption {
	return func(q url.Values) { q.Set("agent_id", agentID) }
}

// WithEventTag filters events that have the specified tag.
func WithEventTag(tag string) GetContextOption {
	return func(q url.Values) { q.Set("tags", tag) }
}

// WithEventPage sets the page number (1-based) for paginated results.
func WithEventPage(page int) GetContextOption {
	return func(q url.Values) { q.Set("page", fmt.Sprintf("%d", page)) }
}

// WithEventPageSize sets the number of events per page.
func WithEventPageSize(size int) GetContextOption {
	return func(q url.Values) { q.Set("page_size", fmt.Sprintf("%d", size)) }
}

// GetContext retrieves events from the event bus for the given project.
// Use option functions (WithEventType, WithEventTaskID, etc.) to filter results.
func (c *Client) GetContext(ctx context.Context, projectID string, opts ...GetContextOption) ([]Event, error) {
	q := url.Values{}
	for _, opt := range opts {
		opt(q)
	}

	path := "/api/v1/projects/" + projectID + "/events"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var page eventPage
	if err := c.get(ctx, path, &page); err != nil {
		return nil, fmt.Errorf("GetContext: %w", err)
	}
	return page.Items, nil
}

// GetEvent returns a single event by ID.
func (c *Client) GetEvent(ctx context.Context, eventID string) (*Event, error) {
	var event Event
	if err := c.get(ctx, "/api/v1/events/"+eventID, &event); err != nil {
		return nil, fmt.Errorf("GetEvent: %w", err)
	}
	return &event, nil
}
