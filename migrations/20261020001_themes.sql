-- +goose Up
-- Theme selection (ADR themes-three-layers, D3/D12). Additive only: two new
-- tables, nothing existing is altered. workspaces.settings is deliberately not
-- used: it has no validation and no version.

-- The instance default. A singleton like instance_extension_config, but a table
-- of its own: that row carries the update policy, and a theme choice with its
-- own settings and version does not belong in it.
CREATE TABLE instance_theme (
    id         BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    theme_id   VARCHAR(64) REFERENCES installed_extensions (extension_id) ON DELETE SET NULL,
    settings   JSONB NOT NULL DEFAULT '{}'::jsonb,
    version    INTEGER NOT NULL DEFAULT 1,
    updated_by UUID,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO instance_theme (id) VALUES (true);

-- A workspace's choice. theme_id NULL means "as the instance"; settings are the
-- workspace-level values of the chosen theme.
CREATE TABLE workspace_theme (
    workspace_id UUID PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    theme_id     VARCHAR(64) REFERENCES installed_extensions (extension_id) ON DELETE SET NULL,
    settings     JSONB NOT NULL DEFAULT '{}'::jsonb,
    version      INTEGER NOT NULL DEFAULT 1,
    updated_by   UUID,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS workspace_theme;
DROP TABLE IF EXISTS instance_theme;
