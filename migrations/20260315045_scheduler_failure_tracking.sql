-- +goose Up

-- Track per-schedule failure state for createInstance fail-safe (Fix #2).
-- consecutive_failures: resets to 0 on any successful instance creation.
-- quarantined_at: set when consecutive_failures reaches the threshold (3); schedule is deactivated.
-- last_error: most recent createInstance error message, for alerting and debugging.
ALTER TABLE recurring_schedules
    ADD COLUMN consecutive_failures INT NOT NULL DEFAULT 0,
    ADD COLUMN quarantined_at       TIMESTAMPTZ,
    ADD COLUMN last_error           TEXT;

-- +goose Down

ALTER TABLE recurring_schedules
    DROP COLUMN IF EXISTS consecutive_failures,
    DROP COLUMN IF EXISTS quarantined_at,
    DROP COLUMN IF EXISTS last_error;
