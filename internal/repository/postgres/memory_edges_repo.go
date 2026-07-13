package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
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

// ReinforceEdge increments weight by 0.1 (capped at 5.0) and stamps last_traversed_at=NOW()
// for the edge identified by (fromID, toID, relType). No-op when the edge does not exist.
func (r *MemoryEdgesRepo) ReinforceEdge(ctx context.Context, fromID, toID uuid.UUID, relType domain.MemoryEdgeRelationshipType) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE memory_edges
		SET last_traversed_at  = NOW(),
		    weight             = LEAST(weight + 0.1, 5.0)
		WHERE memory_from_id    = $1
		  AND memory_to_id      = $2
		  AND relationship_type = $3
	`, fromID, toID, string(relType))
	if err != nil {
		return fmt.Errorf("memory edges reinforce: %w", err)
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

// memoryEdgeRow is the DB scan target for a memory_edges row.
type memoryEdgeRow struct {
	ID               uuid.UUID                         `db:"id"`
	MemoryFromID     uuid.UUID                         `db:"memory_from_id"`
	MemoryToID       uuid.UUID                         `db:"memory_to_id"`
	RelationshipType domain.MemoryEdgeRelationshipType `db:"relationship_type"`
	Weight           float32                           `db:"weight"`
	WorkspaceID      uuid.UUID                         `db:"workspace_id"`
	CreatedAt        time.Time                         `db:"created_at"`
	LastTraversedAt  *time.Time                        `db:"last_traversed_at"`
}

func (row *memoryEdgeRow) toDomain() domain.MemoryEdge {
	return domain.MemoryEdge{
		ID:               row.ID,
		MemoryFromID:     row.MemoryFromID,
		MemoryToID:       row.MemoryToID,
		RelationshipType: row.RelationshipType,
		Weight:           row.Weight,
		WorkspaceID:      row.WorkspaceID,
		CreatedAt:        row.CreatedAt,
		LastTraversedAt:  row.LastTraversedAt,
	}
}

// GetNeighbors returns edges bidirectionally connected to any memory in ids with
// weight >= weightThreshold, ordered by weight DESC, capped at limit rows.
// When ids is empty, a nil slice is returned without querying the database.
//
// Edges are confined to workspaceID. Graph traversal must not be able to hop out
// of the caller's tenant: before this predicate existed, a single cross-workspace
// edge would have let RecallGraph's BFS expand into another tenant's memories.
// Same-workspace was previously an invariant of the edge-WRITE paths only — i.e.
// held by convention; here it is enforced by the query.
func (r *MemoryEdgesRepo) GetNeighbors(ctx context.Context, ids []uuid.UUID, workspaceID uuid.UUID, weightThreshold float64, limit int) ([]domain.MemoryEdge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if workspaceID == uuid.Nil {
		return nil, fmt.Errorf("memory edges get neighbors: workspace id is required")
	}
	if limit <= 0 {
		limit = 200
	}

	const q = `
		SELECT id, memory_from_id, memory_to_id, relationship_type, weight, workspace_id, created_at, last_traversed_at
		FROM   memory_edges
		WHERE  (memory_from_id = ANY($1) OR memory_to_id = ANY($1))
		  AND  workspace_id = $2
		  AND  weight >= $3
		ORDER  BY weight DESC
		LIMIT  $4
	`

	var rows []memoryEdgeRow
	if err := r.db.SelectContext(ctx, &rows, q, uuidArrayParam(ids), workspaceID, weightThreshold, limit); err != nil {
		return nil, fmt.Errorf("memory edges get neighbors: %w", err)
	}

	result := make([]domain.MemoryEdge, len(rows))
	for i, row := range rows {
		result[i] = row.toDomain()
	}
	return result, nil
}

// uuidArrayParam encodes a []uuid.UUID as a PostgreSQL array literal string
// that lib/pq will pass as a TEXT[] parameter, which Postgres casts to uuid[].
// Format: {"uuid1","uuid2",...}
func uuidArrayParam(ids []uuid.UUID) uuidArrayValue {
	ss := make(uuidArrayValue, len(ids))
	for i, id := range ids {
		ss[i] = id.String()
	}
	return ss
}

// uuidArrayValue is a []string that serialises itself as a PostgreSQL array literal.
// It implements the driver.Valuer interface so lib/pq passes it as a single TEXT[] arg.
type uuidArrayValue []string

func (v uuidArrayValue) Value() (interface{}, error) {
	quoted := make([]string, len(v))
	for i, s := range v {
		quoted[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}", nil
}
