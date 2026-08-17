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
	// RelevantUserIDs names the people this event is actually about, for events
	// that are not addressed to one person (TargetUserID) but are not the whole
	// workspace's business either: a comment concerns the task's assignee,
	// reviewer and creator, plus the person being replied to — not every
	// colleague who happens to subscribe to comment.created.
	//
	// It is TargetUserID generalised from one recipient to a set, and every
	// channel honours it in exactly the same place TargetUserID is honoured.
	// Delivery is not a per-channel opinion about who deserves to know: a
	// recipient set that is right for email is right for the in-app bell too,
	// and the alternative — one channel quietly keeping a wider audience than
	// the others — is the kind of difference nobody discovers until it leaks.
	//
	// Nil means "no relevance information available", which is not the same as
	// "nobody is relevant": channels treat nil as the old broadcast behaviour and
	// only narrow delivery when the caller actually supplied a set. That
	// distinction is what keeps a producer that has not been taught to fill this
	// in from silently going quiet, and it is why deliberately workspace-wide
	// events (task.status_changed) can simply leave it unset.
	//
	// Advisory, and never widening: it can only remove recipients that the
	// membership, subscription and TargetUserID checks already allowed.
	RelevantUserIDs []uuid.UUID `json:"relevant_user_ids,omitempty"`
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
