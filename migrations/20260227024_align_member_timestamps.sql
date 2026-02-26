-- +goose Up

-- Add created_at and updated_at to workspace_members.
-- The existing joined_at column is used to backfill the new columns.
ALTER TABLE workspace_members
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Backfill from joined_at.
UPDATE workspace_members SET created_at = joined_at, updated_at = joined_at;

-- Add created_at and updated_at to project_members.
-- The existing added_at column is used to backfill the new columns.
ALTER TABLE project_members
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Backfill from added_at.
UPDATE project_members SET created_at = added_at, updated_at = added_at;

-- +goose Down

ALTER TABLE workspace_members
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS updated_at;

ALTER TABLE project_members
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS updated_at;
