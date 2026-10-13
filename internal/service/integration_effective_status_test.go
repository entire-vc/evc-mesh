package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
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

func newStatusResolver(repo *fakeVCSIntegrationRepo, telegram bool) *IntegrationStatusResolver {
	return NewIntegrationStatusResolver(repo,
		NewVCSIntegrationResolver(repo, VCSEnvFallback{
			GitLabBaseURL: "https://git.example", GitLabToken: "t", GitHubToken: "g",
		}),
		NewSparkIntegrationResolver(repo, SparkEnvFallback{URL: "https://spark.example", Enabled: true}),
		NewTeamRelayIntegrationResolver(repo, TeamRelayEnvFallback{}),
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
