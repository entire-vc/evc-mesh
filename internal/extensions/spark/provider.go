// Package spark is the Spark catalog extension: manifest, typed settings,
// Resolve and Health. It is the template the other builtin transfers
// (W1.6–W1.9) copy.
package spark

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

// ID is the extension id (manifest id, integration_configs.provider).
const ID = "spark"

const healthTimeout = 5 * time.Second

// Settings is the typed form of one level's settings. The legacy keys are the
// names rows used before the manifest fixed `spark_url`: workspace rows kept
// `base_url`, the env bootstrap wrote `url`. Rows in the database are not
// rewritten, so both stay readable.
type Settings struct {
	URL     string `json:"spark_url"`
	URLOld  string `json:"url"`
	BaseURL string `json:"base_url"`
}

// SparkURL is the catalog base URL the settings carry, "" when none.
func (s Settings) SparkURL() string {
	for _, v := range []string{s.URL, s.URLOld, s.BaseURL} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// URL returns the catalog base URL of an effective configuration.
func URL(r extensions.Resolved) string {
	s, err := extensions.DecodeSettings[Settings](r.Settings)
	if err != nil {
		return ""
	}
	return s.SparkURL()
}

// Provider implements extensions.Provider for Spark.
type Provider struct {
	extensions.BaseProvider
	HTTP *http.Client
}

// New builds the Spark provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("spark: embedded manifest: %v", err))
	}
	return &Provider{
		BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst},
		HTTP:         &http.Client{Timeout: healthTimeout},
	}
}

// Validate checks the settings of a level: a spark_url that is an http(s) URL.
func (p *Provider) Validate(level extensions.Level, settings map[string]any) error {
	if level != extensions.LevelInstance && level != extensions.LevelWorkspace {
		return fmt.Errorf("spark has no %s-level settings", level)
	}
	s, err := extensions.DecodeSettings[Settings](settings)
	if err != nil {
		return err
	}
	raw := s.SparkURL()
	if raw == "" {
		return errors.New("spark_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("spark_url %q is not an http(s) URL", raw)
	}
	return nil
}

// Resolve is the shared resolution order with one Spark rule on top: an
// enabled configuration without a URL carries nothing to call, so it does not
// govern. A workspace placeholder row (active, no URL) falls through to the
// instance level; an instance entry without a URL is off. An explicit
// is_active=false workspace row still switches Spark off for that workspace.
func (p *Provider) Resolve(ctx context.Context, workspaceID uuid.UUID) (extensions.Resolved, error) {
	res, err := p.BaseProvider.Resolve(ctx, workspaceID)
	if err != nil {
		return extensions.Resolved{}, err
	}
	if !res.Enabled || URL(res) != "" {
		return res, nil
	}
	if res.Source == extensions.SourceWorkspace {
		return extensions.ResolveSettings(ctx, nil, p.Inst, workspaceID, p.Man.ID)
	}
	return extensions.Resolved{ExtensionID: p.Man.ID, Source: extensions.SourceNone}, nil
}

// Health is GET {spark_url}/health: OK on any 2xx. Spark serves no
// /api/v1/health (404); /health is its liveness route.
func (p *Provider) Health(ctx context.Context, r extensions.Resolved) extensions.HealthReport {
	base := strings.TrimRight(URL(r), "/")
	if base == "" {
		return extensions.HealthReport{Message: "spark_url is not set"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", http.NoBody)
	if err != nil {
		return extensions.HealthReport{Message: "bad spark_url"}
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return extensions.HealthReport{Message: "spark unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return extensions.HealthReport{Message: fmt.Sprintf("spark health: HTTP %d", resp.StatusCode)}
	}
	return extensions.HealthReport{OK: true}
}

// ResolveSparkURL is what the Spark runtime asks before every call: the
// catalog URL governing workspaceID right now, where it came from
// ("workspace" | "instance"), or ok=false when Spark is off for it and the
// caller must refuse with a named reason. workspaceID may be uuid.Nil (catalog
// browse without a workspace): no row matches and the instance level decides.
// A failed workspace lookup falls back to the instance level, as before.
func (p *Provider) ResolveSparkURL(ctx context.Context, workspaceID uuid.UUID) (sparkURL, source string, ok bool) {
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil {
		log.Printf("[spark-integration] resolve failed for workspace %s: %v — trying instance level", workspaceID, err)
		res, err = extensions.ResolveSettings(ctx, nil, p.Inst, workspaceID, p.Man.ID)
		if err != nil {
			log.Printf("[spark-integration] instance level failed: %v", err)
			return "", "", false
		}
	}
	if u := URL(res); res.Enabled && u != "" {
		return u, string(res.Source), true
	}
	return "", "", false
}

// InstanceAvailable reports whether the instance level alone would run Spark.
func (p *Provider) InstanceAvailable(ctx context.Context) bool {
	_, ok := p.InstanceURL(ctx)
	return ok
}

// InstanceURL returns the catalog URL of the instance level alone, with no
// workspace context (the public capability endpoint needs it before a
// workspace is picked).
func (p *Provider) InstanceURL(ctx context.Context) (sparkURL string, ok bool) {
	res, err := extensions.ResolveSettings(ctx, nil, p.Inst, uuid.Nil, p.Man.ID)
	if err != nil || !res.Enabled {
		return "", false
	}
	u := URL(res)
	return u, u != ""
}
