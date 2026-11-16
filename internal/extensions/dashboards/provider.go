// Package dashboards is the Dashboards extension (PRD Dashboards §5–§6): the
// home screen of a workspace built from widget grids. This package owns what
// does not need the database: the manifest, the static catalog of core widget
// types, the templates, the default composition and the layout rules. The
// store (store.go) reads and writes the ext_dashboards_* tables; the HTTP
// surface is handler.DashboardHandler.
//
// It is a system part (extensions.IsSystemPart): always on, left out of the
// extension lists, nothing to configure at any level.
package dashboards

import (
	"fmt"

	_ "embed"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

//go:embed mesh-extension.json
var manifestJSON []byte

// ID is the extension id; its tables are ext_dashboards_*.
const ID = "dashboards"

// Provider implements extensions.Provider for Dashboards.
type Provider struct {
	extensions.BaseProvider
}

// New builds the provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("dashboards: embedded manifest: %v", err))
	}
	return &Provider{BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst, Allow: d.Allow}}
}

// Validate: Dashboards has no settings at any level.
func (p *Provider) Validate(level extensions.Level, _ map[string]any) error {
	return fmt.Errorf("dashboards has no %s-level settings", level)
}

// HiddenFromListings keeps the home screen out of the extension lists: it is a
// part of Mesh, not something an administrator switches on or off.
func (p *Provider) HiddenFromListings() bool { return true }
