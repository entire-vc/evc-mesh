-- +goose Up
-- Fast verification path for connection (agent_workspace_grants) keys, same
-- scheme as agents.api_key_sha256: HMAC-SHA256 hex of the raw key. bcrypt is
-- one-way so no backfill is possible; the application fills it on the first
-- successful authentication after this migration. NULL = still on the bcrypt
-- path. Nullable, no default: catalogue-only change.
ALTER TABLE agent_workspace_grants ADD COLUMN IF NOT EXISTS api_key_sha256 TEXT;

-- +goose Down
ALTER TABLE agent_workspace_grants DROP COLUMN IF EXISTS api_key_sha256;
