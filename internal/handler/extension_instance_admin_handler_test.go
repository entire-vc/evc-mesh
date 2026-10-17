package handler

import (
	"context"
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

type fakePolicyRepo struct {
	policies map[string]domain.InstanceExtensionPolicy
	auto     bool
	journal  []domain.InstanceExtensionJournalEntry
}

func newFakePolicyRepo() *fakePolicyRepo {
	return &fakePolicyRepo{policies: map[string]domain.InstanceExtensionPolicy{}}
}

func (f *fakePolicyRepo) Allowed(_ context.Context, id string) (bool, error) {
	if p, ok := f.policies[id]; ok {
		return p.Allowed, nil
	}
	return true, nil
}
func (f *fakePolicyRepo) ListPolicies(context.Context) (map[string]domain.InstanceExtensionPolicy, error) {
	return f.policies, nil
}
func (f *fakePolicyRepo) SetAllowed(_ context.Context, id string, allowed bool, by *uuid.UUID) error {
	f.policies[id] = domain.InstanceExtensionPolicy{ExtensionID: id, Allowed: allowed, UpdatedBy: by}
	return nil
}
func (f *fakePolicyRepo) GetConfig(context.Context) (*domain.InstanceExtensionConfig, error) {
	return &domain.InstanceExtensionConfig{AutoUpdate: f.auto}, nil
}
func (f *fakePolicyRepo) SetAutoUpdate(_ context.Context, on bool, _ *uuid.UUID) error {
	f.auto = on
	return nil
}
func (f *fakePolicyRepo) AppendJournal(_ context.Context, e *domain.InstanceExtensionJournalEntry) error {
	f.journal = append([]domain.InstanceExtensionJournalEntry{*e}, f.journal...)
	return nil
}
func (f *fakePolicyRepo) ListJournal(context.Context, int) ([]domain.InstanceExtensionJournalEntry, error) {
	return f.journal, nil
}

func adminCtx(method, body, extID string, agent bool) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if extID != "" {
		c.SetParamNames("ext_id")
		c.SetParamValues(extID)
	}
	c.Set(middleware.ContextKeyUserID, uuid.New())
	if agent {
		c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeAgent)
	}
	return c, rec
}

// adminInstalledRepo finds rows by id; the shared fake's Get always misses.
type adminInstalledRepo struct{ fakeInstalledRepo }

func (f *adminInstalledRepo) Get(_ context.Context, id string) (*domain.InstalledExtension, error) {
	for i := range f.rows {
		if f.rows[i].ExtensionID == id {
			return &f.rows[i], nil
		}
	}
	return nil, nil
}

func newAdminHandler(admin bool) (*ExtensionInstanceAdminHandler, *fakePolicyRepo) {
	pol := newFakePolicyRepo()
	installed := &adminInstalledRepo{fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "team-relay", Manifest: []byte(testManifest), TrustLevel: domain.ExtensionTrustBuiltin},
		{ExtensionID: "community-x", Manifest: []byte(strings.Replace(testManifest, `"team-relay"`, `"community-x"`, 1)), TrustLevel: "community"},
	}}}
	return NewExtensionInstanceAdminHandler(pol, &fakeInstRepo{}, installed, fakeAdmins{ok: admin}), pol
}

func TestInstanceAdmin_NonAdminAndAgentRefused(t *testing.T) {
	h, pol := newAdminHandler(false)
	for name, call := range map[string]func(echo.Context) error{
		"overview": h.Overview, "journal": h.Journal, "auto-update": h.SetAutoUpdate, "allowed": h.SetAllowed,
	} {
		c, rec := adminCtx(http.MethodPut, `{"allowed":true,"auto_update":true}`, "community-x", false)
		require.NoError(t, call(c), name)
		require.Equal(t, http.StatusForbidden, rec.Code, name)
	}
	require.Empty(t, pol.policies, "a refused call must not write")
	require.False(t, pol.auto)

	h, _ = newAdminHandler(true)
	c, rec := adminCtx(http.MethodGet, "", "", true)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusForbidden, rec.Code, "an agent key is refused even when the user is an admin")
}

