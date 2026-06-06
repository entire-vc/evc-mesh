//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// edgeTestWorkspace inserts a workspace row and returns its ID.
// The caller registers cleanup for the workspace and its dependent rows.
func edgeTestWorkspace(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	wsID := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
		 VALUES ($1, 'EdgeTest WS', $2, $3, '{}', NOW(), NOW())`,
		wsID, "et-ws-"+wsID.String()[:8], uuid.New(),
	)
	require.NoError(t, err)
	return wsID
}

// edgeTestMemory inserts a minimal memory row in wsID with the given importance score.
func edgeTestMemory(t *testing.T, db *sqlx.DB, wsID uuid.UUID, importance float32) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO memories (id, workspace_id, key, content, scope, source_type, relevance, importance_score, tags, created_at, updated_at)
		 VALUES ($1, $2, $3, 'test content', 'workspace', 'agent', 1.0, $4, '{}', NOW(), NOW())`,
		id, wsID, "et-mem-"+id.String()[:8], importance,
	)
	require.NoError(t, err)
	return id
}

// edgeTestCleanup removes all edges and memories for wsID, then the workspace itself.
func edgeTestCleanup(t *testing.T, db *sqlx.DB, wsID uuid.UUID) func() {
	t.Helper()
	return func() {
		ctx := context.Background()
		_, _ = db.ExecContext(ctx, "DELETE FROM memory_edges WHERE workspace_id=$1", wsID)
		_, _ = db.ExecContext(ctx, "DELETE FROM memories WHERE workspace_id=$1", wsID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id=$1", wsID)
	}
}

func TestMemoryEdgesRepo_UpsertEdge_InsertAndConflict(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	wsID := edgeTestWorkspace(t, db)
	t.Cleanup(edgeTestCleanup(t, db, wsID))

	fromID := edgeTestMemory(t, db, wsID, 0.6)
	toID := edgeTestMemory(t, db, wsID, 0.6)

	// First insert.
	edge := &domain.MemoryEdge{
		MemoryFromID:     fromID,
		MemoryToID:       toID,
		RelationshipType: domain.EdgeRelatesTo,
		Weight:           0.8,
		WorkspaceID:      wsID,
	}
	require.NoError(t, repo.UpsertEdge(ctx, edge))
	assert.NotEqual(t, uuid.Nil, edge.ID, "ID should be assigned")

	// Conflict with a higher weight — GREATEST(0.8, 1.2) = 1.2.
	edge2 := &domain.MemoryEdge{
		MemoryFromID:     fromID,
		MemoryToID:       toID,
		RelationshipType: domain.EdgeRelatesTo,
		Weight:           1.2,
		WorkspaceID:      wsID,
	}
	require.NoError(t, repo.UpsertEdge(ctx, edge2))

	var gotWeight float32
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT weight FROM memory_edges
		 WHERE memory_from_id=$1 AND memory_to_id=$2 AND relationship_type='relates_to'`,
		fromID, toID,
	).Scan(&gotWeight))
	assert.InDelta(t, 1.2, gotWeight, 0.01, "weight should be updated to max of stored vs incoming")
}

func TestMemoryEdgesRepo_ReinforceEdge(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	wsID := edgeTestWorkspace(t, db)
	t.Cleanup(edgeTestCleanup(t, db, wsID))

	fromID := edgeTestMemory(t, db, wsID, 0.5)
	toID := edgeTestMemory(t, db, wsID, 0.5)

	edge := &domain.MemoryEdge{
		MemoryFromID:     fromID,
		MemoryToID:       toID,
		RelationshipType: domain.EdgeDerivedFrom,
		Weight:           1.0,
		WorkspaceID:      wsID,
	}
	require.NoError(t, repo.UpsertEdge(ctx, edge))
	require.NoError(t, repo.ReinforceEdge(ctx, fromID, toID, domain.EdgeDerivedFrom))

	var gotWeight float32
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT weight FROM memory_edges
		 WHERE memory_from_id=$1 AND memory_to_id=$2 AND relationship_type='derived_from'`,
		fromID, toID,
	).Scan(&gotWeight))
	assert.InDelta(t, 1.1, gotWeight, 0.01, "weight should be incremented by 0.1")
}

func TestMemoryEdgesRepo_GetNeighbors_Empty(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	result, err := repo.GetNeighbors(ctx, nil, 0.1, 200)
	require.NoError(t, err)
	assert.Nil(t, result, "nil ids → nil result without DB call")

	result2, err := repo.GetNeighbors(ctx, []uuid.UUID{}, 0.1, 200)
	require.NoError(t, err)
	assert.Nil(t, result2, "empty slice → nil result without DB call")
}

