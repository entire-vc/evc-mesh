package extensions

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// HealthTTL is how long a health report is served from cache.
const HealthTTL = 60 * time.Second

// Status is what the API shows and the runtime acts on for one extension in
// one workspace. Enabled and Source always come from the provider's Resolve,
// so the two can never disagree (lesson #a4d108eb: UI decided by the row,
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
	} else {
		st.ConsentedCapabilities = declaredCapabilities(m.Capabilities)
	}

	if !res.Enabled {
		// Nothing to check on a disabled extension; do not cache.
		return st, nil
	}
	e := r.health.get(healthKey{workspaceID, extensionID}, force, func() HealthReport { return p.Health(ctx, res) })
	st.Health, st.HealthCheckedAt, st.LastError = e.report, e.at, e.lastErr
	return st, nil
}

func (c *statusCache) get(k healthKey, force bool, compute func() HealthReport) healthEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if e, ok := c.entries[k]; ok && !force && now.Sub(e.at) < HealthTTL {
		return e
	}
	rep := compute()
	e := healthEntry{report: rep, at: now}
	if !rep.OK {
		e.lastErr = rep.Message
	} else if prev, ok := c.entries[k]; ok {
		e.lastErr = prev.lastErr
	}
	c.entries[k] = e
	return e
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
