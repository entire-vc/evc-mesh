package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// NotificationPreference stores per-actor notification subscription settings.
type NotificationPreference struct {
	ID          uuid.UUID       `db:"id"           json:"id"`
	WorkspaceID uuid.UUID       `db:"workspace_id" json:"workspace_id"`
	UserID      *uuid.UUID      `db:"user_id"      json:"user_id"`
	AgentID     *uuid.UUID      `db:"agent_id"     json:"agent_id"`
	Channel     string          `db:"channel"      json:"channel"`
	Events      pq.StringArray  `db:"events"       json:"events"`
	IsEnabled   bool            `db:"is_enabled"   json:"is_enabled"`
	Config      json.RawMessage `db:"config"      json:"config"`
	CreatedAt   time.Time       `db:"created_at"   json:"created_at"`
	UpdatedAt   time.Time       `db:"updated_at"   json:"updated_at"`
}

// Notification is a persisted in-app notification for a user.
type Notification struct {
	ID          uuid.UUID       `db:"id"           json:"id"`
	WorkspaceID uuid.UUID       `db:"workspace_id" json:"workspace_id"`
	UserID      *uuid.UUID      `db:"user_id"      json:"user_id"`
	EventType   string          `db:"event_type"   json:"event_type"`
	Title       string          `db:"title"        json:"title"`
	Body        string          `db:"body"         json:"body"`
	Metadata    json.RawMessage `db:"metadata"    json:"metadata"`
	IsRead      bool            `db:"is_read"      json:"is_read"`
	CreatedAt   time.Time       `db:"created_at"   json:"created_at"`
}

// NotificationEvent carries the data used to build and dispatch a notification.
type NotificationEvent struct {
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	TaskID      *uuid.UUID `json:"task_id,omitempty"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	EventType   string     `json:"event_type"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	// TargetUserID, when set, restricts delivery to this one user's own preference
	// row instead of fanning out to every subscribed workspace member. Use for
	// events that are inherently about one specific person (e.g. "you were made
	// reviewer"), where broadcasting to the whole workspace would be wrong.
	TargetUserID *uuid.UUID `json:"target_user_id,omitempty"`
	// Labels carries the task's labels/tags at the moment the event was
	// raised, for channels (Telegram) whose message format includes them.
	// Not persisted; purely a dispatch-time hint from the caller, which
	// already has the task object in hand.
	Labels   []string       `json:"labels,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// PushSubscription represents a browser Web Push subscription for a user.
type PushSubscription struct {
	ID         uuid.UUID `db:"id"           json:"id"`
	UserID     uuid.UUID `db:"user_id"      json:"user_id"`
	Endpoint   string    `db:"endpoint"     json:"endpoint"`
	P256DHKey  string    `db:"p256dh_key"   json:"p256dh_key"`
	AuthKey    string    `db:"auth_key"     json:"auth_key"`
	UserAgent  string    `db:"user_agent"   json:"user_agent"`
	CreatedAt  time.Time `db:"created_at"   json:"created_at"`
	LastSeenAt time.Time `db:"last_seen_at" json:"last_seen_at"`
}

// PushPayload is the JSON body delivered to the browser via Web Push.
type PushPayload struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	Tag       string `json:"tag"`
	Icon      string `json:"icon"`
	EventType string `json:"event_type"`
}
