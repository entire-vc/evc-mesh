package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/middleware"
)

type fakeInstRepo struct {
	row        *domain.InstanceExtensionSetting
	getErr     error
	upsertErr  error
	upsertKeys []string
}

func (f *fakeInstRepo) Get(context.Context, string) (*domain.InstanceExtensionSetting, error) {
	return f.row, f.getErr
}
func (f *fakeInstRepo) InsertIfAbsent(context.Context, *domain.InstanceExtensionSetting, []string) (bool, error) {
	return false, nil
}
func (f *fakeInstRepo) Upsert(_ context.Context, s *domain.InstanceExtensionSetting, keys []string) error {
	f.upsertKeys = keys
	if f.upsertErr != nil {
		return f.upsertErr
	}
	c := *s
	f.row = &c
	return nil
}

type fakeAdmins struct {
	ok  bool
	err error
}

func (f fakeAdmins) IsInstanceAdmin(context.Context, uuid.UUID) (bool, error) { return f.ok, f.err }

func callInstanceSettings(t *testing.T, h *ExtensionInstanceSettingsHandler, method, extID, body string, agent bool) (*httptest.ResponseRecorder, error) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ext_id")
	c.SetParamValues(extID)
	c.Set(middleware.ContextKeyUserID, uuid.New())
	if agent {
		c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeAgent)
	}
	if method == http.MethodGet {
		return rec, h.Get(c)
	}
	return rec, h.Put(c)
}

func TestInstanceSettings_NonAdminAndUnknownRefused(t *testing.T) {
	repo := &fakeInstRepo{}
	srcs := extensions.BuiltinEnvSources()

	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: false}, nil, srcs)
	rec, _ := callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true}`, false)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Nil(t, repo.row, "a refused PUT must not write")

	h = NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, nil, srcs)
	rec, _ = callInstanceSettings(t, h, http.MethodGet, "no-such-ext", "", false)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInstanceSettings_PutKeepsMaskedSecret_GetMasks(t *testing.T) {
	repo := &fakeInstRepo{row: &domain.InstanceExtensionSetting{
		ExtensionID: "gitlab", Enabled: true, Source: domain.InstanceSettingSourceEnv,
		Settings: json.RawMessage(`{"base_url":"https://git.old","token":"real-token"}`),
	}}
	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, nil, extensions.BuiltinEnvSources())

	rec, _ := callInstanceSettings(t, h, http.MethodPut, "gitlab",
		`{"enabled":true,"settings":{"base_url":"https://git.new","token":"********"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "real-token")
	require.JSONEq(t, `{"base_url":"https://git.new","token":"real-token"}`, string(repo.row.Settings))
	require.Equal(t, domain.InstanceSettingSourceAdmin, repo.row.Source)

	rec, _ = callInstanceSettings(t, h, http.MethodGet, "gitlab", "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"token":"********"`)
	require.NotContains(t, rec.Body.String(), "real-token")
}

func TestInstanceSettings_AuthorizationFailuresAreDistinct(t *testing.T) {
	srcs := extensions.BuiltinEnvSources()
	repo := &fakeInstRepo{}

	// an agent key is refused before any admin lookup
	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, nil, srcs)
	rec, _ := callInstanceSettings(t, h, http.MethodGet, "gitlab", "", true)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// no authenticated user -> 401, not 403
	e := echo.New()
	rec = httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("ext_id")
	c.SetParamValues("gitlab")
	require.NoError(t, h.Get(c))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// a failing admin lookup is a 500, never a silent allow
	h = NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true, err: errors.New("db down")}, nil, srcs)
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true}`, false)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Nil(t, repo.row)
}

func TestInstanceSettings_GetWithoutRowAndStorageErrors(t *testing.T) {
	srcs := extensions.BuiltinEnvSources()
	admin := fakeAdmins{ok: true}

	h := NewExtensionInstanceSettingsHandler(&fakeInstRepo{}, admin, nil, srcs)
	rec, _ := callInstanceSettings(t, h, http.MethodGet, "gitlab", "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"source":"none"`)
	require.Contains(t, rec.Body.String(), `"enabled":false`)

	h = NewExtensionInstanceSettingsHandler(&fakeInstRepo{getErr: errors.New("boom")}, admin, nil, srcs)
	rec, _ = callInstanceSettings(t, h, http.MethodGet, "gitlab", "", false)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true}`, false)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	repo := &fakeInstRepo{upsertErr: errors.New("boom")}
	h = NewExtensionInstanceSettingsHandler(repo, admin, nil, srcs)
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true}`, false)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestInstanceSettings_PutRejectsBadInputAndUnknownExtension(t *testing.T) {
	srcs := extensions.BuiltinEnvSources()
	repo := &fakeInstRepo{}
	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, nil, srcs)

	rec, _ := callInstanceSettings(t, h, http.MethodPut, "no-such-ext", `{"enabled":true}`, false)
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec, _ = callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":`, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Nil(t, repo.row, "a rejected PUT must not write")

	// no previous row and a masked secret: the mask must not be stored as the secret
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "gitlab",
		`{"enabled":true,"settings":{"base_url":"https://g","token":"********"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"base_url":"https://g"}`, string(repo.row.Settings),
		"first save with a mask and no stored value must drop the placeholder")
}

type manifestProvider struct{ m extensions.Manifest }

