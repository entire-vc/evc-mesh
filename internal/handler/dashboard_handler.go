package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions/dashboards"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DashboardHandler serves the Dashboards extension (PRD Dashboards §6.2) under
// /workspaces/:ws_id/dashboards, /dashboard-widgets and /dashboard-templates.
//
// Human sessions only (the routes carry RequireUserAuth): a dashboard is a
// person's screen; an agent key or a connector has none. Tenancy is the
// workspace guard's (ws_id, and dashboard_id is checked to belong to it);
// who may read, edit, manage or share a dashboard is the service's.
type DashboardHandler struct {
	svc *dashboards.Service
}

// NewDashboardHandler returns a new DashboardHandler.
func NewDashboardHandler(svc *dashboards.Service) *DashboardHandler {
	return &DashboardHandler{svc: svc}
}

// ThemeDashboardSource adapts the theme service to dashboards.ThemeSource.
type ThemeDashboardSource struct {
	Themes *themes.Service
}

// DashboardTheme implements dashboards.ThemeSource.
func (t ThemeDashboardSource) DashboardTheme(ctx context.Context, workspaceID uuid.UUID) (dashboards.Theme, error) {
	if t.Themes == nil {
		return dashboards.Theme{}, nil
	}
	c, err := t.Themes.Dashboard(ctx, workspaceID)
	if err != nil {
		return dashboards.Theme{}, err
	}
	return dashboards.Theme{ID: c.ThemeID, Widgets: c.Widgets, OwnWidgets: c.WidgetSlots}, nil
}

func (h *DashboardHandler) actor(c echo.Context) (dashboards.Actor, error) {
	userID, err := middleware.GetUserID(c)
	if err != nil || middleware.IsAgent(c) {
		return dashboards.Actor{}, apierror.Unauthorized("User authentication required")
	}
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return dashboards.Actor{}, apierror.BadRequest("invalid workspace id")
	}
	a, err := h.svc.ResolveActor(c.Request().Context(), wsID, userID)
	if err != nil {
		return dashboards.Actor{}, toAPIError(err)
	}
	return a, nil
}

// toAPIError renders a service refusal in the API error shape; stale_layout
// carries the dashboard as it is now in context.dashboard.
func toAPIError(err error) error {
	var e *dashboards.Error
	if !errors.As(err, &e) {
		return err
	}
	out := &apierror.Error{Code: e.Status, ErrorCode: e.Code, Message: e.Message, Validation: e.Fields}
	if e.Current != nil {
		out.Context = map[string]any{"dashboard": e.Current}
	}
	return out
}

func bindJSON(c echo.Context, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		return apierror.BadRequest("invalid request body")
	}
	return nil
}

// List handles GET /workspaces/:ws_id/dashboards[?include_hidden=true].
func (h *DashboardHandler) List(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	list, err := h.svc.List(c.Request().Context(), a, c.QueryParam("include_hidden") == "true")
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"dashboards": list})
}

// Get handles GET /workspaces/:ws_id/dashboards/:dashboard_id.
func (h *DashboardHandler) Get(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Get(c.Request().Context(), a, c.Param("dashboard_id"))
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusOK, d)
}

// Create handles POST /workspaces/:ws_id/dashboards.
func (h *DashboardHandler) Create(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	var in dashboards.CreateInput
	err = bindJSON(c, &in)
	if err != nil {
		return err
	}
	d, err := h.svc.Create(c.Request().Context(), a, in)
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusCreated, d)
}

// Patch handles PATCH /workspaces/:ws_id/dashboards/:dashboard_id. 204 when
// the change took the dashboard out of the caller's sight (an admin making
// someone's shared dashboard personal again).
func (h *DashboardHandler) Patch(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	var in dashboards.PatchInput
	err = bindJSON(c, &in)
	if err != nil {
		return err
	}
	d, err := h.svc.Patch(c.Request().Context(), a, c.Param("dashboard_id"), in)
	if err != nil {
		return toAPIError(err)
	}
	if d == nil {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, d)
}

