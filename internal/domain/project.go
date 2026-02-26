package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// DefaultAssigneeType determines who new tasks are assigned to by default.
type DefaultAssigneeType string

const (
	DefaultAssigneeUser  DefaultAssigneeType = "user"
	DefaultAssigneeAgent DefaultAssigneeType = "agent"
	DefaultAssigneeNone  DefaultAssigneeType = "none"
)

// ProjectRoleAdmin, ProjectRoleMember, ProjectRoleViewer are the valid project-level roles.
const (
	ProjectRoleAdmin  = "admin"
	ProjectRoleMember = "member"
	ProjectRoleViewer = "viewer"
)

// ProjectMember represents a user's explicit membership in a project with a specific role.
type ProjectMember struct {
	ID        uuid.UUID `json:"id" db:"id"`
	ProjectID uuid.UUID `json:"project_id" db:"project_id"`
	UserID    uuid.UUID `json:"user_id" db:"user_id"`
	Role      string    `json:"role" db:"role"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// ProjectMemberWithUser embeds ProjectMember with the associated user's brief info.
type ProjectMemberWithUser struct {
	ProjectMember
	User UserBrief `json:"user"`
}

// Project belongs to a Workspace and contains tasks, statuses, and custom fields.
type Project struct {
	ID                  uuid.UUID           `json:"id" db:"id"`
	WorkspaceID         uuid.UUID           `json:"workspace_id" db:"workspace_id"`
	Name                string              `json:"name" db:"name"`
	Description         string              `json:"description" db:"description"`
	Slug                string              `json:"slug" db:"slug"`
	Icon                string              `json:"icon" db:"icon"`
	Settings            json.RawMessage     `json:"settings" db:"settings"`
	DefaultAssigneeType DefaultAssigneeType `json:"default_assignee_type" db:"default_assignee_type"`
	IsArchived          bool                `json:"is_archived" db:"is_archived"`
	CreatedAt           time.Time           `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at" db:"updated_at"`
}
