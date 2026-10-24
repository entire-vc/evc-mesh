package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
)

// themeRoles stands in for workspace membership and instance administration:
// user id → role, per workspace.
type themeRoles struct {
	workspace *domain.Workspace
	roles     map[uuid.UUID]string
	admins    map[uuid.UUID]bool
}

type themeMembers struct {
	repository.WorkspaceMemberRepository
	*themeRoles
}

type themeWorkspaces struct {
	repository.WorkspaceRepository
	*themeRoles
}

func (r *themeMembers) GetRole(_ context.Context, ws, user uuid.UUID) (string, error) {
	if role, ok := r.roles[user]; ok && ws == r.workspace.ID {
		return role, nil
	}
	return "", errors.New("not a member")
}

func (r *themeMembers) GetByWorkspaceAndUser(_ context.Context, ws, user uuid.UUID) (*domain.WorkspaceMember, error) {
	if role, ok := r.roles[user]; ok && ws == r.workspace.ID {
		return &domain.WorkspaceMember{WorkspaceID: ws, UserID: user, Role: role}, nil
	}
	return nil, nil
}

func (r *themeWorkspaces) GetBySlug(_ context.Context, slug string) (*domain.Workspace, error) {
	if slug == r.workspace.Slug {
		return r.workspace, nil
	}
	return nil, nil
}

func (r *themeRoles) IsInstanceAdmin(_ context.Context, u uuid.UUID) (bool, error) {
	return r.admins[u], nil
}

type themeFixture struct {
	e                        *echo.Echo
	h                        *ThemeHandler
	svc                      *themes.Service
	inst                     *themeStore
	store                    *themestest.Store
	ws                       *domain.Workspace
	owner, member, instAdmin uuid.UUID
	stranger                 uuid.UUID
}

// newThemeFixture wires the handler on the same routes, with the same
// middleware, as cmd/api/main.go; only the authentication step is replaced by
// reading X-Test-User / X-Test-Agent.
func newThemeFixture(t *testing.T) *themeFixture {
	t.Helper()
	f := &themeFixture{
		ws:    &domain.Workspace{ID: uuid.New(), Slug: "acme"},
		owner: uuid.New(), member: uuid.New(), instAdmin: uuid.New(), stranger: uuid.New(),
		inst: &themeStore{repoStore: newRepoStore()}, store: themestest.NewStore(),
	}
	roles := &themeRoles{workspace: f.ws, roles: map[uuid.UUID]string{f.owner: domain.RoleOwner, f.member: domain.RoleMember, f.instAdmin: domain.RoleAdmin}, admins: map[uuid.UUID]bool{f.instAdmin: true}}
	th := themestest.NewThemes()
	th.OnDelete = func(id string) bool {
		f.inst.mu.Lock()
		defer f.inst.mu.Unlock()
		_, ok := f.inst.rows[id]
		delete(f.inst.rows, id)
		return ok
	}
	th.OnInstall = func(e *domain.InstalledExtension) error { return f.inst.UpsertBuiltin(context.Background(), e) }
	f.svc = &themes.Service{Installed: f.inst, Themes: th, Store: f.store}
	h := NewThemeHandler(f.svc, roles, &themeWorkspaces{themeRoles: roles}, &themeMembers{themeRoles: roles})
	h.WithRepoInstall(&repoinstall.Service{
		Client: &repoinstall.Client{HTTP: http.DefaultClient, AllowInsecure: true},
		Repo:   f.inst, Installed: f.inst, Policy: f.inst, Themes: f.svc,
	})

	f.h = h
	e := echo.New()
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		_ = c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	// authenticate mimics DualAuth/OptionalAuth: it only sets the context.
	authenticate := func(required bool) echo.MiddlewareFunc {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				if u := c.Request().Header.Get("X-Test-User"); u != "" {
					id := uuid.MustParse(u)
					c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeUser)
					c.Set(middleware.ContextKeyUserID, id)
				} else if c.Request().Header.Get("X-Test-Agent") != "" {
					c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeAgent)
					c.Set(middleware.ContextKeyAgentAuthWorkspaceID, f.ws.ID)
					c.Set(middleware.ContextKeyWorkspaceID, f.ws.ID)
				} else if required {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "auth"})
				}
				return next(c)
			}
		}
	}
	// WorkspaceRLS puts the path's workspace into the context before rbac().
	wsContext := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if id, err := uuid.Parse(c.Param("ws_id")); err == nil && !middleware.IsAgent(c) {
				c.Set(middleware.ContextKeyWorkspaceID, id)
			}
			return next(c)
		}
	}
	rbac := middleware.RequirePermission(middleware.PermManageMembers, &themeMembers{themeRoles: roles})
	e.GET("/api/v1/theme", h.Get, authenticate(false))
	e.GET("/api/v1/theme/assets/:file", h.Asset)
	e.GET("/api/v1/theme/manifest.json", h.Manifest)
	api := e.Group("/api/v1", authenticate(true))
	api.GET("/themes", h.List)
	api.GET("/workspaces/:ws_id/theme", h.GetWorkspace, wsContext, rbac)
	api.PUT("/workspaces/:ws_id/theme", h.PutWorkspace, wsContext, rbac)
	api.GET("/instance/theme", h.GetInstance, middleware.RequireUserAuth())
	api.PUT("/instance/theme", h.PutInstance, middleware.RequireUserAuth())
	api.POST("/instance/themes", h.Install, middleware.RequireUserAuth())
	api.DELETE("/instance/themes/:id", h.Delete, middleware.RequireUserAuth())
	f.e = e
	return f
}

