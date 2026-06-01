//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedPerfData inserts nMemories memories and nEdges edges into the DB under wsID.
// It returns the list of memory IDs in insertion order.
// batchSize controls the multi-row INSERT chunk size.
func seedPerfData(t *testing.T, db *sqlx.DB, wsID uuid.UUID, nMemories, nEdges, batchSize int) []uuid.UUID {
	t.Helper()
	ctx := context.Background()

	// ---- memories -------------------------------------------------------
	memIDs := make([]uuid.UUID, nMemories)
	for i := range memIDs {
		memIDs[i] = uuid.New()
	}

	for start := 0; start < nMemories; start += batchSize {
		end := start + batchSize
		if end > nMemories {
			end = nMemories
		}
		chunk := memIDs[start:end]
		n := len(chunk)

		placeholders := make([]string, n)
		args := make([]interface{}, 0, n*5)
		param := 1
		for j, id := range chunk {
			importanceScore := float32(0.5) + float32(j%5)*0.1
			key := fmt.Sprintf("perf-mem-%d", start+j)
			placeholders[j] = fmt.Sprintf("($%d,$%d,$%d,'test content','workspace','agent',1.0,$%d,'{}',NOW(),NOW())",
				param, param+1, param+2, param+3)
			args = append(args, id, wsID, key, importanceScore)
			param += 4
		}

		q := "INSERT INTO memories (id,workspace_id,key,content,scope,source_type,relevance,importance_score,tags,created_at,updated_at) VALUES " +
			strings.Join(placeholders, ",")
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}

	// ---- edges ----------------------------------------------------------
	type edgePair struct{ from, to int }
	seen := make(map[edgePair]bool, nEdges)
	edgePairs := make([]edgePair, 0, nEdges)

	rng := uint64(12345)
	nextRand := func() uint64 {
		rng = rng*6364136223846793005 + 1442695040888963407
		return rng
	}

	for len(edgePairs) < nEdges {
		from := int(nextRand() % uint64(nMemories))
		to := int(nextRand() % uint64(nMemories))
		if from == to {
			continue
		}
		p := edgePair{from, to}
		if seen[p] {
			continue
		}
		seen[p] = true
		edgePairs = append(edgePairs, p)
	}

	for start := 0; start < nEdges; start += batchSize {
		end := start + batchSize
		if end > nEdges {
			end = nEdges
		}
		chunk := edgePairs[start:end]
		n := len(chunk)

		placeholders := make([]string, n)
		args := make([]interface{}, 0, n*6)
		param := 1
		for j, p := range chunk {
			edgeID := uuid.New()
			weight := float32(0.3) + float32((p.from+p.to)%7)*0.1
			placeholders[j] = fmt.Sprintf("($%d,$%d,$%d,'relates_to',$%d,$%d)",
				param, param+1, param+2, param+3, param+4)
			args = append(args, edgeID, memIDs[p.from], memIDs[p.to], weight, wsID)
			param += 5
		}

		q := "INSERT INTO memory_edges (id,memory_from_id,memory_to_id,relationship_type,weight,workspace_id) VALUES " +
			strings.Join(placeholders, ",")
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}

	return memIDs
}

