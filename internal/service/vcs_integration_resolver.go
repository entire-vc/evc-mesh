package service

import (
	"context"
	"encoding/json"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// GitHubIntegrationConfig is the shape stored in IntegrationConfig.Config
// for provider="github". Token and WebhookSecret are AES-256-GCM ciphertext
// from pkg/encryption when non-empty (never the bare secret) — mirrors
// TelegramIntegrationConfig's contract (telegram_bot_manager.go), safe to
// round-trip through the database and, with both fields stripped, through
// an API response.
type GitHubIntegrationConfig struct {
	Token         string `json:"token,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// GitLabIntegrationConfig is the shape stored in IntegrationConfig.Config
// for provider="gitlab". BaseURL is plaintext (it is not a secret — it is
// the self-hosted instance's URL, e.g. "https://git.entire.host"); Token and
// WebhookSecret are encrypted the same way GitHubIntegrationConfig's are.
type GitLabIntegrationConfig struct {
	BaseURL       string `json:"base_url,omitempty"`
	Token         string `json:"token,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// decodeGitHubIntegration extracts a usable GitHubIntegrationConfig (token
// and webhook secret already decrypted) from an integration row, or
// ok=false for every "this row cannot govern anything" case: nil, inactive,
// wrong provider, or a row that carries neither a token nor a webhook
// secret. That last case is deliberate, not an oversight: an active row
// with a fully empty config carries no information, so treating it as
// "governs, with empty values" would let a placeholder row (created before
// this feature existed — see the resolver's package doc) silently disable a
// working env-configured secret. Absence of USABLE configuration is folded
// into the same "no row" outcome the caller falls through to env for.
func decodeGitHubIntegration(cfg *domain.IntegrationConfig) (GitHubIntegrationConfig, bool) {
	if cfg == nil || !cfg.IsActive || cfg.Provider != domain.IntegrationProviderGitHub {
		return GitHubIntegrationConfig{}, false
	}
	var parsed GitHubIntegrationConfig
	if len(cfg.Config) > 0 {
		if err := json.Unmarshal(cfg.Config, &parsed); err != nil {
			return GitHubIntegrationConfig{}, false
		}
	}
	if parsed.Token == "" && parsed.WebhookSecret == "" {
		return GitHubIntegrationConfig{}, false
	}
	if parsed.Token != "" {
		if plain, err := encryption.Decrypt(parsed.Token); err == nil {
			parsed.Token = plain
		} else {
			parsed.Token = ""
		}
	}
	if parsed.WebhookSecret != "" {
		if plain, err := encryption.Decrypt(parsed.WebhookSecret); err == nil {
			parsed.WebhookSecret = plain
		} else {
			parsed.WebhookSecret = ""
		}
	}
	return parsed, true
}

// decodeGitLabIntegration is decodeGitHubIntegration's GitLab counterpart.
// BaseURL is not encrypted (it is not a secret) so it round-trips as-is;
// Token and WebhookSecret follow the same decrypt-or-drop rule.
func decodeGitLabIntegration(cfg *domain.IntegrationConfig) (GitLabIntegrationConfig, bool) {
	if cfg == nil || !cfg.IsActive || cfg.Provider != domain.IntegrationProviderGitLab {
		return GitLabIntegrationConfig{}, false
	}
	var parsed GitLabIntegrationConfig
	if len(cfg.Config) > 0 {
		if err := json.Unmarshal(cfg.Config, &parsed); err != nil {
			return GitLabIntegrationConfig{}, false
		}
	}
	if parsed.BaseURL == "" && parsed.Token == "" && parsed.WebhookSecret == "" {
		return GitLabIntegrationConfig{}, false
	}
	if parsed.Token != "" {
		if plain, err := encryption.Decrypt(parsed.Token); err == nil {
			parsed.Token = plain
		} else {
			parsed.Token = ""
		}
	}
	if parsed.WebhookSecret != "" {
		if plain, err := encryption.Decrypt(parsed.WebhookSecret); err == nil {
			parsed.WebhookSecret = plain
		} else {
			parsed.WebhookSecret = ""
		}
	}
	return parsed, true
}

// VCSEnvFallback carries the instance-wide env values (MESH_GITHUB_*,
// MESH_GITLAB_*) that govern GitHub/GitLab when no workspace has configured
// its own connection — the "одноинстальная установка" branch of §4's
// resolution order. Read once at process start (env doesn't change without
// a restart anyway) and handed to VCSIntegrationResolver as a plain value.
type VCSEnvFallback struct {
	GitHubToken         string
	GitHubWebhookSecret string
	GitLabBaseURL       string
	GitLabToken         string
	GitLabWebhookSecret string
}

// VCSIntegrationResolver resolves GitHub/GitLab connection details fresh on
// every call (§C1 of specsintegration-provider-contract) instead of once at
// process start, honoring the three-tier order §4 of that spec defines for
// every provider: an active, meaningfully-configured workspace row wins
// wholly; otherwise the instance-wide env value wins wholly; otherwise the
// provider is disabled. "Wins wholly" — never smeared with env for the
// fields the row left empty — is deliberate (§4: "не смешивать уровни
// внутри одного провайдера").
//
// GitHub/GitLab differ from Slack/Telegram in one respect this resolver has
// to account for: both are reached through a SINGLE shared webhook endpoint
// (/webhooks/github, /webhooks/gitlab) with no workspace_id in the URL, so
// which workspace's config should validate an inbound delivery isn't known
// until after the payload names a task. The workspace-scoped Resolve*
// methods below are for callers that DO already know the workspace (the
// done-evidence gate's live re-check, once it has resolved the link's task
// and project); the *WebhookSecrets methods are for the webhook receiver,
// which validates against every currently-active workspace's secret (each
// workspace still gets a working channel) rather than guessing one.
//
// KNOWN LIMITATION, named rather than hidden: with more than one workspace
// simultaneously configuring ITS OWN GitHub/GitLab connection, the webhook
// receiver has no way to route an inbound delivery to "the" workspace it
// belongs to (that would need a repo→project→workspace mapping this
// codebase does not have yet) — it can only accept-if-any-secret-matches.
// True per-repo routing is out of scope for this change; see the parent
// spec's §7 for what's deliberately not covered here.
type VCSIntegrationResolver struct {
	repo repository.IntegrationRepository
	env  VCSEnvFallback
}

// NewVCSIntegrationResolver creates a VCSIntegrationResolver. repo may be
// nil (falls straight through to env every time, useful for tests that
// don't care about workspace overrides).
func NewVCSIntegrationResolver(repo repository.IntegrationRepository, env VCSEnvFallback) *VCSIntegrationResolver {
	return &VCSIntegrationResolver{repo: repo, env: env}
}

// ResolveGitHub returns the GitHub config governing workspaceID right now,
// and where it came from ("workspace" | "env"), or ok=false when neither a
// workspace row nor env supplies anything usable — the provider is
// disabled for this workspace.
func (r *VCSIntegrationResolver) ResolveGitHub(ctx context.Context, workspaceID uuid.UUID) (cfg GitHubIntegrationConfig, source string, ok bool) {
	if r.repo != nil {
		row, err := r.repo.GetByProvider(ctx, workspaceID, domain.IntegrationProviderGitHub)
		if err != nil {
			log.Printf("[vcs-integration] github lookup failed for workspace %s: %v — falling back to env", workspaceID, err)
		} else if parsed, usable := decodeGitHubIntegration(row); usable {
			return parsed, "workspace", true
		}
	}
	if r.env.GitHubToken != "" || r.env.GitHubWebhookSecret != "" {
		return GitHubIntegrationConfig{Token: r.env.GitHubToken, WebhookSecret: r.env.GitHubWebhookSecret}, "env", true
	}
	return GitHubIntegrationConfig{}, "", false
}

// ResolveGitLab is ResolveGitHub's GitLab counterpart.
func (r *VCSIntegrationResolver) ResolveGitLab(ctx context.Context, workspaceID uuid.UUID) (cfg GitLabIntegrationConfig, source string, ok bool) {
	if r.repo != nil {
		row, err := r.repo.GetByProvider(ctx, workspaceID, domain.IntegrationProviderGitLab)
		if err != nil {
			log.Printf("[vcs-integration] gitlab lookup failed for workspace %s: %v — falling back to env", workspaceID, err)
		} else if parsed, usable := decodeGitLabIntegration(row); usable {
			return parsed, "workspace", true
		}
	}
	if r.env.GitLabBaseURL != "" && r.env.GitLabToken != "" {
		return GitLabIntegrationConfig{BaseURL: r.env.GitLabBaseURL, Token: r.env.GitLabToken, WebhookSecret: r.env.GitLabWebhookSecret}, "env", true
	}
	return GitLabIntegrationConfig{}, "", false
}

// GitHubWebhookSecrets returns every secret that should currently validate
// an inbound GitHub webhook on the single shared endpoint: every active
// workspace's configured webhook_secret, or — only when NO workspace has
// one — the env fallback. An empty, "" source result means the provider is
// disabled entirely: the caller MUST refuse the request, not accept it
// unvalidated (the pre-C2 behavior this replaces treated an empty secret as
// "validation off", which is exactly the silently-open door §2 of the
// parent spec's comments flagged).
func (r *VCSIntegrationResolver) GitHubWebhookSecrets(ctx context.Context) (secrets []string, source string) {
	if r.repo != nil {
		rows, err := r.repo.ListActiveByProvider(ctx, domain.IntegrationProviderGitHub)
		if err != nil {
			log.Printf("[vcs-integration] github active-list failed: %v — falling back to env", err)
		} else {
			for i := range rows {
				if cfg, ok := decodeGitHubIntegration(&rows[i]); ok && cfg.WebhookSecret != "" {
					secrets = append(secrets, cfg.WebhookSecret)
				}
			}
		}
	}
	if len(secrets) > 0 {
		return secrets, "workspace"
	}
	if r.env.GitHubWebhookSecret != "" {
		return []string{r.env.GitHubWebhookSecret}, "env"
	}
	return nil, ""
}

// GitLabWebhookSecrets is GitHubWebhookSecrets's GitLab counterpart.
func (r *VCSIntegrationResolver) GitLabWebhookSecrets(ctx context.Context) (secrets []string, source string) {
	if r.repo != nil {
		rows, err := r.repo.ListActiveByProvider(ctx, domain.IntegrationProviderGitLab)
		if err != nil {
			log.Printf("[vcs-integration] gitlab active-list failed: %v — falling back to env", err)
		} else {
			for i := range rows {
				if cfg, ok := decodeGitLabIntegration(&rows[i]); ok && cfg.WebhookSecret != "" {
					secrets = append(secrets, cfg.WebhookSecret)
				}
			}
		}
	}
	if len(secrets) > 0 {
		return secrets, "workspace"
	}
	if r.env.GitLabWebhookSecret != "" {
		return []string{r.env.GitLabWebhookSecret}, "env"
	}
	return nil, ""
}
