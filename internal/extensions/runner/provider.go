// Package runner is the Runner extension: the agent runtime catalog (pools,
// accounts, profiles, bindings, controllers) as a builtin plugin. It used to be
// the special integration provider "agent_runtime".
package runner

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

//go:embed mesh-extension.json
var manifestJSON []byte

// ID is the extension id. LegacyID is the provider name rows carry until the
// rename migration runs, and the synonym the API keeps accepting for one
// release (the dispatcher and fiddler still send it).
const (
	ID       = "runner"
	LegacyID = "agent_runtime"
)

// ErrVersionedAPI is why a generic settings write is refused: the catalog is
// versioned (compare-and-swap on revision, controller and grant checks) and
// only the runtime API writes it.
var ErrVersionedAPI = errors.New("runner settings are changed through the versioned runtime API")

// Provider implements extensions.Provider for Runner.
type Provider struct {
	extensions.BaseProvider
}

// New builds the Runner provider over the shared stores.
func New(d extensions.Deps) *Provider {
	m, err := extensions.ParseManifest(manifestJSON)
	if err != nil {
		panic(fmt.Sprintf("runner: embedded manifest: %v", err))
	}
	return &Provider{BaseProvider: extensions.BaseProvider{Man: m, Conns: d.Conns, Inst: d.Inst}}
}

// Aliases lets the extension registry resolve the legacy provider name.
func (p *Provider) Aliases() []string { return []string{LegacyID} }

// Canonical maps the legacy provider name to the extension id and leaves every
// other name alone.
func Canonical(id string) string {
	if id == LegacyID {
		return ID
	}
	return id
}

// Validate holds the strictness the old special branches had. A catalog that
// does not parse is refused with the reason class; a catalog that does parse is
// still refused for a generic write, because only the versioned runtime API
// may store it. The instance level carries no settings.
func (p *Provider) Validate(level extensions.Level, settings map[string]any) error {
	switch level {
	case extensions.LevelInstance:
		if len(settings) > 0 {
			return errors.New("runner has no instance-level settings")
		}
		return nil
	case extensions.LevelWorkspace:
		raw, err := jsonMarshal(settings)
		if err != nil {
			return err
		}
		if _, err := domain.ParseRuntimeCatalog(raw); err != nil {
			return errors.New("invalid runtime catalog")
		}
		return ErrVersionedAPI
	}
	return fmt.Errorf("runner has no %s-level settings", level)
}

// Resolve reads the workspace connection under the new name first and the
// legacy one second (the rename migration is a later release), and never
// returns the catalog: it is a resource inventory the generic status and
// listing endpoints must not disclose; the scoped runtime API is the only
// place that does.
func (p *Provider) Resolve(ctx context.Context, workspaceID uuid.UUID) (extensions.Resolved, error) {
	for _, id := range []string{ID, LegacyID} {
		res, err := extensions.ResolveSettings(ctx, p.Conns, nil, workspaceID, id)
		if err != nil {
			return extensions.Resolved{}, err
		}
		if res.Source == extensions.SourceWorkspace {
			res.ExtensionID, res.Settings = ID, nil
			return res, nil
		}
	}
	res, err := extensions.ResolveSettings(ctx, nil, p.Inst, workspaceID, ID)
	if err != nil {
		return extensions.Resolved{}, err
	}
	res.ExtensionID, res.Settings = ID, nil
	return res, nil
}

// HideConnectionSettings keeps the catalog out of the workspace extension view
// (service.SettingsHider).
func (p *Provider) HideConnectionSettings() bool { return true }

func jsonMarshal(v map[string]any) ([]byte, error) { return json.Marshal(v) }
