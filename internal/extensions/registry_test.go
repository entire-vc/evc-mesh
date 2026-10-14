package extensions_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

type fakeConns struct {
	row *extensions.Connection
	err error
}

func (f fakeConns) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return f.row, f.err
}

type fakeInst struct{ s *extensions.InstanceSetting }

func (f fakeInst) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return f.s, nil
}

// stubProvider is the test provider: shared Resolve via BaseProvider, own
// Validate, one route and one event handler.
type stubProvider struct {
	extensions.BaseProvider
	handled *int
}

func (stubProvider) Validate(extensions.Level, map[string]any) error { return nil }

func (p stubProvider) Hooks() extensions.Hooks {
	return extensions.Hooks{
		Routes: []extensions.Route{{
			Method:  http.MethodPost,
			Path:    "/webhooks/stub",
			Handler: func(c echo.Context) error { return c.String(http.StatusOK, "stub") },
		}},
		EventHandlers: map[string]extensions.EventHandler{
			"task.updated": func(context.Context, uuid.UUID, []byte) error { *p.handled++; return nil },
		},
	}
}

func stubManifest(t *testing.T) extensions.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("schema", "fixtures", "valid", "gitlab-builtin.json"))
	require.NoError(t, err)
	m, err := extensions.ParseManifest(raw)
	require.NoError(t, err)
	return m
}

func newStub(t *testing.T, row *extensions.Connection, inst *extensions.InstanceSetting) (p stubProvider, handled *int) {
	n := 0
	return stubProvider{
		BaseProvider: extensions.BaseProvider{Man: stubManifest(t), Conns: fakeConns{row: row}, Inst: fakeInst{s: inst}},
		handled:      &n,
	}, &n
}

func TestResolve_SixCases(t *testing.T) {
	ws := uuid.New()
	rowSettings := map[string]any{"base_url": "https://ws.example"}
	instSettings := map[string]any{"base_url": "https://inst.example"}

	tests := []struct {
		name         string
		row          *extensions.Connection
		inst         *extensions.InstanceSetting
		wantEnabled  bool
		wantSource   extensions.Source
		wantSettings map[string]any
	}{
		{"row active", &extensions.Connection{Settings: rowSettings, IsActive: true}, &extensions.InstanceSetting{Settings: instSettings, Enabled: true}, true, extensions.SourceWorkspace, rowSettings},
		{"row inactive does not fall through to instance", &extensions.Connection{Settings: rowSettings, IsActive: false}, &extensions.InstanceSetting{Settings: instSettings, Enabled: true}, false, extensions.SourceWorkspace, nil},
		{"instance enabled", nil, &extensions.InstanceSetting{Settings: instSettings, Enabled: true}, true, extensions.SourceInstance, instSettings},
		{"instance disabled", nil, &extensions.InstanceSetting{Settings: instSettings, Enabled: false}, false, extensions.SourceNone, nil},
		{"nothing, no instance entry", nil, nil, false, extensions.SourceNone, nil},
		{"row inactive and nothing at instance", &extensions.Connection{IsActive: false}, nil, false, extensions.SourceWorkspace, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newStub(t, tc.row, tc.inst)
			reg := extensions.NewRegistry()
			require.NoError(t, reg.Register(p))

			got, err := reg.Resolve(context.Background(), ws, "gitlab")
			require.NoError(t, err)
			require.Equal(t, tc.wantEnabled, got.Enabled)
			require.Equal(t, tc.wantSource, got.Source)
			require.Equal(t, tc.wantSettings, got.Settings)
			require.Equal(t, "gitlab", got.ExtensionID)
		})
	}
}

func TestResolve_StoreErrorPropagates(t *testing.T) {
	boom := errors.New("db down")
	_, err := extensions.ResolveSettings(context.Background(), fakeConns{err: boom}, nil, uuid.New(), "gitlab")
	require.ErrorIs(t, err, boom)
}

func TestRegistry_RegisterRules(t *testing.T) {
	reg := extensions.NewRegistry()
	p, _ := newStub(t, nil, nil)
	require.NoError(t, reg.Register(p))
	require.Error(t, reg.Register(p), "duplicate id must be refused")
	require.Error(t, reg.Register(stubProvider{}), "empty id must be refused")

	_, ok := reg.Get("gitlab")
	require.True(t, ok)
	_, ok = reg.Get("nope")
	require.False(t, ok)
	_, err := reg.Resolve(context.Background(), uuid.New(), "nope")
	require.Error(t, err)
}

func TestRegistry_HooksWalk(t *testing.T) {
	reg := extensions.NewRegistry()
	p, handled := newStub(t, nil, nil)
	require.NoError(t, reg.Register(p))

	e := echo.New()
	reg.RegisterRoutes(e)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhooks/stub", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "stub", rec.Body.String())

	hs := reg.EventHandlers("task.updated")
	require.Len(t, hs, 1)
	require.NoError(t, hs[0](context.Background(), uuid.New(), nil))
	require.Equal(t, 1, *handled)
	require.Empty(t, reg.EventHandlers("comment.created"))
}

type fakeInstalledRepo struct{ got []*domain.InstalledExtension }

func (f *fakeInstalledRepo) UpsertBuiltin(_ context.Context, e *domain.InstalledExtension) error {
	f.got = append(f.got, e)
	return nil
}
func (f *fakeInstalledRepo) Get(context.Context, string) (*domain.InstalledExtension, error) {
	return nil, nil
}
func (f *fakeInstalledRepo) List(context.Context) ([]domain.InstalledExtension, error) {
	return nil, nil
}

func TestRegistry_SyncBuiltin(t *testing.T) {
	reg := extensions.NewRegistry()
	p, _ := newStub(t, nil, nil)
	require.NoError(t, reg.Register(p))

	repo := &fakeInstalledRepo{}
	require.NoError(t, reg.SyncBuiltin(context.Background(), repo))
	require.Len(t, repo.got, 1)
	require.Equal(t, "gitlab", repo.got[0].ExtensionID)
	require.Equal(t, "1.4.0", repo.got[0].Version)
	require.Equal(t, domain.ExtensionSourceBuiltin, repo.got[0].Source)
	require.JSONEq(t, string(p.Man.Raw), string(repo.got[0].Manifest))
}

func TestParseManifest(t *testing.T) {
	_, err := extensions.ParseManifest([]byte(`{"id":"x"}`))
	require.Error(t, err)
	_, err = extensions.ParseManifest([]byte(`not json`))
	require.Error(t, err)
}

func TestDecodeSettings(t *testing.T) {
	type cfg struct {
		BaseURL string `json:"base_url"`
	}
	got, err := extensions.DecodeSettings[cfg](map[string]any{"base_url": "https://x"})
	require.NoError(t, err)
	require.Equal(t, "https://x", got.BaseURL)
	_, err = extensions.DecodeSettings[cfg](map[string]any{"base_url": 5})
	require.Error(t, err)
}
