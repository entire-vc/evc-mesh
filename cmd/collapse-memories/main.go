// Command collapse-memories dedupes active scope=workspace rows in the
// memories table by (workspace_id, key) and clears the stray project_id
// that resolveProjectSlug used to stamp onto workspace-scope rows before
// PR #444 (memory_service.go:481-487) — see task #4edf3fb5.
//
// It is intentionally standalone: no dependency on internal/service or
// internal/repository, so it can be built, reviewed and run independently
// of the application code that produced the bug (task #4edf3fb5, subtask 4).
//
// For each (workspace_id, key) group with more than one active row:
//   - winner = most recent by updated_at (tie-break: larger id)
//   - losers -> status='superseded', superseded_by=<winner id>
//   - winner.tags <- distinct union of tags across the whole group
//
// Separately, every active scope=workspace row that still has project_id
// set (winners and never-duplicated rows alike) gets project_id set to
// NULL. Hard DELETE is never issued — superseded rows stay queryable for
// audit, matching the existing status/superseded_by columns (migration
// 20260704080_memory_health_lifecycle.sql).
//
// Idempotent: a second run finds no groups with >1 active row (losers are
// no longer status='active') and no active rows left with project_id set,
// so it is a no-op. Safe to re-run after a partial failure.
//
// Usage:
//
//	go run ./cmd/collapse-memories                      # dry-run (default), prints the plan, writes nothing
//	go run ./cmd/collapse-memories -dry-run=false        # applies the plan inside one transaction
//	go run ./cmd/collapse-memories -workspace-id=<uuid>  # restrict to one workspace (either mode)
//
// Connects using the same DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME/
// DB_SSL_MODE env vars as cmd/api (internal/config/config.go).
//
// Rollback (superseding is reversible, nothing is destroyed):
//
//	UPDATE memories SET status='active', superseded_by=NULL
//	WHERE superseded_by = '<winner id>';
//
// project_id nulling has no recorded prior value to restore to — it was a
// bug artifact (resolveProjectSlug should never have set it on a
// workspace-scope row), not meaningful data, so there is nothing to roll
// back to.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/lib/pq"
)

type memRow struct {
	ID        string
	Key       string
	Tags      []string
	ProjectID sql.NullString
	UpdatedAt time.Time
}

