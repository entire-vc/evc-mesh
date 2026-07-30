// Command collapse-memories dedupes rows in the memories table by declared
// scope-identity, and clears the stray project_id that resolveProjectSlug
// used to stamp onto workspace-scope rows before PR #444
// (memory_service.go:481-487) — see task #4edf3fb5.
//
// It is intentionally standalone: no dependency on internal/service or
// internal/repository, so it can be built, reviewed and run independently
// of the application code that produced the bug (task #4edf3fb5, subtask 4).
//
// Group predicate is `status <> 'superseded' AND archived = false` — the
// same "current" definition GetByKey (memory_repo.go) and every read path
// (recall/FTS/vector) use, NOT `status = 'active'`. Task #2c0154db/F2a found
// that the original status='active' predicate missed every group where a
// colliding row sat in status='review_needed': those rows are surfaced as
// current by every read path but were invisible to this tool's original
// dedup pass, so 12 keys kept handing recall multiple live-looking versions
// of the same fact after the first collapse. Winner selection must use the
// exact same predicate GetByKey uses for identity-lookup, or the two can
// disagree about which row is "the" current one.
//
// Grouped by scope-identity, matching GetByKey's three branches exactly:
//   - scope=workspace -> (workspace_id, key)
//   - scope=project   -> (workspace_id, project_id, key)
//   - scope=agent     -> (workspace_id, agent_id, key)
//
// For each group with more than one current row:
//   - winner = most recent by updated_at (tie-break: larger id) — the same
//     ORDER BY GetByKey now applies
//   - losers -> status='superseded', superseded_by=<winner id>
//   - winner.tags <- distinct union of tags across the whole group
//
// Separately, every current scope=workspace row that still has project_id
// set (winners and never-duplicated rows alike) gets project_id set to
// NULL. Hard DELETE is never issued — superseded rows stay queryable for
// audit, matching the existing status/superseded_by columns (migration
// 20260704080_memory_health_lifecycle.sql).
//
// Idempotent: a second run finds no groups with >1 current row (losers are
// no longer status<>'superseded') and no current rows left with a stray
// project_id, so it is a no-op. Safe to re-run after a partial failure.
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
	Scope       string // "workspace" | "project" | "agent"
	WorkspaceID string
	// IdentityExtra is the project_id (scope=project) or agent_id
	// (scope=agent) that, together with WorkspaceID+Key, makes up the full
	// scope-identity tuple. Empty for scope=workspace.
	IdentityExtra string
	Key           string
	// Rows is winner-first: sorted by updated_at DESC, id DESC by the query.
	Rows []memRow
}

// scopeIdentityConfig describes how to load candidate rows for one scope's
// identity grouping — the column that supplies IdentityExtra (empty for
// workspace scope, which has none), and the WHERE clause selecting rows of
// that scope.
type scopeIdentityConfig struct {
	scope        string
	identityCol  string // "" for workspace (no extra identity column)
	requireExtra bool   // true if identityCol must be NOT NULL to form a valid identity
}