func (f *themeFixture) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

func as(u uuid.UUID) map[string]string { return map[string]string{"X-Test-User": u.String()} }

func (f *themeFixture) wsPath() string { return "/api/v1/workspaces/" + f.ws.ID.String() + "/theme" }

func (f *themeFixture) addTheme(m themestest.Manifest) {
	raw := m.JSON()
	id := m.ID
	if id == "" {
		id = "graphite"
	}
	f.inst.rows[id] = &domain.InstalledExtension{ExtensionID: id, Version: "1.0.0", Manifest: raw, Source: domain.ExtensionSourceRepo, TrustLevel: domain.ExtensionTrustCustom}
}

func TestThemeAPI_WorkspacePutNeedsAdmin(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{})
	body := `{"theme_id":"graphite"}`

	require.Equal(t, http.StatusForbidden, f.do(http.MethodPut, f.wsPath(), body, as(f.member)).Code, "a plain member")
	require.Equal(t, http.StatusForbidden, f.do(http.MethodPut, f.wsPath(), body, as(f.stranger)).Code, "not in the workspace")
	require.Equal(t, http.StatusForbidden, f.do(http.MethodPut, f.wsPath(), body, map[string]string{"X-Test-Agent": "1"}).Code, "an agent key")
	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodPut, f.wsPath(), body, nil).Code, "no credentials")
	rec := f.do(http.MethodGet, f.wsPath(), "", as(f.member))
	require.Equal(t, http.StatusForbidden, rec.Code, "reading the choice form is for administrators too")

	// Control: the owner is accepted, and the refused calls changed nothing.
	rec = f.do(http.MethodGet, f.wsPath(), "", as(f.owner))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"theme_id":null`)
	rec = f.do(http.MethodPut, f.wsPath(), body, as(f.owner))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"theme_id":"graphite"`)
}

