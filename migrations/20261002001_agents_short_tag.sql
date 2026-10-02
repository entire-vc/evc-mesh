-- +goose Up

-- Agent "short_tag" (Mesh aa1b4845): an optional human-authored role label,
-- <=24 chars (AgentShortTagMaxLen), distinct from role (long-form text) and
-- from name/slug (identity, never derived from). Written only via
-- PATCH /agents/:id by an editor holding the existing agent-management
-- permission; NULL means "no label". Additive and nullable, same shape as the
-- model column (#5548367d): an older binary never names it, so rolling the
-- code back alone is safe.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS short_tag TEXT;

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS short_tag;
