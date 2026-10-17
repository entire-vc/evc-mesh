package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/spark"
	teamrelay "github.com/entire-vc/evc-mesh/internal/extensions/team-relay"
)

func statusOf(t *testing.T, all []EffectiveIntegrationStatus, p domain.IntegrationProvider) EffectiveIntegrationStatus {
	t.Helper()
	for _, s := range all {
		if s.Provider == string(p) {
			return s
		}
	}
	t.Fatalf("no status for %s", p)
	return EffectiveIntegrationStatus{}
}

// sparkInstance is an extensions.InstanceStore with one fixed Spark setting.
type sparkInstance struct{ s *extensions.InstanceSetting }

func (f sparkInstance) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return f.s, nil
}

func newStatusResolver(repo *fakeVCSIntegrationRepo, telegram bool) *IntegrationStatusResolver {
	return NewIntegrationStatusResolver(repo,
		NewVCSIntegrationResolver(repo, VCSEnvFallback{
			GitLabBaseURL: "https://git.example", GitLabToken: "t", GitHubToken: "g",
		}),
		spark.New(extensions.Deps{
			Conns: extensions.IntegrationConnections{Repo: repo},
			Inst:  sparkInstance{&extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"spark_url": "https://spark.example"}}},
		}),
		teamrelay.New(extensions.Deps{
			Conns: extensions.IntegrationConnections{Repo: repo},
			Inst:  sparkInstance{},
		}),
		telegram)
}

// Prod trap (#a4d108eb): no gitlab row, env configured -> the runtime works,
// so the screen must say enabled via env.
func TestEffectiveStatus_NoRowEnvSet_EnabledViaEnv(t *testing.T) {
	ws := uuid.New()
	got := statusOf(t, newStatusResolver(newFakeVCSIntegrationRepo(), true).Effective(context.Background(), ws), domain.IntegrationProviderGitLab)
	assert.True(t, got.EffectiveEnabled)
	assert.Equal(t, "env", got.Source)
	assert.False(t, got.HasWorkspaceRow)
	assert.True(t, got.EnvAvailable)
	assert.Equal(t, "ok", got.Health)
}

// A config:{} placeholder row must not count as a workspace source.
func TestEffectiveStatus_PlaceholderRowIsNotWorkspaceSource(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderSpark, IsActive: true, Config: json.RawMessage(`{}`)})
	got := statusOf(t, newStatusResolver(repo, true).Effective(context.Background(), ws), domain.IntegrationProviderSpark)
	assert.True(t, got.EffectiveEnabled)
	assert.Equal(t, "env", got.Source)
	assert.True(t, got.HasWorkspaceRow)
}

func TestEffectiveStatus_DisabledRowBlocksEnv(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: false, Config: json.RawMessage(`{}`)})
	got := statusOf(t, newStatusResolver(repo, true).Effective(context.Background(), ws), domain.IntegrationProviderGitLab)
	assert.False(t, got.EffectiveEnabled)
	assert.Equal(t, "none", got.Source)
	assert.Equal(t, "disabled", got.Health)
	assert.True(t, got.EnvAvailable)
}

func TestEffectiveStatus_NothingConfigured(t *testing.T) {
	got := statusOf(t, newStatusResolver(newFakeVCSIntegrationRepo(), true).Effective(context.Background(), uuid.New()), domain.IntegrationProviderTeamRelay)
	assert.False(t, got.EffectiveEnabled)
	assert.Equal(t, "none", got.Source)
	assert.Equal(t, "not_configured", got.Health)
}

func TestEffectiveStatus_Telegram(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderTelegram, IsActive: true, Config: json.RawMessage(`{"bot_token":"x"}`)})
	on := statusOf(t, newStatusResolver(repo, true).Effective(context.Background(), ws), domain.IntegrationProviderTelegram)
	require.True(t, on.EffectiveEnabled)
	off := statusOf(t, newStatusResolver(repo, false).Effective(context.Background(), ws), domain.IntegrationProviderTelegram)
	assert.False(t, off.EffectiveEnabled, "MESH_TELEGRAM_ENABLED=false switches the channel off whatever the row says")
}

// The instance level alone runs Team Relay: the screen says "env" (its word for
// the instance level) and env_available, with no workspace row.
func TestEffectiveStatus_TeamRelayInstanceLevelReadsAsEnv(t *testing.T) {
	repo := newFakeVCSIntegrationRepo()
	relay := teamrelay.New(extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: repo},
		Inst:  sparkInstance{&extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"relay_url": "https://relay.example"}}},
	})
	r := NewIntegrationStatusResolver(repo, nil, nil, relay, false)
	got := statusOf(t, r.Effective(context.Background(), uuid.New()), domain.IntegrationProviderTeamRelay)
	assert.True(t, got.EffectiveEnabled)
	assert.Equal(t, "env", got.Source)
	assert.False(t, got.HasWorkspaceRow)
	assert.True(t, got.EnvAvailable)
	assert.Equal(t, "ok", got.Health)
}

// An explicit is_active=false workspace row switches Team Relay off even though
// the instance level is available.
func TestEffectiveStatus_TeamRelayDisabledRowBlocksInstance(t *testing.T) {
	ws := uuid.New()
	repo := newFakeVCSIntegrationRepo()
	repo.put(ws, domain.IntegrationConfig{Provider: domain.IntegrationProviderTeamRelay, IsActive: false, Config: json.RawMessage(`{"relay_url":"https://ws.example"}`)})
	relay := teamrelay.New(extensions.Deps{
		Conns: extensions.IntegrationConnections{Repo: repo},
		Inst:  sparkInstance{&extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"relay_url": "https://relay.example"}}},
	})
	got := statusOf(t, NewIntegrationStatusResolver(repo, nil, nil, relay, false).Effective(context.Background(), ws), domain.IntegrationProviderTeamRelay)
	assert.False(t, got.EffectiveEnabled)
	assert.Equal(t, "none", got.Source)
	assert.Equal(t, "disabled", got.Health)
	assert.True(t, got.EnvAvailable)
}

// Without a Team Relay resolver wired the screen reports no env availability
// rather than dereferencing nil.
func TestEffectiveStatus_TeamRelayUnwiredIsNotEnvAvailable(t *testing.T) {
	r := NewIntegrationStatusResolver(newFakeVCSIntegrationRepo(), nil, nil, nil, false)
	assert.False(t, r.EnvAvailable(domain.IntegrationProviderTeamRelay))
}
