-- +goose NO TRANSACTION
-- +goose Up
-- Fast verification path for agent API keys.
--
-- api_key_hash holds a bcrypt digest at cost 12, which costs ~163 ms of CPU per
-- comparison on the prod VM — paid on every request carrying X-Agent-Key. bcrypt
-- is the correct choice for a human-chosen password, where the work factor is
-- what stands between a stolen hash and a dictionary. An agent key is not that:
-- it is 24 bytes from crypto/rand (192 bits), so there is no dictionary to
-- stretch and the work factor buys nothing but latency.
--
-- This column stores the keyed digest of the same key (HMAC-SHA256, hex). No
-- backfill is possible — bcrypt is one-way, so the plaintext needed to compute
-- this exists only at the moment a key is presented or issued. The application
-- therefore fills it opportunistically: registration and rotation write it
-- directly, and an authentication that falls back to bcrypt writes it for next
-- time. Rows stay NULL until their key is used, and a NULL simply means "still
-- on the slow path".
--
-- Nullable and with no default, so ADD COLUMN is a catalogue-only change: no
-- table rewrite, no lock held while rows are touched.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS api_key_sha256 TEXT;

-- UNIQUE rather than a plain index. Two live agents cannot legitimately share a
-- key digest — that would mean one key authenticating as two identities — so
-- this is an invariant worth having the database enforce, and it doubles as the
-- lookup index if the prefix scan is ever retired in favour of going straight
-- to the digest. Partial, because NULL is the normal state for a not-yet-seen
-- key and must not collide with any other NULL.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agents_api_key_sha256
    ON agents (api_key_sha256)
    WHERE api_key_sha256 IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS uq_agents_api_key_sha256;
ALTER TABLE agents DROP COLUMN IF EXISTS api_key_sha256;
