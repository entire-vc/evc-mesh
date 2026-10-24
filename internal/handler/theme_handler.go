package handler

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

var themeAssetName = regexp.MustCompile(`^([0-9a-f]{64})\.(css|js)$`)

// ThemeHandler serves the theme API: what a browser paints (GET /theme), the
// bundle files, the installed themes, and the instance and workspace choice.
type ThemeHandler struct {
	svc        *themes.Service
	admins     InstanceAdminChecker
	repo       *repoinstall.Service
	workspaces repository.WorkspaceRepository
	members    repository.WorkspaceMemberRepository
}

func NewThemeHandler(svc *themes.Service, admins InstanceAdminChecker, workspaces repository.WorkspaceRepository, members repository.WorkspaceMemberRepository) *ThemeHandler {
	return &ThemeHandler{svc: svc, admins: admins, workspaces: workspaces, members: members}
}

// WithRepoInstall enables installing a theme from a repository.
func (h *ThemeHandler) WithRepoInstall(r *repoinstall.Service) *ThemeHandler {
	h.repo = r
	return h
}

// Get handles GET /theme[?workspace=<slug>]. Without a workspace it is public
// (the sign-in screen needs it); with one the caller must be a member.
func (h *ThemeHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()
	var wsID *uuid.UUID
	cache := "public, no-cache"
	if slug := strings.TrimSpace(c.QueryParam("workspace")); slug != "" {
		id, denied, err := h.authorizeMember(c, slug)
		if denied {
			return err
		}
		wsID = &id
		cache = "private, no-cache"
	}
	res, err := h.svc.Resolve(ctx, wsID)
	if err != nil {
		return handleError(c, err)
	}
	etag := `"` + res.Hash + `"`
	hdr := c.Response().Header()
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", cache)
	if etagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return c.JSON(http.StatusOK, res)
}

// Manifest handles GET /theme/manifest.json: the PWA manifest in the colours of
// the instance theme. Public like the sign-in screen; the edge maps
// /manifest.json here. Always complete — mesh-default colours when the
// instance has no theme.
func (h *ThemeHandler) Manifest(c echo.Context) error {
	res, err := h.svc.Resolve(c.Request().Context(), nil)
	if err != nil {
		return handleError(c, err)
	}
	etag := `"` + res.Hash + `"`
	hdr := c.Response().Header()
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "public, no-cache")
	if etagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Blob(http.StatusOK, "application/manifest+json", themes.WebManifest(res.Brand))
}

// authorizeMember resolves a workspace slug for a caller who must belong to it.
// An unknown workspace and a workspace the caller is not in answer the same 404.
func (h *ThemeHandler) authorizeMember(c echo.Context, slug string) (id uuid.UUID, denied bool, err error) {
	ctx := c.Request().Context()
	notFound := func() (uuid.UUID, bool, error) {
		return uuid.Nil, true, c.JSON(http.StatusNotFound, apierror.NotFound("workspace"))
	}
	if middleware.IsAgent(c) {
		agentWS, aerr := middleware.GetAgentAuthWorkspaceID(c)
		if aerr != nil {
			return uuid.Nil, true, c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
		}
		ws, werr := h.workspaces.GetBySlug(ctx, slug)
		if werr != nil || ws == nil || ws.ID != agentWS {
			return notFound()
		}
		return ws.ID, false, nil
	}
	uid, uerr := middleware.GetUserID(c)
	if uerr != nil {
		return uuid.Nil, true, c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required to read a workspace theme"))
	}
	ws, werr := h.workspaces.GetBySlug(ctx, slug)
	if werr != nil {
		var ae *apierror.Error
		if errors.As(werr, &ae) && ae.StatusCode() == http.StatusNotFound {
			return notFound()
		}
		return uuid.Nil, true, handleError(c, werr)
	}
	if ws == nil {
		return notFound()
	}
	m, merr := h.members.GetByWorkspaceAndUser(ctx, ws.ID, uid)
	if merr != nil {
		return uuid.Nil, true, handleError(c, merr)
	}
	if m == nil {
		return notFound()
	}
	return ws.ID, false, nil
}

