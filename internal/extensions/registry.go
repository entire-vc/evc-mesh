package extensions

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Registry holds the providers registered at startup.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	consents  ConsentStore
	health    *statusCache
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: map[string]Provider{},
		health:    &statusCache{now: time.Now, entries: map[healthKey]healthEntry{}},
	}
}

// Register adds a provider. The manifest id must be non-empty and unique.
func (r *Registry) Register(p Provider) error {
	id := p.Manifest().ID
	if id == "" {
		return fmt.Errorf("register extension: empty manifest id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.providers[id]; dup {
		return fmt.Errorf("register extension: %q already registered", id)
	}
	r.providers[id] = p
	return nil
}

// Replace registers p, overwriting a provider with the same id. External
// extensions use it: an update or rollback swaps the manifest in place.
func (r *Registry) Replace(p Provider) error {
	id := p.Manifest().ID
	if id == "" {
		return fmt.Errorf("replace extension: empty manifest id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[id] = p
	r.health.drop(id)
	return nil
}

// Unregister removes a provider; an unknown id is a no-op.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, id)
	r.health.drop(id)
}

// Aliased is implemented by a provider that stays reachable under older ids.
type Aliased interface {
	Aliases() []string
}

// Canonical maps an alias to the registered extension id; any other id is
// returned unchanged.
func (r *Registry) Canonical(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.providers[id]; ok {
		return id
	}
	for pid, p := range r.providers {
		if a, ok := p.(Aliased); ok {
			for _, alias := range a.Aliases() {
				if alias == id {
					return pid
				}
			}
		}
	}
	return id
}

// Get returns the provider for an extension id or one of its aliases.
func (r *Registry) Get(id string) (Provider, bool) {
	id = r.Canonical(id)
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// List returns all providers ordered by extension id.
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Provider, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.providers[id])
	}
	return out
}

// Resolve resolves one extension for a workspace through its provider.
func (r *Registry) Resolve(ctx context.Context, workspaceID uuid.UUID, extensionID string) (Resolved, error) {
	p, ok := r.Get(extensionID)
	if !ok {
		return Resolved{}, fmt.Errorf("resolve: extension %q is not registered", extensionID)
	}
	return p.Resolve(ctx, workspaceID)
}

// RegisterRoutes walks every provider's Hooks() and adds its routes to e.
// It replaces static per-provider route registration in cmd/api/main.go.
func (r *Registry) RegisterRoutes(e *echo.Echo) {
	for _, p := range r.List() {
		for _, rt := range p.Hooks().Routes {
			e.Add(rt.Method, rt.Path, rt.Handler, rt.Middlewares...)
		}
	}
}

// EventHandlers returns the handlers registered for an event, ordered by
// extension id.
func (r *Registry) EventHandlers(event string) []EventHandler {
	var out []EventHandler
	for _, p := range r.List() {
		if h, ok := p.Hooks().EventHandlers[event]; ok {
			out = append(out, h)
		}
	}
	return out
}

// SyncBuiltin upserts every registered provider's manifest into
// installed_extensions with source=builtin. Rows of other sources are not
// overwritten.
func (r *Registry) SyncBuiltin(ctx context.Context, repo repository.InstalledExtensionRepository) error {
	for _, p := range r.List() {
		m := p.Manifest()
		if err := repo.UpsertBuiltin(ctx, &domain.InstalledExtension{
			ExtensionID: m.ID,
			Version:     m.Version,
			Manifest:    m.Raw,
			Source:      domain.ExtensionSourceBuiltin,
			TrustLevel:  domain.ExtensionTrustBuiltin,
		}); err != nil {
			return fmt.Errorf("sync builtin extension %q: %w", m.ID, err)
		}
	}
	return nil
}
