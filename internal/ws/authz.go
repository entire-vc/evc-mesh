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
	// MayAccessProject applies the REST project access rule to the principal: the
	// project must lie in the principal's workspace, and then a human passes as
	// owner/admin of that workspace or with a project_members row, an agent only
	// with a project_members row. Any lookup error is a refusal.
	MayAccessProject(ctx context.Context, p Principal, projectID uuid.UUID) bool
}

// dbAuthorizer is the production Authorizer, reading the same tables and applying
// the same membership rule as the HTTP guard — including the owner fallback for
// workspaces whose owner membership row was never written. Reusing
// middleware.UserIsWorkspaceMember rather than repeating the query is deliberate:
// a WebSocket that disagrees with the REST API about who is a member is a hole
// with extra steps.
type dbAuthorizer struct {
	db      *sqlx.DB
	members middleware.ProjectMembership
}

// NewDBAuthorizer returns an Authorizer backed by the application database.
func NewDBAuthorizer(db *sqlx.DB) Authorizer {
	return &dbAuthorizer{db: db, members: middleware.NewDBProjectMembership(db)}
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

// accessFacts are the lookups the project access rule reads. dbAuthorizer
// answers them from the database; tests answer them from memory and still run
// the production rule (mayAccessProject) on top.
type accessFacts interface {
	ProjectWorkspaceID(ctx context.Context, projectID uuid.UUID) (uuid.UUID, error)
	// UserWorkspaceRole is the user's workspace role, "" for a non-member.
	UserWorkspaceRole(ctx context.Context, wsID, userID uuid.UUID) string
	middleware.ProjectMembership
}

// mayAccessProject is the rule of middleware.RequireProjectMember, asked without
// an Echo context: the same helper decides the human half, so the socket never
// hands out a project the REST API would refuse. Agents never get the
// owner/admin bypass, whatever role their workspace grant carries.
func mayAccessProject(ctx context.Context, f accessFacts, p Principal, projectID uuid.UUID) bool {
	if p.WorkspaceID == uuid.Nil || projectID == uuid.Nil {
		return false
	}
	wsID, err := f.ProjectWorkspaceID(ctx, projectID)
	if err != nil || wsID != p.WorkspaceID {
		return false
	}
	switch {
	case p.AgentID != uuid.Nil:
		ok, err := f.ExistsMember(ctx, projectID, nil, &p.AgentID)
		return err == nil && ok
	case p.UserID != uuid.Nil:
		role := f.UserWorkspaceRole(ctx, p.WorkspaceID, p.UserID)
		if role == "" {
			return false
		}
		return middleware.UserMayAccessProject(ctx, f, projectID, p.UserID, role)
	}
	return false
}

func (a *dbAuthorizer) UserWorkspaceRole(ctx context.Context, wsID, userID uuid.UUID) string {
	return middleware.UserWorkspaceRole(ctx, a.db, wsID, userID)
}

func (a *dbAuthorizer) ExistsMember(ctx context.Context, projectID uuid.UUID, userID, agentID *uuid.UUID) (bool, error) {
	return a.members.ExistsMember(ctx, projectID, userID, agentID)
}

func (a *dbAuthorizer) MayAccessProject(ctx context.Context, p Principal, projectID uuid.UUID) bool {
	if a.db == nil {
		return false
	}
	return mayAccessProject(ctx, a, p, projectID)
}
