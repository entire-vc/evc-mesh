// Command backfill-memory-project-id fixes memories.project_id for scope='project'
// rows that predate the write-path inference fix in memory_service.go's Remember()
// (Mesh audit #1b010be6, plan:1.11: project_id was NULL on 4484/5161 memories).
//
// Two passes, in order:
//
//  1. source_task_id -> tasks.project_id. A deterministic join: every task has a
//     non-null project_id, so any memory whose source_task_id resolves to a live
//     task gets that task's project unambiguously. This is the DOMINANT shape per
//     the audit — the MCP client's active-task auto-populate (evc-mesh-mcp#44)
//     stamps source_task_id on almost every write, while a hand-typed
//     project:<slug> tag is comparatively rare.
//  2. project:<slug> tag -> projects.id, for whatever remains after pass 1. Uses
//     service.ResolveProjectSlugForTags — the EXACT SAME alias table and matching
//     rules Remember() itself uses at write time — rather than a second copy of
//     projectSlugAliases. A batch tool resolving slugs differently than the live
//     write path would silently backfill some rows to the wrong project; see that
//     function's doc for the full reasoning (CLAUDE-workflow.md §1q, the
//     `hold`-label drift class).
//
// Unlike cmd/collapse-memories (deliberately standalone, no internal/ dependency,
// so it could be reviewed independently of the bug it was fixing), this tool
// intentionally DOES import internal/service: its entire purpose is to match the
// live write path exactly, so importing the live resolver is the correct choice,
// not a shortcut.
//
// Dry-run by default; -dry-run=false applies inside one transaction with
// SELECT ... FOR UPDATE on every candidate set, so the plan printed is the exact
// snapshot acted on (same TOCTOU protection as cmd/collapse-memories).
//
// Usage:
//
//	go run ./cmd/backfill-memory-project-id                      # dry-run, prints the plan, writes nothing
//	go run ./cmd/backfill-memory-project-id -dry-run=false        # applies
//	go run ./cmd/backfill-memory-project-id -workspace-id=<uuid>  # restrict to one workspace (either mode)
//
// Connects using the same DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME/DB_SSL_MODE
// env vars as cmd/api and cmd/collapse-memories (internal/config/config.go).
//
// Rollback: every row this tool touches had project_id IS NULL beforehand — there
// is no prior value to restore, so "rollback" means setting project_id back to
// NULL for the rows this run touched. -dry-run=false prints every touched id
// before applying; redirect stdout to a file to keep that list as the rollback
// anchor:
//
//	go run ./cmd/backfill-memory-project-id -dry-run=false | tee backfill-run.log
//	# rollback, from the ids in backfill-run.log:
//	UPDATE memories SET project_id = NULL WHERE id = ANY('{<ids>}');
//
// A pg_dump of the memories table taken immediately before applying is the
// recommended anchor beyond that (CLAUDE-workflow.md §1h — Self-Verify-with-Rollback).
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/service"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run holds all resources (db, tx) as locals with their own defers, so a
// mid-function failure always unwinds through those defers before the process
// exits — main() itself never defers anything, which is what lets it use
// log.Fatal safely on the returned error (same structure as cmd/collapse-memories).
func run() error {
	dryRun := flag.Bool("dry-run", true, "print the backfill plan without writing (default true — pass -dry-run=false to apply)")
	limit := flag.Int("limit", 25, "max sample rows to print per section (0 = print all)")
	workspaceID := flag.String("workspace-id", "", "restrict to a single workspace_id (optional, default = all workspaces)")
	flag.Parse()

	db, err := sql.Open("postgres", buildDSN())
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	db.SetMaxOpenConns(5)
	if err = db.Ping(); err != nil {
		db.Close()
		return fmt.Errorf("ping: %w", err)
	}
	defer db.Close()

	totalBefore, err := countMemories(db)
	if err != nil {
		return fmt.Errorf("count memories before: %w", err)
	}

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

	taskCandidates, err := loadSourceTaskCandidates(tx, *workspaceID)
	if err != nil {
		return fmt.Errorf("load source_task_id candidates: %w", err)
	}

	// Tag candidates must be loaded AFTER the (not-yet-applied) task-based plan is
	// known, so rows the task pass will resolve are excluded from the tag pass's
	// preview — otherwise the printed plan would double-count rows both passes
	// could theoretically touch (a row can carry both source_task_id and a
	// resolvable tag; the task pass always wins per Remember()'s own priority —
	// see its comment on the write-path ordering).
	taskResolvedSet := make(map[string]bool, len(taskCandidates))
	for _, c := range taskCandidates {
		taskResolvedSet[c.id] = true
	}

	tagCandidates, err := loadTagCandidates(tx, *workspaceID)
	if err != nil {
		return fmt.Errorf("load tag candidates: %w", err)
	}
	var resolvedTagCandidates []tagCandidate
	for _, c := range tagCandidates {
		if taskResolvedSet[c.id] {
			continue
		}
		slug, ok := service.ResolveProjectSlugForTags(c.tags)
		if !ok {
			continue
		}
		projID, lookupErr := lookupProjectBySlug(tx, c.workspaceID, slug)
		if lookupErr != nil {
			return fmt.Errorf("lookup project by slug %q: %w", slug, lookupErr)
		}
		if projID == "" {
			continue
		}
		c.resolvedProjectID = projID
		c.resolvedSlug = slug
		resolvedTagCandidates = append(resolvedTagCandidates, c)
	}

	unresolvedBefore, err := countUnresolved(tx, *workspaceID)
	if err != nil {
		return fmt.Errorf("count unresolved before: %w", err)
	}

	printPlan(taskCandidates, resolvedTagCandidates, unresolvedBefore, *limit)

	if *dryRun {
		fmt.Println("\n[dry-run] no rows written (transaction rolled back). Re-run with -dry-run=false to apply.")
		return nil
	}

	fmt.Println("\n=== APPLYING ===")
	fmt.Println("touched ids (source_task_id pass):")
	for _, c := range taskCandidates {
		fmt.Printf("  %s\n", c.id)
	}
	n, err := applySourceTaskBackfill(tx, *workspaceID)
	if err != nil {
		return fmt.Errorf("apply source_task_id backfill: %w", err)
	}
	fmt.Printf("source_task_id pass: %d rows updated\n", n)

	fmt.Println("touched ids (tag pass):")
	for _, c := range resolvedTagCandidates {
		fmt.Printf("  %s (slug=%s)\n", c.id, c.resolvedSlug)
	}
	if err = applyTagBackfill(tx, resolvedTagCandidates); err != nil {
		return fmt.Errorf("apply tag backfill: %w", err)
	}
	fmt.Printf("tag pass: %d rows updated\n", len(resolvedTagCandidates))

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	totalAfter, err := countMemories(db)
	if err != nil {
		return fmt.Errorf("count memories after: %w", err)
	}
	unresolvedAfter, err := countUnresolvedNoTx(db, *workspaceID)
	if err != nil {
		return fmt.Errorf("verify AC (project_id backfilled): %w", err)
	}

	fmt.Println("\n=== VERIFICATION ===")
	fmt.Printf("total memories: before=%d after=%d (must match — nothing deleted)\n", totalBefore, totalAfter)
	fmt.Printf("AC `project_id IS NULL AND scope='project'` remaining: %d (task asks for 0)\n", unresolvedAfter)
	if unresolvedAfter > 0 {
		fmt.Println("NOTE: a nonzero remainder means those rows have neither a resolvable source_task_id")
		fmt.Println("nor a resolvable project:<slug> tag — there is no signal left to infer from. That is a")
		fmt.Println("genuinely different situation from a bug in this tool; inspect the remaining rows by hand.")
	}

	if totalBefore != totalAfter {
		return fmt.Errorf("FATAL: total row count changed even though no DELETE was issued — investigate before trusting this run")
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

func countMemories(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&n)
	return n, err
}

// unresolvedPredicate is the AC this whole tool exists to satisfy — kept as the
// ONE literal so the pre-run count, the plan preview, and the post-run
// verification can never silently disagree about what "unresolved" means.
// Qualified with the m. alias because two of its four call sites JOIN against
// tasks, which ALSO has a project_id column — an unqualified predicate compiles
// fine in the single-table call sites and fails with a genuinely confusing
// "column reference is ambiguous" only in the joined ones. Qualifying it
// everywhere means every call site uses the identical literal, joined or not.
const unresolvedPredicate = `m.scope = 'project' AND m.project_id IS NULL`

func countUnresolved(tx *sql.Tx, workspaceFilter string) (int, error) {
	q := `SELECT COUNT(*) FROM memories m WHERE ` + unresolvedPredicate
	args := []interface{}{}
	if workspaceFilter != "" {
		q += " AND m.workspace_id = $1"
		args = append(args, workspaceFilter)
	}
	var n int
	err := tx.QueryRow(q, args...).Scan(&n)
	return n, err
}

func countUnresolvedNoTx(db *sql.DB, workspaceFilter string) (int, error) {
	q := `SELECT COUNT(*) FROM memories m WHERE ` + unresolvedPredicate
	args := []interface{}{}
	if workspaceFilter != "" {
		q += " AND m.workspace_id = $1"
		args = append(args, workspaceFilter)
	}
	var n int
	err := db.QueryRow(q, args...).Scan(&n)
	return n, err
}

type taskCandidate struct {
	id            string
	key           string
	sourceTaskID  string
	taskProjectID string
}

// loadSourceTaskCandidates finds every unresolved memory whose source_task_id
// points at a live task, alongside that task's project_id — the join IS the
// resolution, so there is nothing further to decide per row.
func loadSourceTaskCandidates(tx *sql.Tx, workspaceFilter string) ([]taskCandidate, error) {
	q := `
		SELECT m.id, m.key, m.source_task_id, t.project_id
		FROM memories m
		JOIN tasks t ON t.id = m.source_task_id
		WHERE ` + unresolvedPredicate + `
		  AND m.source_task_id IS NOT NULL`
	args := []interface{}{}
	if workspaceFilter != "" {
		q += " AND m.workspace_id = $1"
		args = append(args, workspaceFilter)
	}
	q += " ORDER BY m.key, m.id FOR UPDATE OF m"

	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []taskCandidate
	for rows.Next() {
		var c taskCandidate
		if err := rows.Scan(&c.id, &c.key, &c.sourceTaskID, &c.taskProjectID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func applySourceTaskBackfill(tx *sql.Tx, workspaceFilter string) (int64, error) {
	q := `
		UPDATE memories m
		SET project_id = t.project_id, updated_at = NOW()
		FROM tasks t
		WHERE t.id = m.source_task_id
		  AND ` + unresolvedPredicate + `
		  AND m.source_task_id IS NOT NULL`
	args := []interface{}{}
	if workspaceFilter != "" {
		q += " AND m.workspace_id = $1"
		args = append(args, workspaceFilter)
	}
	result, err := tx.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type tagCandidate struct {
	id                string
	workspaceID       string
	key               string
	tags              []string
	resolvedProjectID string
	resolvedSlug      string
}

// loadTagCandidates finds every unresolved memory carrying at least one
// project:-prefixed tag — resolution (alias lookup + ambiguity check) happens in
// Go via service.ResolveProjectSlugForTags, not here, so this query is a rough
// pre-filter, not the decision itself.
func loadTagCandidates(tx *sql.Tx, workspaceFilter string) ([]tagCandidate, error) {
	q := `
		SELECT m.id, m.workspace_id, m.key, m.tags
		FROM memories m
		WHERE ` + unresolvedPredicate + `
		  AND EXISTS (SELECT 1 FROM unnest(m.tags) tg WHERE tg LIKE 'project:%')`
	args := []interface{}{}
	if workspaceFilter != "" {
		q += " AND m.workspace_id = $1"
		args = append(args, workspaceFilter)
	}
	q += " ORDER BY m.key, m.id FOR UPDATE OF m"

	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []tagCandidate
	for rows.Next() {
		var c tagCandidate
		var tags pq.StringArray
		if err := rows.Scan(&c.id, &c.workspaceID, &c.key, &tags); err != nil {
			return nil, err
		}
		c.tags = []string(tags)
		out = append(out, c)
	}
	return out, rows.Err()
}

func lookupProjectBySlug(tx *sql.Tx, workspaceID, slug string) (string, error) {
	var id string
	err := tx.QueryRow(
		`SELECT id FROM projects WHERE workspace_id = $1 AND slug = $2 AND deleted_at IS NULL`,
		workspaceID, slug,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func applyTagBackfill(tx *sql.Tx, candidates []tagCandidate) error {
	for _, c := range candidates {
		if _, err := tx.Exec(
			`UPDATE memories SET project_id = $1, updated_at = NOW() WHERE id = $2`,
			c.resolvedProjectID, c.id,
		); err != nil {
			return fmt.Errorf("update memory %s: %w", c.id, err)
		}
	}
	return nil
}

func printPlan(taskCandidates []taskCandidate, tagCandidates []tagCandidate, unresolvedBefore, limit int) {
	fmt.Printf("=== AC BEFORE: `%s` = %d ===\n\n", unresolvedPredicate, unresolvedBefore)

	fmt.Println("=== PASS 1: source_task_id -> tasks.project_id ===")
	fmt.Printf("candidates: %d\n", len(taskCandidates))
	printTaskSample(taskCandidates, limit)

	fmt.Println("\n=== PASS 2: project:<slug> tag -> projects.id (via service.ResolveProjectSlugForTags) ===")
	fmt.Printf("candidates: %d\n", len(tagCandidates))
	printTagSample(tagCandidates, limit)

	remaining := unresolvedBefore - len(taskCandidates) - len(tagCandidates)
	fmt.Printf("\nprojected remaining unresolved after both passes: %d\n", remaining)
}

func printTaskSample(cs []taskCandidate, limit int) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].key < cs[j].key })
	n := len(cs)
	if limit > 0 && n > limit {
		n = limit
	}
	for i := 0; i < n; i++ {
		c := cs[i]
		fmt.Printf("  %s  key=%s  source_task_id=%s -> project_id=%s\n", c.id, c.key, c.sourceTaskID, c.taskProjectID)
	}
	if limit > 0 && len(cs) > limit {
		fmt.Printf("  ... and %d more\n", len(cs)-limit)
	}
}

func printTagSample(cs []tagCandidate, limit int) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].key < cs[j].key })
	n := len(cs)
	if limit > 0 && n > limit {
		n = limit
	}
	for i := 0; i < n; i++ {
		c := cs[i]
		fmt.Printf("  %s  key=%s  tags=%v -> slug=%s -> project_id=%s\n", c.id, c.key, c.tags, c.resolvedSlug, c.resolvedProjectID)
	}
	if limit > 0 && len(cs) > limit {
		fmt.Printf("  ... and %d more\n", len(cs)-limit)
	}
}
