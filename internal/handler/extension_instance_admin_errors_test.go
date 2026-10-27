package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
)

var errBoom = errors.New("boom")

// failingPolicyRepo fails the named operations and delegates the rest.
type failingPolicyRepo struct {
	*fakePolicyRepo
	fail map[string]bool
}

func (f *failingPolicyRepo) ListPolicies(ctx context.Context) (map[string]domain.InstanceExtensionPolicy, error) {
	if f.fail["list"] {
		return nil, errBoom
	}
	return f.fakePolicyRepo.ListPolicies(ctx)
}
func (f *failingPolicyRepo) GetConfig(ctx context.Context) (*domain.InstanceExtensionConfig, error) {
	if f.fail["config"] {
		return nil, errBoom
	}
	return f.fakePolicyRepo.GetConfig(ctx)
}
func (f *failingPolicyRepo) SetAllowed(ctx context.Context, id string, a bool, by *uuid.UUID) error {
	if f.fail["setallowed"] {
		return errBoom
	}
	return f.fakePolicyRepo.SetAllowed(ctx, id, a, by)
}
func (f *failingPolicyRepo) SetAutoUpdate(ctx context.Context, on bool, by *uuid.UUID) error {
	if f.fail["setauto"] {
		return errBoom
	}
	return f.fakePolicyRepo.SetAutoUpdate(ctx, on, by)
}
func (f *failingPolicyRepo) AppendJournal(ctx context.Context, e *domain.InstanceExtensionJournalEntry) error {
	if f.fail["append"] {
		return errBoom
	}
	return f.fakePolicyRepo.AppendJournal(ctx, e)
}
func (f *failingPolicyRepo) ListJournal(ctx context.Context, n int) ([]domain.InstanceExtensionJournalEntry, error) {
	if f.fail["journal"] {
		return nil, errBoom
	}
	return f.fakePolicyRepo.ListJournal(ctx, n)
}

func failingHandler(fail ...string) (*ExtensionInstanceAdminHandler, *failingPolicyRepo) {
	h, pol := newAdminHandler(true)
	fp := &failingPolicyRepo{fakePolicyRepo: pol, fail: map[string]bool{}}
	for _, k := range fail {
		fp.fail[k] = true
	}
	h.policy = fp
	return h, fp
}

func TestInstanceAdmin_StoreErrorsAre500(t *testing.T) {
	cases := map[string]struct {
		fail   string
		method string
		body   string
		ext    string
		call   func(*ExtensionInstanceAdminHandler) func(echo.Context) error
	}{
		"overview policies": {"list", http.MethodGet, "", "", func(h *ExtensionInstanceAdminHandler) func(echo.Context) error { return h.Overview }},
		"overview config":   {"config", http.MethodGet, "", "", func(h *ExtensionInstanceAdminHandler) func(echo.Context) error { return h.Overview }},
		"set allowed":       {"setallowed", http.MethodPut, `{"allowed":true}`, "community-x", func(h *ExtensionInstanceAdminHandler) func(echo.Context) error { return h.SetAllowed }},
		"set auto-update":   {"setauto", http.MethodPut, `{"auto_update":true}`, "", func(h *ExtensionInstanceAdminHandler) func(echo.Context) error { return h.SetAutoUpdate }},
		"journal":           {"journal", http.MethodGet, "", "", func(h *ExtensionInstanceAdminHandler) func(echo.Context) error { return h.Journal }},
	}
	for name, tc := range cases {
		h, pol := failingHandler(tc.fail)
		c, rec := adminCtx(tc.method, tc.body, tc.ext, false)
		require.NoError(t, tc.call(h)(c), name)
		require.Equal(t, http.StatusInternalServerError, rec.Code, name)
		require.Empty(t, pol.journal, "%s: a failed write must not be journaled", name)
	}
}

