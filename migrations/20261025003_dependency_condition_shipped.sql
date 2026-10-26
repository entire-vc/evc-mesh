-- +goose Up

-- `shipped` joins the dependency conditions: the edge is released when the
-- blocker is shipped (tasks.is_shipped) or done. Cancelled never releases it.
-- Same column, same default (task_done): existing rows and behaviour unchanged.
ALTER TABLE task_dependencies DROP CONSTRAINT IF EXISTS chk_task_deps_condition;
ALTER TABLE task_dependencies
    ADD CONSTRAINT chk_task_deps_condition CHECK (condition IN
        ('task_done', 'access_ready', 'contract_ready', 'artifact_published', 'shipped', 'deployed', 'accepted'));

-- +goose Down
-- Rows already using `shipped` fall back to the default before the narrower
-- CHECK is restored.
UPDATE task_dependencies SET condition = 'task_done' WHERE condition = 'shipped';
ALTER TABLE task_dependencies DROP CONSTRAINT IF EXISTS chk_task_deps_condition;
ALTER TABLE task_dependencies
    ADD CONSTRAINT chk_task_deps_condition CHECK (condition IN
        ('task_done', 'access_ready', 'contract_ready', 'artifact_published', 'deployed', 'accepted'));
