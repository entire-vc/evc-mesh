package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MemoryEdgesRepo implements storage for knowledge-graph edges between memories.
type MemoryEdgesRepo struct {
	db *sqlx.DB
}

// NewMemoryEdgesRepo creates a new MemoryEdgesRepo.
func NewMemoryEdgesRepo(db *sqlx.DB) *MemoryEdgesRepo {
	return &MemoryEdgesRepo{db: db}
}

// memoryEdgeRow is the DB row representation for the memory_edges table.
type memoryEdgeRow struct {
	ID               uuid.UUID                      `db:"id"`
	MemoryFromID     uuid.UUID                      `db:"memory_from_id"`
	MemoryToID       uuid.UUID                      `db:"memory_to_id"`
	RelationshipType domain.MemoryEdgeRelationshipType `db:"relationship_type"`
	Weight           float64                        `db:"weight"`
	WorkspaceID      uuid.UUID                      `db:"workspace_id"`
	CreatedAt        time.Time                      `db:"created_at"`
	LastTraversedAt  *time.Time                     `db:"last_traversed_at"`
}

func (r *memoryEdgeRow) toDomain() domain.MemoryEdge {
	return domain.MemoryEdge{
		ID:               r.ID,
		MemoryFromID:     r.MemoryFromID,
		MemoryToID:       r.MemoryToID,
		RelationshipType: r.RelationshipType,
		Weight:           r.Weight,
		WorkspaceID:      r.WorkspaceID,
		CreatedAt:        r.CreatedAt,
		LastTraversedAt:  r.LastTraversedAt,
	}
}

// UpsertEdge inserts or updates an edge between two memories.
// On conflict of (memory_from_id, memory_to_id, relationship_type) it updates weight and last_traversed_at.
func (r *MemoryEdgesRepo) UpsertEdge(ctx context.Context, edge *domain.MemoryEdge) error {
	if edge.ID == uuid.Nil {
		edge.ID = uuid.New()
	}
	now := time.Now()
	if edge.CreatedAt.IsZero() {
		edge.CreatedAt = now
	}
	if edge.Weight == 0 {
		edge.Weight = 1.0
	}

	const q = `
		INSERT INTO memory_edges (id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (memory_from_id, memory_to_id, relationship_type)
		DO UPDATE SET
			weight           = EXCLUDED.weight,
			last_traversed_at = NOW()
	`
	_, err := r.db.ExecContext(ctx, q,
		edge.ID, edge.MemoryFromID, edge.MemoryToID, edge.RelationshipType,
		edge.Weight, edge.WorkspaceID, edge.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("memory edge upsert: %w", err)
	}
	return nil
}

// GetEdgesByMemoryID returns all edges where memory_from_id OR memory_to_id equals memoryID,
// filtered by workspace_id, joined against memories to skip archived endpoints.
// Results are ordered by weight DESC.
func (r *MemoryEdgesRepo) GetEdgesByMemoryID(ctx context.Context, memoryID, workspaceID uuid.UUID) ([]domain.MemoryEdge, error) {
	const q = `
		SELECT e.id, e.memory_from_id, e.memory_to_id, e.relationship_type,
		       e.weight, e.workspace_id, e.created_at, e.last_traversed_at
		FROM memory_edges e
		JOIN memories mf ON mf.id = e.memory_from_id AND mf.archived = false
		JOIN memories mt ON mt.id = e.memory_to_id   AND mt.archived = false
		WHERE (e.memory_from_id = $1 OR e.memory_to_id = $1)
		  AND e.workspace_id = $2
		ORDER BY e.weight DESC
	`
	var rows []memoryEdgeRow
	if err := r.db.SelectContext(ctx, &rows, q, memoryID, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory edges get by memory id: %w", err)
	}
	edges := make([]domain.MemoryEdge, len(rows))
	for i, row := range rows {
		edges[i] = row.toDomain()
	}
	return edges, nil
}

// DecayWeights reduces weight by 10% (floor 0.1) for edges not traversed in 30+ days.
// Returns the number of rows updated.
func (r *MemoryEdgesRepo) DecayWeights(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE memory_edges
		SET weight = GREATEST(weight * 0.9, 0.1)
		WHERE (last_traversed_at IS NULL OR last_traversed_at < NOW() - INTERVAL '30 days')
		  AND weight > 0.1
	`)
	if err != nil {
		return 0, fmt.Errorf("memory edges decay weights: %w", err)
	}
	return result.RowsAffected()
}

// PruneDeadEdges removes edges whose weight has fallen below 0.05.
// Returns the number of rows deleted.
func (r *MemoryEdgesRepo) PruneDeadEdges(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM memory_edges WHERE weight < 0.05`,
	)
	if err != nil {
		return 0, fmt.Errorf("memory edges prune dead: %w", err)
	}
	return result.RowsAffected()
}

// ReinforceEdge increments weight by 0.1 (capped at 5.0) and sets last_traversed_at = NOW().
func (r *MemoryEdgesRepo) ReinforceEdge(ctx context.Context, edgeID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE memory_edges
		SET weight           = LEAST(weight + 0.1, 5.0),
		    last_traversed_at = NOW()
		WHERE id = $1
	`, edgeID)
	if err != nil {
		return fmt.Errorf("memory edge reinforce: %w", err)
	}
	return nil
}
