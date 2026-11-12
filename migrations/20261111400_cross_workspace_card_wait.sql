-- +goose Up
-- Typed card waits alone may bridge workspaces. A durable receipt lets the
-- runner observe its own card without reading the target with a home key.
ALTER TABLE task_waits ADD COLUMN server_observed boolean NOT NULL DEFAULT false;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_server_card
    CHECK (NOT server_observed OR kind = 'card');
ALTER TABLE task_dependencies ADD COLUMN wait_agent_id uuid REFERENCES agents(id) ON DELETE CASCADE;
CREATE INDEX idx_task_waits_server_agent ON task_waits(agent_id)
    WHERE server_observed AND resolved_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION card_wait_agent_member(agent uuid, ws uuid) RETURNS boolean
LANGUAGE sql VOLATILE AS $$
    SELECT EXISTS (SELECT 1 FROM agents a WHERE a.id=agent AND a.deleted_at IS NULL AND
        (a.workspace_id=ws OR EXISTS (SELECT 1 FROM agent_workspace_grants g
            WHERE g.agent_id=a.id AND g.workspace_id=ws AND g.revoked_at IS NULL)));
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION card_wait_membership_lock(agent uuid, ws uuid, exclusive boolean) RETURNS void
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    IF exclusive THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('mesh.card-wait:' || agent::text || ':' || ws::text, 0));
    ELSE
        PERFORM pg_advisory_xact_lock_shared(hashtextextended('mesh.card-wait:' || agent::text || ':' || ws::text, 0));
    END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION completion_graph_lock(ws uuid) RETURNS void
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    -- A waiting writer must read a fresh snapshot after acquiring the lock.
    -- Higher isolation levels retain a transaction snapshot; fail closed.
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'completion graph writes require read committed isolation';
    END IF;
    -- Cross-workspace edges make the completion graph one connected lock
    -- domain. Keep the workspace lock as well for older in-flight writers.
    PERFORM pg_advisory_xact_lock(7042026, 0);
    PERFORM pg_advisory_xact_lock(7042026, hashtext(ws::text));
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION completion_graph_assert_edge(ws uuid, source uuid, target uuid,
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
    witness text;
BEGIN
    IF source = target THEN
        RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_cycle',
            MESSAGE='completion cycle', DETAIL=source::text || ' -> ' || target::text;
    END IF;
    -- VOLATILE PL/pgSQL statements take fresh READ COMMITTED snapshots AFTER
    -- completion_graph_lock has returned. Traverse the full graph: a later
    -- LOCAL edge can close a path that crosses workspace boundaries.
    SELECT COALESCE(jsonb_agg(jsonb_build_object('src',src,'dst',dst)), '[]'::jsonb)
    INTO edges FROM (
        SELECT d.task_id AS src, d.depends_on_task_id AS dst
        FROM task_dependencies d
        JOIN tasks a ON a.id=d.task_id AND a.deleted_at IS NULL
        JOIN projects ap ON ap.id=a.project_id
        JOIN tasks b ON b.id=d.depends_on_task_id AND b.deleted_at IS NULL
        JOIN projects bp ON bp.id=b.project_id
        WHERE d.dependency_type='blocks'
          AND d.id IS DISTINCT FROM replaced_dependency
        UNION
        SELECT p.id,c.id FROM tasks c
        JOIN tasks p ON p.id=c.parent_task_id AND p.deleted_at IS NULL
        JOIN projects cp ON cp.id=c.project_id
        JOIN projects pp ON pp.id=p.project_id
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
                witness := ''; -- never disclose foreign node IDs through error DETAIL
                IF NOT EXISTS (SELECT 1 FROM tasks t JOIN projects p ON p.id=t.project_id
                    WHERE t.id=ANY(path) AND p.workspace_id<>ws) THEN
                    witness := array_to_string(path,' -> ');
                END IF;
                RAISE EXCEPTION USING ERRCODE='23514', CONSTRAINT='completion_cycle',
                    MESSAGE='completion cycle', DETAIL=witness;
            END IF;
            next_frontier := array_append(next_frontier,step.dst);
        END LOOP;
        visited := visited || next_frontier; frontier := next_frontier;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION completion_graph_dependency_guard() RETURNS trigger
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
    IF NEW.task_id=NEW.depends_on_task_id OR other_ws IS NULL
       OR (other_ws<>ws AND NOT (
           NEW.dependency_type='blocks' AND NEW.wait_agent_id IS NOT NULL
           AND card_wait_agent_member(NEW.wait_agent_id,ws)
           AND card_wait_agent_member(NEW.wait_agent_id,other_ws))) THEN
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

-- +goose StatementBegin
CREATE FUNCTION resolve_server_card_waits(target uuid) RETURNS void
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    -- SQL twin of domain.DependencyReleasedSQL. Updating ONLY waits avoids
    -- target->source row locks opposite the declaration's source->target order.
    UPDATE task_waits w SET resolved_at=now(), resolution='met', resolve_cause='target_condition_met'
    FROM tasks bt JOIN task_statuses bts ON bts.id=bt.status_id,
         task_dependencies d
    WHERE w.server_observed AND w.resolved_at IS NULL AND w.target_task_id=target
      AND bt.id=target AND bt.deleted_at IS NULL
      AND d.task_id=w.task_id AND d.depends_on_task_id=target
      AND d.dependency_type='blocks' AND d.condition=COALESCE(w.target_condition,'task_done')
      AND COALESCE(
          (d.condition<>'task_done' AND d.evidence_status='trusted'
             AND d.condition_met_at IS NOT NULL AND COALESCE(btrim(d.evidence_url),'')<>'')
          OR CASE d.condition
              WHEN 'shipped' THEN bt.is_shipped OR bts.category='done'
              WHEN 'deployed' THEN bt.is_shipped OR EXISTS (
                  SELECT 1 FROM vcs_links dv WHERE dv.task_id=bt.id AND dv.link_type='pr'
                    AND COALESCE(dv.metadata->'deployed'->>'sha','')<>''
                    AND dv.metadata->'deployed'->>'at' IS NOT NULL
                    AND dv.metadata->'deployed'->>'environment'='prod')
              WHEN 'accepted' THEN bts.category='done'
              ELSE bts.category IN ('done','cancelled') END, false);
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION server_card_wait_task_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' OR NEW.deleted_at IS NOT NULL THEN
        UPDATE task_waits SET resolved_at=now(), resolution='invalid', resolve_cause='target_deleted'
        WHERE server_observed AND resolved_at IS NULL AND target_task_id=OLD.id;
    ELSE
        PERFORM resolve_server_card_waits(NEW.id);
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER zz_server_card_wait_target AFTER UPDATE OF status_id,is_shipped,deleted_at ON tasks
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_task_event();
-- Hard-delete must record the receipt before FK ON DELETE SET NULL erases its target.
CREATE TRIGGER zz_server_card_wait_target_delete BEFORE DELETE ON tasks
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_task_event();

-- +goose StatementBegin
CREATE FUNCTION server_card_wait_delete_graph_lock() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM completion_graph_lock(NULL);
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER server_card_wait_delete_graph BEFORE DELETE ON tasks
    FOR EACH STATEMENT EXECUTE FUNCTION server_card_wait_delete_graph_lock();

-- +goose StatementBegin
CREATE FUNCTION server_card_wait_status_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
    IF NEW.category IS DISTINCT FROM OLD.category THEN
        FOR target IN SELECT id FROM tasks WHERE status_id=NEW.id ORDER BY id LOOP
            PERFORM resolve_server_card_waits(target);
        END LOOP;
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER zz_server_card_wait_status AFTER UPDATE OF category ON task_statuses
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_status_event();

-- VCS writes also update task snapshots and may revoke dependency evidence.
-- Take graph then task locks BEFORE those triggers; run the wait resolver LAST.
-- +goose StatementBegin
CREATE FUNCTION server_card_wait_vcs_lock() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ids uuid[];
BEGIN
    PERFORM completion_graph_lock(NULL);
    IF TG_OP='INSERT' THEN ids:=ARRAY[NEW.task_id];
    ELSIF TG_OP='DELETE' THEN ids:=ARRAY[OLD.task_id];
    ELSE ids:=ARRAY[OLD.task_id,NEW.task_id]; END IF;
    PERFORM id FROM tasks WHERE id=ANY(ids) ORDER BY id FOR NO KEY UPDATE;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER aa_server_card_wait_vcs_lock BEFORE INSERT OR UPDATE OR DELETE ON vcs_links
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_vcs_lock();
-- +goose StatementBegin
CREATE FUNCTION server_card_wait_vcs_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP<>'INSERT' THEN PERFORM resolve_server_card_waits(OLD.task_id); END IF;
    IF TG_OP<>'DELETE' THEN PERFORM resolve_server_card_waits(NEW.task_id); END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER zz_server_card_wait_vcs AFTER INSERT OR UPDATE OR DELETE ON vcs_links
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_vcs_event();

-- Snapshot trigger must run before receipt updates (source -> wait, never the inverse).
-- +goose StatementBegin
CREATE FUNCTION server_card_wait_dependency_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        UPDATE task_waits SET resolved_at=now(), resolution='invalid', resolve_cause='dependency_removed'
        WHERE server_observed AND resolved_at IS NULL
          AND task_id=OLD.task_id AND target_task_id=OLD.depends_on_task_id;
    ELSE
        UPDATE task_waits SET resolved_at=now(), resolution='invalid', resolve_cause='dependency_changed'
        WHERE server_observed AND resolved_at IS NULL
          AND task_id=NEW.task_id AND target_task_id=NEW.depends_on_task_id
          AND (agent_id IS DISTINCT FROM NEW.wait_agent_id
               OR target_condition IS DISTINCT FROM NEW.condition OR NEW.dependency_type<>'blocks');
        PERFORM resolve_server_card_waits(NEW.depends_on_task_id);
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER zz_server_card_wait_dependency AFTER INSERT OR UPDATE OR DELETE ON task_dependencies
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_dependency_event();

-- +goose StatementBegin
CREATE FUNCTION server_card_wait_grant_lock() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.revoked_at IS NULL AND (TG_OP='DELETE' OR NEW.revoked_at IS NOT NULL) THEN
        -- Declaration takes shared membership gates before graph/source locks
        -- and reads grants by MVCC (no grant row lock), so this order is safe.
        PERFORM card_wait_membership_lock(OLD.agent_id,OLD.workspace_id,true);
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER aa_server_card_wait_grant_lock BEFORE UPDATE OF revoked_at OR DELETE ON agent_workspace_grants
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_grant_lock();

-- +goose StatementBegin
CREATE FUNCTION server_card_wait_grant_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.revoked_at IS NOT NULL OR card_wait_agent_member(OLD.agent_id,OLD.workspace_id) THEN
        RETURN NULL;
    END IF;
    PERFORM completion_graph_lock(NULL);
    -- Deleting a dependency updates the SOURCE task's snapshot. Prelock all
    -- sources, then delete edges, then lock/update waits; never wait -> source.
    PERFORM t.id FROM tasks t WHERE EXISTS (
        SELECT 1 FROM task_dependencies d
        JOIN tasks bt ON bt.id=d.depends_on_task_id
        JOIN projects sp ON sp.id=t.project_id JOIN projects bp ON bp.id=bt.project_id
        WHERE d.task_id=t.id AND d.wait_agent_id=OLD.agent_id
          AND (sp.workspace_id=OLD.workspace_id OR bp.workspace_id=OLD.workspace_id))
        ORDER BY t.id FOR NO KEY UPDATE;
    -- Mark the cause before the dependency trigger handles the removed edge:
    -- it must retain grant_revoked rather than dependency_removed. Sources are
    -- already locked and no later step can acquire a new source lock.
    UPDATE task_waits w SET resolved_at=now(), resolution='invalid', resolve_cause='grant_revoked'
    FROM tasks st JOIN projects sp ON sp.id=st.project_id,
         tasks bt JOIN projects bp ON bp.id=bt.project_id
    WHERE w.server_observed AND w.resolved_at IS NULL AND w.agent_id=OLD.agent_id
      AND st.id=w.task_id AND bt.id=w.target_task_id
      AND (sp.workspace_id=OLD.workspace_id OR bp.workspace_id=OLD.workspace_id);
    DELETE FROM task_dependencies d USING tasks st,projects sp,tasks bt,projects bp
    WHERE d.wait_agent_id=OLD.agent_id AND d.task_id=st.id AND st.project_id=sp.id
      AND d.depends_on_task_id=bt.id AND bt.project_id=bp.id
      AND (sp.workspace_id=OLD.workspace_id OR bp.workspace_id=OLD.workspace_id);
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER zz_server_card_wait_grant AFTER UPDATE OF revoked_at OR DELETE ON agent_workspace_grants
    FOR EACH ROW EXECUTE FUNCTION server_card_wait_grant_event();


-- +goose Down
DROP TRIGGER zz_server_card_wait_grant ON agent_workspace_grants;
DROP TRIGGER aa_server_card_wait_grant_lock ON agent_workspace_grants;
DROP TRIGGER zz_server_card_wait_dependency ON task_dependencies;
DROP TRIGGER zz_server_card_wait_vcs ON vcs_links;
DROP TRIGGER aa_server_card_wait_vcs_lock ON vcs_links;
DROP TRIGGER zz_server_card_wait_status ON task_statuses;
DROP TRIGGER zz_server_card_wait_target ON tasks;
DROP TRIGGER zz_server_card_wait_target_delete ON tasks;
DROP TRIGGER server_card_wait_delete_graph ON tasks;
DROP FUNCTION server_card_wait_grant_event();
DROP FUNCTION server_card_wait_grant_lock();
DROP FUNCTION server_card_wait_dependency_event();
DROP FUNCTION server_card_wait_vcs_event();
DROP FUNCTION server_card_wait_vcs_lock();
DROP FUNCTION server_card_wait_status_event();
DROP FUNCTION server_card_wait_task_event();
DROP FUNCTION server_card_wait_delete_graph_lock();
DROP FUNCTION resolve_server_card_waits(uuid);
-- Restore the original boundary without leaving unobservable cross waits.
UPDATE task_waits SET resolved_at=now(),resolution='invalid',resolve_cause='feature_rollback'
    WHERE server_observed AND resolved_at IS NULL;
DELETE FROM task_dependencies WHERE wait_agent_id IS NOT NULL;


-- +goose StatementBegin
CREATE OR REPLACE FUNCTION completion_graph_dependency_guard() RETURNS trigger
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION completion_graph_assert_edge(ws uuid, source uuid, target uuid,
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
CREATE OR REPLACE FUNCTION completion_graph_lock(ws uuid) RETURNS void
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

DROP FUNCTION card_wait_membership_lock(uuid,uuid,boolean);
DROP FUNCTION card_wait_agent_member(uuid,uuid);
ALTER TABLE task_dependencies DROP COLUMN wait_agent_id;
ALTER TABLE task_waits DROP COLUMN server_observed;
