//go:build integration

package reconciler_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/reconciler"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://evc:evc@localhost:5437/evc_mesh_test?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// seedWorkspace inserts a throwaway workspace and returns its ID.
func seedWorkspace(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, "Reconciler Test WS", "rec-"+uuid.New().String()[:8], uuid.New(),
		json.RawMessage(`{}`), time.Now().UTC(), time.Now().UTC(),
	)
	require.NoError(t, err)
	return id
}

// seedProject inserts a throwaway project in ws and returns its ID. Used to build
// cross-scope same-key fixtures: the memories.key uniqueness constraint is scoped
// per (workspace,key)/(workspace,project,key)/(workspace,agent,key), each its own
// partial unique index (migration 088), so two memories can legitimately share the
// literal key string across different scopes without violating anything.
func seedProject(t *testing.T, db *sqlx.DB, ws uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	slug := "rec-test-" + id.String()[:8]
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO projects (id, workspace_id, name, slug)
		VALUES ($1, $2, $3, $4)`,
		id, ws, "Reconciler Test Project", slug,
	)
	require.NoError(t, err)
	return id
}

// seedMemory inserts a memory with explicit lifecycle timestamps/status and returns its ID.
type memSeed struct {
	workspaceID     uuid.UUID
	projectID       *uuid.UUID // non-nil => scope='project'; nil => scope='workspace'
	key             string
	status          domain.MemoryStatus
	importanceScore float32
	createdAt       time.Time
	updatedAt       time.Time
	validUntil      *time.Time
	contentSimhash  *int64
	lastAccessedAt  *time.Time
	tags            []string
}

func seedMemory(t *testing.T, db *sqlx.DB, s memSeed) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if s.status == "" {
		s.status = domain.MemoryStatusActive
	}
	tags := s.tags
	if tags == nil {
		tags = []string{}
	}
	scope := domain.ScopeWorkspace
	if s.projectID != nil {
		scope = domain.ScopeProject
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO memories
			(id, workspace_id, project_id, key, content, scope, tags, source_type, relevance,
			 importance_score, created_at, updated_at, status, freshness_score, valid_until,
			 content_simhash, last_accessed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'agent', 0.5,
		        $8, $9, $10, $11, 1.0, $12, $13, $14)`,
		id, s.workspaceID, s.projectID, s.key, "content for "+s.key, scope, pq.Array(tags),
		s.importanceScore, s.createdAt, s.updatedAt, s.status, s.validUntil,
		s.contentSimhash, s.lastAccessedAt,
	)
	require.NoError(t, err)
	return id
}

// getMemory returns status, tags, and superseded_by for a single memory — the fields
// TestReconciler_ReviewTriage_* assert on.
func getMemory(t *testing.T, db *sqlx.DB, id uuid.UUID) (status domain.MemoryStatus, tags []string, supersededBy *uuid.UUID) {
	t.Helper()
	var pqTags pq.StringArray
	err := db.QueryRowContext(context.Background(),
		`SELECT status, tags, superseded_by FROM memories WHERE id = $1`, id).
		Scan(&status, &pqTags, &supersededBy)
	require.NoError(t, err)
	return status, []string(pqTags), supersededBy
}

func getStatus(t *testing.T, db *sqlx.DB, id uuid.UUID) (status domain.MemoryStatus, freshness float32) {
	t.Helper()
	err := db.QueryRowContext(context.Background(),
		`SELECT status, freshness_score FROM memories WHERE id = $1`, id).Scan(&status, &freshness)
	require.NoError(t, err)
	return status, freshness
}

func newReconciler(db *sqlx.DB, epoch time.Time) *reconciler.MemoryReconciler {
	memRepo := postgres.NewMemoryRepo(db)
	edgeRepo := postgres.NewMemoryEdgesRepo(db)
	return reconciler.New(memRepo, edgeRepo, nil, reconciler.Config{Epoch: epoch})
}

