package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/extensions/uictx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// UIContextSigner is what the handler needs of the instance signing service.
type UIContextSigner interface {
	Issue(ctx context.Context, c uictx.Claims) (string, time.Time, error)
	JWKS(ctx context.Context) ([]byte, error)
}

type extUIMembers interface {
	GetRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error)
}

type extUIUsers interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
}

type extUITasks interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error)
}

type extUIProjects interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
}

// ExtensionUIHandler serves the Mesh side of iframe screens of external
// extensions: the JWKS and the context token (docs/extensions/ui-iframe.md).
type ExtensionUIHandler struct {
	reg      *extensions.Registry
	signer   UIContextSigner
	members  extUIMembers
	users    extUIUsers
	tasks    extUITasks
	projects extUIProjects
	// selfHosts are lower-cased hostnames the Mesh UI is served from (configured URLs).
	selfHosts []string
}

func NewExtensionUIHandler(reg *extensions.Registry, signer UIContextSigner, members extUIMembers, users extUIUsers, tasks extUITasks, projects extUIProjects) *ExtensionUIHandler {
	return &ExtensionUIHandler{reg: reg, signer: signer, members: members, users: users, tasks: tasks, projects: projects}
}

// WithSelfOrigins registers the configured public URLs of this Mesh instance (MESH_BASE_URL, CORS origins).
func (h *ExtensionUIHandler) WithSelfOrigins(origins ...string) *ExtensionUIHandler {
	for _, o := range origins {
		if host := hostnameOf(o); host != "" {
			h.selfHosts = append(h.selfHosts, host)
		}
	}
	return h
}

// hostnameOf returns the lower-cased hostname of a URL, an origin or a bare host[:port] ("" if none).
func hostnameOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "//" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

// servedFromMesh reports whether the iframe service host is a host Mesh itself is reached on. The frame
// runs with allow-scripts + allow-same-origin, so on the Mesh origin it could script the parent page and
// remove its own sandbox. Hostnames are compared (any port or scheme), which is stricter than the origin.
func (h *ExtensionUIHandler) servedFromMesh(frameOrigin string, r *http.Request) bool {
	host := hostnameOf(frameOrigin)
	if host == "" {
		return true
	}
	for _, own := range h.selfHosts {
		if own == host {
			return true
		}
	}
	for _, seen := range []string{r.Host, r.Header.Get("X-Forwarded-Host"), r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if hostnameOf(seen) == host {
			return true
		}
	}
	return false
}

// JWKS handles GET /.well-known/jwks.json. Public by design: it carries public keys only.
func (h *ExtensionUIHandler) JWKS(c echo.Context) error {
	doc, err := h.signer.JWKS(c.Request().Context())
	if err != nil {
		if errors.Is(err, uictx.ErrKeyUnavailable) {
			return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("signing key is not available on this instance"))
		}
		return handleError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return c.Blob(http.StatusOK, "application/jwk-set+json", doc)
}

type uiContextRequest struct {
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	Slot        string     `json:"slot"`
	ProjectID   *uuid.UUID `json:"project_id"`
	TaskID      *uuid.UUID `json:"task_id"`
}

type uiContextResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	// FrameURL is the screen address without the token; the host appends it as ?token=.
	FrameURL string `json:"frame_url"`
	Origin   string `json:"origin"`
	Title    string `json:"title"`
}

// UIContext handles POST /ext/:ext_id/ui-context.
func (h *ExtensionUIHandler) UIContext(c echo.Context) error {
	ctx := c.Request().Context()
	uid, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("a signed-in user is required"))
	}
	var req uiContextRequest
	if err := c.Bind(&req); err != nil || req.WorkspaceID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("workspace_id and slot are required"))
	}
	if !external.IsFrameSlot(req.Slot) {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("slot must be sidebar.item or task.panel"))
	}
	role, err := h.members.GetRole(ctx, req.WorkspaceID, uid)
	if err != nil || role == "" {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("not a member of this workspace"))
	}
	extID := c.Param("ext_id")
	p, found := h.reg.Get(extID)
	if !found || p.Manifest().Runtime != "external" {
		return c.JSON(http.StatusNotFound, apierror.NotFound("extension"))
	}
	res, err := h.reg.Resolve(ctx, req.WorkspaceID, extID)
	if err != nil {
		return handleError(c, err)
	}
	if !res.Enabled {
		return c.JSON(http.StatusConflict, apierror.Conflict("the extension is not enabled in this workspace"))
	}
	frame, err := external.FrameFor(p.Manifest(), res.Settings, req.Slot)
	if err != nil {
		if errors.Is(err, external.ErrNoScreen) {
			return c.JSON(http.StatusNotFound, apierror.NotFound("screen"))
		}
		return c.JSON(http.StatusConflict, apierror.Conflict(err.Error()))
	}
	if h.servedFromMesh(frame.Origin, c.Request()) {
		return c.JSON(http.StatusConflict, apierror.Conflict("the extension service must not be served from the Mesh host"))
	}

	projectID, taskID := req.ProjectID, req.TaskID
	if taskID != nil {
		t, terr := h.tasks.GetByID(ctx, *taskID)
		if terr != nil || t == nil {
			return c.JSON(http.StatusNotFound, apierror.NotFound("task"))
		}
		pr, perr := h.projects.GetByID(ctx, t.ProjectID)
		if perr != nil || pr == nil || pr.WorkspaceID != req.WorkspaceID {
			// Same answer as a missing task: do not confirm that it exists elsewhere.
			return c.JSON(http.StatusNotFound, apierror.NotFound("task"))
		}
		projectID = &t.ProjectID
	} else if projectID != nil {
		pr, perr := h.projects.GetByID(ctx, *projectID)
		if perr != nil || pr == nil || pr.WorkspaceID != req.WorkspaceID {
			return c.JSON(http.StatusNotFound, apierror.NotFound("project"))
		}
	}

	user, err := h.users.GetByID(ctx, uid)
	if err != nil || user == nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("a signed-in user is required"))
	}
	token, exp, err := h.signer.Issue(ctx, uictx.Claims{
		UserID: uid, Email: user.Email, WorkspaceID: req.WorkspaceID,
		ProjectID: projectID, TaskID: taskID, Roles: []string{role}, Audience: extID,
	})
	if err != nil {
		if errors.Is(err, uictx.ErrKeyUnavailable) {
			return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("signing key is not available on this instance"))
		}
		return handleError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, uiContextResponse{Token: token, ExpiresAt: exp, FrameURL: frame.URL, Origin: frame.Origin, Title: frame.Title})
}
