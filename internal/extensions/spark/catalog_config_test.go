package spark_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/spark"
)

func catalogInst(enabled bool, settings map[string]any) inst {
	return inst{&extensions.InstanceSetting{Enabled: enabled, Settings: settings}}
}

// Red control: the business resolver says "off" for a disabled instance row,
// which is exactly what used to blank the catalog URL.
func TestCatalogConfig_DisabledSparkStillServesCatalog(t *testing.T) {
	p := newProvider(nil, catalogInst(false, map[string]any{"spark_url": "https://spark.example"}))
	_, ok := p.InstanceURL(context.Background())
	require.False(t, ok, "control: Spark integration itself stays off")

	cfg := p.CatalogConfig(context.Background())
	require.Equal(t, "https://spark.example", cfg.URL)
	require.True(t, cfg.CounterOn)
}

func TestCatalogConfig_NoRowNoStoreOrNoURLUsesDefaultWithoutCounter(t *testing.T) {
	for name, p := range map[string]*spark.Provider{
		"no row":   newProvider(nil, inst{}),
		"no store": newProvider(nil, nil),
		"no url":   newProvider(nil, catalogInst(true, map[string]any{"catalog_install_counter": true})),
	} {
		cfg := p.CatalogConfig(context.Background())
		require.Equal(t, spark.DefaultCatalogURL, cfg.URL, name)
		require.False(t, cfg.CounterOn, name)
	}
}

func TestCatalogConfig_WorkspaceSettingsDoNotChangeIt(t *testing.T) {
	i := catalogInst(false, map[string]any{"spark_url": "https://inst.example"})
	want := newProvider(nil, i).CatalogConfig(context.Background())
	for _, row := range []*extensions.Connection{
		ws("https://other.example"),
		{IsActive: false, Settings: map[string]any{"spark_url": "https://off.example"}},
	} {
		p := newProvider(conns{row: row}, i)
		require.Equal(t, want, p.CatalogConfig(context.Background()))
	}
}

func TestCatalogConfig_LegacyKeysKeyAndCounter(t *testing.T) {
	for _, k := range []string{"spark_url", "url", "base_url"} {
		p := newProvider(nil, catalogInst(true, map[string]any{k: "https://legacy.example"}))
		require.Equal(t, "https://legacy.example", p.CatalogConfig(context.Background()).URL, k)
	}
	p := newProvider(nil, catalogInst(false, map[string]any{
		"spark_url": "https://x.example", "catalog_public_key": "  abc= ", "catalog_install_counter": false,
	}))
	cfg := p.CatalogConfig(context.Background())
	require.Equal(t, "abc=", cfg.PublicKey)
	require.False(t, cfg.CounterOn)
}
