-- +goose NO TRANSACTION
-- +goose Up
ALTER TYPE agent_type ADD VALUE IF NOT EXISTS 'codex';
ALTER TYPE agent_type ADD VALUE IF NOT EXISTS 'cursor';
ALTER TYPE agent_type ADD VALUE IF NOT EXISTS 'copilot';
ALTER TYPE agent_type ADD VALUE IF NOT EXISTS 'gemini_cli';

-- +goose Down
-- Note: PostgreSQL does not support removing enum values; no data to revert (additive only).
