-- +goose Up
-- Project scope of an extension connection. 'workspace' (the default, and what
-- every existing connection gets) keeps today's reach: the whole workspace the
-- extension was connected to. 'bound' limits the extension actor's API calls and
-- event deliveries to the projects that have an enabled binding for it in
-- project_integrations (extension_id = integration_configs.provider); disabling
-- a binding cuts both.
--
-- Additive with a constant default: the previous image lists its columns
-- explicitly and never reads this one.
ALTER TABLE integration_configs
    ADD COLUMN IF NOT EXISTS project_scope TEXT NOT NULL DEFAULT 'workspace';

ALTER TABLE integration_configs
    ADD CONSTRAINT integration_configs_project_scope_check
    CHECK (project_scope IN ('workspace', 'bound'));

-- +goose Down
ALTER TABLE integration_configs DROP CONSTRAINT IF EXISTS integration_configs_project_scope_check;
ALTER TABLE integration_configs DROP COLUMN IF EXISTS project_scope;