func TestInstanceAdmin_AllowListDefaultsAndExplicitOverride(t *testing.T) {
	h, pol := newAdminHandler(true)
	c, rec := adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"team-relay"`)
	require.Regexp(t, `"id":"team-relay"[^}]*"allowed":true,"allow_explicit":false`, rec.Body.String())
	require.Regexp(t, `"id":"community-x"[^}]*"allowed":false,"allow_explicit":false`, rec.Body.String())

	c, rec = adminCtx(http.MethodPut, `{"allowed":true}`, "community-x", false)
	require.NoError(t, h.SetAllowed(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, pol.policies["community-x"].Allowed)

	c, rec = adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Regexp(t, `"id":"community-x"[^}]*"allowed":true,"allow_explicit":true`, rec.Body.String())

	c, rec = adminCtx(http.MethodPut, `{"allowed":true}`, "no-such", false)
	require.NoError(t, h.SetAllowed(c))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInstanceAdmin_AutoUpdateAndJournal(t *testing.T) {
	h, pol := newAdminHandler(true)

	c, rec := adminCtx(http.MethodPut, `{"auto_update":true}`, "", false)
	require.NoError(t, h.SetAutoUpdate(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, pol.auto)

	c, _ = adminCtx(http.MethodPut, `{"allowed":true}`, "community-x", false)
	require.NoError(t, h.SetAllowed(c))

	c, rec = adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Contains(t, rec.Body.String(), `"auto_update":true`)

	c, rec = adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Journal(c))
	require.Equal(t, http.StatusOK, rec.Code)
	// newest first: the allow change, then the auto-update flip
	require.Regexp(t, `allow_changed.*auto_update_changed`, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"extension_id":"community-x"`)
	require.Len(t, pol.journal, 2)
	require.NotNil(t, pol.journal[0].ActorID, "the journal says who")
}

func TestInstanceSettings_PutIsJournaledWithKeysOnly(t *testing.T) {
	pol := newFakePolicyRepo()
	repo := &fakeInstRepo{}
	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, nil, extensions.BuiltinEnvSources()).WithJournal(pol)
	rec, _ := callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true,"settings":{"base_url":"https://git.new","token":"real-token"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, pol.journal, 1)
	require.Equal(t, domain.InstanceJournalSettingsChanged, pol.journal[0].Action)
	require.Equal(t, "gitlab", pol.journal[0].ExtensionID)
	require.NotContains(t, string(pol.journal[0].Details), "real-token", "values never reach the journal")
	require.Contains(t, string(pol.journal[0].Details), "base_url")

	// a refused PUT leaves no journal entry
	h = NewExtensionInstanceSettingsHandler(&fakeInstRepo{}, fakeAdmins{ok: false}, nil, extensions.BuiltinEnvSources()).WithJournal(pol)
	callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true}`, false)
	require.Len(t, pol.journal, 1)
}

func TestInstanceSettings_JournalListsOnlyChangedKeys(t *testing.T) {
	pol := newFakePolicyRepo()
	repo := &fakeInstRepo{}
	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, nil, extensions.BuiltinEnvSources()).WithJournal(pol)
	rec, _ := callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true,"settings":{"base_url":"https://git.one","token":"real-token"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code)
	// the fake journal is newest first; the form posts the whole object; only base_url differs
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":true,"settings":{"base_url":"https://git.two","token":"real-token"}}`, false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, pol.journal, 2)
	require.Contains(t, string(pol.journal[0].Details), "base_url")
	require.NotContains(t, string(pol.journal[0].Details), "token", "an unchanged field is not a change")

	// red control: the first save, where everything is new, lists both keys
	require.Contains(t, string(pol.journal[1].Details), "token")
}

func TestInstanceSettings_JournalEnabledOnlyAndNoOp(t *testing.T) {
	pol := newFakePolicyRepo()
	h := NewExtensionInstanceSettingsHandler(&fakeInstRepo{}, fakeAdmins{ok: true}, nil, extensions.BuiltinEnvSources()).WithJournal(pol)
	body := `{"enabled":true,"settings":{"base_url":"https://git.one"}}`
	callInstanceSettings(t, h, http.MethodPut, "gitlab", body, false)
	require.Len(t, pol.journal, 1)

	// the same save again changes nothing: no entry
	callInstanceSettings(t, h, http.MethodPut, "gitlab", body, false)
	require.Len(t, pol.journal, 1, "a no-op save is not journaled")

	// only the flag flips: an entry that says so, with no keys
	callInstanceSettings(t, h, http.MethodPut, "gitlab", `{"enabled":false,"settings":{"base_url":"https://git.one"}}`, false)
	require.Len(t, pol.journal, 2)
	require.Contains(t, string(pol.journal[0].Details), `"enabled":false`)
	require.Contains(t, string(pol.journal[0].Details), `"keys":[]`)
}
