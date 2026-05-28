-- +goose Up
-- Add checkout_acquired_at to track when a checkout lock was originally acquired.
-- This enables the 409 response to surface "held since <timestamp>" to callers.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS checkout_acquired_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS checkout_acquired_at;
