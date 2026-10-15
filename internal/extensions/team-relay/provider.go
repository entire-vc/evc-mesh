// Package teamrelay is the Team Relay extension: manifest, typed settings for
// the connection (instance/workspace) and the binding (project), Resolve and
// Health. It follows the Spark package (W1.5), the template for builtin
// transfers.
package teamrelay

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

//go:embed mesh-extension.json
var manifestJSON []byte

// ID is the manifest id. The manifest id pattern has no underscore, so the
// legacy key "team_relay" (integration_configs.provider, instance setting
// extension_id, existing rows) cannot be the id; LegacyID maps to it. Stored
// rows are not rewritten.
const ID = "team-relay"

// LegacyID is the key existing connection and instance rows are stored under.
const LegacyID = "team_relay"

const healthTimeout = 5 * time.Second

// Settings is the typed connection (instance and workspace level).
type Settings struct {
	RelayURL         string `json:"relay_url"`
	WebBaseURL       string `json:"web_base_url"`
	TransportEnabled bool   `json:"transport_enabled"`
}

// ProjectSettings is the typed binding of a project to a share. AgentKey is a
// secret: it is carried through but never logged or echoed in errors.
type ProjectSettings struct {
	ShareID      string `json:"share_id"`
	Subfolder    string `json:"subfolder"`
	AgentKey     string `json:"agent_key"`
	KeyExpiresAt string `json:"key_expires_at"`
}

// RelayURL returns the relay base URL of an effective configuration, "" when
// none.
func RelayURL(r extensions.Resolved) string {
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s.RelayURL)
}

// legacyConns and legacyInst read the rows stored under LegacyID when the
// registry asks for the manifest id.
type legacyConns struct{ next extensions.ConnectionStore }

func (s legacyConns) Connection(ctx context.Context, ws uuid.UUID, id string) (*extensions.Connection, error) {
	return s.next.Connection(ctx, ws, toLegacy(id))
}

type legacyInst struct{ next extensions.InstanceStore }

func (s legacyInst) InstanceSetting(ctx context.Context, id string) (*extensions.InstanceSetting, error) {
	return s.next.InstanceSetting(ctx, toLegacy(id))
}

func toLegacy(id string) string {
	if id == ID {
		return LegacyID
	}
	return id
}

// Provider implements extensions.Provider for Team Relay.
type Provider struct {
	extensions.BaseProvider
	HTTP *http.Client
}

// New builds the Team Relay provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("team-relay: embedded manifest: %v", err))
	}
	b := extensions.BaseProvider{Man: m}
	if d.Conns != nil {
		b.Conns = legacyConns{d.Conns}
	}
	if d.Inst != nil {
		b.Inst = legacyInst{d.Inst}
	}
	return &Provider{BaseProvider: b, HTTP: &http.Client{Timeout: healthTimeout}}
}

// Validate checks the settings of a level. Connection levels need an http(s)
// relay_url (and web_base_url when given); the project level needs a share_id
// UUID and, when set, a parsable key_expires_at.
func (p *Provider) Validate(level extensions.Level, settings map[string]any) error {
	switch level {
	case extensions.LevelInstance, extensions.LevelWorkspace:
		s, err := extensions.DecodeSettings[Settings](settings)
		if err != nil {
			return err
		}
		if strings.TrimSpace(s.RelayURL) == "" {
			return errors.New("relay_url is required")
		}
		if err := httpURL("relay_url", s.RelayURL); err != nil {
			return err
		}
		if strings.TrimSpace(s.WebBaseURL) != "" {
			return httpURL("web_base_url", s.WebBaseURL)
		}
		return nil
	case extensions.LevelProject:
		s, err := extensions.DecodeSettings[ProjectSettings](settings)
		if err != nil {
			return err
		}
		if _, err := uuid.Parse(strings.TrimSpace(s.ShareID)); err != nil {
			return errors.New("share_id must be a share UUID")
		}
		if s.KeyExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339, s.KeyExpiresAt); err != nil {
				return errors.New("key_expires_at must be an RFC 3339 timestamp")
			}
		}
		return nil
	default:
		return fmt.Errorf("team-relay has no %s-level settings", level)
	}
}

func httpURL(field, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s is not an http(s) URL", field)
	}
	return nil
}

// Resolve is the shared resolution order with one Team Relay rule on top, the
// one the old resolver had: an enabled configuration without a relay_url
// carries nothing to call, so it does not govern. A workspace placeholder row
// (active, no relay_url) falls through to the instance level; an instance
// entry without a relay_url is off. An explicit is_active=false workspace row
// still switches Team Relay off for that workspace and env does not override it.
func (p *Provider) Resolve(ctx context.Context, workspaceID uuid.UUID) (extensions.Resolved, error) {
	res, err := p.BaseProvider.Resolve(ctx, workspaceID)
	if err != nil {
		return extensions.Resolved{}, err
	}
	if !res.Enabled || RelayURL(res) != "" {
		return res, nil
	}
	if res.Source == extensions.SourceWorkspace {
		return extensions.ResolveSettings(ctx, nil, p.Inst, workspaceID, p.Man.ID)
	}
	return extensions.Resolved{ExtensionID: p.Man.ID, Source: extensions.SourceNone}, nil
}

// Health is GET {relay_url}/health: OK on any 2xx.
func (p *Provider) Health(ctx context.Context, r extensions.Resolved) extensions.HealthReport {
	base := strings.TrimRight(RelayURL(r), "/")
	if base == "" {
		return extensions.HealthReport{Message: "relay_url is not set"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", http.NoBody)
	if err != nil {
		return extensions.HealthReport{Message: "bad relay_url"}
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return extensions.HealthReport{Message: "team relay unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return extensions.HealthReport{Message: fmt.Sprintf("team relay health: HTTP %d", resp.StatusCode)}
	}
	return extensions.HealthReport{OK: true}
}

// ResolveRelayURL is what the Team Relay runtime asks before every call: the
// relay URL governing workspaceID right now, where it came from ("workspace" |
// "instance"), or ok=false when Team Relay is off for it and the caller must
// refuse with a named reason. A failed workspace lookup falls back to the
// instance level, as the old resolver fell back to env.
func (p *Provider) ResolveRelayURL(ctx context.Context, workspaceID uuid.UUID) (relayURL, source string, ok bool) {
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil {
		log.Printf("[teamrelay-integration] resolve failed for workspace %s: %v — trying instance level", workspaceID, err)
		res, err = extensions.ResolveSettings(ctx, nil, p.Inst, workspaceID, p.Man.ID)
		if err != nil {
			log.Printf("[teamrelay-integration] instance level failed: %v", err)
			return "", "", false
		}
	}
	if u := RelayURL(res); res.Enabled && u != "" {
		return u, string(res.Source), true
	}
	return "", "", false
}

// TransportEnabled reports whether artifacts are pushed to the relay for
// workspaceID.
func (p *Provider) TransportEnabled(ctx context.Context, workspaceID uuid.UUID) bool {
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil || !res.Enabled {
		return false
	}
	s, err := extensions.DecodeSettings[Settings](res.Settings)
	return err == nil && s.TransportEnabled
}
