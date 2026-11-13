package telegram_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/extensions/telegram"
)

type conns struct {
	row *extensions.Connection
	err error
}

func (c conns) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return c.row, c.err
}

type inst struct {
	s   *extensions.InstanceSetting
	err error
}

func (i inst) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return i.s, i.err
}

func newProvider(c extensions.ConnectionStore, i extensions.InstanceStore) *telegram.Provider {
	return telegram.New(extensions.Deps{Conns: c, Inst: i})
}

func bot(active bool) *extensions.Connection {
	return &extensions.Connection{IsActive: active, Settings: map[string]any{"bot_token": "123:abc", "bot_username": "mesh_bot"}}
}

func TestResolveBot_WorkspaceRowDecidesInstanceIsOnlyASwitch(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()

	// no instance setting = on (the env default); the workspace's bot is used
	tok, user, ok := newProvider(conns{row: bot(true)}, inst{}).ResolveBot(ctx, ws)
	require.True(t, ok)
	require.Equal(t, "123:abc", tok)
	require.Equal(t, "mesh_bot", user)

	// instance entry enabled: same
	_, _, ok = newProvider(conns{row: bot(true)}, inst{s: &extensions.InstanceSetting{Enabled: true}}).ResolveBot(ctx, ws)
	require.True(t, ok)

	// instance switched off: no bot for any workspace, whatever the row says
	_, _, ok = newProvider(conns{row: bot(true)}, inst{s: &extensions.InstanceSetting{Enabled: false}}).ResolveBot(ctx, ws)
	require.False(t, ok)

	// no row, inactive row, token missing: nothing to talk to
	_, _, ok = newProvider(conns{}, inst{}).ResolveBot(ctx, ws)
	require.False(t, ok)
	_, _, ok = newProvider(conns{row: bot(false)}, inst{}).ResolveBot(ctx, ws)
	require.False(t, ok)
	_, _, ok = newProvider(conns{row: &extensions.Connection{IsActive: true, Settings: map[string]any{}}}, inst{}).ResolveBot(ctx, ws)
	require.False(t, ok)

	// a failed lookup is not an answer
	_, _, ok = newProvider(conns{err: errors.New("db down")}, inst{}).ResolveBot(ctx, ws)
	require.False(t, ok)
}

func TestResolve_SourceAndEnabled(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()
	r, err := newProvider(conns{row: bot(true)}, inst{}).Resolve(ctx, ws)
	require.NoError(t, err)
	require.True(t, r.Enabled)
	require.Equal(t, extensions.SourceWorkspace, r.Source)

	r, err = newProvider(conns{row: bot(false)}, inst{}).Resolve(ctx, ws)
	require.NoError(t, err)
	require.False(t, r.Enabled)
	require.Equal(t, extensions.SourceWorkspace, r.Source)
	require.Empty(t, r.Settings, "a switched-off bot exposes no settings")

	r, err = newProvider(conns{row: bot(true)}, inst{s: &extensions.InstanceSetting{Enabled: false}}).Resolve(ctx, ws)
	require.NoError(t, err)
	require.False(t, r.Enabled)
	require.Equal(t, extensions.SourceNone, r.Source)
}

func TestInstanceEnabled(t *testing.T) {
	ctx := context.Background()
	require.True(t, newProvider(nil, nil).InstanceEnabled(ctx))
	require.True(t, newProvider(nil, inst{}).InstanceEnabled(ctx))
	require.True(t, newProvider(nil, inst{s: &extensions.InstanceSetting{Enabled: true}}).InstanceEnabled(ctx))
	require.False(t, newProvider(nil, inst{s: &extensions.InstanceSetting{Enabled: false}}).InstanceEnabled(ctx))
	require.True(t, newProvider(nil, inst{err: errors.New("db")}).InstanceEnabled(ctx), "unreadable setting keeps the channel on")
}

func TestHealth_GetMeAndNoTokenLeak(t *testing.T) {
	status := http.StatusOK
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(status)
	}))
	p := newProvider(nil, nil)
	p.APIBase = srv.URL
	r := extensions.Resolved{Enabled: true, Settings: map[string]any{"bot_token": "123:abc"}}

	rep := p.Health(context.Background(), r)
	require.True(t, rep.OK, rep.Message)
	require.Equal(t, "/bot123:abc/getMe", gotPath)

	status = http.StatusUnauthorized
	rep = p.Health(context.Background(), r)
	require.False(t, rep.OK)
	require.Contains(t, rep.Message, "HTTP 401")

	srv.Close()
	rep = p.Health(context.Background(), r)
	require.False(t, rep.OK)
	require.NotContains(t, rep.Message, "123:abc", "the token must not reach a status response")

	require.False(t, p.Health(context.Background(), extensions.Resolved{Enabled: true}).OK)
}

func TestValidate(t *testing.T) {
	p := newProvider(nil, nil)
	require.NoError(t, p.Validate(extensions.LevelInstance, map[string]any{}))
	require.Error(t, p.Validate(extensions.LevelInstance, map[string]any{"bot_token": "x"}))
	require.NoError(t, p.Validate(extensions.LevelWorkspace, map[string]any{"bot_token": "123:abc"}))
	require.Error(t, p.Validate(extensions.LevelWorkspace, map[string]any{}))
	require.Error(t, p.Validate(extensions.LevelWorkspace, map[string]any{"bot_token": "  "}))
	require.Error(t, p.Validate(extensions.LevelProject, map[string]any{"bot_token": "x"}))
}

