-- +goose Up
-- input_revision is the version of the assignment itself. tasks.version
-- advances on every row write (checkout, extend, release, moves, comments touching
-- updated_at), so a long-running worker cannot use it to tell whether what it was asked
-- to do has changed. input_revision advances only when the assignment content changes:
-- title, description, custom_fields or delegation_level. Checkout, extend, release, heartbeat,
-- runtime_state, comments, artifacts, status moves and assignee changes leave it alone.
-- The trigger owns the column: a direct write to it is ignored.
ALTER TABLE tasks ADD COLUMN input_revision bigint NOT NULL DEFAULT 1;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_task_input_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.title IS DISTINCT FROM OLD.title
       OR NEW.description IS DISTINCT FROM OLD.description
       OR NEW.custom_fields IS DISTINCT FROM OLD.custom_fields
       OR NEW.delegation_level IS DISTINCT FROM OLD.delegation_level THEN
        NEW.input_revision := OLD.input_revision + 1;
    ELSE
        NEW.input_revision := OLD.input_revision;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER tasks_advance_input_revision
    BEFORE UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION advance_task_input_revision();

-- +goose Down
DROP TRIGGER IF EXISTS tasks_advance_input_revision ON tasks;
DROP FUNCTION IF EXISTS advance_task_input_revision();
ALTER TABLE tasks DROP COLUMN input_revision;
