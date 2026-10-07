-- +goose Up
ALTER TABLE tasks ADD COLUMN version bigint NOT NULL DEFAULT 1;
-- Cover every existing writer, including lease/gate writers, without changing
-- the generation protocol. Versions are independent of wall-clock timestamps.
-- +goose StatementBegin
CREATE FUNCTION advance_task_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.version := OLD.version + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER task_version_before_update BEFORE UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION advance_task_version();

-- Snapshot invalidation alone does not change a list row. Preserve ADR-0004:
-- comments do not invalidate list cursors, and artifact/VCS triggers already
-- bump the list revision once for their own changes.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_bump_task_list_revision_from_tasks()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (to_jsonb(NEW) - 'version') = (to_jsonb(OLD) - 'version') THEN
        RETURN NULL;
    END IF;
    PERFORM bump_task_list_revision(NEW.project_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Child activity takes the same task row lock as mutations. A comment/artifact
-- committed after candidate selection invalidates its version, even if its
-- timestamp is backdated. This also serializes concurrent reaper decisions.
-- +goose StatementBegin
CREATE FUNCTION invalidate_task_snapshot_on_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        UPDATE tasks SET version = version WHERE id = OLD.task_id;
        RETURN OLD;
    END IF;
    UPDATE tasks SET version = version WHERE id = NEW.task_id;
    IF TG_OP = 'UPDATE' AND OLD.task_id IS DISTINCT FROM NEW.task_id THEN
        UPDATE tasks SET version = version WHERE id = OLD.task_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER comment_task_snapshot AFTER INSERT OR UPDATE OR DELETE ON comments
    FOR EACH ROW EXECUTE FUNCTION invalidate_task_snapshot_on_activity();
CREATE TRIGGER artifact_task_snapshot AFTER INSERT OR UPDATE OR DELETE ON artifacts
    FOR EACH ROW EXECUTE FUNCTION invalidate_task_snapshot_on_activity();
CREATE TRIGGER vcs_task_snapshot AFTER INSERT OR UPDATE OR DELETE ON vcs_links
    FOR EACH ROW EXECUTE FUNCTION invalidate_task_snapshot_on_activity();

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_bump_task_list_revision_from_tasks()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM bump_task_list_revision(NEW.project_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER vcs_task_snapshot ON vcs_links;
DROP TRIGGER artifact_task_snapshot ON artifacts;
DROP TRIGGER comment_task_snapshot ON comments;
DROP FUNCTION invalidate_task_snapshot_on_activity();
DROP TRIGGER task_version_before_update ON tasks;
DROP FUNCTION advance_task_version();
ALTER TABLE tasks DROP COLUMN version;
