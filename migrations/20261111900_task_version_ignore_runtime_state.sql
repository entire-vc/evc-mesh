-- +goose Up
-- runtime_state is a status beacon the runner refreshes every few minutes; it is
-- not an edit. advance_task_version bumped tasks.version on every UPDATE, so each
-- beacon refresh invalidated version CAS (PATCH expected_version, acceptance
-- intents) with no visible change. An UPDATE whose only change is runtime_state
-- now keeps the version.
--
-- The runtime_state comparison must stay: snapshot invalidation writes
-- `UPDATE tasks SET version = version` (a row with no column change) and relies
-- on that bump.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_task_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.runtime_state IS DISTINCT FROM OLD.runtime_state
       AND (to_jsonb(NEW) - 'version' - 'runtime_state') = (to_jsonb(OLD) - 'version' - 'runtime_state') THEN
        NEW.version := OLD.version;
        RETURN NEW;
    END IF;
    NEW.version := OLD.version + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION advance_task_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.version := OLD.version + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
