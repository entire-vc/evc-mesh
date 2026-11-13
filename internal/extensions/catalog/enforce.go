package catalog

import (
	"context"
	"encoding/json"
	"errors"
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
	// Exact reads the exact receipt of id@version (Client.FetchExact). A
	// scoped install is judged by it: Spark leaves scoped blocks out of the
	// default blocklist and answers a blocked, yanked or suspended exact
	// selection with 404. Nil judges scoped installs by the default blocklist only.
	Exact func(ctx context.Context, id, version string) (*ExactReceipt, error)
}

// ReasonScopedWithdrawn is the block reason of a scoped install whose exact
// receipt Spark no longer serves (404).
const ReasonScopedWithdrawn = "Spark no longer offers this exact release (blocked, yanked or withdrawn)"

// verdict is whether row must be blocked and why. A scoped row's exact
// receipt is re-read on every pass, also when the default blocklist already
// names it: either source blocks, the default block's reason wins. ok=false:
// the state cannot be judged now (Spark unreachable, receipt does not verify)
// and the row is left as it is, unless the default blocklist alone blocks it.
func (e *Enforcer) verdict(ctx context.Context, cat *Catalog, row *domain.InstalledExtension) (b Block, hit, ok bool) {
	def, defHit := cat.Blocks(row.ExtensionID, row.Version)
	if !IsScoped(row) || e.Exact == nil {
		return def, defHit, true
	}
	b, hit, ok = e.exactVerdict(ctx, row)
	if defHit {
		return def, true, true
	}
	return b, hit, ok
}

// exactVerdict judges a scoped row by its exact receipt alone.
func (e *Enforcer) exactVerdict(ctx context.Context, row *domain.InstalledExtension) (b Block, hit, ok bool) {
	rc, err := e.Exact(ctx, row.ExtensionID, row.Version)
	switch {
	case errors.Is(err, ErrSelectionUnavailable):
		return Block{ID: row.ExtensionID, Version: row.Version, Reason: ReasonScopedWithdrawn}, true, true
	case err != nil:
		log.Printf("[extensions] blocklist: exact receipt %s@%s: %v; state kept", row.ExtensionID, row.Version, err)
		return Block{}, false, false
	}
	if rel := rc.Entry.Latest; rel.CommitSHA != row.CommitSHA || rel.ArtifactSHA256 == nil || *rel.ArtifactSHA256 != row.ArtifactSHA256 {
		// The same id@version now names other bytes: what runs here is not
		// what Spark offers any more.
		return Block{ID: row.ExtensionID, Version: row.Version, Reason: ReasonScopedWithdrawn}, true, true
	}
	b, hit = rc.Catalog.Blocks(row.ExtensionID, row.Version)
	return b, hit, true
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
		b, hit, judged := e.verdict(ctx, cat, row)
		if !judged {
			continue
		}
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
