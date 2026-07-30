package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// maxTenantBodyPeek bounds how much of a request body this guard will buffer to
// find the tenant in it. Every route it is registered on posts a small JSON
// object; a body larger than this is not one of them, and buffering an unbounded
// upload to look for a workspace_id would be its own denial of service.
const maxTenantBodyPeek = 1 << 20 // 1 MiB

// RequireBodyWorkspace guards the routes that name their tenant in the request
// body instead of the path.
//
// RequireWorkspaceMemberScoped can only check what the router names. A route with
// no workspace-scoping path parameter looks exactly like /auth/me to it — nothing
// to resolve, nothing to check — even when the handler is about to read
// workspace_id straight out of the JSON body and answer with that tenant's data.
// POST /rules/evaluate was that route: it has no path parameter at all, bound
// req.WorkspaceID and returned the matching rules' names and violation messages,
// so any authenticated caller could read another tenant's private rule set by
// pasting their workspace id into the body. No test that reads the router could
// have seen it, because as far as the router is concerned there is no id there.
//
// So this is the guard that reads the body. It buffers the body, pulls
// workspace_id (and project_id, when present) out of it, checks the caller against
// that workspace with the same rule the path-based guard uses, and puts the body
// back for the handler to bind as usual.
//
// It works for an agent key as well as a user JWT, which matters: rbac() does not.
// On an agent key rbac() short-circuits to a static capability map and never looks
// at the target object's workspace, so "the route carries rbac(...)" is not a
// tenancy check.
//
// TestBodyTenantFieldsAreDeclared holds the rest of the class shut by refusing any
// new tenant-shaped field in a request struct that has not been reviewed.
func RequireBodyWorkspace(db *sqlx.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if req.Body == nil {
				return next(c)
			}

			raw, err := io.ReadAll(io.LimitReader(req.Body, maxTenantBodyPeek))
			if err != nil {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
			}
			_ = req.Body.Close()
			// Whatever happens below, the handler must still see the body it was
			// sent — this middleware reads it, it does not consume it.
			req.Body = io.NopCloser(bytes.NewReader(raw))

			var body struct {
				WorkspaceID *uuid.UUID `json:"workspace_id"`
				ProjectID   *uuid.UUID `json:"project_id"`
			}
			if len(bytes.TrimSpace(raw)) > 0 {
				// A body that does not parse is the handler's 400 to give, not a
				// reason to let the request past the guard — but there is also no
				// tenant in it to check, so there is nothing for us to do.
				if jsonErr := json.Unmarshal(raw, &body); jsonErr != nil {
					return next(c)
				}
			}

			wsID, err := bodyTenantWorkspace(c, db, body.WorkspaceID, body.ProjectID)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			if wsID == uuid.Nil {
				// Neither id was supplied. The handler falls back to the caller's
				// own workspace or refuses; either way nothing here names a tenant.
				return next(c)
			}

			if !ActorMayAccessWorkspace(c, db, wsID) {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}

			// The workspace the caller proved access to is the one the rest of the
			// request should run in, so RLS and any downstream check agree with the
			// guard rather than with the body.
			c.Set(ContextKeyWorkspaceID, wsID)
			c.Set(ContextKeyWorkspaceSource, WorkspaceSourceParam)
			return next(c)
		}
	}
}

// bodyTenantWorkspace reduces the tenant ids in a body to the one workspace they
// name. A project_id resolves to its own workspace, and if a workspace_id was sent
// alongside it the two have to agree — the same "every id, not any id" rule the
// path-based resolvers follow, for the same reason.
func bodyTenantWorkspace(c echo.Context, db *sqlx.DB, wsID, projID *uuid.UUID) (uuid.UUID, error) {
	var resolved uuid.UUID
	if wsID != nil && *wsID != uuid.Nil {
		resolved = *wsID
	}
	if projID == nil || *projID == uuid.Nil {
		return resolved, nil
	}

	projWS, ok := queryWorkspace(c.Request().Context(), db,
		`SELECT workspace_id FROM projects WHERE id = $1`, *projID)
	if !ok {
		return uuid.Nil, errTenantNotResolved
	}
	if resolved != uuid.Nil && resolved != projWS {
		return uuid.Nil, errTenantNotResolved
	}
	return projWS, nil
}

// ActorMayAccessWorkspace reports whether the caller — user JWT or agent key — is
// entitled to act inside wsID.
//
// It is the membership rule of RequireWorkspaceMember, in a form that handlers and
// body-reading guards can call. MemoryHandler.workspaceAllowed has been doing
// exactly this since the memories endpoints were found trusting a client-supplied
// workspace_id; writing it once is what keeps the next handler that has to do the
// same from inventing a weaker version of it.
func ActorMayAccessWorkspace(c echo.Context, db *sqlx.DB, wsID uuid.UUID) bool {
	if db == nil || wsID == uuid.Nil {
		return false
	}
	ctx := c.Request().Context()

	if IsAgent(c) {
		agentID, err := GetAgentID(c)
		if err != nil {
			return false
		}
		return AgentIsInWorkspace(ctx, db, wsID, agentID)
	}

	userID, err := GetUserID(c)
	if err != nil {
		return false
	}
	return UserIsWorkspaceMember(ctx, db, wsID, userID)
}

// errTenantNotResolved marks a body that named a tenant this database does not
// have, or named two of them.
var errTenantNotResolved = errors.New("workspace not resolved from request body")
