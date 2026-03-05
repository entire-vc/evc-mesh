-- +goose Up

-- Enum for recurring frequency presets
CREATE TYPE recurring_frequency AS ENUM (
    'daily',
    'weekly',
    'monthly',
    'custom'
);

CREATE TABLE recurring_schedules (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id            UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- Template fields
    title_template        VARCHAR(500) NOT NULL,
    description_template  TEXT NOT NULL DEFAULT '',

    -- Schedule
    frequency             recurring_frequency NOT NULL DEFAULT 'weekly',
    cron_expr             VARCHAR(100) NOT NULL,
    timezone              VARCHAR(100) NOT NULL DEFAULT 'UTC',

    -- Assignee (copied to each instance)
    assignee_id           UUID,
    assignee_type         assignee_type NOT NULL DEFAULT 'unassigned',

    -- Task properties for each instance
    priority              task_priority NOT NULL DEFAULT 'none',
    labels                TEXT[] NOT NULL DEFAULT '{}',
    status_id             UUID REFERENCES task_statuses(id) ON DELETE SET NULL,

    -- Lifecycle
    is_active             BOOLEAN NOT NULL DEFAULT TRUE,
    starts_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ends_at               TIMESTAMPTZ,
    max_instances         INT,

    -- Scheduler state
    next_run_at           TIMESTAMPTZ,
    last_triggered_at     TIMESTAMPTZ,
    instance_count        INT NOT NULL DEFAULT 0,

    -- Metadata
    created_by            UUID NOT NULL,
    created_by_type       actor_type NOT NULL DEFAULT 'user',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at            TIMESTAMPTZ
);

CREATE INDEX idx_recurring_schedules_project ON recurring_schedules(project_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_recurring_schedules_next_run ON recurring_schedules(next_run_at)
    WHERE is_active = TRUE AND deleted_at IS NULL;

CREATE INDEX idx_recurring_schedules_workspace ON recurring_schedules(workspace_id)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS recurring_schedules;
DROP TYPE IF EXISTS recurring_frequency;
