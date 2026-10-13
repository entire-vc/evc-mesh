package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Effective-status sources reported to the Integrations screen.
const (
	IntegrationSourceWorkspace = "workspace"
	IntegrationSourceEnv       = "env"
	IntegrationSourceNone      = "none"
)

// EffectiveIntegrationStatus is what the runtime actually does for one
// provider in one workspace — the single answer the Integrations screen
// renders. It is produced by the same resolvers the runtime calls, so the UI
// and the runtime cannot disagree about whether an integration works.
type EffectiveIntegrationStatus struct {
	Provider         string `json:"provider"`
	EffectiveEnabled bool   `json:"effective_enabled"`
	// Source is "workspace" (an active, meaningful integration_configs row),
	// "env" (instance-level MESH_* variables) or "none".
	Source          string `json:"source"`
	HasWorkspaceRow bool   `json:"has_workspace_row"`
	// EnvAvailable is true when the instance env alone would run the
	// provider. Disabling a workspace row of such a provider blocks env.
	EnvAvailable bool `json:"env_available"`
	// Health is "ok" when effective_enabled, "disabled" when a workspace row
	// explicitly turns it off, "not_configured" otherwise.
	Health string `json:"health"`
}

// IntegrationStatusResolver computes EffectiveIntegrationStatus.
type IntegrationStatusResolver struct {
	repo            repository.IntegrationRepository
	vcs             *VCSIntegrationResolver
	spark           *SparkIntegrationResolver
	relay           *TeamRelayIntegrationResolver
	telegramEnabled bool
}

// NewIntegrationStatusResolver wires the status resolver on top of the
// runtime resolvers. telegramEnabled is the instance switch
// MESH_TELEGRAM_ENABLED: Telegram has no env credentials, only a switch.
func NewIntegrationStatusResolver(
	repo repository.IntegrationRepository,
	vcs *VCSIntegrationResolver,
	spark *SparkIntegrationResolver,
	relay *TeamRelayIntegrationResolver,
	telegramEnabled bool,
) *IntegrationStatusResolver {
	return &IntegrationStatusResolver{repo: repo, vcs: vcs, spark: spark, relay: relay, telegramEnabled: telegramEnabled}
}

// EnvAvailable reports whether the instance env alone would run provider.
func (r *IntegrationStatusResolver) EnvAvailable(provider domain.IntegrationProvider) bool {
	switch provider {
	case domain.IntegrationProviderGitHub:
		return r.vcs != nil && (r.vcs.env.GitHubToken != "" || r.vcs.env.GitHubWebhookSecret != "")
	case domain.IntegrationProviderGitLab:
		return r.vcs != nil && r.vcs.env.GitLabBaseURL != "" && r.vcs.env.GitLabToken != ""
	case domain.IntegrationProviderSpark:
		return r.spark != nil && r.spark.env.Enabled && r.spark.env.URL != ""
	case domain.IntegrationProviderTeamRelay:
		return r.relay != nil && r.relay.env.RelayURL != ""
	}
	return false
}

// Effective returns the status of every env-aware provider for workspaceID.
func (r *IntegrationStatusResolver) Effective(ctx context.Context, workspaceID uuid.UUID) []EffectiveIntegrationStatus {
	out := make([]EffectiveIntegrationStatus, 0, 5)
	add := func(p domain.IntegrationProvider, enabled bool, source string) {
		hasRow, rowActive := false, false
		if r.repo != nil {
			if row, err := r.repo.GetByProvider(ctx, workspaceID, p); err == nil && row != nil {
				hasRow, rowActive = true, row.IsActive
			}
		}
		if !enabled {
			source = IntegrationSourceNone
		}
		health := "not_configured"
		switch {
		case enabled:
			health = "ok"
		case hasRow && !rowActive:
			health = "disabled"
		}
		out = append(out, EffectiveIntegrationStatus{
			Provider: string(p), EffectiveEnabled: enabled, Source: source,
			HasWorkspaceRow: hasRow, EnvAvailable: r.EnvAvailable(p), Health: health,
		})
	}

	if r.vcs != nil {
		_, src, ok := r.vcs.ResolveGitHub(ctx, workspaceID)
		add(domain.IntegrationProviderGitHub, ok, src)
		_, src, ok = r.vcs.ResolveGitLab(ctx, workspaceID)
		add(domain.IntegrationProviderGitLab, ok, src)
	}
	if r.spark != nil {
		_, src, ok := r.spark.ResolveSparkURL(ctx, workspaceID)
		add(domain.IntegrationProviderSpark, ok, src)
	}
	if r.relay != nil {
		_, src, ok := r.relay.ResolveRelayURL(ctx, workspaceID)
		add(domain.IntegrationProviderTeamRelay, ok, src)
	}
	// Telegram: the bot is always a workspace row; env only switches the
	// whole channel on or off for the instance.
	tgOn := false
	if r.telegramEnabled && r.repo != nil {
		if row, err := r.repo.GetByProvider(ctx, workspaceID, domain.IntegrationProviderTelegram); err == nil && row != nil && row.IsActive {
			tgOn = true
		}
	}
	add(domain.IntegrationProviderTelegram, tgOn, IntegrationSourceWorkspace)
	return out
}
