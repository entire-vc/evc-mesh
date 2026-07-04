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
	_ "github.com/lib/pq"
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

// seedMemory inserts a memory with explicit lifecycle timestamps/status and returns its ID.
type memSeed struct {
	workspaceID     uuid.UUID
	key             string
	status          domain.MemoryStatus
	importanceScore float32
	createdAt       time.Time
	updatedAt       time.Time
	validUntil      *time.Time
}

func seedMemory(t *testing.T, db *sqlx.DB, s memSeed) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if s.status == "" {
		s.status = domain.MemoryStatusActive
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO memories
			(id, workspace_id, key, content, scope, tags, source_type, relevance,
			 importance_score, created_at, updated_at, status, freshness_score, valid_until)
		VALUES ($1, $2, $3, $4, 'workspace', ARRAY[]::text[], 'agent', 0.5,
		        $5, $6, $7, $8, 1.0, $9)`,
		id, s.workspaceID, s.key, "content for "+s.key,
		s.importanceScore, s.createdAt, s.updatedAt, s.status, s.validUntil,
	)
	require.NoError(t, err)
	return id
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
