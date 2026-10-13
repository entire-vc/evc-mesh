-- +goose Up

-- i18n L1.1: per-user interface language and per-workspace default.
-- Additive only: both columns are NOT NULL with a constant default, so the
-- running code (which does not know them yet) keeps working and existing rows
-- read as 'en'. The CHECK lists the supported languages; adding a third one is
-- a new migration that widens it, together with the locale file.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS locale TEXT NOT NULL DEFAULT 'en',
    ADD COLUMN IF NOT EXISTS locale_set_explicitly BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE users
    ADD CONSTRAINT users_locale_check CHECK (locale IN ('en', 'ru'));

ALTER TABLE workspaces
    ADD COLUMN IF NOT EXISTS default_locale TEXT NOT NULL DEFAULT 'en';

ALTER TABLE workspaces
    ADD CONSTRAINT workspaces_default_locale_check CHECK (default_locale IN ('en', 'ru'));

-- +goose Down
ALTER TABLE workspaces DROP CONSTRAINT IF EXISTS workspaces_default_locale_check;
ALTER TABLE workspaces DROP COLUMN IF EXISTS default_locale;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_locale_check;
ALTER TABLE users DROP COLUMN IF EXISTS locale_set_explicitly;
ALTER TABLE users DROP COLUMN IF EXISTS locale;
