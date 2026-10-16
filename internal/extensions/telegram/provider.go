// Package telegram is the Telegram notification channel as an extension:
// manifest, typed settings, Resolve and Health. The bot manager and the
// dispatcher in internal/service keep the Telegram protocol; this package owns
// the question "is the channel on, and with which bot token, for this
// workspace right now".
package telegram

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

//go:embed mesh-extension.json
var manifestJSON []byte

// ID is the extension id (manifest id, integration_configs.provider).
const ID = "telegram"

const healthTimeout = 5 * time.Second

// apiBase is the Bot API host; tests point Provider.APIBase at a stub.
const apiBase = "https://api.telegram.org"

// Settings is the typed form of a workspace row. BotToken is stored as
// AES-256-GCM ciphertext (or plaintext when no encryption key is set).
type Settings struct {
	BotToken    string `json:"bot_token"`
	BotUsername string `json:"bot_username"`
}

// Provider implements extensions.Provider for Telegram.
type Provider struct {
	extensions.BaseProvider
	HTTP    *http.Client
	APIBase string
}

// New builds the Telegram provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("telegram: embedded manifest: %v", err))
	}
	return &Provider{
		BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst},
		HTTP:         &http.Client{Timeout: healthTimeout},
		APIBase:      apiBase,
	}
}

// Validate: the instance level carries no settings; a workspace row needs a bot token.
func (p *Provider) Validate(level extensions.Level, settings map[string]any) error {
	switch level {
	case extensions.LevelInstance:
		if len(settings) > 0 {
			return errors.New("telegram has no instance-level settings; only enabled/disabled")
		}
		return nil
	case extensions.LevelWorkspace:
		s, err := extensions.DecodeSettings[Settings](settings)
		if err != nil {
			return err
		}
		if strings.TrimSpace(s.BotToken) == "" {
			return errors.New("bot_token is required")
		}
		return nil
	}
	return fmt.Errorf("telegram has no %s-level settings", level)
}

// InstanceEnabled reports whether this deployment offers the channel at all
// (MESH_TELEGRAM_ENABLED). The default is ON: an instance with no instance
// setting offers the channel, and only an explicit disabled entry switches it
// off. A failed read keeps the channel on, as the env switch did.
func (p *Provider) InstanceEnabled(ctx context.Context) bool {
	if p.Inst == nil {
		return true
	}
	s, err := p.Inst.InstanceSetting(ctx, p.Man.ID)
	if err != nil {
		log.Printf("[telegram] instance setting unreadable, channel stays on: %v", err)
		return true
	}
	return s == nil || s.Enabled
}

// Resolve: the workspace's own bot row governs (active or switched off); the
// instance level is a kill switch over all workspaces, not a source of settings.
func (p *Provider) Resolve(ctx context.Context, workspaceID uuid.UUID) (extensions.Resolved, error) {
	off := extensions.Resolved{ExtensionID: p.Man.ID, Source: extensions.SourceNone}
	if !p.InstanceEnabled(ctx) || p.Conns == nil {
		return off, nil
	}
	c, err := p.Conns.Connection(ctx, workspaceID, p.Man.ID)
	if err != nil {
		return extensions.Resolved{}, err
	}
	if c == nil {
		return off, nil
	}
	res := extensions.Resolved{ExtensionID: p.Man.ID, Source: extensions.SourceWorkspace, Enabled: c.IsActive}
	if c.IsActive {
		res.Settings = c.Settings
	}
	return res, nil
}

// BotToken is the decrypted token and the bot's @username of an effective
// configuration, ok=false when it carries no usable bot.
func BotToken(r extensions.Resolved) (token, username string, ok bool) {
	if !r.Enabled {
		return "", "", false
	}
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	if err != nil || s.BotToken == "" {
		return "", "", false
	}
	plain, err := encryption.Decrypt(s.BotToken)
	if err != nil || plain == "" {
		return "", "", false
	}
	return plain, s.BotUsername, true
}

// ResolveBot is what the Telegram runtime asks before every poll and send: the
// bot of workspaceID right now, ok=false when the channel is off for it
// (instance switch, no row, inactive row, unusable token).
func (p *Provider) ResolveBot(ctx context.Context, workspaceID uuid.UUID) (token, username string, ok bool) {
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil {
		log.Printf("[telegram] resolve failed for workspace %s: %v", workspaceID, err)
		return "", "", false
	}
	return BotToken(res)
}

// Health is getMe with the workspace's bot: OK when Telegram accepts the token.
func (p *Provider) Health(ctx context.Context, r extensions.Resolved) extensions.HealthReport {
	token, _, ok := BotToken(r)
	if !ok {
		return extensions.HealthReport{Message: "bot_token is not set"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.APIBase+"/bot"+token+"/getMe", http.NoBody)
	if err != nil {
		return extensions.HealthReport{Message: "bad bot request"}
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		// The error text of a failed request carries the URL, and the URL carries the token.
		return extensions.HealthReport{Message: "telegram unreachable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return extensions.HealthReport{Message: fmt.Sprintf("telegram getMe: HTTP %d", resp.StatusCode)}
	}
	return extensions.HealthReport{OK: true}
}
