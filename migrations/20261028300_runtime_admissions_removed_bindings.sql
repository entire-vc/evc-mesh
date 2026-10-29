-- +goose Up
-- An admission left enabled after its binding was taken out of the catalog, or
-- given to another agent, grant or workspace, kept the agent listed as led by
-- the runner. Save now disables such admissions; this disables the ones already
-- left behind. The rows it changes are kept in
-- runtime_admissions_removed_bindings_backup so Down can turn them back on.

CREATE TABLE runtime_admissions_removed_bindings_backup (
    integration_id UUID NOT NULL,
    binding_ref    TEXT NOT NULL,
    updated_by     UUID NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (integration_id, binding_ref)
);

WITH latest AS (
    SELECT DISTINCT ON (integration_id) integration_id, config
    FROM runtime_catalog_revisions
    ORDER BY integration_id, revision DESC
), stale AS (
    SELECT adm.integration_id, adm.binding_ref, adm.updated_by, adm.updated_at
    FROM runtime_binding_admissions adm
    JOIN latest ON latest.integration_id = adm.integration_id
    WHERE adm.enabled AND (
        latest.config->'bindings'->adm.binding_ref->'binding'->>'agent_id' IS DISTINCT FROM adm.agent_id::text
        OR latest.config->'bindings'->adm.binding_ref->'binding'->>'workspace_id' IS DISTINCT FROM adm.workspace_id::text
        OR latest.config->'bindings'->adm.binding_ref->'binding'->>'grant_id' IS DISTINCT FROM adm.grant_id::text)
), saved AS (
    INSERT INTO runtime_admissions_removed_bindings_backup
    SELECT * FROM stale
    RETURNING integration_id, binding_ref
)
UPDATE runtime_binding_admissions adm SET enabled = false, updated_at = now()
FROM saved
WHERE adm.integration_id = saved.integration_id AND adm.binding_ref = saved.binding_ref;

-- +goose Down
UPDATE runtime_binding_admissions adm SET enabled = true, updated_by = b.updated_by, updated_at = b.updated_at
FROM runtime_admissions_removed_bindings_backup b
WHERE adm.integration_id = b.integration_id AND adm.binding_ref = b.binding_ref AND NOT adm.enabled;

DROP TABLE runtime_admissions_removed_bindings_backup;
