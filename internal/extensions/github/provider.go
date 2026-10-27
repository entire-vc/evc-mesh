// Package github is the GitHub extension: manifest, typed settings, Resolve and
// the inbound webhook route. The webhook handler itself lives with the VCS
// link service and is handed in by main, so this package stays below it.
package github

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
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
)

//go:embed mesh-extension.json
var manifestJSON []byte

// ID is the extension id (manifest id, integration_configs.provider).
const ID = "github"

// WebhookPath is the canonical inbound route. /webhooks/github and
// /api/v1/integrations/github/webhook stay registered as aliases of it.
const WebhookPath = "/webhooks/github/github"

// Settings is the typed form of the connection settings of one level. Both
// values are stored encrypted; use Config for the decrypted pair.
type Settings struct {
	Token         string `json:"token"`
	WebhookSecret string `json:"webhook_secret"`
}

// Config is what the GitHub runtime needs: the API token of the live PR check
// and the secrets that may sign a webhook delivery, already decrypted.
type Config struct {
	Token         string
	WebhookSecret string
	// Degraded is why a stored secret could not be opened (secret store down);
	// Token and WebhookSecret are then empty and must not be used.
	Degraded string
}

// Provider implements extensions.Provider for GitHub.
type Provider struct {
	extensions.BaseProvider
	// WebhookHandler serves the inbound route. Set before RegisterRoutes.
	WebhookHandler echo.HandlerFunc
}

// New builds the GitHub provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("github: embedded manifest: %v", err))
	}
	return &Provider{BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst, Allow: d.Allow}}
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
		if s.Token == "" && s.WebhookSecret == "" {
			return errors.New("token or webhook_secret is required")
		}
		return nil
	case extensions.LevelProject:
		repo, _ := settings["repo"].(string)
		parts := strings.Split(repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("repo %q is not owner/name", repo)
		}
		return nil
	}
	return fmt.Errorf("github has no %s-level settings", level)
}

func hasConnection(r extensions.Resolved) bool {
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	return err == nil && (s.Token != "" || s.WebhookSecret != "")
}

// Resolve is the shared order with one GitHub rule on top: an enabled
// configuration that carries neither a token nor a webhook secret governs
// nothing. A placeholder workspace row falls through to the instance level; an
// instance entry without either is off. An explicit is_active=false workspace
// row still switches GitHub off for that workspace, whatever the instance says.
func (p *Provider) Resolve(ctx context.Context, workspaceID uuid.UUID) (extensions.Resolved, error) {
	res, err := p.BaseProvider.Resolve(ctx, workspaceID)
	if err != nil {
		return extensions.Resolved{}, err
	}
	if !res.Enabled || hasConnection(res) {
		return res, nil
	}
	if res.Source == extensions.SourceWorkspace {
		return extensions.ResolveSettings(ctx, nil, p.Inst, p.Allow, workspaceID, p.Man.ID)
	}
	return extensions.Resolved{ExtensionID: p.Man.ID, Source: extensions.SourceNone}, nil
}

// Health only says whether a token is available: GitHub offers no
// unauthenticated liveness route worth calling per status read.
func (p *Provider) Health(_ context.Context, r extensions.Resolved) extensions.HealthReport {
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	if err != nil || (s.Token == "" && s.WebhookSecret == "") {
		return extensions.HealthReport{Message: "no token or webhook secret configured"}
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
	var c Config
	if s.Token != "" {
		plain, reason := secretstore.Open(context.Background(), s.Token)
		if reason != "" {
			log.Printf("[github-integration] Token unavailable: %s", reason)
			c.Degraded = reason
		}
		c.Token = plain
	}
	if s.WebhookSecret != "" {
		plain, reason := secretstore.Open(context.Background(), s.WebhookSecret)
		if reason != "" {
			log.Printf("[github-integration] WebhookSecret unavailable: %s", reason)
			c.Degraded = reason
		}
		c.WebhookSecret = plain
	}
	return c
}

// ResolveConfig is what the GitHub runtime asks before every live check: the
// configuration governing workspaceID right now and where it came from
// ("workspace" | "instance"), ok=false when GitHub is off for it. A failed
// workspace lookup falls back to the instance level.
func (p *Provider) ResolveConfig(ctx context.Context, workspaceID uuid.UUID) (token, webhookSecret, source string, ok bool) {
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil {
		log.Printf("[github-integration] resolve failed for workspace %s: %v — trying instance level", workspaceID, err)
		res, err = extensions.ResolveSettings(ctx, nil, p.Inst, p.Allow, workspaceID, p.Man.ID)
		if err != nil {
			log.Printf("[github-integration] instance level failed: %v", err)
			return "", "", "", false
		}
	}
	if !res.Enabled {
		return "", "", "", false
	}
	c := decode(res)
	if c.Degraded != "" {
		return "", "", "", false
	}
	if c.Token == "" && c.WebhookSecret == "" {
		return "", "", "", false
	}
	return c.Token, c.WebhookSecret, string(res.Source), true
}

// InstanceWebhookSecret is the webhook secret of the instance level alone, ""
// when the instance level is off or has none.
func (p *Provider) InstanceWebhookSecret(ctx context.Context) string {
	res, err := extensions.ResolveSettings(ctx, nil, p.Inst, p.Allow, uuid.Nil, p.Man.ID)
	if err != nil || !res.Enabled {
		return ""
	}
	return decode(res).WebhookSecret
}
