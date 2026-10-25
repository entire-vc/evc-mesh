-- +goose Up

-- A `blocks` edge is normally released when the blocker reaches done/cancelled.
-- That never happens for a perpetual monitor or recurring card, so a dependent
-- waiting on one stays held forever. `condition` names what the edge is really
-- waiting for; a condition other than task_done is satisfied once
-- condition_met_at is set together with a non-empty evidence_url, whatever the
-- blocker's status. Existing rows default to task_done: behaviour unchanged.
ALTER TABLE task_dependencies
    ADD COLUMN condition text NOT NULL DEFAULT 'task_done',
    ADD COLUMN evidence_url text NULL,
    ADD COLUMN condition_met_at timestamptz NULL;

ALTER TABLE task_dependencies
    ADD CONSTRAINT chk_task_deps_condition CHECK (condition IN
        ('task_done', 'access_ready', 'contract_ready', 'artifact_published', 'deployed', 'accepted'));

-- +goose Down
ALTER TABLE task_dependencies DROP CONSTRAINT IF EXISTS chk_task_deps_condition;
ALTER TABLE task_dependencies
    DROP COLUMN IF EXISTS condition_met_at,
    DROP COLUMN IF EXISTS evidence_url,
    DROP COLUMN IF EXISTS condition;
