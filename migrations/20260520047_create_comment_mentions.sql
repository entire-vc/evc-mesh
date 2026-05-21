-- +goose Up
CREATE TABLE comment_mentions (
    comment_id     UUID        NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    mentioned_id   UUID        NOT NULL,
    mentioned_kind TEXT        NOT NULL CHECK (mentioned_kind IN ('agent', 'user')),
    mentioned_slug TEXT        NOT NULL,
    extracted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    seen_at        TIMESTAMPTZ NULL,
    PRIMARY KEY (comment_id, mentioned_id)
);

CREATE INDEX ix_comment_mentions_mentioned ON comment_mentions (mentioned_id, seen_at);
CREATE INDEX ix_comment_mentions_extracted ON comment_mentions (extracted_at DESC);

-- +goose Down
DROP TABLE IF EXISTS comment_mentions;
