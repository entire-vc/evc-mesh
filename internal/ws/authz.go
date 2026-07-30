package ws

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/middleware"
)

// ErrWorkspaceNotFound is returned by Authorizer when a slug names no live
// workspace. Callers must answer it exactly as they answer "not a member",
// otherwise the handshake becomes a slug oracle — and slugs are ws-<8 hex>,
// short enough to be worth guessing and visible in every /w/<slug>/... URL.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// Authorizer answers the tenancy questions the WebSocket endpoint has to ask.
// The HTTP API asks them through Echo middleware; this connection is upgraded
// before any of that runs, so it asks them directly.
type Authorizer interface {
	// WorkspaceIDBySlug resolves a workspace slug, or ErrWorkspaceNotFound.
	WorkspaceIDBySlug(ctx context.Context, slug string) (uuid.UUID, error)
	// UserIsWorkspaceMember reports whether the user may act inside the workspace.
	UserIsWorkspaceMember(ctx context.Context, wsID, userID uuid.UUID) bool
	// ProjectWorkspaceID returns the workspace a project belongs to.
	ProjectWorkspaceID(ctx context.Context, projectID uuid.UUID) (uuid.UUID, error)
}

// dbAuthorizer is the production Authorizer, reading the same tables and applying
// the same membership rule as the HTTP guard — including the owner fallback for
// workspaces whose owner membership row was never written. Reusing
// middleware.UserIsWorkspaceMember rather than repeating the query is deliberate:
// a WebSocket that disagrees with the REST API about who is a member is a hole
// with extra steps.
type dbAuthorizer struct {
	db *sqlx.DB
}

// NewDBAuthorizer returns an Authorizer backed by the application database.
func NewDBAuthorizer(db *sqlx.DB) Authorizer {
	return &dbAuthorizer{db: db}
}

func (a *dbAuthorizer) WorkspaceIDBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	if a.db == nil {
		return uuid.Nil, ErrWorkspaceNotFound
	}
	var id uuid.UUID
	if err := a.db.QueryRowContext(ctx,
		"SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL",
		slug,
	).Scan(&id); err != nil {
		return uuid.Nil, ErrWorkspaceNotFound
	}
	return id, nil
}

func (a *dbAuthorizer) UserIsWorkspaceMember(ctx context.Context, wsID, userID uuid.UUID) bool {
	return middleware.UserIsWorkspaceMember(ctx, a.db, wsID, userID)
}

func (a *dbAuthorizer) ProjectWorkspaceID(ctx context.Context, projectID uuid.UUID) (uuid.UUID, error) {
	if a.db == nil {
		return uuid.Nil, ErrWorkspaceNotFound
	}
	var wsID uuid.UUID
	if err := a.db.QueryRowContext(ctx,
		"SELECT workspace_id FROM projects WHERE id = $1 AND deleted_at IS NULL",
		projectID,
	).Scan(&wsID); err != nil {
		return uuid.Nil, ErrWorkspaceNotFound
	}
	return wsID, nil
}
