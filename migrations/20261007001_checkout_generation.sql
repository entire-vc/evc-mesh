-- +goose Up
ALTER TABLE tasks
    ADD COLUMN checkout_session_id uuid,
    ADD COLUMN checkout_request_id uuid,
    ADD COLUMN checkout_generation bigint NOT NULL DEFAULT 0;
-- Existing leases remain token-compatible, but acquire a real generation.
UPDATE tasks SET checkout_generation = 1 WHERE checked_out_by IS NOT NULL;

-- +goose Down
-- Roll back the application binary first: old code never reads these columns.
ALTER TABLE tasks DROP COLUMN checkout_session_id,
    DROP COLUMN checkout_request_id, DROP COLUMN checkout_generation;
