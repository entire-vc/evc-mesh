// Package actorctx provides utilities for propagating actor identity through Go context.
// It is a leaf package with no internal dependencies, safe to import from both
// middleware and service layers.
package actorctx

import (
	"context"

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