func TestThemeAPI_InstanceRoutesNeedInstanceAdmin(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{})
	routes := map[string][2]string{
		"put theme": {http.MethodPut, "/api/v1/instance/theme"},
		"get theme": {http.MethodGet, "/api/v1/instance/theme"},
		"install":   {http.MethodPost, "/api/v1/instance/themes"},
		"delete":    {http.MethodDelete, "/api/v1/instance/themes/graphite"},
	}
	bodies := map[string]string{"put theme": `{"theme_id":"graphite"}`, "get theme": ``, "install": `{"source":"builtin","id":"graphite"}`, "delete": ``}
	for name, r := range routes {
		t.Run(name, func(t *testing.T) {
			// A workspace owner is not an instance administrator.
			require.Equal(t, http.StatusForbidden, f.do(r[0], r[1], bodies[name], as(f.owner)).Code)
			require.Equal(t, http.StatusForbidden, f.do(r[0], r[1], bodies[name], as(f.member)).Code)
			code := f.do(r[0], r[1], bodies[name], map[string]string{"X-Test-Agent": "1"}).Code
			require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, code, "an agent key")
			require.Equal(t, http.StatusUnauthorized, f.do(r[0], r[1], bodies[name], nil).Code)
		})
	}
	require.Empty(t, f.svcInstanceChoice(), "no refused call may have stored a choice")
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, "/api/v1/instance/theme", `{"theme_id":"graphite"}`, as(f.instAdmin)).Code, "control: the instance admin")
}

func TestThemeAPI_DeleteFallsBackWhereTheThemeWasChosen(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{Tokens: map[string]map[string]string{"light": {"primary": "10 90% 40%", "background": "0 0% 100%"}}})
	path := "/api/v1/theme?workspace=" + f.ws.Slug

	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.wsPath(), `{"theme_id":"graphite"}`, as(f.owner)).Code)
	rec := f.do(http.MethodGet, path, "", as(f.member))
	require.Contains(t, rec.Body.String(), `"theme_id":"graphite"`, "red: the workspace paints the theme before the delete")

	require.Equal(t, http.StatusNoContent, f.do(http.MethodDelete, "/api/v1/instance/themes/graphite", "", as(f.instAdmin)).Code)
	rec = f.do(http.MethodGet, path, "", as(f.member))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `graphite`, "the workspace is back on mesh-default")
	require.Contains(t, rec.Body.String(), `"theme_id":"mesh-default"`)

	rec = f.do(http.MethodGet, "/api/v1/themes", "", as(f.member))
	require.NotContains(t, rec.Body.String(), `"id":"graphite"`)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodDelete, "/api/v1/instance/themes/graphite", "", as(f.instAdmin)).Code, "already gone")
}

func (f *themeFixture) svcInstanceChoice() string {
	sel, _ := f.svc.GetSelection(context.Background(), nil)
	if sel.ThemeID == nil {
		return ""
	}
	return *sel.ThemeID
}

func TestThemeAPI_SettingsOutsideSchemaAre422(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{Settings: themestest.ColourSettings()})
	rec := f.do(http.MethodPut, f.wsPath(), `{"theme_id":"graphite","settings":{"unknown_knob":1}}`, as(f.owner))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), themes.CodeSettingsInvalid)
	rec = f.do(http.MethodPut, f.wsPath(), `{"theme_id":"graphite","settings":{"accent_colour":"10 20% 30%","dense":true}}`, as(f.owner))
	require.Equal(t, http.StatusOK, rec.Code, "control: settings inside the schema")
}

