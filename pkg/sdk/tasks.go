package sdk

import (
	"context"
	"fmt"
	"net/url"
)

// Task represents a Mesh task returned by the API.
type Task struct {
	ID             string         `json:"id"`
	ProjectID      string         `json:"project_id"`
	StatusID       string         `json:"status_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description,omitempty"`
	Priority       string         `json:"priority"`
	AssigneeID     *string        `json:"assignee_id,omitempty"`
	AssigneeType   string         `json:"assignee_type,omitempty"`
	ParentTaskID   *string        `json:"parent_task_id,omitempty"`
	Position       float64        `json:"position"`
	DueDate        *string        `json:"due_date,omitempty"`
	EstimatedHours *float64       `json:"estimated_hours,omitempty"`
	Labels         []string       `json:"labels,omitempty"`
	CustomFields   map[string]any `json:"custom_fields,omitempty"`
	CreatedBy      string         `json:"created_by"`
	CreatedByType  string         `json:"created_by_type"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
	CompletedAt    *string        `json:"completed_at,omitempty"`
}

// CreateTaskInput is the request body for creating a new task.
type CreateTaskInput struct {
	Title          string         `json:"title"`
	Description    string         `json:"description,omitempty"`
	Priority       string         `json:"priority,omitempty"` // urgent|high|medium|low|none
	StatusID       string         `json:"status_id,omitempty"`
	AssigneeID     string         `json:"assignee_id,omitempty"`
	AssigneeType   string         `json:"assignee_type,omitempty"` // agent|user — omit for auto-assign
	Labels         []string       `json:"labels,omitempty"`
	DueDate        *string        `json:"due_date,omitempty"`
	EstimatedHours *float64       `json:"estimated_hours,omitempty"`
	CustomFields   map[string]any `json:"custom_fields,omitempty"`
}

// UpdateTaskInput is the partial update body for a task.
// Only non-nil pointer fields are applied.
type UpdateTaskInput struct {
	Title          *string        `json:"title,omitempty"`
	Description    *string        `json:"description,omitempty"`
	Priority       *string        `json:"priority,omitempty"`
	AssigneeID     *string        `json:"assignee_id,omitempty"`
	AssigneeType   *string        `json:"assignee_type,omitempty"`
	DueDate        *string        `json:"due_date,omitempty"`
	EstimatedHours *float64       `json:"estimated_hours,omitempty"`
	Labels         *[]string      `json:"labels,omitempty"`
	CustomFields   map[string]any `json:"custom_fields,omitempty"`
}

// ListOption configures task list queries.
type ListOption func(q url.Values)

// WithStatus filters tasks by status UUID.
func WithStatus(statusID string) ListOption {
	return func(q url.Values) { q.Set("status", statusID) }
}

// WithPriority filters tasks by priority (urgent|high|medium|low|none).
func WithPriority(priority string) ListOption {
	return func(q url.Values) { q.Set("priority", priority) }
}

// WithAssigneeType filters tasks by assignee type (user|agent|unassigned).
func WithAssigneeType(t string) ListOption {
	return func(q url.Values) { q.Set("assignee_type", t) }
}

// WithLabel filters tasks that have the given label.
func WithLabel(label string) ListOption {
	return func(q url.Values) { q.Set("labels", label) }
}

// WithSearch filters tasks by a search string.
func WithSearch(s string) ListOption {
	return func(q url.Values) { q.Set("search", s) }
}

// WithPage sets the page number (1-based).
func WithPage(page int) ListOption {
	return func(q url.Values) { q.Set("page", fmt.Sprintf("%d", page)) }
}

// WithPageSize sets the number of items per page.
func WithPageSize(size int) ListOption {
	return func(q url.Values) { q.Set("page_size", fmt.Sprintf("%d", size)) }
}

// taskPage mirrors the server-side pagination.Page[Task] response.
type taskPage struct {
	Items      []Task `json:"items"`
	TotalCount int    `json:"total_count"`
	Page       int    `json:"page"`
	PageSize   int    `json:"page_size"`
	TotalPages int    `json:"total_pages"`
	HasMore    bool   `json:"has_more"`
}

// ListTasks returns tasks in a project with optional filters.
func (c *Client) ListTasks(ctx context.Context, projectID string, opts ...ListOption) ([]Task, error) {
	q := url.Values{}
	for _, opt := range opts {
		opt(q)
	}
	path := "/api/v1/projects/" + projectID + "/tasks"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var page taskPage
	if err := c.get(ctx, path, &page); err != nil {
		return nil, fmt.Errorf("ListTasks: %w", err)
	}
	return page.Items, nil
}

// GetTask returns a single task by ID.
func (c *Client) GetTask(ctx context.Context, taskID string) (*Task, error) {
	var task Task
	if err := c.get(ctx, "/api/v1/tasks/"+taskID, &task); err != nil {
		return nil, fmt.Errorf("GetTask: %w", err)
	}
	return &task, nil
}

// CreateTask creates a new task in the given project.
func (c *Client) CreateTask(ctx context.Context, projectID string, input CreateTaskInput) (*Task, error) {
	var task Task
	if err := c.post(ctx, "/api/v1/projects/"+projectID+"/tasks", input, &task); err != nil {
		return nil, fmt.Errorf("CreateTask: %w", err)
	}
	return &task, nil
}

// UpdateTask partially updates a task. Only fields set on UpdateTaskInput are changed.
func (c *Client) UpdateTask(ctx context.Context, taskID string, input UpdateTaskInput) (*Task, error) {
	var task Task
	if err := c.patch(ctx, "/api/v1/tasks/"+taskID, input, &task); err != nil {
		return nil, fmt.Errorf("UpdateTask: %w", err)
	}
	return &task, nil
}

// moveTaskBody is the request body for POST /tasks/:id/move.
type moveTaskBody struct {
	StatusID *string  `json:"status_id,omitempty"`
	Position *float64 `json:"position,omitempty"`
}

// MoveTask changes a task's status. Returns the updated task by fetching it after the move.
func (c *Client) MoveTask(ctx context.Context, taskID, statusID string) (*Task, error) {
	body := moveTaskBody{StatusID: &statusID}
	if err := c.post(ctx, "/api/v1/tasks/"+taskID+"/move", body, nil); err != nil {
		return nil, fmt.Errorf("MoveTask: %w", err)
	}
	return c.GetTask(ctx, taskID)
}

// assignTaskBody is the request body for POST /tasks/:id/assign.
type assignTaskBody struct {
	AssigneeID   *string `json:"assignee_id,omitempty"`
	AssigneeType string  `json:"assignee_type"`
}

// AssignTask assigns a task to a user or agent.
// assigneeType must be "user", "agent", or "unassigned".
// Pass an empty assigneeID and assigneeType="unassigned" to unassign.
func (c *Client) AssignTask(ctx context.Context, taskID, assigneeID, assigneeType string) (*Task, error) {
	body := assignTaskBody{AssigneeType: assigneeType}
	if assigneeID != "" {
		body.AssigneeID = &assigneeID
	}

	var task Task
	if err := c.post(ctx, "/api/v1/tasks/"+taskID+"/assign", body, &task); err != nil {
		return nil, fmt.Errorf("AssignTask: %w", err)
	}
	return &task, nil
}

// myTasksResponse mirrors GET /agents/me/tasks response envelope.
type myTasksResponse struct {
	Tasks []Task `json:"tasks"`
	Count int    `json:"count"`
}

// GetMyTasks returns tasks assigned to the calling agent.
func (c *Client) GetMyTasks(ctx context.Context) ([]Task, error) {
	var resp myTasksResponse
	if err := c.get(ctx, "/api/v1/agents/me/tasks", &resp); err != nil {
		return nil, fmt.Errorf("GetMyTasks: %w", err)
	}
	return resp.Tasks, nil
}

// DeleteTask deletes a task by ID.
func (c *Client) DeleteTask(ctx context.Context, taskID string) error {
	if err := c.delete(ctx, "/api/v1/tasks/"+taskID); err != nil {
		return fmt.Errorf("DeleteTask: %w", err)
	}
	return nil
}

// CreateSubtaskInput is the request body for creating a subtask.
type CreateSubtaskInput struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Priority    string `json:"priority,omitempty"`
}

// CreateSubtask creates a subtask under the given parent task.
func (c *Client) CreateSubtask(ctx context.Context, parentTaskID string, input CreateSubtaskInput) (*Task, error) {
	var task Task
	if err := c.post(ctx, "/api/v1/tasks/"+parentTaskID+"/subtasks", input, &task); err != nil {
		return nil, fmt.Errorf("CreateSubtask: %w", err)
	}
	return &task, nil
}

// ListSubtasks returns the subtasks of a given parent task.
func (c *Client) ListSubtasks(ctx context.Context, parentTaskID string) ([]Task, error) {
	var tasks []Task
	if err := c.get(ctx, "/api/v1/tasks/"+parentTaskID+"/subtasks", &tasks); err != nil {
		return nil, fmt.Errorf("ListSubtasks: %w", err)
	}
	return tasks, nil
}
