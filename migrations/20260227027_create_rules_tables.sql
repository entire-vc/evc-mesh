-- +goose Up

CREATE TABLE workspace_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    rule_type VARCHAR(50) NOT NULL,
    config JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, rule_type)
);
CREATE INDEX idx_workspace_rules_ws ON workspace_rules(workspace_id);

CREATE TABLE project_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    rule_type VARCHAR(50) NOT NULL,
    config JSONB NOT NULL DEFAULT '{}',
    enforcement_mode VARCHAR(20) NOT NULL DEFAULT 'advisory',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, rule_type)
);
CREATE INDEX idx_project_rules_proj ON project_rules(project_id);

CREATE TABLE rule_violation_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id UUID REFERENCES projects(id) ON DELETE SET NULL,
    actor_id UUID NOT NULL,
    actor_type VARCHAR(20) NOT NULL,
    rule_type VARCHAR(50) NOT NULL,
    violation_detail JSONB NOT NULL DEFAULT '{}',
    action_taken VARCHAR(20) NOT NULL DEFAULT 'allowed',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_rule_violation_logs_ws ON rule_violation_logs(workspace_id);
CREATE INDEX idx_rule_violation_logs_created ON rule_violation_logs(created_at);

-- +goose Down

DROP TABLE IF EXISTS rule_violation_logs;
DROP TABLE IF EXISTS project_rules;
DROP TABLE IF EXISTS workspace_rules;