func TestInstanceAdmin_InstalledStoreError(t *testing.T) {
	h, _ := newAdminHandler(true)
	h.installed = &adminInstalledRepo{fakeInstalledRepo{err: errBoom}}
	c, rec := adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

type errGetInstalled struct{ adminInstalledRepo }

func (errGetInstalled) Get(context.Context, string) (*domain.InstalledExtension, error) {
	return nil, errBoom
}

func TestInstanceAdmin_SetAllowedLookupError(t *testing.T) {
	h, pol := newAdminHandler(true)
	h.installed = &errGetInstalled{}
	c, rec := adminCtx(http.MethodPut, `{"allowed":true}`, "community-x", false)
	require.NoError(t, h.SetAllowed(c))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Empty(t, pol.policies)
}

func TestInstanceAdmin_SettingsReadErrorFailsOverview(t *testing.T) {
	h, _ := newAdminHandler(true)
	h.settings = &fakeInstRepo{getErr: errBoom}
	c, rec := adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestInstanceAdmin_OverviewSkipsBrokenManifestAndShowsSettings(t *testing.T) {
	h, _ := newAdminHandler(true)
	h.installed = &adminInstalledRepo{fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "broken", Manifest: []byte(`{`), TrustLevel: "community"},
		{ExtensionID: "team-relay", Manifest: []byte(testManifest), TrustLevel: domain.ExtensionTrustBuiltin},
	}}}
	h.settings = &fakeInstRepo{row: &domain.InstanceExtensionSetting{Enabled: true, Source: domain.InstanceSettingSourceEnv}}
	c, rec := adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, `"id":"broken"`)
	require.Regexp(t, `"id":"team-relay".*"enabled":true,"settings_source":"env","settings_updated_at":"`, body)
}

func TestInstanceAdmin_AuthDenials(t *testing.T) {
	h, _ := newAdminHandler(true)
	c, rec := adminCtx(http.MethodGet, "", "", false)
	c.Set(middleware.ContextKeyUserID, "not-a-uuid")
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	h.admins = fakeAdmins{err: errBoom}
	c, rec = adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestInstanceAdmin_BadBodyIs400(t *testing.T) {
	h, pol := newAdminHandler(true)
	c, rec := adminCtx(http.MethodPut, `{`, "community-x", false)
	require.NoError(t, h.SetAllowed(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	c, rec = adminCtx(http.MethodPut, `{`, "", false)
	require.NoError(t, h.SetAutoUpdate(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, pol.policies)
	require.False(t, pol.auto)
}

func TestInstanceAdmin_JournalWriteFailureDoesNotFailTheAction(t *testing.T) {
	h, pol := failingHandler("append")
	c, rec := adminCtx(http.MethodPut, `{"allowed":true}`, "community-x", false)
	require.NoError(t, h.SetAllowed(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, pol.policies["community-x"].Allowed, "the change itself went through")
}

func TestInstanceAdmin_JournalLimitIsClamped(t *testing.T) {
	h, _ := newAdminHandler(true)
	for _, q := range []string{"?limit=5", "?limit=9999", "?limit=-1", "?limit=abc"} {
		c, rec := adminCtx(http.MethodGet, "", "", false)
		c.Request().URL.RawQuery = strings.TrimPrefix(q, "?")
		require.NoError(t, h.Journal(c), q)
		require.Equal(t, http.StatusOK, rec.Code, q)
	}
}

type stubAllow struct {
	ok  bool
	err error
}

func (s stubAllow) Allowed(context.Context, string) (bool, error) { return s.ok, s.err }

func TestExtensionCatalog_AllowListVerdict(t *testing.T) {
	rows := []domain.InstalledExtension{{ExtensionID: "team-relay", Manifest: []byte(testManifest), TrustLevel: domain.ExtensionTrustBuiltin}}
	h := NewExtensionHandler(&fakeInstalledRepo{rows: rows}, nil).WithAllowList(stubAllow{ok: false})
	c, rec := adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.List(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"allowed":false`)

	h = NewExtensionHandler(&fakeInstalledRepo{rows: rows}, nil).WithAllowList(stubAllow{err: errBoom})
	c, rec = adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.List(c))
	require.Equal(t, http.StatusInternalServerError, rec.Code, "an unknown verdict must not look like allowed")
}

// Themes stay in the overview (allow/journal), but never offer the instance-settings form.
func TestInstanceAdmin_OverviewThemeHasNoInstanceForm(t *testing.T) {
	h, _ := newAdminHandler(true)
	h.installed = &adminInstalledRepo{fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "board-studio", TrustLevel: domain.ExtensionTrustBuiltin, Manifest: []byte(`{"schema":1,"id":"board-studio","name":"Board Studio","version":"1.0.0","kind":"theme","runtime":"builtin",
			"levels":{"instance":true,"workspace":true},"settings":{"instance":{"$ref":"#/schemas/look"}},
			"schemas":{"look":{"properties":{"accent":{"type":"string"}}}}}`)},
	}}}
	h.settings = &fakeInstRepo{}
	c, rec := adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"board-studio"`)
	require.Contains(t, rec.Body.String(), `"has_instance_form":false`)
}
