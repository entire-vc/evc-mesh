-- +goose Up
-- +goose StatementBegin

-- Memory Knowledge Graph edges: typed, weighted directed links between memory entries.
-- relationship_type semantics:
--   relates_to   – general association
--   supersedes   – from replaces / updates to
--   depends_on   – from logically requires to
--   contradicts  – from conflicts with to
--   derived_from – from was derived / inferred from to
CREATE TABLE memory_edges (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    memory_from_id    UUID        NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    memory_to_id      UUID        NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    relationship_type TEXT        NOT NULL
                      CHECK (relationship_type IN ('relates_to', 'supersedes', 'depends_on', 'contradicts', 'derived_from')),
    weight            REAL        NOT NULL DEFAULT 1.0,
    workspace_id      UUID        NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_traversed_at TIMESTAMPTZ,
    UNIQUE (memory_from_id, memory_to_id, relationship_type)
);

-- Outbound traversal: fetch all edges leaving a node, heaviest first.
CREATE INDEX idx_edges_from ON memory_edges(memory_from_id, weight DESC);

-- Inbound traversal: fetch all edges arriving at a node, heaviest first.
CREATE INDEX idx_edges_to ON memory_edges(memory_to_id, weight DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS memory_edges CASCADE;

-- +goose StatementEnd
