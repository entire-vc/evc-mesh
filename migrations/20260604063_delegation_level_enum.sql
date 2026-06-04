-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'delegation_level') THEN
        CREATE TYPE delegation_level AS ENUM ('auto', 'review', 'supervised');
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS delegation_level delegation_level NOT NULL DEFAULT 'review';

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS delegation_level;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_type WHERE typname = 'delegation_level') THEN
        DROP TYPE delegation_level;
    END IF;
END
$$;
-- +goose StatementEnd
