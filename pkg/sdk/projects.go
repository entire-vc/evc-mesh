package sdk

import (
	"context"
	"fmt"
)

// Project represents a Mesh project.
type Project struct {
	ID                  string `json:"id"`
	WorkspaceID         string `json:"workspace_id"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	Slug                string `json:"slug"`
	Icon                string `json:"icon,omitempty"`
	DefaultAssigneeType string `json:"default_assignee_type"`
	IsArchived          bool   `json:"is_archived"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

// projectPage mirrors pagination.Page[Project] from the server.
type projectPage struct {
	Items      []Project `json:"items"`
	TotalCount int       `json:"total_count"`
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
	TotalPages int       `json:"total_pages"`
	HasMore    bool      `json:"has_more"`
}

// ListProjects returns all projects in the agent's workspace.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	path := "/api/v1/workspaces/" + c.wsID + "/projects"
	var page projectPage
	if err := c.get(ctx, path, &page); err != nil {
		return nil, fmt.Errorf("ListProjects: %w", err)
	}
	return page.Items, nil
}

// GetProject returns a project by ID.
func (c *Client) GetProject(ctx context.Context, projectID string) (*Project, error) {
	var proj Project
	if err := c.get(ctx, "/api/v1/projects/"+projectID, &proj); err != nil {
		return nil, fmt.Errorf("GetProject: %w", err)
	}
	return &proj, nil
}

// TaskStatus represents a project-level task status.
type TaskStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	Category string `json:"category"` // backlog|todo|in_progress|review|done|cancelled
	Position int    `json:"position"`
}

// ListStatuses returns the configured statuses for a project.
func (c *Client) ListStatuses(ctx context.Context, projectID string) ([]TaskStatus, error) {
	var statuses []TaskStatus
	if err := c.get(ctx, "/api/v1/projects/"+projectID+"/statuses", &statuses); err != nil {
		return nil, fmt.Errorf("ListStatuses: %w", err)
	}
	return statuses, nil
}

// CustomFieldDefinition describes a custom field defined on a project.
type CustomFieldDefinition struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	FieldType string `json:"field_type"` // text|number|date|select|multiselect|checkbox|url|email|phone|person|rating|progress
	Required  bool   `json:"required"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListCustomFields returns the custom field definitions for a project.
func (c *Client) ListCustomFields(ctx context.Context, projectID string) ([]CustomFieldDefinition, error) {
	var fields []CustomFieldDefinition
	if err := c.get(ctx, "/api/v1/projects/"+projectID+"/custom-fields", &fields); err != nil {
		return nil, fmt.Errorf("ListCustomFields: %w", err)
	}
	return fields, nil
}