var scopeConfigs = []scopeIdentityConfig{
	{scope: "workspace", identityCol: ""},
	{scope: "project", identityCol: "project_id", requireExtra: true},
	{scope: "agent", identityCol: "agent_id", requireExtra: true},
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

// currentPredicate is the single definition of "current" every read path
// (recall/FTS/vector) and GetByKey's identity-lookup share. Keep this the
// ONE place that literal lives — see task #2c0154db/F2a and the sibling
// gap this exact drift caused (learning-status-cleanup-must-match-every-
// status-predicate): a predicate typed out twice always ends up meaning two
// different things eventually.
const currentPredicate = "status <> 'superseded' AND archived = false"

func loadDuplicateGroups(tx *sql.Tx, workspaceFilter string) ([]group, error) {
	var dupGroups []group
	for _, cfg := range scopeConfigs {
		groups, err := loadDuplicateGroupsForScope(tx, cfg, workspaceFilter)
		if err != nil {
			return nil, fmt.Errorf("scope=%s: %w", cfg.scope, err)
		}
		dupGroups = append(dupGroups, groups...)
	}
	return dupGroups, nil
}

func loadDuplicateGroupsForScope(tx *sql.Tx, cfg scopeIdentityConfig, workspaceFilter string) ([]group, error) {
	extraSelect := "'' AS identity_extra"
	extraOrder := ""
	if cfg.identityCol != "" {
		extraSelect = cfg.identityCol + "::text AS identity_extra"
		extraOrder = "identity_extra, "
	}

	q := fmt.Sprintf(`SELECT id, workspace_id, %s, key, tags, project_id, updated_at
		FROM memories
		WHERE scope = $1 AND %s`, extraSelect, currentPredicate)
	args := []interface{}{cfg.scope}
	if cfg.requireExtra {
		q += fmt.Sprintf(" AND %s IS NOT NULL", cfg.identityCol)
	}
	if workspaceFilter != "" {
		q += fmt.Sprintf(" AND workspace_id = $%d", len(args)+1)
		args = append(args, workspaceFilter)
	}
	q += fmt.Sprintf(" ORDER BY workspace_id, %skey, updated_at DESC, id DESC FOR UPDATE", extraOrder)

	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := map[string]*group{}
	var order []string
	for rows.Next() {
		var (
			id, wsID, extra, key string
			tags                 []string
			projectID            sql.NullString
			updatedAt            time.Time
		)
		if err := rows.Scan(&id, &wsID, &extra, &key, pq.Array(&tags), &projectID, &updatedAt); err != nil {
			return nil, err
		}
		gk := wsID + "\x00" + extra + "\x00" + key
		g, ok := byKey[gk]
		if !ok {
			g = &group{Scope: cfg.scope, WorkspaceID: wsID, IdentityExtra: extra, Key: key}
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
		WHERE scope = 'workspace' AND ` + currentPredicate + ` AND project_id IS NOT NULL`
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
	fmt.Println("=== DUPLICATE GROUPS (all scopes, current = status<>'superseded' AND archived=false, count>1) ===")
	if len(groups) == 0 {
		fmt.Println("(none)")
	}
	for _, g := range groups {
		winner := g.Rows[0]
		losers := g.Rows[1:]
		merged := mergeTags(g.Rows)
		identity := fmt.Sprintf("workspace_id=%s", g.WorkspaceID)
		if g.IdentityExtra != "" {
			identity += fmt.Sprintf(" %s_id=%s", g.Scope, g.IdentityExtra)
		}
		fmt.Printf("\nscope=%s %s key=%q (%d rows)\n", g.Scope, identity, g.Key, len(g.Rows))
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

	fmt.Printf("\n=== PROJECT_ID -> NULL (scope=workspace, current, project_id IS NOT NULL, excluding prospective losers above) ===\n")
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
				WHERE id = ANY($2::uuid[]) AND `+currentPredicate,
			winner.ID, pq.Array(loserIDs),
		); err != nil {
			return fmt.Errorf("supersede losers for scope=%s workspace_id=%s key=%q: %w", g.Scope, g.WorkspaceID, g.Key, err)
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
			WHERE id = ANY($1::uuid[]) AND scope = 'workspace' AND `+currentPredicate,
		pq.Array(ids),
	)
	return err
}

func countMemories(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM memories`).Scan(&n)
	return n, err
}

// countActiveDuplicates is the "same-key-any-scope-2plus-READABLE" metric
// from task #2c0154db/F2a's own AC: groups by each scope's real identity
// tuple over the CURRENT predicate (not status='active'), matching exactly
// what loadDuplicateGroupsForScope collapses. A non-zero result here is what
// a recall() call can observe as "multiple live versions of one fact".
func countActiveDuplicates(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`
		SELECT count(*) FROM (
			SELECT workspace_id, key FROM memories
			WHERE ` + currentPredicate + ` AND scope = 'workspace'
			GROUP BY 1, 2 HAVING count(*) > 1
			UNION ALL
			SELECT workspace_id, project_id::text || key FROM memories
			WHERE ` + currentPredicate + ` AND scope = 'project' AND project_id IS NOT NULL
			GROUP BY 1, 2 HAVING count(*) > 1
			UNION ALL
			SELECT workspace_id, agent_id::text || key FROM memories
			WHERE ` + currentPredicate + ` AND scope = 'agent' AND agent_id IS NOT NULL
			GROUP BY 1, 2 HAVING count(*) > 1
		) d`).Scan(&n)
	return n, err
}

func countActiveWorkspaceWithProjectID(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM memories WHERE scope = 'workspace' AND project_id IS NOT NULL AND ` + currentPredicate,
	).Scan(&n)
	return n, err
}
