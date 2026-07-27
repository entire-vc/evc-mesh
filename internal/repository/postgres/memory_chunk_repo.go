package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MemoryChunkRepo implements repository.MemoryChunkRepository with PostgreSQL.
// See ADR-0002 and domain.MemoryChunk.
type MemoryChunkRepo struct {
	db *sqlx.DB
}

// NewMemoryChunkRepo creates a new MemoryChunkRepo.
func NewMemoryChunkRepo(db *sqlx.DB) *MemoryChunkRepo {
	return &MemoryChunkRepo{db: db}
}

// ReplaceChunks deletes every existing chunk row for memoryID and inserts
// chunks in its place, in one transaction — the chunker is deterministic, so
// this delete+reinsert is what makes re-embedding a memory idempotent.
func (r *MemoryChunkRepo) ReplaceChunks(ctx context.Context, memoryID uuid.UUID, chunks []domain.MemoryChunk) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace chunks: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback on error or panic

	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_chunks WHERE memory_id = $1`, memoryID); err != nil {
		return fmt.Errorf("replace chunks: delete: %w", err)
	}

	const insertQ = `
		INSERT INTO memory_chunks
			(memory_id, chunk_idx, chunk_start, chunk_end, embedding, embedding_model, embedding_dim)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	for _, c := range chunks {
		if _, err := tx.ExecContext(ctx, insertQ,
			memoryID, c.ChunkIdx, c.ChunkStart, c.ChunkEnd, c.Embedding, c.EmbeddingModel, c.EmbeddingDim,
		); err != nil {
			return fmt.Errorf("replace chunks: insert chunk_idx=%d: %w", c.ChunkIdx, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace chunks: commit: %w", err)
	}
	return nil
}

// ListByMemoryIDs returns every chunk row for the given memory IDs. Never
// touches memories.content — callers hydrate the parent memory separately.
func (r *MemoryChunkRepo) ListByMemoryIDs(ctx context.Context, memoryIDs []uuid.UUID) ([]domain.MemoryChunk, error) {
	if len(memoryIDs) == 0 {
		return nil, nil
	}
	const q = `
		SELECT id, memory_id, chunk_idx, chunk_start, chunk_end, embedding, embedding_model, embedding_dim, created_at
		FROM memory_chunks
		WHERE memory_id = ANY($1)`
	var chunks []domain.MemoryChunk
	if err := r.db.SelectContext(ctx, &chunks, q, pq.Array(memoryIDs)); err != nil {
		return nil, fmt.Errorf("list chunks by memory ids: %w", err)
	}
	return chunks, nil
}

// MemoryIDsWithChunks reports which of the given memory IDs have at least one
// chunk row, so callers can fall back to memories.embedding for the rest.
func (r *MemoryChunkRepo) MemoryIDsWithChunks(ctx context.Context, memoryIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	result := make(map[uuid.UUID]bool, len(memoryIDs))
	if len(memoryIDs) == 0 {
		return result, nil
	}
	const q = `SELECT DISTINCT memory_id FROM memory_chunks WHERE memory_id = ANY($1)`
	var ids []uuid.UUID
	if err := r.db.SelectContext(ctx, &ids, q, pq.Array(memoryIDs)); err != nil {
		return nil, fmt.Errorf("memory ids with chunks: %w", err)
	}
	for _, id := range ids {
		result[id] = true
	}
	return result, nil
}
