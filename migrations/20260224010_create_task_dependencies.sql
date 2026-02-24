-- +goose Up

CREATE TABLE task_dependencies (
    id                  UUID PRIMARY KEY,
    task_id             UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    depends_on_task_id  UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    dependency_type     dependency_type NOT NULL DEFAULT 'blocks',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_task_deps_not_self CHECK (task_id != depends_on_task_id),
    CONSTRAINT uq_task_deps_pair UNIQUE (task_id, depends_on_task_id)
);

CREATE INDEX idx_task_deps_task ON task_dependencies(task_id);
CREATE INDEX idx_task_deps_depends ON task_dependencies(depends_on_task_id);

-- +goose Down
DROP TABLE IF EXISTS task_dependencies;
