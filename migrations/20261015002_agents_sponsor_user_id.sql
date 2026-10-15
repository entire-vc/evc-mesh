-- +goose Up
-- The user who stands behind an agent: who registered it, or issued its latest
-- key (W3.5). Keep's tenancy sync needs a human owner for every agent it mirrors.
-- Nullable and additive: NULL means "not known", never "nobody".
--
-- Existing agents are backfilled from supervisor_user_id, the nearest thing the
-- schema already records; agents without a supervisor stay NULL until their next
-- key is issued by a user.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS sponsor_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
UPDATE agents SET sponsor_user_id = supervisor_user_id
 WHERE sponsor_user_id IS NULL AND supervisor_user_id IS NOT NULL;

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS sponsor_user_id;