// Asset handles GET /theme/assets/<sha256>.<css|js>: public, content-addressed,
// so it is cached for good and its ETag is the hash.
func (h *ThemeHandler) Asset(c echo.Context) error {
	m := themeAssetName.FindStringSubmatch(c.Param("file"))
	if m == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("theme asset"))
	}
	etag := `"` + m[1] + `"`
	if etagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		return c.NoContent(http.StatusNotModified)
	}
	data, ctype, err := h.svc.Asset(c.Request().Context(), m[1], m[2])
	if err != nil {
		if errors.Is(err, themes.ErrAssetNotFound) {
			return c.JSON(http.StatusNotFound, apierror.NotFound("theme asset"))
		}
		return handleError(c, err)
	}
	hdr := c.Response().Header()
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	hdr.Set("X-Content-Type-Options", "nosniff")
	return c.Blob(http.StatusOK, ctype, data)
}

// List handles GET /themes: the installed themes with their settings schemas,
// and the built-in ones that can still be installed.
func (h *ThemeHandler) List(c echo.Context) error {
	items, err := h.svc.List(c.Request().Context())
	if err != nil {
		return handleError(c, err)
	}
	have := map[string]bool{}
	for _, t := range items {
		have[t.ID] = true
	}
	available := []string{}
	for _, id := range themes.BuiltinIDs() {
		if !have[id] {
			available = append(available, id)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"default": domain.DefaultThemeID, "themes": items, "builtin_available": available})
}

type themeBody struct {
	ThemeID  *string        `json:"theme_id"`
	Settings map[string]any `json:"settings"`
}

func (h *ThemeHandler) put(c echo.Context, ws *uuid.UUID, actor uuid.UUID) error {
	var body themeBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	sel, err := h.svc.Select(c.Request().Context(), ws, actor, body.ThemeID, body.Settings)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, sel)
}

// workspaceActor is the human behind a workspace theme change. RBAC has already
// passed (PermManageMembers); the id is only recorded.
func workspaceActor(c echo.Context) (uuid.UUID, bool) {
	uid, err := middleware.GetUserID(c)
	return uid, err == nil
}

// GetWorkspace handles GET /workspaces/:ws_id/theme.
func (h *ThemeHandler) GetWorkspace(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	sel, err := h.svc.GetSelection(c.Request().Context(), &ws)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, sel)
}

// PutWorkspace handles PUT /workspaces/:ws_id/theme (PermManageMembers).
func (h *ThemeHandler) PutWorkspace(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	actor, ok := workspaceActor(c)
	if !ok {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("a workspace theme is changed by a signed-in administrator"))
	}
	return h.put(c, &ws, actor)
}

// GetInstance handles GET /instance/theme.
func (h *ThemeHandler) GetInstance(c echo.Context) error {
	if _, denied, err := authorizeInstanceAdmin(c, h.admins); denied {
		return err
	}
	sel, err := h.svc.GetSelection(c.Request().Context(), nil)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, sel)
}

// PutInstance handles PUT /instance/theme.
func (h *ThemeHandler) PutInstance(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	return h.put(c, nil, uid)
}

// Delete handles DELETE /instance/themes/:id.
func (h *ThemeHandler) Delete(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	if err := h.svc.Delete(c.Request().Context(), uid, c.Param("id")); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

type installThemeBody struct {
	Source    string `json:"source"`
	ID        string `json:"id"`
	RepoURL   string `json:"repo_url"`
	Ref       string `json:"ref"`
	AuthToken string `json:"auth_token"`
}

// Install handles POST /instance/themes: {source:"builtin", id} or
// {source:"repo", repo_url, ref, auth_token?}.
func (h *ThemeHandler) Install(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	var b installThemeBody
	if berr := c.Bind(&b); berr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	ctx := c.Request().Context()
	var row *domain.InstalledExtension
	switch b.Source {
	case "builtin":
		row, err = h.svc.InstallBuiltin(ctx, uid, strings.TrimSpace(b.ID))
	case "repo":
		url, ref := strings.TrimSpace(b.RepoURL), strings.TrimSpace(b.Ref)
		if url == "" || ref == "" {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("repo_url and ref are required"))
		}
		if h.repo == nil {
			return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("installing from a repository is not available"))
		}
		row, err = h.repo.InstallTheme(ctx, uid, url, ref, strings.TrimSpace(b.AuthToken))
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(`source must be "builtin" or "repo"`))
	}
	if err != nil {
		return handleError(c, err)
	}
	warnings, werr := h.svc.Warnings(ctx, row.ExtensionID)
	if werr != nil {
		return handleError(c, werr)
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"extension_id": row.ExtensionID, "version": row.Version, "source": row.Source, "trust_level": row.TrustLevel,
		"contrast_warnings": warnings,
	})
}
