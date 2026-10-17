package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/middleware"
)

type emptyInstalledRepo struct{}

func (emptyInstalledRepo) UpsertBuiltin(context.Context, *domain.InstalledExtension) error {
	return nil
}
func (emptyInstalledRepo) Get(context.Context, string) (*domain.InstalledExtension, error) {
	return nil, nil
}
func (emptyInstalledRepo) List(context.Context) ([]domain.InstalledExtension, error) {
	return nil, nil
}

// TestInstanceExtensionRoutes_Contract checks every instance extension route
// exactly as main.go registers it: the paths and guards are read from main.go,
// the router is built from them, and each route is called through that router.
func TestInstanceExtensionRoutes_Contract(t *testing.T) {
	h := handler.NewExtensionHandler(emptyInstalledRepo{}, nil)
	handlers := map[string]echo.HandlerFunc{
		"GET /extensions":                 h.List,
		"GET /extensions/manifest-schema": h.ManifestSchema,
	}
	registered := map[string]auditRoute{}
	for _, r := range parseAuditRoutes(t) {
		registered[r.key()] = r
	}

	e := echo.New()
	for key, fn := range handlers {
		r, ok := registered[key]
		require.True(t, ok, "route %q is not registered in main.go", key)
		var mws []echo.MiddlewareFunc
		if r.method == http.MethodPost {
			assert.Equal(t, "human-only", r.guard, "%s must be human-only", key)
			// Stand-in for mw.RequireUserAuth: record that the guard sits in front.
			mws = append(mws, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Response().Header().Set("X-Guarded", "1")
					return next(c)
				}
			})
		}
		e.Add(r.method, r.path, fn, mws...)
	}

	call := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
		return rec
	}

	t.Run("GET /extensions", func(t *testing.T) {
		rec := call(http.MethodGet, "/extensions")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"extensions":[]}`, rec.Body.String())
	})
	t.Run("GET /extensions/manifest-schema is not shadowed by :ext_id routes", func(t *testing.T) {
		rec := call(http.MethodGet, "/extensions/manifest-schema")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/schema+json", rec.Header().Get("Content-Type"))
		var doc map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	})
}

type memInstanceSettings struct {
	row *domain.InstanceExtensionSetting
}

func (m *memInstanceSettings) Get(context.Context, string) (*domain.InstanceExtensionSetting, error) {
	return m.row, nil
}
func (m *memInstanceSettings) InsertIfAbsent(context.Context, *domain.InstanceExtensionSetting, []string) (bool, error) {
	return false, nil
}
func (m *memInstanceSettings) Upsert(_ context.Context, s *domain.InstanceExtensionSetting, _ []string) error {
	c := *s
	m.row = &c
	return nil
}

type fixedAdmin bool

func (f fixedAdmin) IsInstanceAdmin(context.Context, uuid.UUID) (bool, error) { return bool(f), nil }

// TestInstanceSettingsRoutes_Contract covers GET/PUT /extensions/:ext_id/instance-settings
// as main.go registers them: user-auth guard in front, administrator check in
// the handler, secrets masked on read.
func TestInstanceSettingsRoutes_Contract(t *testing.T) {
	registered := map[string]auditRoute{}
	for _, r := range parseAuditRoutes(t) {
		registered[r.key()] = r
	}
	sources := []extensions.EnvSource{{ExtensionID: "probe", SecretKeys: []string{"token"}}}

	build := func(admin bool) (*echo.Echo, *memInstanceSettings) {
		repo := &memInstanceSettings{}
		h := handler.NewExtensionInstanceSettingsHandler(repo, fixedAdmin(admin), nil, sources)
		userAuth := func(next echo.HandlerFunc) echo.HandlerFunc { // stand-in for mw.RequireUserAuth
			return func(c echo.Context) error {
				c.Set(middleware.ContextKeyUserID, uuid.New())
				return next(c)
			}
		}
		e := echo.New()
		for key, fn := range map[string]echo.HandlerFunc{
			"GET /extensions/:ext_id/instance-settings": h.Get,
			"PUT /extensions/:ext_id/instance-settings": h.Put,
		} {
			r, ok := registered[key]
			require.True(t, ok, "route %q is not registered in main.go", key)
			assert.Equal(t, "human-only", r.guard, "%s must be human-only", key)
			e.Add(r.method, r.path, fn, userAuth)
		}
		return e, repo
	}
	call := func(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	t.Run("administrator writes, read masks the secret", func(t *testing.T) {
		e, repo := build(true)
		put := call(e, http.MethodPut, "/extensions/probe/instance-settings",
			`{"enabled":true,"settings":{"token":"s3cr3t-value","url":"https://x.example"}}`)
		require.Equal(t, http.StatusOK, put.Code, put.Body.String())
		require.NotNil(t, repo.row)

		get := call(e, http.MethodGet, "/extensions/probe/instance-settings", "")
		require.Equal(t, http.StatusOK, get.Code)
		assert.NotContains(t, get.Body.String(), "s3cr3t-value")
		assert.Contains(t, get.Body.String(), "https://x.example")
	})
	t.Run("non-administrator gets 403 and no settings", func(t *testing.T) {
		e, repo := build(false)
		repo.row = &domain.InstanceExtensionSetting{ExtensionID: "probe", Settings: json.RawMessage(`{"url":"https://x.example"}`)}
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			rec := call(e, method, "/extensions/probe/instance-settings", `{"enabled":true}`)
			assert.Equal(t, http.StatusForbidden, rec.Code, method)
			assert.NotContains(t, rec.Body.String(), "x.example", method)
		}
	})
	t.Run("unknown extension is 404", func(t *testing.T) {
		e, _ := build(true)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			rec := call(e, method, "/extensions/nope/instance-settings", `{"enabled":true,"settings":{"token":"s3cr3t-value"}}`)
			assert.Equal(t, http.StatusNotFound, rec.Code, method)
			assert.NotContains(t, rec.Body.String(), "s3cr3t-value", method)
		}
	})
}

// TestExtensionCatalogHandler_WiredWithInstanceStore guards main.go: a catalog
// handler built with a nil instance store reports enabled=false/configured=false
// for every extension, even with saved instance settings.
func TestExtensionCatalogHandler_WiredWithInstanceStore(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	require.NoError(t, err)
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewExtensionHandler" {
			return true
		}
		found++
		require.Len(t, call.Args, 2)
		if id, isIdent := call.Args[1].(*ast.Ident); isIdent {
			assert.NotEqual(t, "nil", id.Name, "NewExtensionHandler must get the instance store")
		}
		return true
	})
	require.Equal(t, 1, found, "main.go must construct the catalog handler exactly once")
}