func TestManifest_ValidAgainstSchemaV1(t *testing.T) {
	raw, err := os.ReadFile("mesh-extension.json")
	require.NoError(t, err)
	sch, err := os.ReadFile("../schema/manifest.v1.json")
	require.NoError(t, err)
	sd, err := jsonschema.UnmarshalJSON(bytes.NewReader(sch))
	require.NoError(t, err)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	require.NoError(t, c.AddResource("manifest.v1.json", sd))
	compiled, err := c.Compile("manifest.v1.json")
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, compiled.Validate(inst))

	m := newProvider(nil, nil).Manifest()
	require.Equal(t, "telegram", m.ID)
	require.Empty(t, m.InstanceSecretKeys(), "the instance level is a switch with no secrets")
}

type fixedStore struct{ res secretstore.Result }

func (fixedStore) Put(context.Context, secretstore.Ref, string) (string, error) { return "", nil }
func (f fixedStore) Resolve(context.Context, string) secretstore.Result         { return f.res }

// A token the store cannot open, or opens to nothing, is "no bot", never an
// empty token passed on as usable.
func TestBotToken_UnopenableOrEmptyIsNoBot(t *testing.T) {
	prev := secretstore.SetDefault(fixedStore{secretstore.Result{Degraded: true, Reason: "keep down"}})
	t.Cleanup(func() { secretstore.SetDefault(prev) })
	r := extensions.Resolved{Enabled: true, Settings: map[string]any{"bot_token": "keep:x", "bot_username": "mesh_bot"}}
	_, _, ok := telegram.BotToken(r)
	require.False(t, ok, "degraded store")

	secretstore.SetDefault(fixedStore{secretstore.Result{Value: ""}})
	_, _, ok = telegram.BotToken(r)
	require.False(t, ok, "empty plaintext")

	secretstore.SetDefault(fixedStore{secretstore.Result{Value: "123:abc"}})
	tok, name, ok := telegram.BotToken(r)
	require.True(t, ok)
	require.Equal(t, "123:abc", tok)
	require.Equal(t, "mesh_bot", name)
}

// settingRows is instance_extension_settings in memory: Get returns the row
// written for an extension, nil when none.
type settingRows map[string]*domain.InstanceExtensionSetting

func (r settingRows) Get(_ context.Context, id string) (*domain.InstanceExtensionSetting, error) {
	return r[id], nil
}

func (r settingRows) InsertIfAbsent(_ context.Context, s *domain.InstanceExtensionSetting, _ []string) (bool, error) {
	if _, ok := r[s.ExtensionID]; ok {
		return false, nil
	}
	c := *s
	r[s.ExtensionID] = &c
	return true, nil
}

func (r settingRows) Upsert(_ context.Context, s *domain.InstanceExtensionSetting, _ []string) error {
	c := *s
	r[s.ExtensionID] = &c
	return nil
}

// The instance switch being off is reported, not only obeyed: the settings
// page reads Resolved.InstanceDisabled to say "disabled at instance level"
// instead of "not connected, ask an admin to connect a bot".
func TestResolve_InstanceOffIsReported(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()

	envOff := func(t *testing.T) extensions.InstanceStore {
		t.Setenv("MESH_TELEGRAM_ENABLED", "false")
		cfg := &config.Config{}
		cfg.Telegram.Enabled = false
		return &extensions.DBInstanceStore{Repo: settingRows{}, Cfg: cfg, Sources: extensions.BuiltinEnvSources()}
	}
	rowOff := func(*testing.T) extensions.InstanceStore {
		return &extensions.DBInstanceStore{Repo: settingRows{telegram.ID: {
			ExtensionID: telegram.ID, Enabled: false, Source: domain.InstanceSettingSourceEnv,
		}}}
	}

	for name, store := range map[string]func(*testing.T) extensions.InstanceStore{"row enabled=f": rowOff, "env false": envOff} {
		for rowName, row := range map[string]*extensions.Connection{"no bot": nil, "active bot": bot(true), "inactive bot": bot(false)} {
			t.Run(name+"/"+rowName, func(t *testing.T) {
				r, err := newProvider(conns{row: row}, store(t)).Resolve(ctx, ws)
				require.NoError(t, err)
				require.True(t, r.InstanceDisabled, "instance switch off must be reported")
				require.False(t, r.Enabled)
				require.Empty(t, r.Settings, "an instance-off channel exposes no bot settings")
			})
		}
	}

	// switched on or unset: nothing to report
	for name, i := range map[string]inst{"unset": {}, "on": {s: &extensions.InstanceSetting{Enabled: true}}, "unreadable": {err: errors.New("db")}} {
		r, err := newProvider(conns{row: bot(true)}, i).Resolve(ctx, ws)
		require.NoError(t, err, name)
		require.False(t, r.InstanceDisabled, name)
		require.True(t, r.Enabled, name)
	}
}
