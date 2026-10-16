package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

type fakeInstalledRepo struct {
	rows []domain.InstalledExtension
	err  error
}

func (f *fakeInstalledRepo) UpsertBuiltin(context.Context, *domain.InstalledExtension) error {
	return nil
}
func (f *fakeInstalledRepo) Get(context.Context, string) (*domain.InstalledExtension, error) {
	return nil, nil
}
func (f *fakeInstalledRepo) List(context.Context) ([]domain.InstalledExtension, error) {
	return f.rows, f.err
}

type fakeInstanceStore struct {
	m   map[string]*extensions.InstanceSetting
	err error
}

func (f fakeInstanceStore) InstanceSetting(_ context.Context, id string) (*extensions.InstanceSetting, error) {
	return f.m[id], f.err
}

const testManifest = `{"schema":1,"id":"team-relay","name":"Team Relay","version":"1.2.0","kind":"integration","runtime":"builtin","publisher":"entire","description":"d","category":"sync","levels":{"instance":true,"workspace":true,"project":false}}`

func doExt(t *testing.T, h echo.HandlerFunc, method string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, "/", http.NoBody), rec)
	require.NoError(t, h(c))
	return rec
}

func TestExtensionList_Contract(t *testing.T) {
	repo := &fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "team-relay", Version: "1.2.0", Source: domain.ExtensionSourceBuiltin, Manifest: json.RawMessage(testManifest)},
		{ExtensionID: "broken", Version: "1", Manifest: json.RawMessage(`{}`)},
	}}
	h := NewExtensionHandler(repo, fakeInstanceStore{m: map[string]*extensions.InstanceSetting{
		"team-relay": {Enabled: true},
	}})
	rec := doExt(t, h.List, http.MethodGet)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Extensions []map[string]any `json:"extensions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Extensions, 1, "row with an unparsable manifest is skipped")
	x := body.Extensions[0]
	assert.Equal(t, "team-relay", x["id"])
	assert.Equal(t, "Team Relay", x["name"])
	assert.Equal(t, "builtin", x["source"])
	assert.Equal(t, map[string]any{"enabled": true, "configured": true}, x["instance"])
	assert.Equal(t, map[string]any{"instance": true, "workspace": true, "project": false}, x["levels"])
	assert.NotNil(t, x["manifest"])
}

func TestExtensionList_NoInstanceStore(t *testing.T) {
	h := NewExtensionHandler(&fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "team-relay", Version: "1.2.0", Manifest: json.RawMessage(testManifest)},
	}}, nil)
	rec := doExt(t, h.List, http.MethodGet)
	assert.Contains(t, rec.Body.String(), `"instance":{"enabled":false,"configured":false}`)
}

func TestExtensionList_Empty(t *testing.T) {
	rec := doExt(t, NewExtensionHandler(&fakeInstalledRepo{}, nil).List, http.MethodGet)
	assert.JSONEq(t, `{"extensions":[]}`, rec.Body.String())
}

func TestExtensionManifestSchema(t *testing.T) {
	rec := doExt(t, NewExtensionHandler(&fakeInstalledRepo{}, nil).ManifestSchema, http.MethodGet)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/schema+json", rec.Header().Get("Content-Type"))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.NotEmpty(t, doc["$schema"])
}

func TestExtensionStubs_501(t *testing.T) {
	rec := doExt(t, NewExtensionHandler(&fakeInstalledRepo{}, nil).NotImplemented, http.MethodPost)
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error_code":"not_implemented"`)
}

// A failing instance store must not read as "disabled, not configured".
func TestExtensionList_InstanceStoreErrorIsNotAFalseStatus(t *testing.T) {
	repo := &fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "team-relay", Version: "1.2.0", Source: domain.ExtensionSourceBuiltin, Manifest: json.RawMessage(testManifest)},
	}}
	h := NewExtensionHandler(repo, fakeInstanceStore{err: errors.New("db down")})
	rec := doExt(t, h.List, http.MethodGet)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"enabled":false`)
}