func TestMemoryEdgesRepo_GetNeighbors_Bidirectional(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	wsID := edgeTestWorkspace(t, db)
	t.Cleanup(edgeTestCleanup(t, db, wsID))

	// Memories: A, B, C, D
	// Edges:
	//   A→B weight 0.9  (outbound from A — above threshold 0.3)
	//   C→A weight 0.7  (inbound to A — above threshold 0.3)
	//   A→D weight 0.2  (outbound from A — below threshold 0.3, must be excluded)
	aID := edgeTestMemory(t, db, wsID, 0.6)
	bID := edgeTestMemory(t, db, wsID, 0.6)
	cID := edgeTestMemory(t, db, wsID, 0.6)
	dID := edgeTestMemory(t, db, wsID, 0.6)

	for _, e := range []*domain.MemoryEdge{
		{MemoryFromID: aID, MemoryToID: bID, RelationshipType: domain.EdgeRelatesTo, Weight: 0.9, WorkspaceID: wsID},
		{MemoryFromID: cID, MemoryToID: aID, RelationshipType: domain.EdgeDependsOn, Weight: 0.7, WorkspaceID: wsID},
		{MemoryFromID: aID, MemoryToID: dID, RelationshipType: domain.EdgeSupersedes, Weight: 0.2, WorkspaceID: wsID},
	} {
		require.NoError(t, repo.UpsertEdge(ctx, e))
	}

	neighbors, err := repo.GetNeighbors(ctx, []uuid.UUID{aID}, 0.3, 200)
	require.NoError(t, err)
	assert.Len(t, neighbors, 2, "A→B and C→A should be returned; A→D (0.2) is below threshold")

	type edgeKey struct{ from, to string }
	found := make(map[edgeKey]bool)
	for _, n := range neighbors {
		found[edgeKey{n.MemoryFromID.String(), n.MemoryToID.String()}] = true
	}
	assert.True(t, found[edgeKey{aID.String(), bID.String()}], "A→B must be present (outbound)")
	assert.True(t, found[edgeKey{cID.String(), aID.String()}], "C→A must be present (inbound)")
	assert.False(t, found[edgeKey{aID.String(), dID.String()}], "A→D must be absent (below threshold)")

	_ = dID // suppress unused-variable warning (dID is used in the edge above)
}

func TestMemoryEdgesRepo_GetNeighbors_MultipleSeeds(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	wsID := edgeTestWorkspace(t, db)
	t.Cleanup(edgeTestCleanup(t, db, wsID))

	// X→Y (0.8), Y→Z (0.6). Querying {X, Y} should return both edges.
	xID := edgeTestMemory(t, db, wsID, 0.7)
	yID := edgeTestMemory(t, db, wsID, 0.7)
	zID := edgeTestMemory(t, db, wsID, 0.7)

	for _, e := range []*domain.MemoryEdge{
		{MemoryFromID: xID, MemoryToID: yID, RelationshipType: domain.EdgeRelatesTo, Weight: 0.8, WorkspaceID: wsID},
		{MemoryFromID: yID, MemoryToID: zID, RelationshipType: domain.EdgeDependsOn, Weight: 0.6, WorkspaceID: wsID},
	} {
		require.NoError(t, repo.UpsertEdge(ctx, e))
	}

	neighbors, err := repo.GetNeighbors(ctx, []uuid.UUID{xID, yID}, 0.5, 200)
	require.NoError(t, err)
	assert.Len(t, neighbors, 2, "X→Y and Y→Z should both be returned")

	_ = zID // referenced in edge above
}

func TestMemoryEdgesRepo_DecayWeights(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	wsID := edgeTestWorkspace(t, db)
	t.Cleanup(edgeTestCleanup(t, db, wsID))

	fromID := edgeTestMemory(t, db, wsID, 0.5)
	toID := edgeTestMemory(t, db, wsID, 0.5)

	edgeID := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO memory_edges (id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id, last_traversed_at)
		 VALUES ($1, $2, $3, 'relates_to', 1.0, $4, NOW() - INTERVAL '60 days')`,
		edgeID, fromID, toID, wsID,
	)
	require.NoError(t, err)

	n, err := repo.DecayWeights(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1), "at least 1 stale edge should be decayed")

	var gotWeight float32
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT weight FROM memory_edges WHERE id=$1", edgeID,
	).Scan(&gotWeight))
	assert.InDelta(t, 0.95, gotWeight, 0.01, "weight should be multiplied by 0.95")
}

func TestMemoryEdgesRepo_PruneDeadEdges(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryEdgesRepo(db)
	ctx := context.Background()

	wsID := edgeTestWorkspace(t, db)
	t.Cleanup(edgeTestCleanup(t, db, wsID))

	fromID := edgeTestMemory(t, db, wsID, 0.5)
	toID := edgeTestMemory(t, db, wsID, 0.5)

	deadID := uuid.New()
	liveID := uuid.New()

	_, err := db.ExecContext(ctx,
		`INSERT INTO memory_edges (id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
		 VALUES ($1, $2, $3, 'relates_to', 0.05, $4)`,
		deadID, fromID, toID, wsID,
	)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO memory_edges (id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
		 VALUES ($1, $2, $3, 'supersedes', 0.5, $4)`,
		liveID, fromID, toID, wsID,
	)
	require.NoError(t, err)

	n, err := repo.PruneDeadEdges(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1), "at least the dead edge should be pruned")

	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_edges WHERE id=$1", deadID,
	).Scan(&count))
	assert.Equal(t, 0, count, "dead edge (weight 0.05) must be deleted")

	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_edges WHERE id=$1", liveID,
	).Scan(&count))
	assert.Equal(t, 1, count, "live edge (weight 0.5) must survive pruning")
}
