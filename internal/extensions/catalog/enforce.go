package catalog

import (
	"context"
	"encoding/json"
	"log"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// InstanceJournalBlocked is the journal action written when the catalog
// blocklist switches an installed extension off, or back on.
const (
	InstanceJournalBlocked   = "catalog_blocked"
	InstanceJournalUnblocked = "catalog_unblocked"
)

// BlockStore is the slice of the installed-extensions store the enforcer uses.
type BlockStore interface {
	List(ctx context.Context) ([]domain.InstalledExtension, error)
	SetBlocked(ctx context.Context, extensionID string, blocked bool, reason string) (changed bool, err error)
}

// Journal records an admin-visible entry.
type Journal interface {
	AppendJournal(ctx context.Context, e *domain.InstanceExtensionJournalEntry) error
}

// Enforcer applies the catalog blocklist to what is installed. A blocked row is
// switched off for every workspace (the runtime skips Blocked rows); a version
// leaving the blocklist switches it back on. Built-in rows are never touched.
type Enforcer struct {
	Store   BlockStore
	Journal Journal
	// Notify tells instance administrators about a change. May be nil.
	Notify func(ctx context.Context, extensionID, reason string, blocked bool)
	// OnChange runs once after a pass that changed anything (runtime resync).
	OnChange func(ctx context.Context)
}

// Apply runs one pass over the installed extensions against cat.
func (e *Enforcer) Apply(ctx context.Context, cat *Catalog) {
	rows, err := e.Store.List(ctx)
	if err != nil {
		log.Printf("[extensions] blocklist: list installed: %v", err)
		return
	}
	changedAny := false
	for i := range rows {
		row := &rows[i]
		if row.Source == domain.ExtensionSourceBuiltin {
			continue
		}
		b, hit := cat.Blocks(row.ExtensionID, row.Version)
		if hit == row.Blocked && (!hit || row.BlockedReason == b.Reason) {
			continue
		}
		changed, serr := e.Store.SetBlocked(ctx, row.ExtensionID, hit, b.Reason)
		if serr != nil {
			log.Printf("[extensions] blocklist: %s: %v", row.ExtensionID, serr)
			continue
		}
		if !changed {
			continue
		}
		changedAny = true
		action := InstanceJournalUnblocked
		if hit {
			action = InstanceJournalBlocked
		}
		details, _ := json.Marshal(map[string]any{"version": row.Version, "reason": b.Reason, "since": b.Since})
		if e.Journal != nil {
			if jerr := e.Journal.AppendJournal(ctx, &domain.InstanceExtensionJournalEntry{ExtensionID: row.ExtensionID, Action: action, Details: details}); jerr != nil {
				log.Printf("[extensions] blocklist journal %s: %v", row.ExtensionID, jerr)
			}
		}
		if e.Notify != nil {
			e.Notify(ctx, row.ExtensionID, b.Reason, hit)
		}
	}
	if changedAny && e.OnChange != nil {
		e.OnChange(ctx)
	}
}
