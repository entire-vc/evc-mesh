package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// NotificationPreference stores per-actor notification subscription settings.
type NotificationPreference struct {
	ID          uuid.UUID      `db:"id"           json:"id"`
	WorkspaceID uuid.UUID      `db:"workspace_id" json:"workspace_id"`
	UserID      *uuid.UUID     `db:"user_id"      json:"user_id"`
	AgentID     *uuid.UUID     `db:"agent_id"     json:"agent_id"`
	Channel     string         `db:"channel"      json:"channel"`
	Events      pq.StringArray `db:"events"       json:"events"`
	IsEnabled   bool           `db:"is_enabled"   json:"is_enabled"`
	Config      json.RawMessage `db:"config"      json:"config"`
	CreatedAt   time.Time      `db:"created_at"   json:"created_at"`
	UpdatedAt   time.Time      `db:"updated_at"   json:"updated_at"`
}

// Notification is a persisted in-app notification for a user.
type Notification struct {
	ID          uuid.UUID      `db:"id"           json:"id"`
	WorkspaceID uuid.UUID      `db:"workspace_id" json:"workspace_id"`
	UserID      *uuid.UUID     `db:"user_id"      json:"user_id"`
	EventType   string         `db:"event_type"   json:"event_type"`
	Title       string         `db:"title"        json:"title"`
	Body        string         `db:"body"         json:"body"`
	Metadata    json.RawMessage `db:"metadata"    json:"metadata"`
	IsRead      bool           `db:"is_read"      json:"is_read"`
	CreatedAt   time.Time      `db:"created_at"   json:"created_at"`
}

// NotificationEvent carries the data used to build and dispatch a notification.
type NotificationEvent struct {
	WorkspaceID uuid.UUID      `json:"workspace_id"`
	TaskID      *uuid.UUID     `json:"task_id,omitempty"`
	ProjectID   *uuid.UUID     `json:"project_id,omitempty"`
	EventType   string         `json:"event_type"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}
