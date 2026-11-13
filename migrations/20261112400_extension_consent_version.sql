-- +goose Up
-- Workspace consent names the manifest release it was given for, so a newer
-- manifest's added rights wait for a new consent (POST .../consent) instead of
-- acting on their own. Empty on rows consented before this migration.
ALTER TABLE integration_configs
    ADD COLUMN IF NOT EXISTS consented_version VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS consented_commit VARCHAR(64) NOT NULL DEFAULT '';

-- A connection with no consent record but an extension actor: the actor's
-- capability snapshot is what the workspace actually granted, so it becomes the
-- recorded consent. A connection with neither stays without a record and is
-- shown as needing consent (it no longer falls back to the declared set).
UPDATE integration_configs ic
SET consented_capabilities = snap.caps,
    consented_at = snap.issued_at
FROM (
    SELECT ea.connection_id,
           ea.issued_at,
           COALESCE((
               SELECT jsonb_agg(e.key || ':' || v.value ORDER BY e.key || ':' || v.value)
               FROM jsonb_each(CASE WHEN jsonb_typeof(ea.capabilities) = 'object' THEN ea.capabilities ELSE '{}'::jsonb END) e
               CROSS JOIN LATERAL jsonb_array_elements_text(
                   CASE WHEN jsonb_typeof(e.value) = 'array' THEN e.value ELSE '[]'::jsonb END) v
           ), '[]'::jsonb) AS caps
    FROM extension_actors ea
) snap
WHERE snap.connection_id = ic.id
  AND ic.consented_capabilities IS NULL;

-- A builtin connection with no consent record (connected before consent was
-- stored): its status has always reported the declared set of the builtin
-- manifest, and builtins act on neither ext_ tokens nor push events, so that
-- set becomes the record. Without it every such connection would show
-- needs_consent for rights it has always had. The manifest is the one the
-- running build synced into installed_extensions (source 'builtin').
UPDATE integration_configs ic
SET consented_capabilities = decl.caps,
    consented_at = ic.created_at
FROM (
    SELECT ie.extension_id,
           COALESCE((
               SELECT jsonb_agg(k.key || ':' || v.value ORDER BY k.key || ':' || v.value)
               FROM jsonb_each(CASE WHEN jsonb_typeof(ie.manifest->'capabilities') = 'object' THEN ie.manifest->'capabilities' ELSE '{}'::jsonb END) k
               CROSS JOIN LATERAL jsonb_array_elements_text(
                   CASE WHEN jsonb_typeof(k.value) = 'array' THEN k.value ELSE '[]'::jsonb END) v
           ), '[]'::jsonb) AS caps
    FROM installed_extensions ie
    WHERE ie.source = 'builtin'
) decl
WHERE decl.extension_id = ic.provider
  AND ic.consented_capabilities IS NULL;

-- +goose Down
-- The backfilled consent is kept: it is what the actor already held.
ALTER TABLE integration_configs
    DROP COLUMN IF EXISTS consented_commit,
    DROP COLUMN IF EXISTS consented_version;