func TestReconciler_ExpireByValidUntil(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)

	past := time.Now().Add(-1 * time.Minute)
	now := time.Now()
	id := seedMemory(t, db, memSeed{
		workspaceID: ws,
		key:         "expire:test:" + uuid.New().String()[:8],
		status:      domain.MemoryStatusActive,
		createdAt:   now.Add(-2 * time.Hour),
		updatedAt:   now.Add(-2 * time.Hour),
		validUntil:  &past,
	})

	// Epoch far in the past so stale marking is possible but shouldn't fire for this one.
	rec := newReconciler(db, now.Add(-365*24*time.Hour))
	require.NoError(t, rec.Run(ctx))

	status, freshness := getStatus(t, db, id)
	require.Equal(t, domain.MemoryStatusArchived, status)
	require.Equal(t, float32(0.0), freshness)
}

func TestReconciler_MarkStaleByAge(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)

	epoch := time.Now().Add(-60 * 24 * time.Hour)
	id := seedMemory(t, db, memSeed{
		workspaceID:     ws,
		key:             "stale:test:" + uuid.New().String()[:8],
		status:          domain.MemoryStatusActive,
		importanceScore: 0.5,
		createdAt:       time.Now().Add(-40 * 24 * time.Hour), // after epoch
		updatedAt:       time.Now().Add(-35 * 24 * time.Hour), // older than 30d staleAfter
	})

	rec := newReconciler(db, epoch)
	require.NoError(t, rec.Run(ctx))

	status, freshness := getStatus(t, db, id)
	require.Equal(t, domain.MemoryStatusStale, status)
	require.Equal(t, float32(0.25), freshness)
}

func TestReconciler_PreEpochNotTouched(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)

	epoch := time.Now().Add(-30 * 24 * time.Hour)
	id := seedMemory(t, db, memSeed{
		workspaceID:     ws,
		key:             "preepoch:test:" + uuid.New().String()[:8],
		status:          domain.MemoryStatusActive,
		importanceScore: 0.5,
		createdAt:       time.Now().Add(-45 * 24 * time.Hour), // BEFORE epoch
		updatedAt:       time.Now().Add(-40 * 24 * time.Hour), // stale-old
	})

	rec := newReconciler(db, epoch)
	require.NoError(t, rec.Run(ctx))

	status, _ := getStatus(t, db, id)
	require.Equal(t, domain.MemoryStatusActive, status, "pre-epoch memory must not be marked stale")
}

func TestReconciler_HighImportanceProtected(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)

	epoch := time.Now().Add(-60 * 24 * time.Hour)
	id := seedMemory(t, db, memSeed{
		workspaceID:     ws,
		key:             "highimp:test:" + uuid.New().String()[:8],
		status:          domain.MemoryStatusActive,
		importanceScore: 0.9, // >= 0.8, protected
		createdAt:       time.Now().Add(-40 * 24 * time.Hour),
		updatedAt:       time.Now().Add(-35 * 24 * time.Hour),
	})

	rec := newReconciler(db, epoch)
	require.NoError(t, rec.Run(ctx))

	status, _ := getStatus(t, db, id)
	require.Equal(t, domain.MemoryStatusActive, status, "high-importance memory must not be marked stale")
}

