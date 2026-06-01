package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// MemoryEdgesRepo handles persistence for the memory_edges KG table.
type MemoryEdgesRepo struct {
	db *sqlx.DB
}

// NewMemoryEdgesRepo creates a MemoryEdgesRepo backed by db.
func NewMemoryEdgesRepo(db *sqlx.DB) *MemoryEdgesRepo {
	return &MemoryEdgesRepo{db: db}
}

// ReinforceTraversal records that edge edgeID was traversed: sets last_traversed_at = NOW()
// and increments weight by 0.1, capped at 5.0. Returns an error wrapping "not found"
// when no edge with that ID exists.
func (r *MemoryEdgesRepo) ReinforceTraversal(ctx context.Context, edgeID uuid.UUID) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE memory_edges
		SET last_traversed_at = NOW(),
		    weight            = LEAST(weight + 0.1, 5.0)
		WHERE id = $1
	`, edgeID)
	if err != nil {
		return fmt.Errorf("memory edges reinforce traversal: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("memory edges reinforce traversal rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("memory edges reinforce traversal: edge %s not found", edgeID)
	}
	return nil
}
