-- +goose Up

-- Threaded comments on tasks. Internal comments (is_internal=true) are for agent-to-agent communication.
CREATE TABLE comments (
    id                  UUID PRIMARY KEY,
    task_id             UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    parent_comment_id   UUID REFERENCES comments(id) ON DELETE SET NULL,
    author_id           UUID NOT NULL,
    author_type         actor_type NOT NULL,
    body                TEXT NOT NULL,
    metadata            JSONB NOT NULL DEFAULT '{}',
    is_internal         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_comments_task ON comments(task_id, created_at ASC);
CREATE INDEX idx_comments_parent ON comments(parent_comment_id)
    WHERE parent_comment_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS comments;
