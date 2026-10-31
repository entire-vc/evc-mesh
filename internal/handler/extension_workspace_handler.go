package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionWorkspaceHandler serves /workspaces/:ws_id/extensions (docs/extensions/api.md).
type ExtensionWorkspaceHandler struct {
	svc *service.ExtensionWorkspaceService
	// reg and scope serve PUT …/project-scope; nil until WithProjectScope.
	reg   *extensions.Registry
	scope repository.ExtensionProjectScopeRepository
}

func NewExtensionWorkspaceHandler(svc *service.ExtensionWorkspaceService) *ExtensionWorkspaceHandler {
	return &ExtensionWorkspaceHandler{svc: svc}
}

// WithProjectScope enables PUT /workspaces/:ws_id/extensions/:ext_id/project-scope.
func (h *ExtensionWorkspaceHandler) WithProjectScope(reg *extensions.Registry, scope repository.ExtensionProjectScopeRepository) *ExtensionWorkspaceHandler {
	h.reg = reg
	h.scope = scope
	return h
}

func extWorkspaceID(c echo.Context) (uuid.UUID, *apierror.Error) {
	id, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return uuid.Nil, apierror.BadRequest("invalid workspace_id")
	}
	return id, nil
}

func (h *ExtensionWorkspaceHandler) fail(c echo.Context, err error) error {
	return handleError(c, err)
}

// List handles GET /workspaces/:ws_id/extensions.
func (h *ExtensionWorkspaceHandler) List(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	items, err := h.svc.List(c.Request().Context(), ws)
	if err != nil {
		return h.fail(c, err)
	}
	return extensionJSON(c, http.StatusOK, map[string]any{"extensions": items})
}

type extensionConnectRequest struct {
	Settings map[string]any `json:"settings"`
	Consent  struct {
		Capabilities []string `json:"capabilities"`
	} `json:"consent"`
}

// Connect handles POST /workspaces/:ws_id/extensions/:ext_id/connect.
func (h *ExtensionWorkspaceHandler) Connect(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	var req extensionConnectRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	var by *uuid.UUID
	if uid, ok := currentUserID(c); ok {
		by = &uid
	}
	v, err := h.svc.Connect(c.Request().Context(), ws, c.Param("ext_id"), by, req.Settings, req.Consent.Capabilities)
	if err != nil {
		return h.fail(c, err)
	}
	return extensionJSON(c, http.StatusCreated, v)
}

// PatchSettings handles PATCH /workspaces/:ws_id/extensions/:ext_id/settings.
func (h *ExtensionWorkspaceHandler) PatchSettings(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	var req struct {
		Settings map[string]any `json:"settings"`
	}
	if err := c.Bind(&req); err != nil || req.Settings == nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("settings object is required"))
	}
	v, err := h.svc.PatchSettings(c.Request().Context(), ws, c.Param("ext_id"), req.Settings)
	if err != nil {
		return h.fail(c, err)
	}
	return extensionJSON(c, http.StatusOK, v)
}

// Enable handles POST /workspaces/:ws_id/extensions/:ext_id/enable.
func (h *ExtensionWorkspaceHandler) Enable(c echo.Context) error {
	return h.setEnabled(c, true)
}

// Disable handles POST /workspaces/:ws_id/extensions/:ext_id/disable.
func (h *ExtensionWorkspaceHandler) Disable(c echo.Context) error {
	return h.setEnabled(c, false)
}

func (h *ExtensionWorkspaceHandler) setEnabled(c echo.Context, enabled bool) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
		}
	}
	// Switching the Runner appends a catalog revision authored by this user.
	ctx := c.Request().Context()
	if user, err := mw.GetUserID(c); err == nil {
		ctx = postgres.WithRuntimeAuthor(ctx, user)
	}
	v, err := h.svc.SetEnabled(ctx, ws, c.Param("ext_id"), enabled, req.Confirm)
	if err != nil {
		return h.fail(c, err)
	}
	return extensionJSON(c, http.StatusOK, v)
}

// Test handles POST /workspaces/:ws_id/extensions/:ext_id/test.
func (h *ExtensionWorkspaceHandler) Test(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	res, err := h.svc.Test(c.Request().Context(), ws, c.Param("ext_id"))
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// Disconnect handles DELETE /workspaces/:ws_id/extensions/:ext_id?data=keep|delete.
func (h *ExtensionWorkspaceHandler) Disconnect(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	purge := false
	switch c.QueryParam("data") {
	case "", "keep":
	case "delete":
		purge = true
	default:
		e := apierror.ValidationError(map[string]string{"data": "must be keep or delete"})
		return c.JSON(e.StatusCode(), e)
	}
	if err := h.svc.Disconnect(c.Request().Context(), ws, c.Param("ext_id"), purge); err != nil {
		return h.fail(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// SetProjectScope handles PUT /workspaces/:ws_id/extensions/:ext_id/project-scope
// with {"project_scope": "workspace"|"bound"}. "bound" limits the extension's
// API calls and events to the projects with an enabled binding for it (PUT
// /projects/:proj_id/extensions/:ext_id); it needs an extension with a project
// level, since only such an extension can be bound to a project at all.
func (h *ExtensionWorkspaceHandler) SetProjectScope(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	if h.scope == nil || h.reg == nil {
		return c.JSON(http.StatusNotImplemented, apierror.InternalError("project scope is not configured"))
	}
	var req struct {
		ProjectScope string `json:"project_scope"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	extID := h.reg.Canonical(c.Param("ext_id"))
	p, found := h.reg.Get(extID)
	if !found {
		return c.JSON(http.StatusNotFound, apierror.NotFound("extension"))
	}
	if req.ProjectScope == domain.ExtensionProjectScopeBound && !p.Manifest().Levels.Project {
		e := apierror.ValidationError(map[string]string{"project_scope": "the extension has no project level, so no project can be bound to it"})
		return c.JSON(e.StatusCode(), e)
	}
	if err := h.scope.SetProjectScope(c.Request().Context(), ws, extID, req.ProjectScope); err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"extension_id": extID, "project_scope": req.ProjectScope})
}