func TestThemeAPI_ContrastIsAWarning(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{Tokens: map[string]map[string]string{"light": {"foreground": "0 0% 60%", "background": "0 0% 70%"}}})
	rec := f.do(http.MethodPut, f.wsPath(), `{"theme_id":"graphite"}`, as(f.owner))
	require.Equal(t, http.StatusOK, rec.Code, "saved despite the poor contrast")
	var out struct {
		Warnings []struct{ Pair string } `json:"contrast_warnings"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Warnings, 1)
	require.Equal(t, "foreground/background", out.Warnings[0].Pair)
}

func TestThemeAPI_GetThemeETag(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{})

	// Public, no credentials: the instance theme (mesh-default here).
	rec := f.do(http.MethodGet, "/api/v1/theme", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Cache-Control"), "public")
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)
	for _, k := range []string{"tokens_css", "theme_css_url", "views_url", "settings", "hash"} {
		require.Contains(t, rec.Body.String(), `"`+k+`"`)
	}

	// Repeat with If-None-Match: 304 and no body.
	rec = f.do(http.MethodGet, "/api/v1/theme", "", map[string]string{"If-None-Match": etag})
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Body.String())

	// Control: a change of the instance theme changes the ETag, so the same
	// conditional request now gets a full answer.
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, "/api/v1/instance/theme", `{"theme_id":"graphite"}`, as(f.instAdmin)).Code)
	rec = f.do(http.MethodGet, "/api/v1/theme", "", map[string]string{"If-None-Match": etag})
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEqual(t, etag, rec.Header().Get("ETag"))
	require.Contains(t, rec.Body.String(), "--primary:")
}

func TestThemeAPI_GetThemeWithWorkspaceNeedsMembership(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{})
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.wsPath(), `{"theme_id":"graphite"}`, as(f.owner)).Code)
	path := "/api/v1/theme?workspace=acme"

	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, path, "", nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, path, "", as(f.stranger)).Code, "a non-member learns nothing")
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/theme?workspace=nope", "", as(f.member)).Code, "same answer for an unknown workspace")
	rec := f.do(http.MethodGet, path, "", as(f.member))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"theme_id":"graphite"`)
	require.Contains(t, rec.Header().Get("Cache-Control"), "private")
	require.Equal(t, http.StatusOK, f.do(http.MethodGet, path, "", map[string]string{"X-Test-Agent": "1"}).Code, "an agent of this workspace")

	// The bare request still shows the instance theme, not the workspace's.
	rec = f.do(http.MethodGet, "/api/v1/theme", "", nil)
	require.Contains(t, rec.Body.String(), `"theme_id":"mesh-default"`)
}

func TestThemeAPI_BuiltinInstallAndList(t *testing.T) {
	f := newThemeFixture(t)
	rec := f.do(http.MethodGet, "/api/v1/themes", "", as(f.member))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"builtin_available":["board-studio","graphite"]`)

	rec = f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"builtin","id":"graphite"}`, as(f.instAdmin))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	rec = f.do(http.MethodGet, "/api/v1/themes", "", as(f.member))
	require.Contains(t, rec.Body.String(), `"id":"graphite"`)
	require.Contains(t, rec.Body.String(), `"builtin_available":["board-studio"]`)

	rec = f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"builtin","id":"nope"}`, as(f.instAdmin))
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"ftp"}`, as(f.instAdmin))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// themeRepoServer is a GitLab that serves one theme repository.
type themeRepoServer struct {
	mu    sync.Mutex
	files map[string][]byte // path → content (manifest included)
}

