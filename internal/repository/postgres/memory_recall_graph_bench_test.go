//go:build integration

package postgres

import (
	"context"
	"fmt"
	"math/rand"
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

// ---------------------------------------------------------------------------
// setupRecallGraphBenchData
// ---------------------------------------------------------------------------

// setupRecallGraphBenchData inserts memCount memory rows and edgeCount directed
// edges into an isolated workspace. Memories alternate between importance_score
// 0.5 and 0.8. Edges have random weights in [0.5, 1.0). All inserts are done
// in batches (500 memories / 1000 edges per statement) to avoid statement timeouts.
//
// A t.Cleanup func is registered that removes all inserted rows (edges, memories,
// workspace). Returns the workspace ID and the slice of inserted memory IDs.
func setupRecallGraphBenchData(t *testing.T, db *sqlx.DB, memCount, edgeCount int) (wsID uuid.UUID, memIDs []uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	// ── Workspace ─────────────────────────────────────────────────────────────
	wsID = uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, '{}', NOW(), NOW())`,
		wsID,
		"bench-ws-"+wsID.String()[:8],
		"bench-"+wsID.String()[:8],
		uuid.New(),
	)
	require.NoError(t, err, "create bench workspace")

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM memory_edges WHERE workspace_id = $1", wsID)
		_, _ = db.ExecContext(ctx, "DELETE FROM memories      WHERE workspace_id = $1", wsID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces    WHERE id            = $1", wsID)
	})

	// ── Memories — batches of 500 ─────────────────────────────────────────────
	memIDs = make([]uuid.UUID, memCount)
	for i := range memIDs {
		memIDs[i] = uuid.New()
	}

	const memBatch = 500
	for batchStart := 0; batchStart < memCount; batchStart += memBatch {
		batchEnd := batchStart + memBatch
		if batchEnd > memCount {
			batchEnd = memCount
		}
		batch := memIDs[batchStart:batchEnd]

		// Build a single INSERT … VALUES (…),(…),… statement.
		var sb strings.Builder
		sb.WriteString(
			`INSERT INTO memories
			  (id, workspace_id, key, content, scope, source_type, relevance, importance_score, tags, created_at, updated_at)
			 VALUES `)

		args := make([]interface{}, 0, len(batch)*4)
		for i, id := range batch {
			if i > 0 {
				sb.WriteString(", ")
			}
			importance := float32(0.5)
			if (batchStart+i)%2 == 1 {
				importance = 0.8
			}
			base := i*4 + 1
			fmt.Fprintf(&sb,
				"($%d, $%d, $%d, 'bench content', 'workspace', 'agent', 1.0, $%d, '{}', NOW(), NOW())",
				base, base+1, base+2, base+3,
			)
			args = append(args, id, wsID, "bench-mem-"+id.String()[:8], importance)
		}
		_, err := db.ExecContext(ctx, sb.String(), args...)
		require.NoError(t, err, "bulk memory insert (batch starting at %d)", batchStart)
	}

	// ── Edges — batches of 1000 ───────────────────────────────────────────────
	if edgeCount > 0 {
		rng := rand.New(rand.NewSource(42))

		type edgeSpec struct {
			fromIdx, toIdx int
			weight         float32
		}
		specs := make([]edgeSpec, 0, edgeCount)
		seen := make(map[[2]int]bool, edgeCount)

		for len(specs) < edgeCount {
			fi := rng.Intn(memCount)
			ti := rng.Intn(memCount)
			if fi == ti || seen[[2]int{fi, ti}] {
				continue
			}
			seen[[2]int{fi, ti}] = true
			weight := float32(0.5) + rng.Float32()*0.5 // [0.5, 1.0)
			specs = append(specs, edgeSpec{fi, ti, weight})
		}

		const edgeBatch = 1000
		for batchStart := 0; batchStart < len(specs); batchStart += edgeBatch {
			batchEnd := batchStart + edgeBatch
			if batchEnd > len(specs) {
				batchEnd = len(specs)
			}
			batch := specs[batchStart:batchEnd]

			var sb strings.Builder
			sb.WriteString(
				`INSERT INTO memory_edges
				  (id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
				 VALUES `)

			args := make([]interface{}, 0, len(batch)*5)
			for i, e := range batch {
				if i > 0 {
					sb.WriteString(", ")
				}
				base := i*5 + 1
				fmt.Fprintf(&sb, "($%d, $%d, $%d, 'relates_to', $%d, $%d)", base, base+1, base+2, base+3, base+4)
				args = append(args, uuid.New(), memIDs[e.fromIdx], memIDs[e.toIdx], e.weight, wsID)
			}
			_, err := db.ExecContext(ctx, sb.String(), args...)
			require.NoError(t, err, "bulk edge insert (batch starting at %d)", batchStart)
		}
	}

	return wsID, memIDs
}

// ---------------------------------------------------------------------------
// TestRecallGraph_GetNeighbors_P95_Under500ms
// ---------------------------------------------------------------------------

// TestRecallGraph_GetNeighbors_P95_Under500ms seeds 5000 memories and 15000
// edges, then runs GetNeighbors 50 times with a random 10-node frontier and
// asserts that the p95 latency is under 500ms.
func TestRecallGraph_GetNeighbors_P95_Under500ms(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	const (
		memCount    = 5000
		edgeCount   = 15000
		runs        = 50
		frontierSz  = 10
		p95Deadline = 500 * time.Millisecond
	)

	_, memIDs := setupRecallGraphBenchData(t, db, memCount, edgeCount)

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		frontier := make([]uuid.UUID, frontierSz)
		for j := range frontier {
			frontier[j] = memIDs[rng.Intn(len(memIDs))]
		}

		start := time.Now()
		_, err := repo.GetNeighbors(ctx, frontier, 0.3, 200)
		elapsed := time.Since(start)
		require.NoError(t, err)
		durations = append(durations, elapsed)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	n := len(durations)
	p95Idx := int(float64(n)*0.95) - 1
	if p95Idx < 0 {
		p95Idx = 0
	}
	p95 := durations[p95Idx]

	t.Logf("GetNeighbors p95 over %d runs (frontier=%d, %d mems, %d edges): %v",
		runs, frontierSz, memCount, edgeCount, p95)

	assert.Less(t, p95, p95Deadline,
		"p95 latency %v must be below %v", p95, p95Deadline)
}

// ---------------------------------------------------------------------------
// BenchmarkGetNeighbors_5kGraph_2Hop
// ---------------------------------------------------------------------------

// BenchmarkGetNeighbors_5kGraph_2Hop benchmarks GetNeighbors on a 5k-memory /
// 15k-edge graph using a 10-node random frontier.  Data is seeded once for the
// entire benchmark run via b.Run so cleanup is registered correctly.
func BenchmarkGetNeighbors_5kGraph_2Hop(b *testing.B) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://evc:evc@localhost:5437/evc_mesh_test?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		b.Fatalf("bench db connect: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	const (
		memCount   = 5000
		edgeCount  = 15000
		frontierSz = 10
	)

	// Use b.Run to get a real *testing.T-compatible handle for setup helpers.
	var (
		memIDs []uuid.UUID
		repo   *MemoryEdgesRepo
	)

	// Seed data inside a sub-benchmark so cleanup is tracked; b.N runs happen
	// after setup completes.
	b.Run("setup", func(b *testing.B) {
		// One-time setup — run only during the very first (and only) sub-benchmark
		// invocation. b.N is forced to 1 for this sub-benchmark.
		ctx := context.Background()
		wsID := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, '{}', NOW(), NOW())`,
			wsID, "bench-bg-"+wsID.String()[:8], "bench-bg-"+wsID.String()[:8], uuid.New(),
		)
		if err != nil {
			b.Fatalf("create bench workspace: %v", err)
		}
		b.Cleanup(func() {
			ctx2 := context.Background()
			_, _ = db.ExecContext(ctx2, "DELETE FROM memory_edges WHERE workspace_id=$1", wsID)
			_, _ = db.ExecContext(ctx2, "DELETE FROM memories     WHERE workspace_id=$1", wsID)
			_, _ = db.ExecContext(ctx2, "DELETE FROM workspaces   WHERE id=$1", wsID)
		})

		rng := rand.New(rand.NewSource(42))
		memIDs = make([]uuid.UUID, memCount)
		for i := range memIDs {
			memIDs[i] = uuid.New()
		}

		// Insert memories in batches of 500.
		const memBatch = 500
		for start := 0; start < memCount; start += memBatch {
			end := start + memBatch
			if end > memCount {
				end = memCount
			}
			batch := memIDs[start:end]
			var sb strings.Builder
			sb.WriteString(`INSERT INTO memories (id, workspace_id, key, content, scope, source_type, relevance, importance_score, tags, created_at, updated_at) VALUES `)
			args := make([]interface{}, 0, len(batch)*4)
			for i, id := range batch {
				if i > 0 {
					sb.WriteString(", ")
				}
				imp := float32(0.5)
				if (start+i)%2 == 1 {
					imp = 0.8
				}
				base := i*4 + 1
				fmt.Fprintf(&sb, "($%d,$%d,$%d,'bench','workspace','agent',1.0,$%d,'{}',NOW(),NOW())", base, base+1, base+2, base+3)
				args = append(args, id, wsID, "bg-mem-"+id.String()[:8], imp)
			}
			if _, err := db.ExecContext(ctx, sb.String(), args...); err != nil {
				b.Fatalf("bulk mem insert: %v", err)
			}
		}

		// Insert edges in batches of 1000.
		type eSpec struct {
			fi, ti int
			w      float32
		}
		specs := make([]eSpec, 0, edgeCount)
		seen := make(map[[2]int]bool, edgeCount)
		for len(specs) < edgeCount {
			fi := rng.Intn(memCount)
			ti := rng.Intn(memCount)
			if fi == ti || seen[[2]int{fi, ti}] {
				continue
			}
			seen[[2]int{fi, ti}] = true
			specs = append(specs, eSpec{fi, ti, float32(0.5) + rng.Float32()*0.5})
		}
		const edgeBatch = 1000
		for start := 0; start < len(specs); start += edgeBatch {
			end := start + edgeBatch
			if end > len(specs) {
				end = len(specs)
			}
			batch := specs[start:end]
			var sb strings.Builder
			sb.WriteString(`INSERT INTO memory_edges (id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id) VALUES `)
			args := make([]interface{}, 0, len(batch)*5)
			for i, e := range batch {
				if i > 0 {
					sb.WriteString(", ")
				}
				base := i*5 + 1
				fmt.Fprintf(&sb, "($%d,$%d,$%d,'relates_to',$%d,$%d)", base, base+1, base+2, base+3, base+4)
				args = append(args, uuid.New(), memIDs[e.fi], memIDs[e.ti], e.w, wsID)
			}
			if _, err := db.ExecContext(ctx, sb.String(), args...); err != nil {
				b.Fatalf("bulk edge insert: %v", err)
			}
		}

		repo = NewMemoryEdgesRepo(db)
	})

	if repo == nil || len(memIDs) == 0 {
		b.Fatal("bench data setup failed")
	}

	ctx := context.Background()
	rng := rand.New(rand.NewSource(99))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		frontier := make([]uuid.UUID, frontierSz)
		for j := range frontier {
			frontier[j] = memIDs[rng.Intn(len(memIDs))]
		}
		if _, err := repo.GetNeighbors(ctx, frontier, 0.3, 200); err != nil {
			b.Fatalf("GetNeighbors: %v", err)
		}
	}
}