// Sharing handles PATCH /workspaces/:ws_id/dashboards/:dashboard_id/sharing
// (§6.2): scope, members_can_edit and default_placement, manage right only.
func (h *DashboardHandler) Sharing(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	var in dashboards.SharingInput
	err = bindJSON(c, &in)
	if err != nil {
		return err
	}
	d, err := h.svc.Sharing(c.Request().Context(), a, c.Param("dashboard_id"), in)
	if err != nil {
		return toAPIError(err)
	}
	if d == nil {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, d)
}

// Delete handles DELETE /workspaces/:ws_id/dashboards/:dashboard_id.
func (h *DashboardHandler) Delete(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.Request().Context(), a, c.Param("dashboard_id")); err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": true})
}

// Duplicate handles POST /workspaces/:ws_id/dashboards/:dashboard_id/duplicate
// and its §6.2 alias /copy; an optional placement picks the menu group.
// The optional body is {"name", "placement"}.
func (h *DashboardHandler) Duplicate(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	var in struct {
		Name      *string `json:"name"`
		Placement string  `json:"placement"`
	}
	if c.Request().ContentLength != 0 {
		err = bindJSON(c, &in)
		if err != nil {
			return err
		}
	}
	suffix := dashboards.CopySuffixEN
	if strings.HasPrefix(strings.ToLower(c.Request().Header.Get("Accept-Language")), "ru") {
		suffix = dashboards.CopySuffixRU
	}
	d, err := h.svc.Copy(c.Request().Context(), a, c.Param("dashboard_id"), in.Name, in.Placement, suffix)
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusCreated, d)
}

// Reset handles POST /workspaces/:ws_id/dashboards/:dashboard_id/reset.
func (h *DashboardHandler) Reset(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Reset(c.Request().Context(), a, c.Param("dashboard_id"))
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusOK, d)
}

// PutLayout handles PUT /workspaces/:ws_id/dashboards/:dashboard_id/layout
// with {"version", "widgets"}.
func (h *DashboardHandler) PutLayout(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	var in dashboards.LayoutInput
	err = bindJSON(c, &in)
	if err != nil {
		return err
	}
	d, err := h.svc.PutLayout(c.Request().Context(), a, c.Param("dashboard_id"), in)
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusOK, d)
}

// SetHidden handles PUT /workspaces/:ws_id/dashboards/:dashboard_id/hidden
// with {"hidden": bool}.
func (h *DashboardHandler) SetHidden(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	var in struct {
		Hidden *bool `json:"hidden"`
	}
	err = bindJSON(c, &in)
	if err != nil {
		return err
	}
	if in.Hidden == nil {
		return apierror.ValidationError(map[string]string{"hidden": "required"})
	}
	d, err := h.svc.SetHidden(c.Request().Context(), a, c.Param("dashboard_id"), *in.Hidden)
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusOK, d)
}

// Order handles PUT /workspaces/:ws_id/dashboards/order with {"top", "bottom"}.
func (h *DashboardHandler) Order(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	var in dashboards.OrderInput
	err = bindJSON(c, &in)
	if err != nil {
		return err
	}
	list, err := h.svc.Order(c.Request().Context(), a, in)
	if err != nil {
		return toAPIError(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"dashboards": list})
}

// Widgets handles GET /workspaces/:ws_id/dashboard-widgets: the catalog of
// widget types, with an ETag so a client revalidates it for free.
func (h *DashboardHandler) Widgets(c echo.Context) error {
	a, err := h.actor(c)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"widgets": h.svc.Catalog(c.Request().Context(), a)})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set("Cache-Control", "private, no-cache")
	if match := c.Request().Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return c.JSONBlob(http.StatusOK, body)
}

// Templates handles GET /workspaces/:ws_id/dashboard-templates.
func (h *DashboardHandler) Templates(c echo.Context) error {
	if _, err := h.actor(c); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"templates": dashboards.Templates()})
}
