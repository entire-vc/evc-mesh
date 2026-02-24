-- +goose Up

-- Registered AI agents within a workspace. Authenticate via API keys, interact via MCP/REST.
CREATE TABLE agents (
    id                      UUID PRIMARY KEY,
    workspace_id            UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                    VARCHAR(255) NOT NULL,
    slug                    VARCHAR(100) NOT NULL,
    agent_type              agent_type NOT NULL DEFAULT 'custom',
    api_key_hash            VARCHAR(128) NOT NULL,
    api_key_prefix          VARCHAR(20) NOT NULL,
    capabilities            JSONB NOT NULL DEFAULT '{}',
    status                  agent_status NOT NULL DEFAULT 'offline',
    last_heartbeat          TIMESTAMPTZ,
    current_task_id         UUID REFERENCES tasks(id) ON DELETE SET NULL,
    settings                JSONB NOT NULL DEFAULT '{}',
    total_tasks_completed   INT NOT NULL DEFAULT 0,
    total_errors            INT NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_agents_workspace_slug UNIQUE (workspace_id, slug),
    CONSTRAINT chk_agents_slug_format
        CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$')
);

CREATE INDEX idx_agents_workspace ON agents(workspace_id, status);
CREATE INDEX idx_agents_key_prefix ON agents(api_key_prefix);

-- +goose Down
DROP TABLE IF EXISTS agents;
