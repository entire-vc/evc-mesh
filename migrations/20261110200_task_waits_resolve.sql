-- +goose Up
-- A wait is ended by the component that observed its event: the runner marks
-- it met through POST /tasks/:task_id/waits/:wait_id/resolve, saying who did
-- and why (resolved_by, resolve_cause). Before this nothing ever marked a wait
-- met except an answered decision request.
ALTER TABLE task_waits
    ADD COLUMN resolved_by   UUID REFERENCES agents(id) ON DELETE SET NULL,
    ADD COLUMN resolve_cause TEXT CHECK (char_length(resolve_cause) <= 200);

-- A closed or deleted card waits for nothing: whatever path closed it (a
-- person's move, a merge webhook, finish_task), its active waits end with it
-- instead of piling up as active rows nobody will ever resolve.
-- +goose StatementBegin
CREATE FUNCTION task_waits_end_with_task() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL)
       OR (NEW.status_id IS DISTINCT FROM OLD.status_id AND EXISTS (
            SELECT 1 FROM task_statuses s WHERE s.id = NEW.status_id AND s.category IN ('done', 'cancelled'))) THEN
        UPDATE task_waits SET resolved_at = now(), resolution = 'cancelled', resolve_cause = 'card closed'
         WHERE task_id = NEW.id AND resolved_at IS NULL;
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER task_waits_end_with_task AFTER UPDATE OF status_id, deleted_at ON tasks
    FOR EACH ROW EXECUTE FUNCTION task_waits_end_with_task();

-- +goose Down
DROP TRIGGER IF EXISTS task_waits_end_with_task ON tasks;
DROP FUNCTION IF EXISTS task_waits_end_with_task();
ALTER TABLE task_waits
    DROP COLUMN IF EXISTS resolve_cause,
    DROP COLUMN IF EXISTS resolved_by;
