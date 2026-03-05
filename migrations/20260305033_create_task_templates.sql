-- +goose Up
CREATE TABLE task_templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    description TEXT DEFAULT '',
    title_template VARCHAR(500) NOT NULL DEFAULT '',
    description_template TEXT DEFAULT '',
    priority VARCHAR(20) DEFAULT 'medium',
    labels TEXT[] DEFAULT '{}',
    estimated_hours REAL,
    custom_fields JSONB DEFAULT '{}',
    assignee_id UUID,
    assignee_type VARCHAR(20),
    status_id UUID,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_task_templates_project ON task_templates(project_id);

-- +goose Down
DROP TABLE IF EXISTS task_templates;
