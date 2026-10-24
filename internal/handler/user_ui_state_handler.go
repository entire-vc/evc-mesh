package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// maxUIStateBytes caps a stored state document. The Docs tree stores one uuid
// per open node, so 64 KB is thousands of nodes; anything bigger is a bug or an
// attempt to use the table as a file store.
const maxUIStateBytes = 64 * 1024

// docsTreeScopePrefix is the only scope family accepted so far. Adding a screen
// means adding a prefix here together with its access rule in authorizeScope —
// an unknown prefix is refused, never stored.
const docsTreeScopePrefix = "docs.tree:"

// appearanceScope holds the person's own light/dark/system choice. It belongs to
// the caller alone (no project or workspace), so a human session is all it needs.
const appearanceScope = "appearance"

// UserUIStateHandler serves /me/ui-state/:scope — the human caller's own
// remembered interface state (which Docs tree branches are open), synced across
// devices.
//
// Human-only on purpose: an agent key or OAuth token acts on a workspace's
// behalf and has no screen to remember. The rows are keyed on the user id taken
// from the session, never from the request, so there is no parameter through
// which one user can name another's row.
type UserUIStateHandler struct {
	db *sqlx.DB
}

// NewUserUIStateHandler returns a new UserUIStateHandler.
func NewUserUIStateHandler(db *sqlx.DB) *UserUIStateHandler {
	return &UserUIStateHandler{db: db}
}

type uiStateResponse struct {
	State     json.RawMessage `json:"state"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Get handles GET /me/ui-state/:scope. 404 when nothing has been stored yet.
func (h *UserUIStateHandler) Get(c echo.Context) error {
	userID, scope, err := h.authorize(c)
	if err != nil {
		return err
	}

	var state []byte
	var updatedAt time.Time
	err = h.db.QueryRowContext(c.Request().Context(),
		`SELECT state, updated_at FROM user_ui_state WHERE user_id = $1 AND scope = $2`,
		userID, scope,
	).Scan(&state, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.NotFound("ui state")
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, uiStateResponse{State: state, UpdatedAt: updatedAt})
}

// Put handles PUT /me/ui-state/:scope with body {"state": <json object>}.
// Upsert, last write wins.
func (h *UserUIStateHandler) Put(c echo.Context) error {
	userID, scope, err := h.authorize(c)
	if err != nil {
		return err
	}

	if c.Request().ContentLength > maxUIStateBytes {
		return tooLargeUIState(c)
	}
	// Read one byte past the limit so "exactly at the limit" and "over" differ
	// even when the client sent no Content-Length (chunked).
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, maxUIStateBytes+1))
	if err != nil {
		return apierror.BadRequest("could not read request body")
	}
	if len(raw) > maxUIStateBytes {
		return tooLargeUIState(c)
	}

	var req struct {
		State json.RawMessage `json:"state"`
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return apierror.BadRequest("invalid request body")
	}
	state := bytes.TrimSpace(req.State)
	if len(state) == 0 || state[0] != '{' || !json.Valid(state) {
		return apierror.ValidationError(map[string]string{"state": "must be a JSON object"})
	}
	if len(state) > maxUIStateBytes {
		return tooLargeUIState(c)
	}

	var updatedAt time.Time
	err = h.db.QueryRowContext(c.Request().Context(),
		`INSERT INTO user_ui_state (user_id, scope, state, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (user_id, scope)
		 DO UPDATE SET state = EXCLUDED.state, updated_at = now()
		 RETURNING updated_at`,
		userID, scope, state,
	).Scan(&updatedAt)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, uiStateResponse{State: state, UpdatedAt: updatedAt})
}

func tooLargeUIState(c echo.Context) error {
	return c.JSON(http.StatusRequestEntityTooLarge, apierror.BadRequest("ui state must be at most 64 KB"))
}

// authorize resolves the caller and the scope, and decides whether this caller
// may touch it. Order matters: the principal check comes before the scope is
// even parsed, so an agent learns nothing about which scopes exist.
func (h *UserUIStateHandler) authorize(c echo.Context) (uuid.UUID, string, error) {
	if authType, _ := c.Get(middleware.ContextKeyAuthType).(string); authType != middleware.AuthTypeUser {
		return uuid.Nil, "", apierror.Unauthorized("user authentication required")
	}
	userID, ok := currentUserID(c)
	if !ok {
		return uuid.Nil, "", apierror.Unauthorized("authentication required")
	}

	scope := c.Param("scope")
	// Echo may hand the segment over still percent-encoded (docs.tree%3A<id>).
	// The whitelist below is strict enough that decoding twice cannot smuggle
	// anything through.
	if dec, err := url.PathUnescape(scope); err == nil {
		scope = dec
	}
	if err := h.authorizeScope(c.Request().Context(), userID, scope); err != nil {
		return uuid.Nil, "", err
	}
	return userID, scope, nil
}

func (h *UserUIStateHandler) authorizeScope(ctx context.Context, userID uuid.UUID, scope string) error {
	if scope == appearanceScope {
		return nil
	}
	rest, ok := strings.CutPrefix(scope, docsTreeScopePrefix)
	if !ok {
		return apierror.ValidationError(map[string]string{"scope": "unknown scope"})
	}
	projectID, err := uuid.Parse(rest)
	if err != nil {
		return apierror.ValidationError(map[string]string{"scope": "docs.tree:<project_id> expected"})
	}

	// Same rule as middleware.RequireProjectMember for a human: a project member,
	// or an owner/admin of the project's workspace. An unknown project id gets
	// the same 403 as a foreign one, so ids cannot be probed.
	var allowed bool
	err = h.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)
		     OR EXISTS(SELECT 1 FROM projects p
		               JOIN workspace_members wm ON wm.workspace_id = p.workspace_id
		               WHERE p.id = $1 AND wm.user_id = $2 AND wm.role IN ($3, $4))`,
		projectID, userID, domain.RoleOwner, domain.RoleAdmin,
	).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return apierror.Forbidden("not a member of this project")
	}
	return nil
}
