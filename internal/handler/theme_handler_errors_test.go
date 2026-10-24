package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/themes/themestest"
)

var errThemeStoreDown = errors.New("store unreachable")

// themeStores swaps the fixture's service stores for ones that can be failed on demand.
func (f *themeFixture) stores() (*themestest.Installed, *themestest.Themes) {
	inst, th := themestest.NewInstalled(), themestest.NewThemes()
	th.Installed = inst
	f.svc.Installed, f.svc.Themes = inst, th
	return inst, th
}

func TestThemeAPI_GetWorkspaceVariant(t *testing.T) {
	f := newThemeFixture(t)
	agent := map[string]string{"X-Test-Agent": "1"}
	path := "/api/v1/theme?workspace=" + f.ws.Slug

	rec := f.do(http.MethodGet, path, "", as(f.member))
	require.Equal(t, http.StatusOK, rec.Code, "a member reads the workspace theme")
	require.Equal(t, "private, no-cache", rec.Header().Get("Cache-Control"))

	rec = f.do(http.MethodGet, path, "", agent)
	require.Equal(t, http.StatusOK, rec.Code, "an agent key of that workspace")

	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, path, "", as(f.stranger)).Code, "not a member")
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/theme?workspace=nope", "", as(f.member)).Code, "unknown slug answers like a foreign one")
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/theme?workspace=nope", "", agent).Code)
	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, path, "", nil).Code, "anonymous callers get the public theme only")
	require.Equal(t, http.StatusOK, f.do(http.MethodGet, "/api/v1/theme", "", nil).Code, "control: the public theme needs no login")
}

func TestThemeAPI_StoreFailuresAre500(t *testing.T) {
	f := newThemeFixture(t)
	inst, th := f.stores()
	inst.Add("graphite", themestest.Manifest{}.JSON())
	owner, admin := as(f.owner), as(f.instAdmin)
	th.Err = errThemeStoreDown

	for name, c := range map[string]struct {
		method, path, body string
		hdr                map[string]string
	}{
		"resolve":        {http.MethodGet, "/api/v1/theme", "", nil},
		"workspace get":  {http.MethodGet, f.wsPath(), "", owner},
		"workspace put":  {http.MethodPut, f.wsPath(), `{"theme_id":"graphite"}`, owner},
		"instance get":   {http.MethodGet, "/api/v1/instance/theme", "", admin},
		"instance put":   {http.MethodPut, "/api/v1/instance/theme", `{"theme_id":"graphite"}`, admin},
		"workspace view": {http.MethodGet, "/api/v1/theme?workspace=" + f.ws.Slug, "", as(f.member)},
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.do(c.method, c.path, c.body, c.hdr)
			require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		})
	}

	th.Err = nil
	require.Equal(t, http.StatusOK, f.do(http.MethodGet, f.wsPath(), "", owner).Code, "control: the same call passes once the store is back")

	inst.Err = errThemeStoreDown
	require.Equal(t, http.StatusInternalServerError, f.do(http.MethodGet, "/api/v1/themes", "", owner).Code, "list")
}

func TestThemeAPI_AssetRefusals(t *testing.T) {
	f := newThemeFixture(t)
	sha := themestest.SHA([]byte("x"))

	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/theme/assets/not-a-hash.css", "", nil).Code, "name is not <sha256>.<ext>")
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/api/v1/theme/assets/"+sha+".css", "", nil).Code, "unknown hash")

	rec := f.do(http.MethodGet, "/api/v1/theme/assets/"+sha+".js", "", map[string]string{"If-None-Match": `"` + sha + `"`})
	require.Equal(t, http.StatusNotModified, rec.Code, "the hash is the ETag; no store read needed")

	f.store.Err = errThemeStoreDown
	require.Equal(t, http.StatusInternalServerError, f.do(http.MethodGet, "/api/v1/theme/assets/"+sha+".js", "", nil).Code)
}

func TestThemeAPI_RequestShapeRefusals(t *testing.T) {
	f := newThemeFixture(t)
	admin, owner := as(f.instAdmin), as(f.owner)

	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPut, f.wsPath(), `{not json`, owner).Code, "workspace put")
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPut, "/api/v1/instance/theme", `{not json`, admin).Code, "instance put")
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, "/api/v1/instance/themes", `{not json`, admin).Code, "install body")
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"ftp"}`, admin).Code, "unknown source")
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"repo","repo_url":"https://x"}`, admin).Code, "ref missing")

	f.h.repo = nil
	rec := f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"repo","repo_url":"https://x/y","ref":"v1"}`, admin)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "no repository installer wired")
}

// failSecondRead serves the read InstallBuiltin makes after saving the row, then
// fails the next one: the database dropped between saving a theme and reading
// it back for its contrast report.
type failSecondRead struct {
	*themestest.Installed
	reads int
}

func (s *failSecondRead) Get(ctx context.Context, id string) (*domain.InstalledExtension, error) {
	s.reads++
	if s.reads > 1 {
		return nil, errThemeStoreDown
	}
	return s.Installed.Get(ctx, id)
}

func TestThemeAPI_InstallBuiltin(t *testing.T) {
	f := newThemeFixture(t)
	admin := as(f.instAdmin)
	_, th := f.stores()

	rec := f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"builtin","id":"graphite"}`, admin)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"extension_id":"graphite"`)
	require.Contains(t, rec.Body.String(), `"contrast_warnings"`)

	require.Equal(t, http.StatusNotFound, f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"builtin","id":"ghost"}`, admin).Code)

	fresh := themestest.NewInstalled()
	th.Installed = fresh
	f.svc.Installed = &failSecondRead{Installed: fresh}
	rec = f.do(http.MethodPost, "/api/v1/instance/themes", `{"source":"builtin","id":"graphite"}`, admin)
	require.Equal(t, http.StatusInternalServerError, rec.Code, "an install whose report cannot be read is not reported as a success")
}
