-- +goose Up

-- Central entity: a unit of work assignable to users or agents.
-- Supports subtasks (parent_task_id), custom fields (JSONB), labels (TEXT[]).
CREATE TABLE tasks (
    id                  UUID PRIMARY KEY,
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status_id           UUID NOT NULL REFERENCES task_statuses(id),
    title               VARCHAR(500) NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    assignee_id         UUID,
    assignee_type       assignee_type NOT NULL DEFAULT 'unassigned',
    priority            task_priority NOT NULL DEFAULT 'none',
    parent_task_id      UUID REFERENCES tasks(id) ON DELETE SET NULL,
    position            FLOAT NOT NULL DEFAULT 0,
    due_date            TIMESTAMPTZ,
    estimated_hours     DECIMAL(8,2),
    custom_fields       JSONB NOT NULL DEFAULT '{}',
    labels              TEXT[] NOT NULL DEFAULT '{}',
    task_number         INT NOT NULL,
    created_by          UUID NOT NULL,
    created_by_type     actor_type NOT NULL DEFAULT 'user',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    deleted_at          TIMESTAMPTZ,

    CONSTRAINT chk_tasks_parent_not_self CHECK (parent_task_id != id),
    CONSTRAINT chk_tasks_estimated_positive CHECK (estimated_hours IS NULL OR estimated_hours >= 0),
    CONSTRAINT uq_tasks_project_number UNIQUE (project_id, task_number)
);

-- Kanban view: tasks per project filtered by status
CREATE INDEX idx_tasks_project_status ON tasks(project_id, status_id)
    WHERE deleted_at IS NULL;

-- Tasks assigned to a specific user/agent
CREATE INDEX idx_tasks_assignee ON tasks(assignee_id, assignee_type)
    WHERE deleted_at IS NULL;

-- Subtask lookup
CREATE INDEX idx_tasks_parent ON tasks(parent_task_id)
    WHERE parent_task_id IS NOT NULL AND deleted_at IS NULL;

-- Custom field search (GIN for JSONB)
CREATE INDEX idx_tasks_custom_fields ON tasks USING GIN(custom_fields);

-- Label search (GIN for array)
CREATE INDEX idx_tasks_labels ON tasks USING GIN(labels);

-- Due date dashboard / notifications
CREATE INDEX idx_tasks_due_date ON tasks(due_date)
    WHERE due_date IS NOT NULL AND deleted_at IS NULL;

-- Tasks sorted by creation date (pagination)
CREATE INDEX idx_tasks_created_at ON tasks(project_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Tasks by priority
CREATE INDEX idx_tasks_priority ON tasks(project_id, priority)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS tasks;
