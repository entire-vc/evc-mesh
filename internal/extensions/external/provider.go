// Package external runs extensions installed from a repository
// (runtime=external): it registers them in the extension registry, performs the
// install handshake with the extension's service and keeps its event
// subscriptions in step with the manifest. The repository token never reaches
// this package: it stays in the repo install store.
package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// Provider is an extensions.Provider for one external extension. Settings and
// resolution come from BaseProvider; Health probes the service itself.
type Provider struct {
	extensions.BaseProvider
	ext    extensions.External
	client *http.Client
	hs     *Handshaker
}

// NewProvider builds the provider of an external manifest. client must be the
// SSRF-guarded client: the service URL is a tenant-chosen setting.
func NewProvider(m extensions.Manifest, d extensions.Deps, client *http.Client, hs *Handshaker) (*Provider, error) {
	if m.Runtime != "external" || m.External == nil {
		return nil, fmt.Errorf("external provider %q: manifest is not runtime=external", m.ID)
	}
	if m.External.BaseURLSetting == "" {
		return nil, fmt.Errorf("external provider %q: external.base_url_setting is empty", m.ID)
	}
	return &Provider{
		BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst, Allow: d.Allow},
		ext:          *m.External, client: client, hs: hs,
	}, nil
}

// IsExternal tells the workspace service to use the shared external save hook.
func (p *Provider) IsExternal() bool { return true }

// baseURL reads the service address out of resolved settings.
func (p *Provider) baseURL(settings map[string]any) (string, error) {
	raw, _ := settings[p.ext.BaseURLSetting].(string)
	return cleanBaseURL(raw)
}

// cleanBaseURL accepts an https address, or http only for a loopback host: the
// install handshake carries the signing secret in its body, so it must not
// travel in clear over a network. The host is compared parsed, never by prefix
// (http://localhost.evil.com and http://127.0.0.1@evil.com are refused).
// Loopback http is meant for tests and local debugging: the production client
// dials through a guard that refuses loopback, so such a service reports degraded.
func cleanBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Hostname() == "" {
		return "", errAddress
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", errAddress
		}
	default:
		return "", errAddress
	}
	return raw, nil
}

var errAddress = errors.New("the service address must be an https URL (http only for localhost)")

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate checks the settings of a level the manifest declares. The instance
// level is checked against the manifest's instance schema. The workspace
// level carries the service address, so it must be present and well formed. The
// project level only narrows a workspace connection: its keys are checked
// against the manifest's project schema by the binding handler, and the service
// address stays a workspace setting.
func (p *Provider) Validate(level extensions.Level, settings map[string]any) error {
	switch level {
	case extensions.LevelWorkspace:
		if !p.Man.Levels.Workspace {
			break
		}
		if _, err := p.baseURL(settings); err != nil {
			return fmt.Errorf("%s: %w", p.ext.BaseURLSetting, err)
		}
		return nil
	case extensions.LevelProject:
		if p.Man.Levels.Project {
			return nil
		}
	case extensions.LevelInstance:
		// The instance level exists when the manifest declares a schema for it
		// (a catalog-installed extension's levels flag may not say so).
		if sch := p.Man.SettingsSchema(string(extensions.LevelInstance)); sch != nil {
			return validateAgainstSchema(sch, settings)
		}
	}
	return fmt.Errorf("%s has no %s-level settings", p.Man.ID, level)
}

// Health re-runs the install handshake while it has not succeeded for this
// connection (so a service that was down at connect time recovers by itself),
// then probes health_path. Any failure is a degraded report; the message never
// carries a response body or a secret.
func (p *Provider) Health(ctx context.Context, r extensions.Resolved) extensions.HealthReport {
	if !r.Enabled {
		return extensions.HealthReport{}
	}
	base, err := p.baseURL(r.Settings)
	if err != nil {
		return extensions.HealthReport{Message: err.Error()}
	}
	if p.hs != nil && r.WorkspaceID != uuid.Nil {
		if herr := p.hs.Ensure(ctx, p, r.WorkspaceID, r.Settings); herr != nil {
			return extensions.HealthReport{Message: "install handshake failed: " + herr.Error()}
		}
	}
	if p.ext.HealthPath == "" {
		return extensions.HealthReport{OK: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+p.ext.HealthPath, http.NoBody)
	if err != nil {
		return extensions.HealthReport{Message: "bad health URL"}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return extensions.HealthReport{Message: "service unreachable"}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return extensions.HealthReport{Message: fmt.Sprintf("health check answered %d", resp.StatusCode)}
	}
	return extensions.HealthReport{OK: true}
}

// validateAgainstSchema checks settings against a manifest-declared JSON Schema.
// The first violation is returned without the schema's own location noise.
func validateAgainstSchema(schemaJSON json.RawMessage, settings map[string]any) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return fmt.Errorf("settings schema is not valid JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err = c.AddResource("settings.json", doc); err != nil {
		return fmt.Errorf("settings schema: %w", err)
	}
	sch, err := c.Compile("settings.json")
	if err != nil {
		return fmt.Errorf("settings schema: %w", err)
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}
	if verr := sch.Validate(inst); verr != nil {
		msg := verr.Error()
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return errors.New(msg)
	}
	return nil
}