func (g *themeRepoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	const base = "/api/v4/projects/grp%2Ftheme/repository/"
	p := r.URL.EscapedPath()
	if !strings.HasPrefix(p, base) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(p, base)
	switch {
	case strings.HasPrefix(rest, "commits/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "abc123"})
	case strings.HasPrefix(rest, "files/"):
		name, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(rest, "files/"), "/raw"))
		if b, ok := g.files[name]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func TestThemeAPI_InstallFromRepoVerifiesTheBundleHash(t *testing.T) {
	f := newThemeFixture(t)
	css, views := []byte(".a{color:red}"), []byte("export default 1")
	repo := &themeRepoServer{files: map[string][]byte{
		"mesh-extension.json": themestest.Manifest{CSS: css, Views: views}.JSON(),
		"theme.css":           css,
		"views.js":            []byte("export default 'swapped after the report was built'"),
	}}
	srv := httptest.NewServer(repo)
	t.Cleanup(srv.Close)
	body := `{"source":"repo","repo_url":"` + srv.URL + `/grp/theme","ref":"v1"}`

	// Red: the bundle differs from the hash in the manifest.
	rec := f.do(http.MethodPost, "/api/v1/instance/themes", body, as(f.instAdmin))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), themes.CodeHashMismatch)
	require.Empty(t, f.inst.rows, "nothing is installed")
	require.Empty(t, f.store.Objects, "nothing is stored")

	// Green: the same request once the repository serves the matching bundle.
	repo.mu.Lock()
	repo.files["views.js"] = views
	repo.mu.Unlock()
	rec = f.do(http.MethodPost, "/api/v1/instance/themes", body, as(f.instAdmin))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, f.inst.rows, "graphite")
	require.Len(t, f.store.Objects, 2)

	// The bundle is then served by hash, immutable, with a hash ETag.
	sha := themestest.SHA(css)
	rec = f.do(http.MethodGet, "/api/v1/theme/assets/"+sha+".css", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
	require.Equal(t, `"`+sha+`"`, rec.Header().Get("ETag"))
	require.Contains(t, rec.Body.String(), "@layer mesh-theme")
	rec = f.do(http.MethodGet, "/api/v1/theme/assets/"+sha+".css", "", map[string]string{"If-None-Match": `"` + sha + `"`})
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/theme/assets/"+strings.Repeat("0", 64)+".css", "", nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/theme/assets/../../etc/passwd", "", nil).Code)
}

func TestThemeAPI_InstallFromRepoRefusesAPlugin(t *testing.T) {
	f := newThemeFixture(t)
	raw, err := readFixture("../extensions/schema/fixtures/valid/mesh-ext-hello.json")
	require.NoError(t, err)
	repo := &themeRepoServer{files: map[string][]byte{"mesh-extension.json": raw}}
	srv := httptest.NewServer(repo)
	t.Cleanup(srv.Close)
	rec := f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"repo","repo_url":"`+srv.URL+`/grp/theme","ref":"v1"}`, as(f.instAdmin))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Empty(t, f.inst.rows)
}

func readFixture(path string) ([]byte, error) { return os.ReadFile(path) }

// themeStore is the repo-install fake with a working UpsertBuiltin.
type themeStore struct{ *repoStore }

func (s *themeStore) UpsertBuiltin(_ context.Context, r *domain.InstalledExtension) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *r
	s.rows[r.ExtensionID] = &c
	return nil
}

func TestThemeAPI_ManifestFollowsInstanceTheme(t *testing.T) {
	f := newThemeFixture(t)
	f.addTheme(themestest.Manifest{Tokens: map[string]map[string]string{"light": {"primary": "0 100% 50%", "background": "0 0% 100%"}}})

	read := func(rec *httptest.ResponseRecorder) map[string]any {
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Header().Get("Content-Type"), "application/manifest+json")
		var doc map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		return doc
	}

	// No theme chosen: mesh-default colours, not empty ones.
	doc := read(f.do(http.MethodGet, "/api/v1/theme/manifest.json", "", nil))
	require.Equal(t, "#006b6b", doc["theme_color"])
	require.Equal(t, "#ffffff", doc["background_color"])

	// The instance admin changes the theme: the manifest follows, the name does not.
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, "/api/v1/instance/theme", `{"theme_id":"graphite"}`, as(f.instAdmin)).Code)
	rec := f.do(http.MethodGet, "/api/v1/theme/manifest.json", "", nil)
	doc = read(rec)
	require.Equal(t, "#ff0000", doc["theme_color"])
	require.Equal(t, "Entire VC Mesh", doc["name"])

	// ETag revalidation, and /theme carries the same brand for the page's meta tag.
	rec2 := f.do(http.MethodGet, "/api/v1/theme/manifest.json", "", map[string]string{"If-None-Match": rec.Header().Get("ETag")})
	require.Equal(t, http.StatusNotModified, rec2.Code)
	require.Contains(t, f.do(http.MethodGet, "/api/v1/theme", "", nil).Body.String(), `"theme_color":"#ff0000"`)
}
