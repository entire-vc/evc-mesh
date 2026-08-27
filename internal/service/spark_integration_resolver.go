package service

import (
	"context"
	"encoding/json"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// SparkIntegrationConfig is the shape stored in IntegrationConfig.Config for
// provider="spark". BaseURL is plaintext (it is not a secret — it is the
// catalog instance's URL, e.g. "https://spark.entire.vc"), mirroring
// GitLabIntegrationConfig's BaseURL field.
type SparkIntegrationConfig struct {
	BaseURL string `json:"base_url,omitempty"`
}

// decodeSparkIntegration is the spark analogue of decodeGitHubIntegration —
// same three-outcome contract (usable / disabled / neither):
//   - usable=true: the row governs, use `parsed`.
//   - usable=false, disabled=true: the row EXISTS and says `is_active=false`
//     — this workspace has explicitly turned Spark off. The caller MUST NOT
//     fall through to env (§4 of specsintegration-provider-contract:
//     "is_active=false означает «выключено», а не «читай env»").
//   - usable=false, disabled=false: nil row, wrong provider, unparseable
//     config, or an ACTIVE row with an empty base_url (a placeholder row —
//     e.g. the two prod rows this fix cleans up, active=true, config={} —
//     carries no information, so treating it as "governs, empty" would let
//     it silently shadow a working env URL). The caller falls through to
//     env for all of these.
func decodeSparkIntegration(cfg *domain.IntegrationConfig) (parsed SparkIntegrationConfig, usable, disabled bool) {
	if cfg == nil || cfg.Provider != domain.IntegrationProviderSpark {
		return SparkIntegrationConfig{}, false, false
	}
	if !cfg.IsActive {
		return SparkIntegrationConfig{}, false, true
	}
	if len(cfg.Config) > 0 {
		if err := json.Unmarshal(cfg.Config, &parsed); err != nil {
			return SparkIntegrationConfig{}, false, false
		}
	}
	if parsed.BaseURL == "" {
		return SparkIntegrationConfig{}, false, false
	}
	return parsed, true, false
}

// SparkEnvFallback carries the instance-wide env values (MESH_SPARK_URL,
// MESH_SPARK_ENABLED) that govern Spark when no workspace has configured its
// own connection — the "одноинстальная установка" branch of §4's resolution
// order. Both fields are required for the fallback to apply, matching the
// pre-C1 behavior this replaces: routes only ever worked when
// MESH_SPARK_ENABLED=true, regardless of whether MESH_SPARK_URL happened to
// be set. Checking URL alone would silently start serving Spark for any
// deployment that set the URL in preparation without having flipped Enabled
// yet — an operator-visible correctness change this fix is not meant to make.
type SparkEnvFallback struct {
	URL     string
	Enabled bool
}

// SparkIntegrationResolver resolves the Spark catalog base URL fresh on
// every call (§C1 of specsintegration-provider-contract) instead of once at
// process start, honoring §4's three-tier order: an active, non-empty
// workspace row wins wholly; otherwise the instance-wide env value;
// otherwise Spark is disabled for this workspace and the caller must refuse
// with a named reason (§4 item 3), never proceed with an empty URL.
//
// Spark's read endpoints (catalog Search/Popular/GetByID) carry no
// workspace_id in their path — they are a shared, instance-wide catalog
// browse, unlike GitHub/GitLab/TeamRelay which are always reached through a
// project/task with a resolvable workspace. Callers pass workspace_id as an
// optional query parameter (mirroring what Install already carries in its
// body); when omitted, ResolveSparkURL is called with uuid.Nil, which never
// matches a stored row (repo.GetByProvider legitimately returns "not
// found") and falls straight through to env — preserving today's behavior
// for any caller that does not yet send workspace_id.
type SparkIntegrationResolver struct {
	repo repository.IntegrationRepository
	env  SparkEnvFallback
}

// NewSparkIntegrationResolver creates a SparkIntegrationResolver. repo may
// be nil (falls straight through to env every time, useful for tests that
// don't care about workspace overrides).
func NewSparkIntegrationResolver(repo repository.IntegrationRepository, env SparkEnvFallback) *SparkIntegrationResolver {
	return &SparkIntegrationResolver{repo: repo, env: env}
}

// ResolveSparkURL returns the Spark catalog base URL governing workspaceID
// right now, and where it came from ("workspace" | "env"), or ok=false when
// either (a) neither a workspace row nor env supplies one, or (b) the
// workspace has its own row with is_active=false — Spark is disabled for
// this workspace either way, and the caller MUST refuse with a named
// reason, never proceed with an empty URL or silently fall back to env.
func (r *SparkIntegrationResolver) ResolveSparkURL(ctx context.Context, workspaceID uuid.UUID) (url, source string, ok bool) {
	if r.repo != nil {
		row, err := r.repo.GetByProvider(ctx, workspaceID, domain.IntegrationProviderSpark)
		if err != nil {
			log.Printf("[spark-integration] lookup failed for workspace %s: %v — falling back to env", workspaceID, err)
		} else if parsed, usable, disabled := decodeSparkIntegration(row); usable {
			return parsed.BaseURL, "workspace", true
		} else if disabled {
			// The workspace has a row and it says is_active=false — that is
			// this workspace's explicit decision and env does not override
			// it, however env is configured.
			return "", "", false
		}
	}
	if r.env.Enabled && r.env.URL != "" {
		return r.env.URL, "env", true
	}
	return "", "", false
}
