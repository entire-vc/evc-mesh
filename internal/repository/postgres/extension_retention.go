package postgres

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/extensions/migrationlint"
)

// extensionIDPattern is the shape of an id the retention job will act on. It
// has no '_' and no wildcard characters, so the id maps to its table prefix
// ext_<id with '-' as '_'>_ one to one and is never interpreted as a pattern.
var extensionIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ExtensionPurgeResult is the outcome for one removed extension.
type ExtensionPurgeResult struct {
	ExtensionID string
	Tables      []ExtensionCleanupCount
}

// PurgeExpiredExtensionData deletes the rows of the ext_<id>_* tables of every
// extension that is removed (no installed_extensions row), whose retention
// has passed (removed_at + retention <= now, recorded by the trigger on
// installed_extensions) and that was not purged yet. Only tables found in
// information_schema under that exact prefix are touched; a table that also
// falls under the longer prefix of another known extension (ids "a" and "a-b"
// share "ext_a_") is skipped. Nothing outside ext_* is read or written.
func PurgeExpiredExtensionData(ctx context.Context, db *sqlx.DB, now time.Time, retention time.Duration) ([]ExtensionPurgeResult, error) {
	var due []string
	if err := db.SelectContext(ctx, &due, `
		SELECT r.extension_id FROM extension_removals r
		WHERE r.purged_at IS NULL AND r.removed_at + $2 * interval '1 second' <= $1
		  AND NOT EXISTS (SELECT 1 FROM installed_extensions i WHERE i.extension_id = r.extension_id)
		ORDER BY r.extension_id`, now, int64(retention.Seconds())); err != nil {
		return nil, err
	}
	if len(due) == 0 {
		return nil, nil
	}
	var results []ExtensionPurgeResult
	for _, id := range due {
		if !extensionIDPattern.MatchString(id) {
			continue
		}
		res, err := purgeOneExtension(ctx, db, id, now, retention)
		if err != nil {
			return results, fmt.Errorf("purge %s: %w", id, err)
		}
		if res != nil {
			results = append(results, *res)
		}
	}
	return results, nil
}

// purgeOneExtension re-checks, inside its own transaction and under a lock on
// installed_extensions, a per-id advisory lock and a row lock on the removal
// record, that the extension is still removed and still due: a reinstall between
// the due-list read and here (its trigger deletes the removal record) must not
// lose the data. The set of other known extensions (whose longer table prefix
// is skipped) is read under the same lock, so an install of "a-b" cannot slip in
// between that read and the deletes of "a". It returns nil when there is
// nothing to do.
func purgeOneExtension(ctx context.Context, db *sqlx.DB, id string, now time.Time, retention time.Duration) (*ExtensionPurgeResult, error) {
	prefix := migrationlint.TablePrefix(id)
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Taken before the advisory lock: an install holds the table lock (row
	// exclusive) before its trigger asks for the advisory lock, so this order
	// cannot deadlock with it. SHARE blocks installs and removals, not reads.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE installed_extensions IN SHARE MODE`); err != nil {
		return nil, err
	}
	// Same per-id lock the installed_extensions insert trigger takes: from here to
	// commit no install of this id can interleave with the checks and deletes.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('ext-purge:' || $1::text))`, id); err != nil {
		return nil, err
	}
	var still int
	if err := tx.GetContext(ctx, &still, `
		SELECT count(*) FROM (
			SELECT 1 FROM extension_removals r
			WHERE r.extension_id = $1 AND r.purged_at IS NULL AND r.removed_at + $3 * interval '1 second' <= $2
			  AND NOT EXISTS (SELECT 1 FROM installed_extensions i WHERE i.extension_id = r.extension_id)
			FOR UPDATE OF r) due`, id, now, int64(retention.Seconds())); err != nil {
		return nil, err
	}
	if still == 0 {
		return nil, nil
	}

	var known []string
	if err := tx.SelectContext(ctx, &known, `
		SELECT extension_id FROM installed_extensions
		UNION SELECT extension_id FROM extension_removals
		UNION SELECT extension_id FROM instance_extension_journal
		UNION SELECT extension_id FROM instance_extension_settings
		UNION SELECT extension_id FROM instance_extension_policy
		UNION SELECT extension_id FROM extension_actors`); err != nil {
		return nil, err
	}
	var longer []string
	for _, k := range known {
		if k != id && extensionIDPattern.MatchString(k) {
			if p := migrationlint.TablePrefix(k); strings.HasPrefix(p, prefix) {
				longer = append(longer, p)
			}
		}
	}

	var names []string
	if err := tx.SelectContext(ctx, &names, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		  AND left(table_name, length($1::text)) = $1::text
		ORDER BY table_name`, prefix); err != nil {
		return nil, err
	}
	res := &ExtensionPurgeResult{ExtensionID: id}
	sort.Strings(names)
	var owned []string
	for _, name := range names {
		if len(name) == len(prefix) || ownedByLonger(name, longer) {
			continue
		}
		var n int64
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM `+pq.QuoteIdentifier(name)); err != nil {
			return nil, fmt.Errorf("count %s: %w", name, err)
		}
		owned = append(owned, pq.QuoteIdentifier(name))
		res.Tables = append(res.Tables, ExtensionCleanupCount{Table: name, Rows: n})
	}
	// One TRUNCATE for the whole set: foreign keys between the extension's own
	// tables cannot make the purge fail, whatever order the names sort in.
	if len(owned) > 0 {
		if _, err := tx.ExecContext(ctx, `TRUNCATE `+strings.Join(owned, ", ")); err != nil {
			return nil, fmt.Errorf("truncate extension tables: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE extension_removals SET purged_at = now() WHERE extension_id = $1`, id); err != nil {
		return nil, err
	}
	return res, tx.Commit()
}

func ownedByLonger(table string, longer []string) bool {
	for _, p := range longer {
		if strings.HasPrefix(table, p) {
			return true
		}
	}
	return false
}
