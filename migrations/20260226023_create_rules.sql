-- +goose Up

CREATE TYPE rule_scope AS ENUM ('workspace', 'project', 'agent');
CREATE TYPE rule_enforcement AS ENUM ('block', 'warn', 'log');

CREATE TABLE rules (
    id              UUID PRIMARY KEY,
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id      UUID REFERENCES projects(id) ON DELETE CASCADE,
    agent_id        UUID REFERENCES agents(id) ON DELETE SET NULL,

    scope           rule_scope NOT NULL,
    rule_type       VARCHAR(100) NOT NULL,
    name            VARCHAR(255) NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    config          JSONB NOT NULL DEFAULT '{}',

    applies_to_actor_types  TEXT[] NOT NULL DEFAULT '{}',
    applies_to_roles        TEXT[] NOT NULL DEFAULT '{}',

    enforcement     rule_enforcement NOT NULL DEFAULT 'block',
    priority        INT NOT NULL DEFAULT 100,
    is_enabled      BOOLEAN NOT NULL DEFAULT TRUE,

    created_by      UUID NOT NULL,
    created_by_type actor_type NOT NULL DEFAULT 'user',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Scope constraints
    CONSTRAINT chk_rules_scope_workspace
        CHECK (scope != 'workspace' OR (project_id IS NULL AND agent_id IS NULL)),
    CONSTRAINT chk_rules_scope_project
        CHECK (scope != 'project' OR (project_id IS NOT NULL AND agent_id IS NULL)),
    CONSTRAINT chk_rules_scope_agent
        CHECK (scope != 'agent' OR agent_id IS NOT NULL),
    CONSTRAINT chk_rules_type_format
        CHECK (rule_type ~ '^[a-z_]+\.[a-z_]+$')
);

CREATE INDEX idx_rules_workspace ON rules(workspace_id) WHERE is_enabled = TRUE;
CREATE INDEX idx_rules_project ON rules(project_id) WHERE is_enabled = TRUE AND project_id IS NOT NULL;
CREATE INDEX idx_rules_agent ON rules(agent_id) WHERE is_enabled = TRUE AND agent_id IS NOT NULL;
CREATE INDEX idx_rules_type ON rules(workspace_id, rule_type) WHERE is_enabled = TRUE;

-- +goose Down

DROP TABLE IF EXISTS rules;
DROP TYPE IF EXISTS rule_enforcement;
DROP TYPE IF EXISTS rule_scope;