func (p manifestProvider) Manifest() extensions.Manifest                 { return p.m }
func (manifestProvider) Validate(extensions.Level, map[string]any) error { return nil }
func (manifestProvider) Resolve(context.Context, uuid.UUID) (extensions.Resolved, error) {
	return extensions.Resolved{}, nil
}
func (manifestProvider) Health(context.Context, extensions.Resolved) extensions.HealthReport {
	return extensions.HealthReport{}
}
func (manifestProvider) Hooks() extensions.Hooks   { return extensions.Hooks{} }
func (manifestProvider) UI() extensions.UIContribs { return extensions.UIContribs{} }

func registeredSecretHandler(t *testing.T, repo *fakeInstRepo) *ExtensionInstanceSettingsHandler {
	t.Helper()
	m, err := extensions.ParseManifest([]byte(`{"id":"acme","version":"1.0.0","runtime":"builtin",
		"settings":{"instance":{"$ref":"#/schemas/connection"}},
		"schemas":{"connection":{"properties":{"base_url":{"type":"string"},"api_key":{"type":"string","x-secret":true}}}}}`))
	require.NoError(t, err)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(manifestProvider{m: m}))
	return NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, reg, nil)
}

// A registered provider's x-secret fields must be encrypted and masked even
// though the extension has no env source.
func TestInstanceSettings_RegisteredProviderSecretsFromManifest(t *testing.T) {
	repo := &fakeInstRepo{}
	h := registeredSecretHandler(t, repo)

	rec, _ := callInstanceSettings(t, h, http.MethodPut, "acme",
		`{"enabled":true,"settings":{"base_url":"https://a","api_key":"plain-key"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"api_key"}, repo.upsertKeys, "repo must be told which keys to encrypt")
	require.NotContains(t, rec.Body.String(), "plain-key")

	rec, _ = callInstanceSettings(t, h, http.MethodGet, "acme", "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"********"`)
	require.NotContains(t, rec.Body.String(), "plain-key")
}

// With no stored secret a masked value must not be saved as the credential.
func TestInstanceSettings_MaskWithoutStoredSecretIsNotSaved(t *testing.T) {
	repo := &fakeInstRepo{}
	h := registeredSecretHandler(t, repo)

	rec, _ := callInstanceSettings(t, h, http.MethodPut, "acme",
		`{"enabled":true,"settings":{"base_url":"https://a","api_key":"********"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"base_url":"https://a"}`, string(repo.row.Settings))
}

// external stands in for an installed external extension: it refuses every
// instance-level setting, as external.Provider does.
type externalProvider struct{ manifestProvider }

func (externalProvider) Validate(extensions.Level, map[string]any) error {
	return errors.New("keep has no instance-level settings")
}

func keepHandler(t *testing.T, repo *fakeInstRepo, pol *fakePolicyRepo) *ExtensionInstanceSettingsHandler {
	t.Helper()
	m, err := extensions.ParseManifest([]byte(`{"id":"keep","version":"1.0.0","runtime":"builtin"}`))
	require.NoError(t, err)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(externalProvider{manifestProvider{m: m}}))
	return NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, reg, nil).WithJournal(pol)
}

func TestInstanceSettings_KeepSecretProviderSwitch(t *testing.T) {
	pol := newFakePolicyRepo()
	repo := &fakeInstRepo{}
	h := keepHandler(t, repo, pol)

	rec, _ := callInstanceSettings(t, h, http.MethodPut, "keep", `{"enabled":true,"settings":{"secret_provider":"keep"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"secret_provider":"keep"}`, string(repo.row.Settings))
	require.Len(t, pol.journal, 1)
	require.Equal(t, "keep", pol.journal[0].ExtensionID)
	require.Contains(t, string(pol.journal[0].Details), `"secret_provider"`, "the journal names the key")
	require.JSONEq(t, `{"keys":["secret_provider"],"enabled":true}`, string(pol.journal[0].Details),
		"the journal carries keys, never values")

	for _, bad := range []string{`"vault"`, `true`} {
		rec, _ = callInstanceSettings(t, h, http.MethodPut, "keep", `{"enabled":true,"settings":{"secret_provider":`+bad+`}}`, false)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, bad)
	}
	require.JSONEq(t, `{"secret_provider":"keep"}`, string(repo.row.Settings), "a refused value is not saved")

	// other keys still go through the extension's own validation
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "keep", `{"enabled":true,"settings":{"secret_provider":"builtin","other":"x"}}`, false)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// keep need not be registered for the switch to be set
	h = NewExtensionInstanceSettingsHandler(&fakeInstRepo{}, fakeAdmins{ok: true}, nil, nil)
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "keep", `{"enabled":true,"settings":{"secret_provider":"builtin"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// A theme is chosen per workspace and its instance default lives in the instance
// admin's Themes tab: it has no instance-settings form here (U4).
func TestInstanceSettings_ThemeIsNotAnInstanceSettingsExtension(t *testing.T) {
	m, err := extensions.ParseManifest([]byte(`{"id":"board-studio","version":"1.0.0","kind":"theme","runtime":"builtin",
		"levels":{"instance":true,"workspace":true},
		"settings":{"instance":{"$ref":"#/schemas/look"}},
		"schemas":{"look":{"properties":{"accent":{"type":"string"}}}}}`))
	require.NoError(t, err)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(manifestProvider{m: m}))
	repo := &fakeInstRepo{}
	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, reg, nil)

	rec, _ := callInstanceSettings(t, h, http.MethodGet, "board-studio", "", false)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "board-studio", `{"enabled":true,"settings":{"accent":"#000"}}`, false)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Nil(t, repo.row, "nothing is stored for a theme")
}