func TestReconciler_Idempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)

	past := time.Now().Add(-1 * time.Minute)
	now := time.Now()
	epoch := now.Add(-60 * 24 * time.Hour)

	expireID := seedMemory(t, db, memSeed{
		workspaceID: ws,
		key:         "idem-expire:test:" + uuid.New().String()[:8],
		status:      domain.MemoryStatusActive,
		createdAt:   now.Add(-2 * time.Hour),
		updatedAt:   now.Add(-2 * time.Hour),
		validUntil:  &past,
	})
	staleID := seedMemory(t, db, memSeed{
		workspaceID:     ws,
		key:             "idem-stale:test:" + uuid.New().String()[:8],
		status:          domain.MemoryStatusActive,
		importanceScore: 0.5,
		createdAt:       now.Add(-40 * 24 * time.Hour),
		updatedAt:       now.Add(-35 * 24 * time.Hour),
	})

	rec := newReconciler(db, epoch)
	require.NoError(t, rec.Run(ctx))
	require.NoError(t, rec.Run(ctx), "second run must not error")

	// States remain the terminal values from the first run.
	s1, f1 := getStatus(t, db, expireID)
	require.Equal(t, domain.MemoryStatusArchived, s1)
	require.Equal(t, float32(0.0), f1)

	s2, f2 := getStatus(t, db, staleID)
	require.Equal(t, domain.MemoryStatusStale, s2)
	require.Equal(t, float32(0.25), f2)

	// No supersedes edges should have been created (linker skipped: noop embedder).
	var edgeCount int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_edges WHERE workspace_id = $1 AND relationship_type = 'supersedes'`,
		ws).Scan(&edgeCount)
	require.NoError(t, err)
	require.Equal(t, 0, edgeCount, "monitor-only run must not create edges")
}

// ---------------------------------------------------------------------------
// TestReconciler_ReviewTriage_*
//
// Audit #1b010be6 (plan:1.11): 1784 review_needed memories (35%) with nothing
// revisiting them — runLinker (exercised above) only ever compares memories
// created in the last 24h against similar history, so a memory it once parked
// at review_needed stays there forever regardless of what happens later.
// RunReviewTriage is the standalone nightly phase that closes that gap.
// ---------------------------------------------------------------------------

// TestReconciler_Run_DoesNotTouchReviewNeeded is the RED control: it pins down
// the exact gap this task closes. Before RunReviewTriage existed, the only
// scheduled phase was Run() (monitor+linker) — this proves review_needed rows
// pass straight through it untouched, no matter how old, so the reader does
// not have to take "nothing revisits them" on faith.
func TestReconciler_Run_DoesNotTouchReviewNeeded(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)

	epoch := time.Now().Add(-365 * 24 * time.Hour)
	id := seedMemory(t, db, memSeed{
		workspaceID: ws,
		key:         "review-untouched:" + uuid.New().String()[:8],
		status:      domain.MemoryStatusReviewNeeded,
		createdAt:   time.Now().Add(-100 * 24 * time.Hour), // old enough that MarkStaleByAge
		updatedAt:   time.Now().Add(-100 * 24 * time.Hour), // WOULD fire if it applied to review_needed
	})

	rec := newReconciler(db, epoch)
	require.NoError(t, rec.Run(ctx))

	status, _, _ := getMemory(t, db, id)
	require.Equal(t, domain.MemoryStatusReviewNeeded, status,
		"Run() (monitor+linker) must NOT touch review_needed rows — MarkStaleByAge only targets status='active'; this is the gap RunReviewTriage exists to close")
}

func TestReconciler_ReviewTriage_SupersededBySameKey(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)
	now := time.Now()

	// The two rows deliberately sit in DIFFERENT scopes (workspace vs project),
	// sharing only the literal key string — memories.key uniqueness is enforced
	// PER SCOPE-IDENTITY (uq_mem_ws_key / uq_mem_proj_key / uq_mem_agent_key, three
	// separate partial indexes, migration 088), so a same-scope active+review_needed
	// pair sharing one key is actually impossible to construct (confirmed live:
	// the same-scope version of this fixture fails with
	// `duplicate key value violates unique constraint "uq_mem_ws_key"` — the DB
	// itself already forbids the worst case going forward). The realistic
	// same-key match this phase exists to catch is exactly this: the same fact
	// re-remembered under a different scope identity.
	proj := seedProject(t, db, ws)
	key := "review-samekey:" + uuid.New().String()[:8]
	oldID := seedMemory(t, db, memSeed{
		workspaceID: ws,
		key:         key,
		status:      domain.MemoryStatusReviewNeeded,
		createdAt:   now.Add(-10 * 24 * time.Hour),
		updatedAt:   now.Add(-10 * 24 * time.Hour),
	})
	// A newer, currently-active row sharing the exact same key, at scope=project.
	newID := seedMemory(t, db, memSeed{
		workspaceID: ws,
		projectID:   &proj,
		key:         key,
		status:      domain.MemoryStatusActive,
		createdAt:   now.Add(-1 * 24 * time.Hour),
		updatedAt:   now.Add(-1 * 24 * time.Hour),
	})

	rec := newReconciler(db, now.Add(-365*24*time.Hour))
	stats, err := rec.RunReviewTriage(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Errored)
	require.GreaterOrEqual(t, stats.Superseded, 1, "this run must have superseded at least our own fixture row")

	status, _, supersededBy := getMemory(t, db, oldID)
	require.Equal(t, domain.MemoryStatusSuperseded, status)
	require.NotNil(t, supersededBy)
	require.Equal(t, newID, *supersededBy)

	// supersede() (reused from the linker's decision engine) must also record the edge.
	var edgeCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_edges
		 WHERE workspace_id = $1 AND memory_from_id = $2 AND memory_to_id = $3 AND relationship_type = 'supersedes'`,
		ws, newID, oldID).Scan(&edgeCount))
	require.Equal(t, 1, edgeCount, "review-triage supersede must create the same supersedes edge the linker does")
}

