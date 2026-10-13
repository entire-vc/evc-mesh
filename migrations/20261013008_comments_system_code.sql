-- +goose Up

-- i18n L3.2a: machine-readable identity of server-generated comments.
-- system_code names the kind of notice (see internal/service/system_codes.go),
-- system_params carries the values the text is built from, so a client can
-- render the line in the reader's language. Additive only: both columns are
-- nullable with no default, existing rows and the running code keep working
-- and read as NULL.
ALTER TABLE comments
    ADD COLUMN IF NOT EXISTS system_code TEXT NULL,
    ADD COLUMN IF NOT EXISTS system_params JSONB NULL;

-- +goose Down
ALTER TABLE comments
    DROP COLUMN IF EXISTS system_params,
    DROP COLUMN IF EXISTS system_code;
