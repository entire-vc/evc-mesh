-- +goose Up
CREATE TABLE auto_transition_rules (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    trigger          TEXT NOT NULL CHECK (trigger IN ('all_subtasks_done', 'blocking_dep_resolved')),
    target_status_id UUID NOT NULL REFERENCES task_statuses(id),
    is_enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, trigger)
);

CREATE INDEX idx_auto_transition_rules_project ON auto_transition_rules(project_id);

-- +goose Down
DROP TABLE IF EXISTS auto_transition_rules;
