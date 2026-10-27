package extensions

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// HealthTTL is how long a health report is served from cache.
const HealthTTL = 60 * time.Second

// Status is what the API shows and the runtime acts on for one extension in
// one workspace. Enabled and Source always come from the provider's Resolve,
// so the two can never disagree (lesson an internal ticket: UI decided by the row,
// runtime by env).
type Status struct {
	ExtensionID           string
	Enabled               bool
	Source                Source
	Version               string
	Health                HealthReport
	HealthCheckedAt       time.Time
	LastError             string
	ConsentedCapabilities []string
	// NotAllowed comes from Resolve: the instance allow-list refuses it.
	NotAllowed bool
}

// ConsentStore returns the capabilities a workspace consented to. A nil store
// means builtin-only: the capabilities the manifest declares are implicitly
// consented.
type ConsentStore interface {
	Consented(ctx context.Context, workspaceID uuid.UUID, extensionID string) ([]string, error)
}

type healthKey struct {
	ws  uuid.UUID
	ext string
}

type healthEntry struct {
	report  HealthReport
	at      time.Time
	lastErr string
}

// statusCache holds health reports for HealthTTL.
type statusCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[healthKey]healthEntry
	// flight holds the check in progress per key: callers of the same key wait
	// for it, other keys are never blocked by it.
	flight map[healthKey]*healthCall
}

type healthCall struct {
	done  chan struct{}
	entry healthEntry
}

// ResolveStatus is the single function behind GET /workspaces/{ws}/extensions
// and the runtime check. Health is cached for HealthTTL; use TestConnection
// (POST .../test) to force a fresh check.
func (r *Registry) ResolveStatus(ctx context.Context, workspaceID uuid.UUID, extensionID string) (Status, error) {
	return r.status(ctx, workspaceID, extensionID, false)
}

// TestConnection recomputes health now and refreshes the cache.
func (r *Registry) TestConnection(ctx context.Context, workspaceID uuid.UUID, extensionID string) (Status, error) {
	return r.status(ctx, workspaceID, extensionID, true)
}

// WithConsentStore sets the consent source. Without it, declared capabilities
// count as consented (builtin only).
func (r *Registry) WithConsentStore(s ConsentStore) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consents = s
	return r
}

// WithIntegrationConsents reads the consent recorded at connect time. A
// connection made before consent was stored keeps what its manifest declares.
func (r *Registry) WithIntegrationConsents(repo repository.ExtensionConsentRepository) *Registry {
	return r.WithConsentStore(IntegrationConsents{Repo: repo, Declared: func(id string) []string {
		p, ok := r.Get(id)
		if !ok {
			return nil
		}
		return DeclaredCapabilities(p.Manifest().Capabilities)
	}})
}

func (r *Registry) status(ctx context.Context, workspaceID uuid.UUID, extensionID string, force bool) (Status, error) {
	p, ok := r.Get(extensionID)
	if !ok {
		return Status{}, fmt.Errorf("resolve status: extension %q is not registered", extensionID)
	}
	res, err := p.Resolve(ctx, workspaceID)
	if err != nil {
		return Status{}, fmt.Errorf("resolve status %q: %w", extensionID, err)
	}
	m := p.Manifest()
	st := Status{
		ExtensionID: extensionID,
		Enabled:     res.Enabled,
		Source:      res.Source,
		Version:     m.Version,
		NotAllowed:  res.NotAllowed,
	}

	r.mu.RLock()
	consents := r.consents
	r.mu.RUnlock()
	if consents != nil {
		caps, err := consents.Consented(ctx, workspaceID, extensionID)
		if err != nil {
			return Status{}, fmt.Errorf("resolve status %q: consents: %w", extensionID, err)
		}
		st.ConsentedCapabilities = caps
		if res.Source == SourceNone || res.Source == SourceInstance {
			// No workspace connection (no instance setting, or the instance level governs): nothing was consented to.
			// The store reports (nil, nil) for "no connection" and for "legacy
			// connection without a record" alike, and only the second keeps the
			// declared capabilities.
			st.ConsentedCapabilities = nil
		}
	} else {
		st.ConsentedCapabilities = declaredCapabilities(m.Capabilities)
	}

	if !res.Enabled {
		// Nothing to check on a disabled extension; do not cache.
		return st, nil
	}
	res.WorkspaceID = workspaceID
	e := r.health.get(healthKey{workspaceID, extensionID}, force, func() HealthReport { return p.Health(ctx, res) })
	st.Health, st.HealthCheckedAt, st.LastError = e.report, e.at, e.lastErr
	return st, nil
}

// drop forgets the cached reports of an extension in every workspace, so the
// next status read computes health against the current provider.
func (c *statusCache) drop(ext string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.ext == ext {
			delete(c.entries, k)
		}
	}
}

// get returns the cached report or computes one. The check (network I/O for
// external extensions) runs outside the lock; concurrent callers of the same
// key share one run.
func (c *statusCache) get(k healthKey, force bool, compute func() HealthReport) (out healthEntry) {
	c.mu.Lock()
	if call, ok := c.flight[k]; ok {
		c.mu.Unlock()
		<-call.done
		return call.entry
	}
	now := c.now()
	if e, ok := c.entries[k]; ok && !force && now.Sub(e.at) < HealthTTL {
		c.mu.Unlock()
		return e
	}
	call := &healthCall{done: make(chan struct{})}
	if c.flight == nil {
		c.flight = map[healthKey]*healthCall{}
	}
	c.flight[k] = call
	c.mu.Unlock()

	var rep HealthReport
	finished := false
	defer func() {
		// A panicking check still releases the waiters and frees the key.
		c.mu.Lock()
		if finished {
			// A passing check clears the error: last_error describes the
			// current state, not the history.
			e := healthEntry{report: rep, at: now}
			if !rep.OK {
				e.lastErr = rep.Message
			}
			c.entries[k] = e
			call.entry = e
		} else {
			call.entry = healthEntry{report: HealthReport{Message: "health check failed"}, at: now, lastErr: "health check failed"}
		}
		delete(c.flight, k)
		c.mu.Unlock()
		out = call.entry
		close(call.done)
	}()
	rep = compute()
	finished = true
	return out
}

func declaredCapabilities(c Capabilities) []string {
	var out []string
	for _, g := range []struct {
		kind string
		vals []string
	}{{"api", c.API}, {"events", c.Events}, {"webhooks_in", c.WebhooksIn}, {"ui", c.UI}, {"secrets", c.Secrets}} {
		for _, v := range g.vals {
			out = append(out, g.kind+":"+v)
		}
	}
	sort.Strings(out)
	return out
}

// DeclaredCapabilities lists a manifest's capabilities as "kind:value" strings,
// sorted: the form consent is recorded and compared in.
func DeclaredCapabilities(c Capabilities) []string { return declaredCapabilities(c) }
