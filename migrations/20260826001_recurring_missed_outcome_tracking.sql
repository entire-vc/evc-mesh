-- +goose Up

-- Track per-schedule "missed outcome" state, separate from the existing
-- consecutive_failures/quarantined_at pair (that pair tracks createInstance
-- erroring — the DB insert itself failing; this tracks the opposite case: the
-- instance was created fine but nobody did any real work on it before the
-- next rollover superseded it).
-- consecutive_missed_outcomes: resets to 0 the moment any superseded instance
--   is found to have had real work (an artifact, a VCS link, or a comment that
--   isn't a duplicate of the others).
-- last_missed_at: timestamp of the most recent rollover that closed a
--   zero-work instance as missed, for surfacing "how long has this been idle".
ALTER TABLE recurring_schedules
    ADD COLUMN consecutive_missed_outcomes INT NOT NULL DEFAULT 0,
    ADD COLUMN last_missed_at              TIMESTAMPTZ;

-- +goose Down

ALTER TABLE recurring_schedules
    DROP COLUMN IF EXISTS consecutive_missed_outcomes,
    DROP COLUMN IF EXISTS last_missed_at;
