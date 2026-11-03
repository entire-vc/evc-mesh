package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Actions POST /ext/me/authorize answers for.
const (
	ExtAuthorizeRead   = "read"   // see the workspace / project / task
	ExtAuthorizeWrite  = "write"  // change tasks, comments, docs in it
	ExtAuthorizeManage = "manage" // administer it (members, settings)
)

// Reasons in the answer. A user id that does not exist and one that is not in
// the extension's workspace get the same reason, so the endpoint cannot be used
// to find out which user ids exist.
const (
	extAuthzNotMember        = "not_workspace_member"
	extAuthzUserDisabled     = "user_disabled"
	extAuthzNotProjectMember = "not_project_member"
	extAuthzRole             = "insufficient_role"
	extAuthzWorkspaceRole    = "workspace_role"
	extAuthzProjectRole      = "project_role"
)

// ExtensionAuthorizeHandler serves POST /ext/me/authorize: an external service
// built on an extension asks whether one of the workspace's users may do an
// action, and gets the answer from the database as it is now — membership, role,
// a disabled account — not from whatever token the user once showed that service.
type ExtensionAuthorizeHandler struct {
	db    *sqlx.DB
	scope repository.ExtensionProjectScopeRepository
	svc   service.ExtensionActorService
}

// NewExtensionAuthorizeHandler creates a new ExtensionAuthorizeHandler. scope
// narrows a project-bound connection to its bound projects: the project is in the
// body, so ExtensionProjectScopeGuard leaves this check to the handler. svc may
// be nil (no violation is recorded).
func NewExtensionAuthorizeHandler(db *sqlx.DB, scope repository.ExtensionProjectScopeRepository, svc service.ExtensionActorService) *ExtensionAuthorizeHandler {
	return &ExtensionAuthorizeHandler{db: db, scope: scope, svc: svc}
}

type extAuthorizeRequest struct {
	UserID      uuid.UUID  `json:"user_id"`
	Action      string     `json:"action"`
	WorkspaceID *uuid.UUID `json:"workspace_id,omitempty"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	TaskID      *uuid.UUID `json:"task_id,omitempty"`
}

type extAuthorizeResponse struct {
	Allowed     bool       `json:"allowed"`
	Reason      string     `json:"reason"`
	UserID      uuid.UUID  `json:"user_id"`
	Action      string     `json:"action"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	CheckedAt   time.Time  `json:"checked_at"`
}

// Authorize handles POST /ext/me/authorize. Only an extension actor with the
// members:authorize capability reaches it (ExtensionCapabilityGuard). The
// workspace is always the extension's own: a workspace, project or task outside
// it is 404, the same as one that does not exist.
func (h *ExtensionAuthorizeHandler) Authorize(c echo.Context) error {
	p := middleware.GetExtensionPrincipal(c)
	if p == nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("only an extension actor can call this endpoint"))
	}

	var req extAuthorizeRequest
	dec := json.NewDecoder(c.Request().Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body: "+err.Error()))
	}
	// Exactly one JSON object: a second value or trailing bytes are not ignored.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body: trailing data after the JSON object"))
	}
	if req.UserID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("user_id is required"))
	}
	switch req.Action {
	case ExtAuthorizeRead, ExtAuthorizeWrite, ExtAuthorizeManage:
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("action must be one of read, write, manage"))
	}
	if req.WorkspaceID != nil && *req.WorkspaceID != p.WorkspaceID {
		return c.JSON(http.StatusNotFound, apierror.NotFound("workspace"))
	}

	projectID, err := h.resolveProject(c, p.WorkspaceID, req.ProjectID, req.TaskID)
	if err != nil {
		return err
	}

	// A project-bound connection may ask only about its bound projects; the
	// answer for another one is the guard's own 403, before any user is looked at.
	if projectID != nil && p.ProjectBound {
		ok, serr := h.scope.AllowsProject(c.Request().Context(), p.ConnectionID, *projectID)
		if serr != nil {
			c.Logger().Errorf("ext authorize: project scope: %v", serr)
			return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to check the extension project scope"))
		}
		if !ok {
			if h.svc != nil {
				h.svc.RecordViolation(c.Request().Context(), p.ConnectionID, "POST /api/v1/ext/me/authorize addresses project "+projectID.String()+" which is not bound to the extension")
			}
			return c.JSON(http.StatusForbidden, apierror.Forbidden("this project is not available to the extension"))
		}
	}

	resp := extAuthorizeResponse{
		UserID:      req.UserID,
		Action:      req.Action,
		WorkspaceID: p.WorkspaceID,
		ProjectID:   projectID,
		CheckedAt:   time.Now().UTC(),
	}
	resp.Allowed, resp.Reason, err = h.decide(c, p.WorkspaceID, projectID, req.UserID, req.Action)
	if err != nil {
		c.Logger().Errorf("ext authorize: %v", err)
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("authorization check failed"))
	}
	return c.JSON(http.StatusOK, resp)
}

