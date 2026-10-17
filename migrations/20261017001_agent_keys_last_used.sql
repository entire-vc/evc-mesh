-- +goose Up
-- When a key was last used to authenticate, for GET /api/v1/agents/{id}/keys
-- (a rotator checks that the old key has gone quiet before revoking it).
-- Additive and nullable; written at most once a minute per key.
ALTER TABLE agent_api_keys ADD COLUMN last_used_at TIMESTAMPTZ;
ALTER TABLE agents ADD COLUMN api_key_last_used_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS api_key_last_used_at;
ALTER TABLE agent_api_keys DROP COLUMN IF EXISTS last_used_at;
