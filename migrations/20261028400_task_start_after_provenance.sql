-- +goose Up
-- Who set a card's start_after and on what grounds (decision of 28.10, doc
-- an internal ticket "Запись агентом" / "Атомарная очистка" / "Legacy и rollout").
--   human_explicit  — a person typed the date; no machine evidence needed;
--   agent_evidence  — an agent set it against a known external event whose
--                     source states the deadline (stored, unverified);
--   legacy_unknown  — set before this migration; kept, never guessed at.
-- Every set and every compare-clear leaves an immutable receipt. The card
-- points at the receipt of its current date, so a clear can prove it removes
-- exactly the schedule it read and not a newer one with the same instant.
CREATE TABLE start_after_receipts (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id            UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    op                 TEXT NOT NULL CHECK (op IN ('set', 'clear')),
    -- set: the new date; clear: the date that was removed.
    start_after        TIMESTAMPTZ NOT NULL,
    origin             TEXT NOT NULL CHECK (origin IN ('human_explicit', 'agent_evidence', 'legacy_unknown')),
    actor_id           UUID NOT NULL,
    actor_type         TEXT NOT NULL,
    source_offset      TEXT,
    event_ref          TEXT,
    evidence_source    TEXT,
    evidence_why       TEXT,
    evidence_status    TEXT,
    -- clear only: the receipt of the schedule it removed (NULL for legacy).
    cleared_receipt_id UUID REFERENCES start_after_receipts(id),
    idempotency_key    TEXT,
    task_version       BIGINT,
    reason             TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT start_after_receipts_agent_evidence CHECK (
        op <> 'set' OR origin <> 'agent_evidence' OR (
            event_ref IS NOT NULL AND evidence_source IS NOT NULL AND evidence_why IS NOT NULL
            AND source_offset IS NOT NULL AND evidence_status = 'unverified')),
    CONSTRAINT start_after_receipts_set_origin CHECK (op <> 'set' OR origin <> 'legacy_unknown'),
    CONSTRAINT start_after_receipts_clear_key CHECK ((op = 'clear') = (idempotency_key IS NOT NULL))
);

-- A repeated compare-clear with the same key returns the first receipt.
CREATE UNIQUE INDEX start_after_receipts_clear_idempotency
    ON start_after_receipts (task_id, idempotency_key) WHERE op = 'clear';
CREATE INDEX start_after_receipts_task ON start_after_receipts (task_id, created_at);

-- Append-only: a set receipt is the evidence behind the current date and a
-- clear receipt is the idempotency record of a compare-clear. Rows still go
-- away with their task (ON DELETE CASCADE runs after the task row is gone);
-- any other UPDATE or DELETE is refused.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION start_after_receipts_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND NOT EXISTS (SELECT 1 FROM tasks WHERE id = OLD.task_id) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'start_after_receipts is append-only';
END $$;
-- +goose StatementEnd
CREATE TRIGGER start_after_receipts_append_only BEFORE UPDATE OR DELETE ON start_after_receipts
    FOR EACH ROW EXECUTE FUNCTION start_after_receipts_append_only();

ALTER TABLE tasks
    ADD COLUMN start_after_origin TEXT
        CHECK (start_after_origin IN ('human_explicit', 'agent_evidence', 'legacy_unknown')),
    -- Deferred: a new card names its receipt before the receipt row exists.
    ADD COLUMN start_after_receipt_id UUID
        REFERENCES start_after_receipts(id) DEFERRABLE INITIALLY DEFERRED;

-- Mark, do not delete: every existing date stays and is legacy_unknown. The
-- version trigger is held off so the marking is not a task change that would
-- make an in-flight compare-and-set writer lose its race for nothing.
ALTER TABLE tasks DISABLE TRIGGER task_version_before_update;
UPDATE tasks SET start_after_origin = 'legacy_unknown' WHERE start_after IS NOT NULL;
ALTER TABLE tasks ENABLE TRIGGER task_version_before_update;

-- A date never exists without its origin, and an agent's date never without
-- its receipt. A writer that bypasses the service validator fails here.
ALTER TABLE tasks
    ADD CONSTRAINT tasks_start_after_origin_pair CHECK ((start_after IS NULL) = (start_after_origin IS NULL)),
    ADD CONSTRAINT tasks_start_after_agent_receipt CHECK (
        start_after_origin IS DISTINCT FROM 'agent_evidence' OR start_after_receipt_id IS NOT NULL);

-- +goose Down
-- The dates themselves stay; only their provenance is dropped. Applying Up
-- again marks them legacy_unknown.
ALTER TABLE tasks
    DROP CONSTRAINT IF EXISTS tasks_start_after_agent_receipt,
    DROP CONSTRAINT IF EXISTS tasks_start_after_origin_pair,
    DROP COLUMN IF EXISTS start_after_receipt_id,
    DROP COLUMN IF EXISTS start_after_origin;
DROP TRIGGER IF EXISTS start_after_receipts_append_only ON start_after_receipts;
DROP FUNCTION IF EXISTS start_after_receipts_append_only();
DROP TABLE IF EXISTS start_after_receipts;
