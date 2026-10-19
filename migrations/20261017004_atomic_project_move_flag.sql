-- +goose Up
-- Atomic multi-task project move. The completion graph guard keeps refusing a
-- project change on any connected task (SQLSTATE 23514, completion_scope)
-- unless `mesh.atomic_move` equals the id of the CURRENT transaction
-- (txid_current()). Only the batch move service sets it, via
-- set_config(..., true), after taking the workspace graph lock and the target
-- project's task_number lock, and it re-validates the whole graph before
-- COMMIT. A constant like 'on', a session-scoped SET, or a value left over from
-- an earlier transaction on a reused connection never matches, so the exception
-- cannot outlive the transaction that opened it. The flag does not relax the
-- same-project parent rule or the cycle assertion. No data is rewritten.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION completion_graph_task_guard() RETURNS trigger
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    ws uuid;
    old_ws uuid;
    parent_project uuid;
BEGIN
    IF TG_OP='UPDATE' AND NEW.parent_task_id IS NOT DISTINCT FROM OLD.parent_task_id
        AND NEW.project_id=OLD.project_id THEN RETURN NEW; END IF;
    SELECT workspace_id INTO STRICT ws FROM projects WHERE id=NEW.project_id;
    IF TG_OP='UPDATE' AND NEW.project_id<>OLD.project_id THEN
        SELECT workspace_id INTO STRICT old_ws FROM projects WHERE id=OLD.project_id;
        IF old_ws<ws THEN PERFORM completion_graph_lock(old_ws); END IF;
    END IF;
    PERFORM completion_graph_lock(ws);
    IF old_ws IS NOT NULL AND old_ws>=ws THEN PERFORM completion_graph_lock(old_ws); END IF;
    IF TG_OP='UPDATE' AND NEW.project_id<>OLD.project_id THEN
        -- Moving a connected node across projects changes the graph's scope.
        -- Only the batch move service may do it, and it proves the flag by
        -- setting it LOCAL after taking the workspace graph lock. Everything
        -- below (same-project parent, cycle assertion) still applies.
        IF COALESCE(current_setting('mesh.atomic_move', true), '') = txid_current()::text THEN
            NULL;
        ELSIF NEW.parent_task_id IS NOT NULL OR EXISTS(SELECT 1 FROM tasks WHERE parent_task_id=NEW.id)
            OR EXISTS(SELECT 1 FROM task_dependencies WHERE task_id=NEW.id OR depends_on_task_id=NEW.id) THEN
            RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
                MESSAGE='connected task cannot change project';
        END IF;
    END IF;
    IF NEW.parent_task_id IS NULL THEN RETURN NEW; END IF;
    IF NEW.parent_task_id=NEW.id THEN
        PERFORM completion_graph_assert_edge(ws,NEW.id,NEW.id,NEW.id);
    END IF;
    SELECT project_id INTO parent_project FROM tasks
        WHERE id=NEW.parent_task_id AND deleted_at IS NULL;
    IF parent_project IS DISTINCT FROM NEW.project_id THEN
        RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
            MESSAGE='parent must be an available task in the same project';
    END IF;
    PERFORM completion_graph_assert_edge(ws,NEW.parent_task_id,NEW.id,NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- Restore the previous guard body verbatim (no flag escape hatch).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION completion_graph_task_guard() RETURNS trigger
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    ws uuid;
    old_ws uuid;
    parent_project uuid;
BEGIN
    IF TG_OP='UPDATE' AND NEW.parent_task_id IS NOT DISTINCT FROM OLD.parent_task_id
        AND NEW.project_id=OLD.project_id THEN RETURN NEW; END IF;
    SELECT workspace_id INTO STRICT ws FROM projects WHERE id=NEW.project_id;
    IF TG_OP='UPDATE' AND NEW.project_id<>OLD.project_id THEN
        SELECT workspace_id INTO STRICT old_ws FROM projects WHERE id=OLD.project_id;
        IF old_ws<ws THEN PERFORM completion_graph_lock(old_ws); END IF;
    END IF;
    PERFORM completion_graph_lock(ws);
    IF old_ws IS NOT NULL AND old_ws>=ws THEN PERFORM completion_graph_lock(old_ws); END IF;
    IF TG_OP='UPDATE' AND NEW.project_id<>OLD.project_id THEN
        -- Moving a connected node across projects changes the graph's scope.
        -- The API does not expose this mutation; refuse rather than skip edges.
        IF NEW.parent_task_id IS NOT NULL OR EXISTS(SELECT 1 FROM tasks WHERE parent_task_id=NEW.id)
            OR EXISTS(SELECT 1 FROM task_dependencies WHERE task_id=NEW.id OR depends_on_task_id=NEW.id) THEN
            RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
                MESSAGE='connected task cannot change project';
        END IF;
    END IF;
    IF NEW.parent_task_id IS NULL THEN RETURN NEW; END IF;
    IF NEW.parent_task_id=NEW.id THEN
        PERFORM completion_graph_assert_edge(ws,NEW.id,NEW.id,NEW.id);
    END IF;
    SELECT project_id INTO parent_project FROM tasks
        WHERE id=NEW.parent_task_id AND deleted_at IS NULL;
    IF parent_project IS DISTINCT FROM NEW.project_id THEN
        RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
            MESSAGE='parent must be an available task in the same project';
    END IF;
    PERFORM completion_graph_assert_edge(ws,NEW.parent_task_id,NEW.id,NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
