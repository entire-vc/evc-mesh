-- +goose Up
-- The completion graph is blocks (task waits on prerequisite) plus
-- parent -> child. Informational dependencies never participate.
-- Guards live at the database boundary so child creation, reparenting and
-- dependency type changes share the same transaction and workspace lock.
-- No historical edges are rewritten by installing this migration.
-- +goose StatementBegin
CREATE FUNCTION completion_graph_lock(ws uuid) RETURNS void
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    -- A waiting writer must read a fresh snapshot after acquiring the lock.
    -- Higher isolation levels retain a transaction snapshot; fail closed.
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'completion graph writes require read committed isolation';
    END IF;
    PERFORM pg_advisory_xact_lock(7042026, hashtext(ws::text));
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION completion_graph_assert_edge(ws uuid, source uuid, target uuid,
    replaced_child uuid DEFAULT NULL, replaced_dependency uuid DEFAULT NULL)
RETURNS void LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    edges jsonb;
    frontier uuid[] := ARRAY[target];
    visited uuid[] := ARRAY[target];
    next_frontier uuid[];
    predecessors jsonb := '{}'::jsonb;
    step record;
    path uuid[];
    current_node uuid;
BEGIN
    IF source = target THEN
        RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_cycle',
            MESSAGE='completion cycle', DETAIL=source::text || ' -> ' || target::text;
    END IF;
    -- VOLATILE PL/pgSQL statements take fresh READ COMMITTED snapshots AFTER
    -- completion_graph_lock has returned. Both endpoints of every traversed
    -- edge are scoped; legacy cross-tenant rows cannot enter an error path.
    SELECT COALESCE(jsonb_agg(jsonb_build_object('src',src,'dst',dst)), '[]'::jsonb)
    INTO edges FROM (
        SELECT d.task_id AS src, d.depends_on_task_id AS dst
        FROM task_dependencies d
        JOIN tasks a ON a.id=d.task_id AND a.deleted_at IS NULL
        JOIN projects ap ON ap.id=a.project_id AND ap.workspace_id=ws
        JOIN tasks b ON b.id=d.depends_on_task_id AND b.deleted_at IS NULL
        JOIN projects bp ON bp.id=b.project_id AND bp.workspace_id=ws
        WHERE d.dependency_type='blocks'
          AND d.id IS DISTINCT FROM replaced_dependency
        UNION
        SELECT p.id,c.id FROM tasks c
        JOIN tasks p ON p.id=c.parent_task_id AND p.deleted_at IS NULL
        JOIN projects cp ON cp.id=c.project_id AND cp.workspace_id=ws
        JOIN projects pp ON pp.id=p.project_id AND pp.workspace_id=ws
        WHERE c.deleted_at IS NULL AND c.id IS DISTINCT FROM replaced_child
    ) graph;
    -- Breadth-first traversal visits each node once, terminating even when
    -- historical cycles exist. It returns an actual shortest cycle witness.
    WHILE cardinality(frontier)>0 LOOP
        next_frontier := ARRAY[]::uuid[];
        FOR step IN
            SELECT DISTINCT ON (e.dst) e.src,e.dst
            FROM jsonb_to_recordset(edges) AS e(src uuid,dst uuid)
            WHERE e.src=ANY(frontier) AND NOT e.dst=ANY(visited)
            ORDER BY e.dst,e.src
        LOOP
            predecessors := predecessors || jsonb_build_object(step.dst::text,step.src::text);
            IF step.dst=source THEN
                path := ARRAY[source]; current_node := source;
                WHILE current_node<>target LOOP
                    current_node := (predecessors->>current_node::text)::uuid;
                    path := array_prepend(current_node,path);
                END LOOP;
                path := array_prepend(source,path);
                RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_cycle',
                    MESSAGE='completion cycle', DETAIL=array_to_string(path,' -> ');
            END IF;
            next_frontier := array_append(next_frontier,step.dst);
        END LOOP;
        visited := visited || next_frontier; frontier := next_frontier;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION completion_graph_task_guard() RETURNS trigger
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

-- +goose StatementBegin
CREATE FUNCTION completion_graph_dependency_guard() RETURNS trigger
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    ws uuid;
    other_ws uuid;
    source_project uuid;
    target_project uuid;
    current_parent uuid;
