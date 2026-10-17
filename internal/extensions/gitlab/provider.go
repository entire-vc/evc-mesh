// Package gitlab is the GitLab extension: manifest, typed settings, Resolve and
// the inbound webhook route. The webhook handler itself lives with the VCS
// link service and is handed in by main, so this package stays below it.
package gitlab

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

//go:embed mesh-extension.json
var manifestJSON []byte

// ID is the extension id (manifest id, integration_configs.provider).
const ID = "gitlab"

// WebhookPath is the canonical inbound route. /webhooks/gitlab (what every
// configured GitLab project points at) stays registered as an alias of it.
const WebhookPath = "/webhooks/gitlab/gitlab"

// Settings is the typed form of the connection settings of one level. The token and secret
// are stored encrypted; use Config for the decrypted values.
type Settings struct {
	BaseURL       string `json:"base_url"`
	Token         string `json:"token"`
	WebhookSecret string `json:"webhook_secret"`
}

// Config is what the GitLab runtime needs: the instance URL and API token of the live MR check
// and the secrets that may sign a webhook delivery, already decrypted.
type Config struct {
	BaseURL       string
	Token         string
	WebhookSecret string
}

// Provider implements extensions.Provider for GitLab.
type Provider struct {
	extensions.BaseProvider
	// WebhookHandler serves the inbound route. Set before RegisterRoutes.
	WebhookHandler echo.HandlerFunc
}

// New builds the GitLab provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("gitlab: embedded manifest: %v", err))
	}
	return &Provider{BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst}}
}

// Validate checks the settings of a level. A connection must carry a token or a
// webhook secret (a row with neither governs nothing); the project level names
// the repository.
func (p *Provider) Validate(level extensions.Level, settings map[string]any) error {
	switch level {
	case extensions.LevelInstance, extensions.LevelWorkspace:
		s, err := extensions.DecodeSettings[Settings](settings)
		if err != nil {
			return err
		}
		if s.BaseURL == "" && s.Token == "" && s.WebhookSecret == "" {
			return errors.New("base_url, token or webhook_secret is required")
		}
		return nil
	case extensions.LevelProject:
		path, _ := settings["repo_path"].(string)
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return fmt.Errorf("repo_path %q is not group/project", path)
		}
		for _, part := range parts {
			if part == "" {
				return fmt.Errorf("repo_path %q is not group/project", path)
			}
		}
		return nil
	}
	return fmt.Errorf("gitlab has no %s-level settings", level)
}

func hasConnection(r extensions.Resolved) bool {
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	return err == nil && (s.BaseURL != "" || s.Token != "" || s.WebhookSecret != "")
}

// Resolve is the shared order with one GitLab rule on top: an enabled
// configuration that carries no base_url, token or webhook secret governs
// nothing. A placeholder workspace row falls through to the instance level; an
// instance entry without either is off. An explicit is_active=false workspace
// row still switches GitLab off for that workspace, whatever the instance says.
func (p *Provider) Resolve(ctx context.Context, workspaceID uuid.UUID) (extensions.Resolved, error) {
	res, err := p.BaseProvider.Resolve(ctx, workspaceID)
	if err != nil {
		return extensions.Resolved{}, err
	}
	if !res.Enabled || hasConnection(res) {
		return res, nil
	}
	if res.Source == extensions.SourceWorkspace {
		return extensions.ResolveSettings(ctx, nil, p.Inst, workspaceID, p.Man.ID)
	}
	return extensions.Resolved{ExtensionID: p.Man.ID, Source: extensions.SourceNone}, nil
}

// Health only says whether a connection is configured: a live call per status
// read is not worth it.
func (p *Provider) Health(_ context.Context, r extensions.Resolved) extensions.HealthReport {
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	if err != nil || (s.BaseURL == "" && s.Token == "" && s.WebhookSecret == "") {
		return extensions.HealthReport{Message: "no base_url, token or webhook secret configured"}
	}
	return extensions.HealthReport{OK: true}
}

// Hooks registers the canonical webhook route once main has set the handler.
func (p *Provider) Hooks() extensions.Hooks {
	if p.WebhookHandler == nil {
		return extensions.Hooks{}
	}
	return extensions.Hooks{Routes: []extensions.Route{{Method: http.MethodPost, Path: WebhookPath, Handler: p.WebhookHandler}}}
}

func decode(r extensions.Resolved) Config {
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	if err != nil {
		return Config{}
	}
	c := Config{BaseURL: s.BaseURL}
	if s.Token != "" {
		if plain, derr := encryption.Decrypt(s.Token); derr == nil {
			c.Token = plain
		}
	}
	if s.WebhookSecret != "" {
		if plain, derr := encryption.Decrypt(s.WebhookSecret); derr == nil {
			c.WebhookSecret = plain
		}
	}
	return c
}

// ResolveConfig is what the GitLab runtime asks before every live check: the
// configuration governing workspaceID right now and where it came from
// ("workspace" | "instance"), ok=false when GitLab is off for it. A failed
// workspace lookup falls back to the instance level.
func (p *Provider) ResolveConfig(ctx context.Context, workspaceID uuid.UUID) (baseURL, token, webhookSecret, source string, ok bool) {
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil {
		log.Printf("[gitlab-integration] resolve failed for workspace %s: %v — trying instance level", workspaceID, err)
		res, err = extensions.ResolveSettings(ctx, nil, p.Inst, workspaceID, p.Man.ID)
		if err != nil {
			log.Printf("[gitlab-integration] instance level failed: %v", err)
			return "", "", "", "", false
		}
	}
	if !res.Enabled {
		return "", "", "", "", false
	}
	c := decode(res)
	if c.BaseURL == "" && c.Token == "" && c.WebhookSecret == "" {
		return "", "", "", "", false
	}
	return c.BaseURL, c.Token, c.WebhookSecret, string(res.Source), true
}

// InstanceWebhookSecret is the webhook secret of the instance level alone, ""
// when the instance level is off or has none.
func (p *Provider) InstanceWebhookSecret(ctx context.Context) string {
	res, err := extensions.ResolveSettings(ctx, nil, p.Inst, uuid.Nil, p.Man.ID)
	if err != nil || !res.Enabled {
		return ""
	}
	return decode(res).WebhookSecret
}