// TestGetNeighbors_Perf_P95_5kRows is an integration performance test that verifies
// GetNeighbors satisfies the epic G1 acceptance criterion of p95 < 500ms on a
// dataset of 5 000 memories and 15 000 edges.
func TestGetNeighbors_Perf_P95_5kRows(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	wsID := edgeTestWorkspace(t, db)
	t.Cleanup(edgeTestCleanup(t, db, wsID))

	const (
		nMemories = 5000
		nEdges    = 15000
		batchSize = 500
	)

	memIDs := seedPerfData(t, db, wsID, nMemories, nEdges, batchSize)

	frontier := memIDs[:10]

	const runs = 30
	durations := make([]time.Duration, runs)

	for i := 0; i < runs; i++ {
		start := time.Now()
		_, err := repo.GetNeighbors(ctx, frontier, 0.4)
		durations[i] = time.Since(start)
		require.NoError(t, err)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	p50 := durations[14]
	p95 := durations[28]
	p99 := durations[29]

	t.Logf("GetNeighbors @ 5k memories / 15k edges / frontier=10: p50=%v p95=%v p99=%v", p50, p95, p99)

	assert.Less(t, p95, 500*time.Millisecond,
		"p95 must be < 500ms (epic G1 acceptance criterion), got %v", p95)
}

// BenchmarkGetNeighbors_2Hop_5kMemories benchmarks GetNeighbors with the same
// 5k memory / 15k edge dataset and a frontier of 10 seed memories.
func BenchmarkGetNeighbors_2Hop_5kMemories(b *testing.B) {
	// testDB takes *testing.T; bridge via a T embedded in the benchmark.
	t := &testing.T{}
	db := testDB(t)
	if db == nil {
		b.Skip("could not connect to test database")
		return
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://evc:evc@localhost:5437/evc_mesh_test?sslmode=disable"
	}
	benchDB, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		b.Skipf("benchmark DB connect failed: %v", err)
		return
	}
	defer benchDB.Close()

	repo := NewMemoryEdgesRepo(benchDB)
	ctx := context.Background()

	// Create workspace for this benchmark run.
	wsID := uuid.New()
	_, err = benchDB.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
		 VALUES ($1, 'BenchWS', $2, $3, '{}', NOW(), NOW())`,
		wsID, "bench-ws-"+wsID.String()[:8], uuid.New(),
	)
	if err != nil {
		b.Fatalf("create benchmark workspace: %v", err)
	}
	defer func() {
		_, _ = benchDB.ExecContext(ctx, "DELETE FROM memory_edges WHERE workspace_id=$1", wsID)
		_, _ = benchDB.ExecContext(ctx, "DELETE FROM memories WHERE workspace_id=$1", wsID)
		_, _ = benchDB.ExecContext(ctx, "DELETE FROM workspaces WHERE id=$1", wsID)
	}()

	// Seed using a bridge *testing.T for the helper.
	// We call the seeding logic inline to avoid the testing.T coupling issue.
	const (
		nMemories = 5000
		nEdges    = 15000
		batchSize = 500
	)

	memIDs := make([]uuid.UUID, nMemories)
	for i := range memIDs {
		memIDs[i] = uuid.New()
	}

	// Insert memories in batches.
	for start := 0; start < nMemories; start += batchSize {
		end := start + batchSize
		if end > nMemories {
			end = nMemories
		}
		chunk := memIDs[start:end]
		n := len(chunk)

		placeholders := make([]string, n)
		args := make([]interface{}, 0, n*4)
		param := 1
		for j, id := range chunk {
			importanceScore := float32(0.5) + float32(j%5)*0.1
			key := fmt.Sprintf("bench-mem-%d", start+j)
			placeholders[j] = fmt.Sprintf("($%d,$%d,$%d,'test content','workspace','agent',1.0,$%d,'{}',NOW(),NOW())",
				param, param+1, param+2, param+3)
			args = append(args, id, wsID, key, importanceScore)
			param += 4
		}

		q := "INSERT INTO memories (id,workspace_id,key,content,scope,source_type,relevance,importance_score,tags,created_at,updated_at) VALUES " +
			strings.Join(placeholders, ",")
		if _, err := benchDB.ExecContext(ctx, q, args...); err != nil {
			b.Fatalf("seed memories batch %d: %v", start, err)
		}
	}

	// Insert edges in batches.
	type edgePair struct{ from, to int }
	seen := make(map[edgePair]bool, nEdges)
	edgePairs := make([]edgePair, 0, nEdges)

	rng := uint64(12345)
	nextRand := func() uint64 {
		rng = rng*6364136223846793005 + 1442695040888963407
		return rng
	}

	for len(edgePairs) < nEdges {
		from := int(nextRand() % uint64(nMemories))
		to := int(nextRand() % uint64(nMemories))
		if from == to {
			continue
		}
		p := edgePair{from, to}
		if seen[p] {
			continue
		}
		seen[p] = true
		edgePairs = append(edgePairs, p)
	}

	for start := 0; start < nEdges; start += batchSize {
		end := start + batchSize
		if end > nEdges {
			end = nEdges
		}
		chunk := edgePairs[start:end]
		n := len(chunk)

		placeholders := make([]string, n)
		args := make([]interface{}, 0, n*5)
		param := 1
		for j, p := range chunk {
			edgeID := uuid.New()
			weight := float32(0.3) + float32((p.from+p.to)%7)*0.1
			placeholders[j] = fmt.Sprintf("($%d,$%d,$%d,'relates_to',$%d,$%d)",
				param, param+1, param+2, param+3, param+4)
			args = append(args, edgeID, memIDs[p.from], memIDs[p.to], weight, wsID)
			param += 5
		}

		q := "INSERT INTO memory_edges (id,memory_from_id,memory_to_id,relationship_type,weight,workspace_id) VALUES " +
			strings.Join(placeholders, ",")
		if _, err := benchDB.ExecContext(ctx, q, args...); err != nil {
			b.Fatalf("seed edges batch %d: %v", start, err)
		}
	}

	frontier := memIDs[:10]

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := repo.GetNeighbors(ctx, frontier, 0.4)
		if err != nil {
			b.Fatalf("GetNeighbors iteration %d: %v", i, err)
		}
	}
}
