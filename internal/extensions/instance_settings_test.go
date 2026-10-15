package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/domain"
)

type fakeSettingRepo struct {
	rows   map[string]*domain.InstanceExtensionSetting
	getErr error
	insErr error
}

func newFakeSettingRepo() *fakeSettingRepo {
	return &fakeSettingRepo{rows: map[string]*domain.InstanceExtensionSetting{}}
}

func (f *fakeSettingRepo) Get(_ context.Context, id string) (*domain.InstanceExtensionSetting, error) {
	return f.rows[id], f.getErr
}

func (f *fakeSettingRepo) InsertIfAbsent(_ context.Context, s *domain.InstanceExtensionSetting, _ []string) (bool, error) {
	if f.insErr != nil {
		return false, f.insErr
	}
	if _, ok := f.rows[s.ExtensionID]; ok {
		return false, nil
	}
	c := *s
	f.rows[s.ExtensionID] = &c
	return true, nil
}

func (f *fakeSettingRepo) Upsert(_ context.Context, s *domain.InstanceExtensionSetting, _ []string) error {
	c := *s
	f.rows[s.ExtensionID] = &c
	return nil
}

func envConfig() *config.Config {
	c := &config.Config{}
	c.Webhook.GitLabURL = "https://git.example.com"
	c.Webhook.GitLabToken = "glpat-x"
	c.Webhook.GitHubToken = "ghp-x"
	c.Spark.URL = "https://spark.example.com"
	c.Spark.Enabled = true
	c.TeamRelay.RelayURL = "https://relay.example.com"
	return c
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestBootstrap_CreatesEnvRowsAndIsIdempotent(t *testing.T) {
	t.Setenv("MESH_TELEGRAM_ENABLED", "true")
	repo := newFakeSettingRepo()
	cfg := envConfig()
	cfg.Telegram.Enabled = true

	require.NoError(t, BootstrapInstanceSettings(context.Background(), repo, cfg, BuiltinEnvSources()))
	require.Len(t, repo.rows, 5)
	for id, r := range repo.rows {
		require.Equal(t, domain.InstanceSettingSourceEnv, r.Source, id)
		require.True(t, r.Enabled, id)
	}
	var gl map[string]any
	require.NoError(t, json.Unmarshal(repo.rows["gitlab"].Settings, &gl))
	require.Equal(t, "https://git.example.com", gl["base_url"])

	// An admin edit must survive the next start; a second start adds nothing.
	repo.rows["spark"].Source = domain.InstanceSettingSourceAdmin
	repo.rows["spark"].Enabled = false
	require.NoError(t, BootstrapInstanceSettings(context.Background(), repo, cfg, BuiltinEnvSources()))
	require.Len(t, repo.rows, 5)
	require.Equal(t, domain.InstanceSettingSourceAdmin, repo.rows["spark"].Source)
	require.False(t, repo.rows["spark"].Enabled)
}

// Red control: no env and empty table → nothing is created and the start log
// says each provider is off.
func TestBootstrap_NoEnvNoRow_LogsDisabled(t *testing.T) {
	buf := captureLog(t)
	repo := newFakeSettingRepo()
	require.NoError(t, BootstrapInstanceSettings(context.Background(), repo, &config.Config{}, BuiltinEnvSources()))
	require.Empty(t, repo.rows)
	for _, id := range []string{"gitlab", "github", "spark", "team_relay"} {
		require.Contains(t, buf.String(), id+": no instance setting and no MESH_* env — disabled")
	}
}

func TestBootstrap_SparkURLWithoutEnabledWritesDisabledRow(t *testing.T) {
	repo := newFakeSettingRepo()
	cfg := &config.Config{}
	cfg.Spark.URL = "https://spark.example.com"
	require.NoError(t, BootstrapInstanceSettings(context.Background(), repo, cfg, BuiltinEnvSources()))
	require.False(t, repo.rows["spark"].Enabled)
}

// The push transport switch moves from the relay client's os.Getenv into the
// instance settings together with relay_url.
func TestBootstrap_TeamRelayCarriesTransportSwitch(t *testing.T) {
	cfg := &config.Config{}
	cfg.TeamRelay.RelayURL = "https://relay.example.com"
	for env, want := range map[string]bool{"true": true, "false": false, "": false} {
		t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", env)
		repo := newFakeSettingRepo()
		require.NoError(t, BootstrapInstanceSettings(context.Background(), repo, cfg, BuiltinEnvSources()))
		var got map[string]any
		require.NoError(t, json.Unmarshal(repo.rows["team_relay"].Settings, &got))
		require.Equal(t, want, got["transport_enabled"], "env=%q", env)
	}
}

func TestDBInstanceStore_RowWinsOverEnv_EnvFallbackWarnsOnce(t *testing.T) {
	buf := captureLog(t)
	repo := newFakeSettingRepo()
	store := &DBInstanceStore{Repo: repo, Cfg: envConfig(), Sources: BuiltinEnvSources()}

	// no row → env fallback with a warning, once
	for i := 0; i < 2; i++ {
		got, err := store.InstanceSetting(context.Background(), "team_relay")
		require.NoError(t, err)
		require.True(t, got.Enabled)
		require.Equal(t, "https://relay.example.com", got.Settings["relay_url"])
	}
	require.Equal(t, 1, strings.Count(buf.String(), "настройка team_relay взята из env, перенесите в настройки инстанса"))

	// row present (even disabled) → the row governs, env is ignored
	repo.rows["team_relay"] = &domain.InstanceExtensionSetting{ExtensionID: "team_relay", Settings: json.RawMessage(`{"relay_url":"https://admin"}`), Enabled: false}
	got, err := store.InstanceSetting(context.Background(), "team_relay")
	require.NoError(t, err)
	require.False(t, got.Enabled)
	require.Equal(t, "https://admin", got.Settings["relay_url"])

	// no row, no env → nil
	store.Cfg = &config.Config{}
	got, err = store.InstanceSetting(context.Background(), "gitlab")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestBootstrap_StorageErrorsAbortStartup(t *testing.T) {
	srcs := BuiltinEnvSources()

	// env set, insert fails -> start must fail loudly, not continue half-migrated
	repo := newFakeSettingRepo()
	repo.insErr = errors.New("insert down")
	require.ErrorContains(t, BootstrapInstanceSettings(context.Background(), repo, envConfig(), srcs), "insert down")

	// env unset, lookup fails -> same
	repo = newFakeSettingRepo()
	repo.getErr = errors.New("read down")
	require.ErrorContains(t, BootstrapInstanceSettings(context.Background(), repo, &config.Config{}, srcs), "read down")
}

func TestDBInstanceStore_ErrorsAndUnknownExtension(t *testing.T) {
	ctx := context.Background()
	srcs := BuiltinEnvSources()

	store := &DBInstanceStore{Repo: &fakeSettingRepo{rows: map[string]*domain.InstanceExtensionSetting{}, getErr: errors.New("down")}, Cfg: envConfig(), Sources: srcs}
	_, err := store.InstanceSetting(ctx, "gitlab")
	require.Error(t, err)

	// a stored row whose JSON is corrupt is an error, never an empty enabled setting
	repo := newFakeSettingRepo()
	repo.rows["gitlab"] = &domain.InstanceExtensionSetting{ExtensionID: "gitlab", Enabled: true, Settings: []byte(`{not json`)}
	store = &DBInstanceStore{Repo: repo, Cfg: envConfig(), Sources: srcs}
	_, err = store.InstanceSetting(ctx, "gitlab")
	require.ErrorContains(t, err, "bad settings")

	// no row, no env source for this id -> not configured
	store = &DBInstanceStore{Repo: newFakeSettingRepo(), Cfg: envConfig(), Sources: srcs}
	got, err := store.InstanceSetting(ctx, "no-such-ext")
	require.NoError(t, err)
	require.Nil(t, got)

	// no row, env source present but env empty -> not configured
	got, err = (&DBInstanceStore{Repo: newFakeSettingRepo(), Cfg: &config.Config{}, Sources: srcs}).InstanceSetting(ctx, "gitlab")
	require.NoError(t, err)
	require.Nil(t, got)
}