// resolveProject returns the project the question is about (nil for a
// workspace-level question). A task names its project; a project_id given next
// to a task must be that task's project. Anything deleted or outside the
// workspace is answered 404 here and the returned error is the written response.
func (h *ExtensionAuthorizeHandler) resolveProject(c echo.Context, wsID uuid.UUID, projID, taskID *uuid.UUID) (*uuid.UUID, error) {
	ctx := c.Request().Context()
	if taskID != nil {
		var taskProject uuid.UUID
		err := h.db.QueryRowxContext(ctx, `
			SELECT t.project_id FROM tasks t JOIN projects p ON p.id = t.project_id
			WHERE t.id = $1 AND t.deleted_at IS NULL AND p.workspace_id = $2 AND p.deleted_at IS NULL`,
			*taskID, wsID).Scan(&taskProject)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && projID != nil && *projID != taskProject) {
			return nil, c.JSON(http.StatusNotFound, apierror.NotFound("task"))
		}
		if err != nil {
			c.Logger().Errorf("ext authorize: task lookup: %v", err)
			return nil, c.JSON(http.StatusInternalServerError, apierror.InternalError("authorization check failed"))
		}
		return &taskProject, nil
	}
	if projID == nil {
		return nil, nil
	}
	var exists bool
	if err := h.db.QueryRowxContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL)`,
		*projID, wsID).Scan(&exists); err != nil {
		c.Logger().Errorf("ext authorize: project lookup: %v", err)
		return nil, c.JSON(http.StatusInternalServerError, apierror.InternalError("authorization check failed"))
	}
	if !exists {
		return nil, c.JSON(http.StatusNotFound, apierror.NotFound("project"))
	}
	return projID, nil
}

// decide answers from the current rows: the user's workspace membership and
// account state, then — for a project — the workspace owner/admin bypass or the
// user's project role, the same rule RequireProjectMember applies to the user's
// own requests.
func (h *ExtensionAuthorizeHandler) decide(c echo.Context, wsID uuid.UUID, projID *uuid.UUID, userID uuid.UUID, action string) (allowed bool, reason string, err error) {
	ctx := c.Request().Context()
	var wsRole string
	var active bool
	err = h.db.QueryRowxContext(ctx, `
		SELECT m.role::text, u.is_active FROM workspace_members m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.user_id = $2`, wsID, userID).Scan(&wsRole, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, extAuthzNotMember, nil
	}
	if err != nil {
		return false, "", err
	}
	if !active {
		return false, extAuthzUserDisabled, nil
	}

	wsAdmin := wsRole == domain.RoleOwner || wsRole == domain.RoleAdmin
	if projID == nil || wsAdmin {
		return roleAllows(wsRole, action), reasonFor(roleAllows(wsRole, action), extAuthzWorkspaceRole), nil
	}

	projRole, member, err := middleware.UserProjectRole(ctx, h.db, *projID, userID)
	if err != nil {
		return false, "", err
	}
	if !member {
		return false, extAuthzNotProjectMember, nil
	}
	// A workspace viewer stays read-only whatever its project row says.
	allowed = roleAllows(projRole, action) && (wsRole != domain.RoleViewer || action == ExtAuthorizeRead)
	return allowed, reasonFor(allowed, extAuthzProjectRole), nil
}

// roleAllows maps a workspace or project role onto the three actions. Workspace
// owner and project admin both reach "manage"; there is no project owner role.
func roleAllows(role, action string) bool {
	switch action {
	case ExtAuthorizeRead:
		return role != ""
	case ExtAuthorizeWrite:
		return role == domain.RoleOwner || role == domain.RoleAdmin || role == domain.RoleMember
	case ExtAuthorizeManage:
		return role == domain.RoleOwner || role == domain.RoleAdmin
	}
	return false
}

func reasonFor(allowed bool, by string) string {
	if allowed {
		return by
	}
	return extAuthzRole
}
