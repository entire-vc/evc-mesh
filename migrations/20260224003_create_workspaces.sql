-- +goose Up

-- Top-level tenant. All data is isolated via workspace_id.
CREATE TABLE workspaces (
    id                  UUID PRIMARY KEY,
    name                VARCHAR(255) NOT NULL,
    slug                VARCHAR(100) NOT NULL UNIQUE,
    owner_id            UUID NOT NULL,
    settings            JSONB NOT NULL DEFAULT '{}',
    billing_plan_id     VARCHAR(50),
    billing_customer_id VARCHAR(100),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

ALTER TABLE workspaces
    ADD CONSTRAINT chk_workspaces_slug_format
    CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$');

CREATE INDEX idx_workspaces_owner ON workspaces(owner_id);
CREATE INDEX idx_workspaces_slug ON workspaces(slug);

-- +goose Down
DROP TABLE IF EXISTS workspaces;