func TestReconciler_ReviewTriage_SupersededBySameSimhash(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)
	now := time.Now()

	var simhash int64 = 0x0F0F0F0F
	oldID := seedMemory(t, db, memSeed{
		workspaceID:    ws,
		key:            "review-simhash-old:" + uuid.New().String()[:8],
		status:         domain.MemoryStatusReviewNeeded,
		createdAt:      now.Add(-10 * 24 * time.Hour),
		updatedAt:      now.Add(-10 * 24 * time.Hour),
		contentSimhash: &simhash,
	})
	// Different key, SAME content_simhash, newer, active.
	newID := seedMemory(t, db, memSeed{
		workspaceID:    ws,
		key:            "review-simhash-new:" + uuid.New().String()[:8],
		status:         domain.MemoryStatusActive,
		createdAt:      now.Add(-1 * 24 * time.Hour),
		updatedAt:      now.Add(-1 * 24 * time.Hour),
		contentSimhash: &simhash,
	})

	rec := newReconciler(db, now.Add(-365*24*time.Hour))
	stats, err := rec.RunReviewTriage(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Errored)
	require.GreaterOrEqual(t, stats.Superseded, 1)

	status, _, supersededBy := getMemory(t, db, oldID)
	require.Equal(t, domain.MemoryStatusSuperseded, status)
	require.NotNil(t, supersededBy)
	require.Equal(t, newID, *supersededBy)
}

func TestReconciler_ReviewTriage_OlderCandidateNotSuperseded(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)
	now := time.Now()

	// Cross-scope for the same reason as SupersededBySameKey above: an active row
	// and a review_needed row sharing one key can only coexist across different
	// scope identities (each has its own partial unique index).
	proj := seedProject(t, db, ws)
	key := "review-older-cand:" + uuid.New().String()[:8]
	// An OLDER active row sharing the key — must NOT supersede the review_needed
	// row (candidates must be created strictly AFTER it).
	seedMemory(t, db, memSeed{
		workspaceID: ws,
		projectID:   &proj,
		key:         key,
		status:      domain.MemoryStatusActive,
		createdAt:   now.Add(-20 * 24 * time.Hour),
		updatedAt:   now.Add(-20 * 24 * time.Hour),
	})
	reviewID := seedMemory(t, db, memSeed{
		workspaceID:    ws,
		key:            key,
		status:         domain.MemoryStatusReviewNeeded,
		createdAt:      now.Add(-10 * 24 * time.Hour),
		updatedAt:      now.Add(-10 * 24 * time.Hour),
		lastAccessedAt: &now, // recently accessed -> falls through to "active", not "stale"
	})

	rec := newReconciler(db, now.Add(-365*24*time.Hour))
	stats, err := rec.RunReviewTriage(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Errored)

	status, tags, supersededBy := getMemory(t, db, reviewID)
	require.Equal(t, domain.MemoryStatusActive, status)
	require.Nil(t, supersededBy)
	require.Contains(t, tags, "auto-reviewed")
}

