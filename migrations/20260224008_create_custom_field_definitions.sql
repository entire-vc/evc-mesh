-- +goose Up

-- Custom field definitions per project. Agents see only fields with is_visible_to_agents = TRUE.
CREATE TABLE custom_field_definitions (
    id                      UUID PRIMARY KEY,
    project_id              UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                    VARCHAR(255) NOT NULL,
    slug                    VARCHAR(100) NOT NULL,
    field_type              custom_field_type NOT NULL,
    description             TEXT NOT NULL DEFAULT '',
    options                 JSONB NOT NULL DEFAULT '{}',
    default_value           JSONB,
    is_required             BOOLEAN NOT NULL DEFAULT FALSE,
    is_visible_to_agents    BOOLEAN NOT NULL DEFAULT TRUE,
    position                INT NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_custom_fields_project_slug UNIQUE (project_id, slug),
    CONSTRAINT chk_custom_fields_slug_format
        CHECK (slug ~ '^[a-z0-9_]{1,100}$')
);

CREATE INDEX idx_custom_fields_project ON custom_field_definitions(project_id, position);

-- +goose Down
DROP TABLE IF EXISTS custom_field_definitions;