type group struct {
	WorkspaceID string
	Key         string
	// Rows is winner-first: sorted by updated_at DESC, id DESC by the query.
	Rows []memRow
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run holds all resources (db, tx) as locals with their own defers, so a
// mid-function failure always unwinds through those defers before the
// process exits — main() itself never defers anything, which is what lets
// it use log.Fatal safely on the returned error.
func run() error {
	dryRun := flag.Bool("dry-run", true, "print the collapse plan without writing (default true — pass -dry-run=false to apply)")
	limit := flag.Int("limit", 25, "max sample rows to print for the project_id-null report (0 = print all)")
	workspaceID := flag.String("workspace-id", "", "restrict to a single workspace_id (optional, default = all workspaces)")
	flag.Parse()

	db, err := sql.Open("postgres", buildDSN())
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	db.SetMaxOpenConns(5)
	err = db.Ping()
	if err != nil {
		db.Close()
		return fmt.Errorf("ping: %w", err)
	}
	defer db.Close()

	totalBefore, err := countMemories(db)
	if err != nil {
		return fmt.Errorf("count memories before: %w", err)
	}

	// Everything below runs inside ONE transaction with SELECT ... FOR UPDATE
	// on both candidate sets, so the plan we print is the exact snapshot we
	// act on — no TOCTOU window against the live fleet writing memories
	// concurrently (see canon-mesh-mutation-consistency-cas-not-gateway:
	// stale-snapshot races, not write-write races, are the real risk here).
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	groups, err := loadDuplicateGroups(tx, *workspaceID)
	if err != nil {
		return fmt.Errorf("load duplicate groups: %w", err)
	}

	nullCandidates, err := loadProjectIDNullCandidates(tx, *workspaceID)
	if err != nil {
		return fmt.Errorf("load project_id null candidates: %w", err)
	}
	// Rows that are about to become losers will flip to status='superseded'
	// in this same run, so they no longer satisfy status='active' — exclude
	// them from the null-report so the preview matches the post-apply state.
	loserSet := map[string]bool{}
	for _, g := range groups {
		for _, r := range g.Rows[1:] {
			loserSet[r.ID] = true
		}
	}
	var survivingNullCandidates []memRow
	for _, r := range nullCandidates {
		if !loserSet[r.ID] {
			survivingNullCandidates = append(survivingNullCandidates, r)
		}
	}

	printPlan(groups, survivingNullCandidates, *limit)

	if *dryRun {
		fmt.Println("\n[dry-run] no rows written (transaction rolled back). Re-run with -dry-run=false to apply.")
		return nil
	}

	fmt.Println("\n=== APPLYING ===")
	err = applyCollapse(tx, groups)
	if err != nil {
		return fmt.Errorf("apply collapse: %w", err)
	}
	err = applyProjectIDNull(tx, survivingNullCandidates)
	if err != nil {
		return fmt.Errorf("apply project_id null: %w", err)
	}
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	totalAfter, err := countMemories(db)
	if err != nil {
		return fmt.Errorf("count memories after: %w", err)
	}
	dupLeft, err := countActiveDuplicates(db)
	if err != nil {
		return fmt.Errorf("verify AC3 (duplicate groups): %w", err)
	}
	nullLeft, err := countActiveWorkspaceWithProjectID(db)
	if err != nil {
		return fmt.Errorf("verify AC4 (project_id nulled): %w", err)
	}

	fmt.Println("\n=== VERIFICATION ===")
	fmt.Printf("AC5 total memories: before=%d after=%d (must match — nothing deleted)\n", totalBefore, totalAfter)
	fmt.Printf("AC3 remaining active scope=workspace duplicate (workspace_id,key) groups: %d (want 0)\n", dupLeft)
	fmt.Printf("AC4 remaining active scope=workspace rows with project_id set: %d (want 0)\n", nullLeft)

	if totalBefore != totalAfter {
		return fmt.Errorf("FATAL: total row count changed even though no DELETE was issued — investigate before trusting this run")
	}
	if *workspaceID == "" && (dupLeft != 0 || nullLeft != 0) {
		return fmt.Errorf("FATAL: AC3/AC4 not satisfied after an unfiltered run — investigate before closing the task")
	}
	return nil
}

func buildDSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		getenv("DB_HOST", "localhost"),
		getenv("DB_PORT", "5437"),
		getenv("DB_USER", "mesh"),
		getenv("DB_PASSWORD", "mesh"),
		getenv("DB_NAME", "mesh"),
		getenv("DB_SSL_MODE", "disable"),
	)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadDuplicateGroups(tx *sql.Tx, workspaceFilter string) ([]group, error) {
	q := `SELECT id, workspace_id, key, tags, project_id, updated_at
		FROM memories
		WHERE scope = 'workspace' AND status = 'active'`
	var args []interface{}
	if workspaceFilter != "" {
		q += " AND workspace_id = $1"
		args = append(args, workspaceFilter)
	}
	q += " ORDER BY workspace_id, key, updated_at DESC, id DESC FOR UPDATE"

	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := map[string]*group{}
	var order []string
	for rows.Next() {
		var (
			id, wsID, key string
			tags          []string
			projectID     sql.NullString
			updatedAt     time.Time
		)
		if err := rows.Scan(&id, &wsID, &key, pq.Array(&tags), &projectID, &updatedAt); err != nil {
			return nil, err
		}
		gk := wsID + "\x00" + key
		g, ok := byKey[gk]
		if !ok {
			g = &group{WorkspaceID: wsID, Key: key}
			byKey[gk] = g
			order = append(order, gk)
		}
		g.Rows = append(g.Rows, memRow{ID: id, Key: key, Tags: tags, ProjectID: projectID, UpdatedAt: updatedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var dupGroups []group
	for _, gk := range order {
		g := byKey[gk]
		if len(g.Rows) > 1 {
			dupGroups = append(dupGroups, *g)
		}
	}
	return dupGroups, nil
}

func loadProjectIDNullCandidates(tx *sql.Tx, workspaceFilter string) ([]memRow, error) {
	q := `SELECT id, key, project_id
		FROM memories
		WHERE scope = 'workspace' AND status = 'active' AND project_id IS NOT NULL`
	var args []interface{}
	if workspaceFilter != "" {
		q += " AND workspace_id = $1"
		args = append(args, workspaceFilter)
	}
	q += " ORDER BY key, id FOR UPDATE"

	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []memRow
	for rows.Next() {
		var r memRow
		if err := rows.Scan(&r.ID, &r.Key, &r.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func mergeTags(rows []memRow) []string {
	set := map[string]bool{}
	for _, r := range rows {
		for _, t := range r.Tags {
			set[t] = true
		}
	}
	merged := make([]string, 0, len(set))
	for t := range set {
		merged = append(merged, t)
	}
	sort.Strings(merged)
	return merged
}

func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func printPlan(groups []group, nullCandidates []memRow, limit int) {
	fmt.Println("=== DUPLICATE GROUPS (scope=workspace, status=active, count>1) ===")
	if len(groups) == 0 {
		fmt.Println("(none)")
	}
	for _, g := range groups {
		winner := g.Rows[0]
		losers := g.Rows[1:]
		merged := mergeTags(g.Rows)
		fmt.Printf("\nworkspace_id=%s key=%q (%d rows)\n", g.WorkspaceID, g.Key, len(g.Rows))
		fmt.Printf("  WINNER  id=%s updated_at=%s tags=%v\n", winner.ID, winner.UpdatedAt.Format(time.RFC3339), winner.Tags)
		for _, l := range losers {
			fmt.Printf("  LOSER   id=%s updated_at=%s tags=%v -> status=superseded, superseded_by=%s\n",
				l.ID, l.UpdatedAt.Format(time.RFC3339), l.Tags, winner.ID)
		}
		if tagsEqual(winner.Tags, merged) {
			fmt.Printf("  tags on winner %s unchanged (already covers the group): %v\n", winner.ID, winner.Tags)
		} else {
			fmt.Printf("  MERGED TAGS on winner %s: %v -> %v\n", winner.ID, winner.Tags, merged)
		}
	}

	fmt.Printf("\n=== PROJECT_ID -> NULL (scope=workspace, status=active, project_id IS NOT NULL, excluding prospective losers above) ===\n")
	fmt.Printf("total rows: %d\n", len(nullCandidates))
	shown := nullCandidates
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, r := range shown {
		fmt.Printf("  id=%s key=%q project_id=%s -> NULL\n", r.ID, r.Key, r.ProjectID.String)
	}
	if len(nullCandidates) > len(shown) {
		fmt.Printf("  ... and %d more (raise -limit to print all)\n", len(nullCandidates)-len(shown))
	}
}

func applyCollapse(tx *sql.Tx, groups []group) error {
	for _, g := range groups {
		winner := g.Rows[0]
		losers := g.Rows[1:]
		merged := mergeTags(g.Rows)

		loserIDs := make([]string, len(losers))
		for i, l := range losers {
			loserIDs[i] = l.ID
		}

		if _, err := tx.Exec(
			`UPDATE memories SET status = 'superseded', superseded_by = $1::uuid
				WHERE id = ANY($2::uuid[]) AND status = 'active'`,
			winner.ID, pq.Array(loserIDs),
		); err != nil {
			return fmt.Errorf("supersede losers for workspace_id=%s key=%q: %w", g.WorkspaceID, g.Key, err)
		}

		if !tagsEqual(winner.Tags, merged) {
			if _, err := tx.Exec(
				`UPDATE memories SET tags = $1 WHERE id = $2::uuid`,
				pq.Array(merged), winner.ID,
			); err != nil {
				return fmt.Errorf("merge tags onto winner id=%s: %w", winner.ID, err)
			}
		}
	}
	return nil
}

func applyProjectIDNull(tx *sql.Tx, rows []memRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	_, err := tx.Exec(
		`UPDATE memories SET project_id = NULL
			WHERE id = ANY($1::uuid[]) AND scope = 'workspace' AND status = 'active'`,
		pq.Array(ids),
	)
	return err
}

func countMemories(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM memories`).Scan(&n)
	return n, err
}

func countActiveDuplicates(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`
		SELECT count(*) FROM (
			SELECT workspace_id, key FROM memories
			WHERE status = 'active' AND scope = 'workspace'
			GROUP BY 1, 2 HAVING count(*) > 1
		) d`).Scan(&n)
	return n, err
}

func countActiveWorkspaceWithProjectID(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM memories WHERE scope = 'workspace' AND project_id IS NOT NULL AND status = 'active'`,
	).Scan(&n)
	return n, err
}
