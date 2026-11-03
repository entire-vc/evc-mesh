package spark

import (
	"context"
	"strings"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// DefaultCatalogURL is the public catalog an instance reads when its instance
// row carries no URL. The catalog body is trusted only through its signature,
// so the default adds no trust; it only lets update discovery work on an
// instance that never configured Spark.
const DefaultCatalogURL = "https://spark.entire.vc"

// CatalogConfig is the instance-wide configuration of the signed extension
// catalog: where it is read from, which key verifies it and whether installs
// are counted. It is independent of the Spark business integration: the
// integration's enabled flag, the allowlist and any workspace connection do not
// change it, so update discovery keeps working when Spark is off for everyone.
type CatalogConfig struct {
	// URL is the catalog base URL: the instance row's, else DefaultCatalogURL.
	URL string
	// PublicKey is the instance override of the catalog key (base64 ed25519),
	// "" for the pinned production key. The caller still fails closed on a
	// malformed value.
	PublicKey string
	// CounterOn reports installs to the catalog; on unless explicitly disabled.
	CounterOn bool
}

// CatalogConfig reads the instance row of the spark extension, whatever its
// enabled flag, and returns the catalog settings from it. Workspace rows are
// never consulted. Legacy URL keys (url, base_url) stay readable. Without an
// explicit URL the default catalog is read and the install counter stays off:
// nothing is reported to a catalog the admin never chose.
func (p *Provider) CatalogConfig(ctx context.Context) CatalogConfig {
	fallback := CatalogConfig{URL: DefaultCatalogURL}
	if p == nil || p.Inst == nil {
		return fallback
	}
	row, err := p.Inst.InstanceSetting(ctx, p.Man.ID)
	if err != nil || row == nil {
		return fallback
	}
	cfg := catalogConfigFromSettings(row.Settings)
	if cfg.URL == "" {
		cfg.URL = DefaultCatalogURL
		cfg.CounterOn = false
	}
	return cfg
}

func catalogConfigFromSettings(settings map[string]any) CatalogConfig {
	cfg := CatalogConfig{CounterOn: true}
	if s, err := extensions.DecodeSettings[Settings](settings); err == nil {
		cfg.URL = s.SparkURL()
	}
	if k, ok := settings["catalog_public_key"].(string); ok {
		cfg.PublicKey = strings.TrimSpace(k)
	}
	if on, ok := settings["catalog_install_counter"].(bool); ok {
		cfg.CounterOn = on
	}
	return cfg
}