func TestReconciler_ReviewTriage_NoMatchOldNoAccess_MarkedStale(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)
	now := time.Now()

	id := seedMemory(t, db, memSeed{
		workspaceID: ws,
		key:         "review-stale:" + uuid.New().String()[:8],
		status:      domain.MemoryStatusReviewNeeded,
		createdAt:   now.Add(-70 * 24 * time.Hour), // > 60d default review-stale window
		updatedAt:   now.Add(-70 * 24 * time.Hour),
		// last_accessed_at left nil -> falls back to created_at
	})

	rec := newReconciler(db, now.Add(-365*24*time.Hour))
	stats, err := rec.RunReviewTriage(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Errored)

	status, tags, supersededBy := getMemory(t, db, id)
	require.Equal(t, domain.MemoryStatusStale, status)
	require.Nil(t, supersededBy)
	require.NotContains(t, tags, "auto-reviewed", "a memory marked stale must not also be tagged auto-reviewed")
}

func TestReconciler_ReviewTriage_NoMatchRecentAccess_MarkedActive(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)
	now := time.Now()

	// created_at is old enough that it WOULD be stale on created_at alone, but
	// last_accessed_at is recent — access recency must win over creation age.
	recentAccess := now.Add(-1 * 24 * time.Hour)
	id := seedMemory(t, db, memSeed{
		workspaceID:    ws,
		key:            "review-recent-access:" + uuid.New().String()[:8],
		status:         domain.MemoryStatusReviewNeeded,
		createdAt:      now.Add(-90 * 24 * time.Hour),
		updatedAt:      now.Add(-90 * 24 * time.Hour),
		lastAccessedAt: &recentAccess,
		tags:           []string{"kind:fact"},
	})

	rec := newReconciler(db, now.Add(-365*24*time.Hour))
	stats, err := rec.RunReviewTriage(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Errored)

	status, tags, _ := getMemory(t, db, id)
	require.Equal(t, domain.MemoryStatusActive, status)
	require.ElementsMatch(t, []string{"kind:fact", "auto-reviewed"}, tags,
		"existing tags must survive, auto-reviewed appended once")
}

func TestReconciler_ReviewTriage_Idempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws := seedWorkspace(t, db)
	now := time.Now()

	id := seedMemory(t, db, memSeed{
		workspaceID:    ws,
		key:            "review-idem:" + uuid.New().String()[:8],
		status:         domain.MemoryStatusReviewNeeded,
		createdAt:      now.Add(-90 * 24 * time.Hour),
		updatedAt:      now.Add(-90 * 24 * time.Hour),
		lastAccessedAt: &now,
	})

	rec := newReconciler(db, now.Add(-365*24*time.Hour))
	stats1, err := rec.RunReviewTriage(ctx)
	require.NoError(t, err)
	require.Zero(t, stats1.Errored)
	require.GreaterOrEqual(t, stats1.Active, 1)

	// Second run: OUR row is no longer review_needed, so this specific row must not
	// be reconsidered. (Considered may be nonzero from unrelated leftover fixtures
	// created by other tests sharing this DB — see ListReviewNeeded's doc: it is
	// deliberately unfiltered by workspace, matching MarkStaleByAge/ExpireByValidUntil.
	// What must hold regardless is that OUR id's tag count below stays at 1.)
	_, err = rec.RunReviewTriage(ctx)
	require.NoError(t, err)

	status, tags, _ := getMemory(t, db, id)
	require.Equal(t, domain.MemoryStatusActive, status)
	require.Equal(t, 1, countOccurrences(tags, "auto-reviewed"), "tag must not be duplicated across runs")
}

func countOccurrences(tags []string, tag string) int {
	n := 0
	for _, t := range tags {
		if t == tag {
			n++
		}
	}
	return n
}
