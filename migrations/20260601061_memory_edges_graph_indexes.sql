-- Migration 061: add indexes to support multi-hop graph traversal (RecallGraph, G1-s1)
--
-- Adds:
--   1. Composite (from_id, weight DESC) and (to_id, weight DESC) covering indexes for
--      bidirectional BFS edge expansion ordered by edge weight.
--      Note: the base indexes idx_edges_from and idx_edges_to (from migration 058)
--      already cover single-column lookups; these composite indexes include weight for
--      efficient ORDER BY weight threshold filtering in traversal queries.
--   2. A plain btree index on relationship_type to support filtering by edge type.
--
-- The existing unique constraint on (memory_from_id, memory_to_id, relationship_type)
-- already provides an index on the triple; these additions are optimised for traversal.

-- +goose Up

-- Composite index: outbound edges ordered by weight descending (BFS forward expansion).
CREATE INDEX IF NOT EXISTS idx_edges_from_weight
    ON memory_edges (memory_from_id, weight DESC)
    WHERE weight >= 0.1;

-- Composite index: inbound edges ordered by weight descending (BFS reverse expansion).
CREATE INDEX IF NOT EXISTS idx_edges_to_weight
    ON memory_edges (memory_to_id, weight DESC)
    WHERE weight >= 0.1;

-- Index on relationship_type for type-filtered traversal queries.
CREATE INDEX IF NOT EXISTS idx_edges_relationship_type
    ON memory_edges (relationship_type);

-- +goose Down
DROP INDEX IF EXISTS idx_edges_relationship_type;
DROP INDEX IF EXISTS idx_edges_to_weight;
DROP INDEX IF EXISTS idx_edges_from_weight;
