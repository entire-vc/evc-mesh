// Package actorctx provides utilities for propagating actor identity through Go context.
// It is a leaf package with no internal dependencies, safe to import from both
// middleware and service layers.
package actorctx

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// contextKeyType is a private type for Go context keys to avoid collisions with other packages.
type contextKeyType string

const (
	keyActorID   contextKeyType = "mesh_actor_id"
	keyActorType contextKeyType = "mesh_actor_type"
	keyActorName contextKeyType = "mesh_actor_name"
)

// WithActor returns a new context with actor identity attached.
func WithActor(ctx context.Context, actorID uuid.UUID, actorType domain.ActorType) context.Context {
	ctx = context.WithValue(ctx, keyActorID, actorID)
	ctx = context.WithValue(ctx, keyActorType, actorType)
	return ctx
}

// WithActorName returns a new context with the actor's display name attached.
func WithActorName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, keyActorName, name)
}

// FromContext extracts actor info from a Go context set by WithActor.
// Returns uuid.Nil and empty ActorType if not set.
func FromContext(ctx context.Context) (uuid.UUID, domain.ActorType) {
	id, _ := ctx.Value(keyActorID).(uuid.UUID)
	at, _ := ctx.Value(keyActorType).(domain.ActorType)
	return id, at
}

// NameFromContext extracts the actor's display name from context.
// Returns empty string if not set.
func NameFromContext(ctx context.Context) string {
	name, _ := ctx.Value(keyActorName).(string)
	return name
}

const keyChatAttribution contextKeyType = "mesh_chat_attribution"

// ChatAttribution says on whose behalf an agent acted: set only for a request
// authenticated with a chat-session token, from the verified token record —
// never from request fields. It carries IDs only, no chat text and no secret.
type ChatAttribution struct {
	InitiatedByUserID uuid.UUID
	ChatThreadID      uuid.UUID
	ChatSessionID     uuid.UUID
	ExecutionEpoch    int64
}

// WithChatAttribution returns a context carrying the chat attribution.
func WithChatAttribution(ctx context.Context, a ChatAttribution) context.Context {
	return context.WithValue(ctx, keyChatAttribution, a)
}

// ChatAttributionFromContext returns the chat attribution and true when the
// request acted for a user through a chat session.
func ChatAttributionFromContext(ctx context.Context) (ChatAttribution, bool) {
	a, ok := ctx.Value(keyChatAttribution).(ChatAttribution)
	return a, ok && a.InitiatedByUserID != uuid.Nil
}

// AnnotateChanges returns changes with the chat attribution merged in under
// initiated_by_user_id / chat_thread_id / chat_session_id / execution_epoch.
// Without an attribution in ctx it returns changes untouched. Keys already
// present are overwritten: attribution comes from the verified token, so a
// handler-supplied value must not be able to shadow it.
func AnnotateChanges(ctx context.Context, changes map[string]any) map[string]any {
	a, ok := ChatAttributionFromContext(ctx)
	if !ok {
		return changes
	}
	out := make(map[string]any, len(changes))
	for k, v := range changes {
		out[k] = v
	}
	out["initiated_by_user_id"] = a.InitiatedByUserID
	out["chat_thread_id"] = a.ChatThreadID
	out["chat_session_id"] = a.ChatSessionID
	out["execution_epoch"] = a.ExecutionEpoch
	return out
}

// AnnotateRawChanges is AnnotateChanges for an already-encoded changes object.
// Anything that is not a JSON object (null, array, invalid) is returned as is
// rather than rewritten.
func AnnotateRawChanges(ctx context.Context, raw json.RawMessage) json.RawMessage {
	if _, ok := ChatAttributionFromContext(ctx); !ok {
		return raw
	}
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return raw
		}
	}
	out, err := json.Marshal(AnnotateChanges(ctx, m))
	if err != nil {
		return raw
	}
	return out
}
