package postgres

import (
	"context"
	"fmt"

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
