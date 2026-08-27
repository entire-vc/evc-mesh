package service

import (
	"context"
	"encoding/json"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// TeamRelayIntegrationConfig is the shape stored in IntegrationConfig.Config
// for provider="team_relay". RelayURL is not a secret — it is the relay's
// base URL (e.g. "https://cp.tr.entire.vc") — so unlike GitHubIntegrationConfig's
// Token it round-trips as plaintext, the same way GitLabIntegrationConfig.BaseURL
// does.
//
// This is the CONNECTION layer only (specsintegration-provider-contract §2):
// where the relay lives. The per-project BINDING — which share, which
// subfolder, the sync agent key — lives in project_integrations and is a
// separate table this resolver never reads or writes.
type TeamRelayIntegrationConfig struct {
	RelayURL string `json:"relay_url,omitempty"`
}

// TeamRelayEnvFallback carries the instance-wide MESH_TEAMRELAY_RELAY_URL
// value (read once at process start into cfg.TeamRelay.RelayURL — env
// doesn't change without a restart anyway) — the "one instance, one relay"
// fallback §4 of specsintegration-provider-contract describes, used only
// when no workspace has configured its own team_relay row.
type TeamRelayEnvFallback struct {
	RelayURL string
}

// decodeTeamRelayIntegration is the team_relay analogue of
// decodeGitHubIntegration/decodeGitLabIntegration (vcs_integration_resolver.go).
// Three outcomes a caller needs to tell apart:
//   - usable=true: the row governs, use `parsed`.
//   - usable=false, disabled=true: the row EXISTS and says `is_active=false`
//     — the workspace has explicitly turned Team Relay off. The caller MUST
//     NOT fall through to env; doing so is the exact bug #33a4bb57 AC2 fixed
//     for GitHub/GitLab (a workspace turning its integration off must
//     actually turn it off, not silently keep working via the instance-wide
//     fallback).
//   - usable=false, disabled=false: nil row, wrong provider, unparseable
//     config, or an ACTIVE row with an empty relay_url (a placeholder row
//     carries no information, so treating it as "governs, empty" would let
//     it silently shadow a working env value). The caller falls through to
//     env for all of these.
func decodeTeamRelayIntegration(cfg *domain.IntegrationConfig) (parsed TeamRelayIntegrationConfig, usable, disabled bool) {
	if cfg == nil || cfg.Provider != domain.IntegrationProviderTeamRelay {
		return TeamRelayIntegrationConfig{}, false, false
	}
	if !cfg.IsActive {
		return TeamRelayIntegrationConfig{}, false, true
	}
	if len(cfg.Config) > 0 {
		if err := json.Unmarshal(cfg.Config, &parsed); err != nil {
			return TeamRelayIntegrationConfig{}, false, false
		}
	}
	if parsed.RelayURL == "" {
		return TeamRelayIntegrationConfig{}, false, false
	}
	return parsed, true, false
}

// TeamRelayIntegrationResolver resolves the Team Relay base URL governing a
// workspace fresh on every call (specsintegration-provider-contract §C1)
// instead of once at process start, honoring the same three-tier §4 order as
// VCSIntegrationResolver: an active, non-empty workspace row wins WHOLLY;
// otherwise the instance-wide env value; otherwise the provider is disabled
// and the caller must refuse with a named reason rather than proceeding with
// an empty URL. A workspace row that exists with `is_active=false` is a
// fourth, terminal outcome distinct from "no row": it means the workspace
// explicitly turned Team Relay off, and env must NOT override that (see
// decodeTeamRelayIntegration's doc comment — #33a4bb57 AC2).
//
// This resolver only ever answers "where does the relay live" — it does not
// touch project_integrations (which share/subfolder/agent key a project is
// bound to). Replaces the os.Getenv("MESH_TEAMRELAY_RELAY_URL") calls that
// used to live directly in tr_search_handler.go and tr_document_handler.go,
// where a typo in the env var name surfaced only at request time.
type TeamRelayIntegrationResolver struct {
	repo repository.IntegrationRepository
	env  TeamRelayEnvFallback
}

// NewTeamRelayIntegrationResolver creates a TeamRelayIntegrationResolver.
// repo may be nil (falls straight through to env every time — useful for
// tests that don't care about workspace overrides).
func NewTeamRelayIntegrationResolver(repo repository.IntegrationRepository, env TeamRelayEnvFallback) *TeamRelayIntegrationResolver {
	return &TeamRelayIntegrationResolver{repo: repo, env: env}
}

// ResolveRelayURL returns the relay base URL governing workspaceID right
// now, and where it came from ("workspace" | "env"), or ok=false when
// either (a) neither a workspace row nor env supplies one, or (b) the
// workspace has its own row with is_active=false — Team Relay is disabled
// for this workspace either way, and the caller MUST refuse with a named
// reason, never proceed with an empty URL or silently fall back to env.
func (r *TeamRelayIntegrationResolver) ResolveRelayURL(ctx context.Context, workspaceID uuid.UUID) (relayURL, source string, ok bool) {
	if r.repo != nil {
		row, err := r.repo.GetByProvider(ctx, workspaceID, domain.IntegrationProviderTeamRelay)
		if err != nil {
			log.Printf("[teamrelay-integration] lookup failed for workspace %s: %v — falling back to env", workspaceID, err)
		} else if parsed, usable, disabled := decodeTeamRelayIntegration(row); usable {
			return parsed.RelayURL, "workspace", true
		} else if disabled {
			// The workspace has a row and it says is_active=false — that is
			// this workspace's explicit decision and env does not override
			// it, however env is configured.
			return "", "", false
		}
	}
	if r.env.RelayURL != "" {
		return r.env.RelayURL, "env", true
	}
	return "", "", false
}