BEGIN
    IF TG_OP='DELETE' THEN
        SELECT p.workspace_id INTO ws FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=OLD.task_id;
        IF ws IS NULL THEN RETURN OLD; END IF; -- cascading task/project delete
        PERFORM completion_graph_lock(ws);
        IF OLD.dependency_type='is_child_of' THEN
            UPDATE tasks SET parent_task_id=NULL,updated_at=now()
            WHERE id=OLD.task_id AND parent_task_id=OLD.depends_on_task_id AND deleted_at IS NULL;
        END IF;
        RETURN OLD;
    END IF;
    IF TG_OP='UPDATE' AND (NEW.task_id<>OLD.task_id OR NEW.depends_on_task_id<>OLD.depends_on_task_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
            MESSAGE='dependency endpoints are immutable; delete and create instead';
    END IF;
    SELECT p.workspace_id INTO STRICT ws FROM tasks t JOIN projects p ON p.id=t.project_id
        WHERE t.id=NEW.task_id AND t.deleted_at IS NULL;
    PERFORM completion_graph_lock(ws);
    SELECT t.project_id,t.parent_task_id,p.workspace_id INTO STRICT source_project,current_parent,other_ws
        FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=NEW.task_id AND t.deleted_at IS NULL;
    IF other_ws<>ws THEN RAISE EXCEPTION 'completion graph scope changed; retry request'; END IF;
    SELECT t.project_id,p.workspace_id INTO target_project,other_ws FROM tasks t JOIN projects p ON p.id=t.project_id
        WHERE t.id=NEW.depends_on_task_id AND t.deleted_at IS NULL;
    IF other_ws IS DISTINCT FROM ws OR NEW.task_id=NEW.depends_on_task_id THEN
        RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
            MESSAGE='dependency endpoints must be distinct available tasks in the same workspace';
    END IF;
    IF TG_OP='UPDATE' AND OLD.dependency_type='is_child_of' AND NEW.dependency_type<>'is_child_of' THEN
        UPDATE tasks SET parent_task_id=NULL,updated_at=now()
        WHERE id=OLD.task_id AND parent_task_id=OLD.depends_on_task_id AND deleted_at IS NULL;
    END IF;
    IF NEW.dependency_type='blocks' THEN
        PERFORM completion_graph_assert_edge(ws,NEW.task_id,NEW.depends_on_task_id,NULL,NEW.id);
    ELSIF NEW.dependency_type='is_child_of' THEN
        IF source_project<>target_project THEN
            RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
                MESSAGE='parent must be in the same project';
        END IF;
        IF current_parent IS NOT NULL AND current_parent<>NEW.depends_on_task_id THEN
            RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_scope',
                MESSAGE='task already has another parent';
        END IF;
        -- Even if this parent is already set, validate the candidate graph.
        PERFORM completion_graph_assert_edge(ws,NEW.depends_on_task_id,NEW.task_id,NEW.task_id,NEW.id);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER completion_graph_tasks BEFORE INSERT OR UPDATE OF parent_task_id,project_id
ON tasks FOR EACH ROW EXECUTE FUNCTION completion_graph_task_guard();
CREATE TRIGGER completion_graph_dependencies BEFORE INSERT OR UPDATE OR DELETE
ON task_dependencies FOR EACH ROW EXECUTE FUNCTION completion_graph_dependency_guard();

-- +goose StatementBegin
CREATE FUNCTION completion_graph_apply_parent() RETURNS trigger
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    IF NEW.dependency_type='is_child_of' THEN
        -- AFTER makes the new type visible to the task guard during a
        -- blocks -> is_child_of conversion. This is still the same transaction.
        UPDATE tasks SET parent_task_id=NEW.depends_on_task_id,updated_at=now()
        WHERE id=NEW.task_id AND parent_task_id IS DISTINCT FROM NEW.depends_on_task_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- INSERT's hierarchy side effect is atomic even for older writers. An existing
-- dependency snapshot trigger already invalidates their pre-insert task CAS;
-- new writers acknowledge the atomic DB result instead of updating a stale task.
CREATE TRIGGER completion_graph_parent_apply AFTER INSERT OR UPDATE
ON task_dependencies FOR EACH ROW EXECUTE FUNCTION completion_graph_apply_parent();

-- +goose Down
DROP TRIGGER completion_graph_parent_apply ON task_dependencies;
DROP FUNCTION completion_graph_apply_parent();
DROP TRIGGER completion_graph_dependencies ON task_dependencies;
DROP TRIGGER completion_graph_tasks ON tasks;
DROP FUNCTION completion_graph_dependency_guard();
DROP FUNCTION completion_graph_task_guard();
DROP FUNCTION completion_graph_assert_edge(uuid,uuid,uuid,uuid,uuid);
DROP FUNCTION completion_graph_lock(uuid);
