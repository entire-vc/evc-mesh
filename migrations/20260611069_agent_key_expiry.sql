-- Agent API key TTL/expiry mechanism.
-- Adds expires_at (nullable, for time-bounded keys) and last_rotated_at
-- (audit trail for key rotation). Existing rows default to NULL (no expiry).

ALTER TABLE agents
    ADD COLUMN expires_at      TIMESTAMPTZ,
    ADD COLUMN last_rotated_at TIMESTAMPTZ;

-- Partial index: only on rows with an expiry set, skips soft-deleted agents.
CREATE INDEX idx_agents_expires_at
    ON agents (expires_at)
    WHERE expires_at IS NOT NULL AND deleted_at IS NULL;
