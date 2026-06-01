package postgres

import (
	"context"
	"fmt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// MemoryEdgesRepo handles persistence and maintenance for the memory_edges KG table.
type MemoryEdgesRepo struct {
	db *sqlx.DB
}

// NewMemoryEdgesRepo creates a MemoryEdgesRepo backed by db.
func NewMemoryEdgesRepo(db *sqlx.DB) *MemoryEdgesRepo {
	return &MemoryEdgesRepo{db: db}
}

// UpsertEdge inserts a new KG edge or, on (from, to, type) conflict, updates weight to the
// maximum of the stored and incoming values and refreshes last_traversed_at.
func (r *MemoryEdgesRepo) UpsertEdge(ctx context.Context, edge *domain.MemoryEdge) error {
	if edge.ID == uuid.Nil {
		edge.ID = uuid.New()
	}
	if edge.Weight == 0 {
		edge.Weight = 1.0
	}
	const q = `
		INSERT INTO memory_edges (id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (memory_from_id, memory_to_id, relationship_type) DO UPDATE
			SET weight           = GREATEST(memory_edges.weight, EXCLUDED.weight),
			    last_traversed_at = NOW()
	`
	_, err := r.db.ExecContext(ctx, q,
		edge.ID, edge.MemoryFromID, edge.MemoryToID,
		string(edge.RelationshipType), edge.Weight, edge.WorkspaceID,
	)
	if err != nil {
		return fmt.Errorf("memory edges upsert: %w", err)
	}
	return nil
}

// DecayWeights applies geometric decay (×0.95) to every edge that has not been traversed
// in more than 30 days. Edges attached to archived memories are included so they decay
// to the pruning threshold naturally — archived memories emit no new traversals, so their
// edges will reach weight < 0.1 and be removed by PruneDeadEdges over time.
// Only edges already above the pruning floor (weight >= 0.1) are touched.
// Returns the count of rows updated.
func (r *MemoryEdgesRepo) DecayWeights(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE memory_edges
		SET weight = weight * 0.95
		WHERE (last_traversed_at IS NULL OR last_traversed_at < NOW() - INTERVAL '30 days')
		  AND weight >= 0.1
	`)
	if err != nil {
		return 0, fmt.Errorf("memory edges decay weights: %w", err)
	}
	return result.RowsAffected()
}

// PruneDeadEdges deletes all edges whose weight has fallen below 0.1.
// This covers naturally-decayed edges and edges whose connected memories are archived
// (archived memories receive no traversals, so their edges decay to the threshold via
// DecayWeights).
// Returns the count of rows deleted.
func (r *MemoryEdgesRepo) PruneDeadEdges(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM memory_edges WHERE weight < 0.1`,
	)
	if err != nil {
		return 0, fmt.Errorf("memory edges prune dead: %w", err)
	}
	return result.RowsAffected()
}
