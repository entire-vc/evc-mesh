-- +goose Up
-- A time wait is allowed only against a known external event whose source
-- states the deadline (decision of 28.10, doc an internal ticket "Запись агентом"):
-- a published job start, an approved measurement window, a provider
-- Retry-After or quota reset. A bare "in 6 hours" is refused. The server keeps
-- the evidence next to the instant and marks it unverified: it checks that a
-- Mesh receipt exists, not that the source really names this deadline.
ALTER TABLE task_waits
    ADD COLUMN event_ref       TEXT,
    ADD COLUMN evidence_source TEXT,
    ADD COLUMN evidence_why    TEXT,
    ADD COLUMN until_at_offset TEXT,
    ADD COLUMN evidence_status TEXT;

-- NOT VALID: a row declared before this migration keeps its shape; every new
-- time wait must carry the evidence.
ALTER TABLE task_waits
    ADD CONSTRAINT task_waits_time_evidence CHECK (
        kind <> 'time' OR (event_ref IS NOT NULL AND evidence_source IS NOT NULL
                           AND evidence_why IS NOT NULL AND until_at_offset IS NOT NULL
                           AND evidence_status = 'unverified')) NOT VALID;

-- +goose Down
ALTER TABLE task_waits DROP CONSTRAINT IF EXISTS task_waits_time_evidence;
ALTER TABLE task_waits
    DROP COLUMN IF EXISTS evidence_status,
    DROP COLUMN IF EXISTS until_at_offset,
    DROP COLUMN IF EXISTS evidence_why,
    DROP COLUMN IF EXISTS evidence_source,
    DROP COLUMN IF EXISTS event_ref;
