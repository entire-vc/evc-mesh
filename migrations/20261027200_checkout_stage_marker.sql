-- +goose Up
-- The lease reaper may only take back a stage the system itself set for a
-- checkout. checkout_stage_status_id records that stage: the checkout
-- auto-progression (todo -> first in_progress on checkout) writes it in the same
-- guarded UPDATE as the status. Any other status change -- a person or agent
-- moving the card, a CRM/Proto business stage, an automatic dependency or parent
-- transition, the reaper itself -- leaves it pointing at the old status, and the
-- trigger below nulls it, so the stage becomes intentional and no sweep may reset
-- it. Without the marker the reaper read "in_progress, no lease, quiet 120 min"
-- as an abandoned agent run and returned manual stages to the first todo status.
ALTER TABLE tasks ADD COLUMN checkout_stage_status_id uuid;

-- Current in_progress cards keep reaper recovery only when their latest move is
-- the checkout auto-progression itself. Anything else (a later manual move, a
-- position drag, no audit row) stays unmarked: the safe direction is to keep
-- the stage, not to reset it.
UPDATE tasks t SET checkout_stage_status_id = t.status_id
FROM task_statuses s
WHERE s.id = t.status_id AND s.category = 'in_progress' AND t.deleted_at IS NULL
  AND (SELECT a.source FROM activity_log a
       WHERE a.entity_type = 'task' AND a.entity_id = t.id AND a.action = 'task.moved'
       ORDER BY a.created_at DESC LIMIT 1) = 'checkout';

-- Invariant: the marker is NULL or equals the current status. Enforced for every
-- writer, including bulk and legacy update paths that never heard of it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tasks_checkout_stage_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.checkout_stage_status_id IS DISTINCT FROM NEW.status_id THEN
        NEW.checkout_stage_status_id := NULL;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER tasks_checkout_stage_guard BEFORE INSERT OR UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION tasks_checkout_stage_guard();

-- +goose Down
DROP TRIGGER IF EXISTS tasks_checkout_stage_guard ON tasks;
DROP FUNCTION IF EXISTS tasks_checkout_stage_guard();
ALTER TABLE tasks DROP COLUMN IF EXISTS checkout_stage_status_id;
