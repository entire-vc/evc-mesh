-- +goose Up

-- An idempotency receipt binds an Idempotency-Key to the write it produced,
-- inside that write's own transaction. idempotency_keys holds
-- the replayable answer of a request, but it is stored after the handler
-- committed, may be lost (a failed store, a 5xx, a crash) and expires after 24
-- hours; the receipt does not. A repeat whose receipt exists answers from it and
-- writes nothing, whatever happened to the idempotency_keys record.
--
-- One receipt per actor, route, task and key: the same key from another actor,
-- on another route or another task is a separate write. task_id is the nil UUID
-- for a write that belongs to no task (a usage report without task_id).
-- resource_id names the written row (comment, artifact, turn outcome, agent
-- session); response is a snapshot of the written object taken in the same
-- transaction, which the replay answers with. Receipts do not expire.
CREATE TABLE idempotency_receipts (
    actor_type    TEXT        NOT NULL,
    actor_id      UUID        NOT NULL,
    route         TEXT        NOT NULL,
    task_id       UUID        NOT NULL,
    idem_key      TEXT        NOT NULL,
    request_hash  TEXT        NOT NULL,
    resource_id   UUID        NOT NULL,
    response      JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (actor_type, actor_id, route, task_id, idem_key)
);

-- A caller reconciles by reading its own writes on a task.
CREATE INDEX idx_idempotency_receipts_task ON idempotency_receipts (task_id, actor_id);

-- +goose Down
DROP TABLE IF EXISTS idempotency_receipts;
