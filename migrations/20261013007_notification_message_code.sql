-- +goose Up

-- i18n L3.1 (#d65d14d6): an in-app notification stores the message code and its
-- language-neutral parameters, and the text is rendered in the viewer's
-- language when it is read. title/body stay as the English fallback for rows
-- written before this migration, for old clients and for agents.
-- Both columns are nullable and additive: a row without a code is displayed as
-- stored.
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS message_code TEXT;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS message_params JSONB;

-- +goose Down
ALTER TABLE notifications DROP COLUMN IF EXISTS message_params;
ALTER TABLE notifications DROP COLUMN IF EXISTS message_code;
