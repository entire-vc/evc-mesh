package external

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Runtime keeps the extension registry in step with the source=repo rows of
// installed_extensions: every installed, not blocked, runtime=external row is
// a registered provider; a removed or blocked one is unregistered.
type Runtime struct {
	Reg       *extensions.Registry
	Installed repository.InstalledExtensionRepository
	Deps      extensions.Deps
	Client    *http.Client
	HS        *Handshaker

	mu      sync.Mutex
	managed map[string]string // extension id -> manifest digest registered by this runtime
}

// Sync reconciles the registry with the installed rows. Call it at startup and
// after install, update and rollback; Run repeats it so a removal or block done
// elsewhere is picked up. A row that cannot be turned into a provider is
// logged and skipped, never fatal.
func (r *Runtime) Sync(ctx context.Context) error {
	rows, err := r.Installed.List(ctx)
	if err != nil {
		return fmt.Errorf("sync external extensions: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.managed == nil {
		r.managed = map[string]string{}
	}
	want := map[string]bool{}
	for i := range rows {
		row := rows[i]
		if (row.Source != domain.ExtensionSourceRepo && row.Source != domain.ExtensionSourceCatalog) || row.Blocked {
			continue
		}
		m, perr := extensions.ParseManifest(row.Manifest)
		if perr != nil || m.Runtime != "external" {
			log.Printf("[extensions] external runtime: skip %s: not a runnable external manifest", row.ExtensionID)
			continue
		}
		if _, taken := r.Reg.Get(m.ID); taken && r.managed[m.ID] == "" {
			log.Printf("[extensions] external runtime: skip %s: id is registered by a builtin", m.ID)
			continue
		}
		want[m.ID] = true
		digest := fmt.Sprintf("%x", sha256.Sum256(row.Manifest))
		if r.managed[m.ID] == digest {
			continue
		}
		p, nerr := NewProvider(m, r.Deps, r.Client, r.HS)
		if nerr != nil {
			log.Printf("[extensions] external runtime: skip %s: %v", m.ID, nerr)
			delete(want, m.ID)
			continue
		}
		if rerr := r.Reg.Replace(p); rerr != nil {
			return rerr
		}
		r.managed[m.ID] = digest
	}
	for id := range r.managed {
		if !want[id] {
			r.Reg.Unregister(id)
			delete(r.managed, id)
		}
	}
	return nil
}

// Run syncs on every interval until ctx ends.
func (r *Runtime) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Sync(ctx); err != nil {
				log.Printf("[extensions] %v", err)
			}
		}
	}
}

// Hook is the shared workspace save hook of external extensions
// (service.ExtensionSaveHook): once a connection is saved it runs the install
// handshake, so a connect, a new address or a re-enable reach the service. A
// failed handshake is logged; the connection stays, Health reports it degraded
// and retries until the service answers.
type Hook struct {
	Reg   *extensions.Registry
	Conns interface {
		GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error)
	}
	HS *Handshaker
}

// Prepare accepts the settings as they are; Validate already checked them.
func (h *Hook) Prepare(context.Context, map[string]any, map[string]any) error { return nil }

// Saved runs the handshake for an active connection.
func (h *Hook) Saved(ctx context.Context, integrationID uuid.UUID) {
	conn, err := h.Conns.GetByID(ctx, integrationID)
	if err != nil || conn == nil || !conn.IsActive {
		return
	}
	pr, ok := h.Reg.Get(string(conn.Provider))
	if !ok {
		return
	}
	p, ok := pr.(*Provider)
	if !ok {
		return
	}
	var settings map[string]any
	if err := json.Unmarshal(conn.Config, &settings); err != nil {
		return
	}
	if err := h.HS.Run(ctx, p, conn.WorkspaceID, settings); err != nil {
		log.Printf("[extensions] %s install handshake (workspace %s): %v", p.Man.ID, conn.WorkspaceID, err)
	}
}

// Removed needs nothing: subscriptions cascade with the connection.
func (h *Hook) Removed(context.Context, uuid.UUID) {}
