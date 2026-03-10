-- +goose Up
ALTER TABLE tasks ADD COLUMN checked_out_by  UUID REFERENCES agents(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN checkout_token   UUID;
ALTER TABLE tasks ADD COLUMN checkout_expires TIMESTAMPTZ;

CREATE INDEX idx_tasks_checkout ON tasks(checked_out_by) WHERE checked_out_by IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_checkout;
ALTER TABLE tasks DROP COLUMN IF EXISTS checkout_expires;
ALTER TABLE tasks DROP COLUMN IF EXISTS checkout_token;
ALTER TABLE tasks DROP COLUMN IF EXISTS checked_out_by;
