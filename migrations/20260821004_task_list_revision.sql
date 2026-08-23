-- +goose Up

-- task_list_revision counter (ADR-0004, dev-docs/adrs/0004-task-list-revision-and-stale-cursor.md,
-- subtask #5/7 of ad22bfda). Schema + triggers only -- no handler/cursor-validation
-- code reads this yet (that's subtask #6), so this migration is safe to ship
-- standalone per §1b: additive, backward-compatible, nothing depends on it today.
--
-- One counter per project_id (ADR Decision 2 -- list_tasks is always scoped to
-- one project; a workspace-wide counter would make unrelated projects'
-- traffic invalidate each other's cursors within seconds). A missing row
-- (project created before this migration, or a race between the backfill
-- below and a brand-new project) reads as revision 0 -- the ON CONFLICT
-- upsert in bump_task_list_revision() makes the backfill a non-blocking
-- correctness net, not a hard prerequisite.
CREATE TABLE IF NOT EXISTS task_list_revisions (
    project_id  UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    revision    BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE task_list_revisions IS
    'Per-project counter bumped by trigger on every write that changes what '
    'list_tasks would return for that project (ADR-0004 Decision 1). Read by '
    'the list_tasks handler (subtask #6, not yet built) to stamp/validate the '
    '`list_revision` pagination field.';

-- Backfill one row per existing project at revision 0. Non-blocking: the
-- ON CONFLICT DO NOTHING means a project inserted concurrently with this
-- migration either gets picked up here or gets its row lazily on first bump
-- (bump_task_list_revision's own ON CONFLICT DO UPDATE handles that case).
INSERT INTO task_list_revisions (project_id, revision, updated_at)
SELECT id, 0, NOW() FROM projects
ON CONFLICT (project_id) DO NOTHING;

-- RLS, Group B pattern (project_id -> projects.workspace_id), matching every
-- other project-scoped table per migrations/20260301030_enable_rls_policies.sql.
-- Note: the app connects as the table owner, which bypasses RLS by default
-- (RLS is not FORCEd -- see that migration's own header comment) -- so this
-- is defense-in-depth for a direct non-owner connection, not something the
-- trigger functions below need to work around. Because the trigger bodies
-- run with the same role as the DML statement that fired them (no SECURITY
-- DEFINER), and that role is always the table owner in this deployment, the
-- ADR's open question ("do the triggers need elevated privilege to write
-- through RLS?") resolves to no -- verified by the negative-control test in
-- task_list_revision_repo_db_test.go actually writing through the trigger
-- path, not just by this reasoning.
ALTER TABLE task_list_revisions ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_task_list_revisions ON task_list_revisions
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = task_list_revisions.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = task_list_revisions.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- Shared bump primitive: every trigger function below resolves its own
-- project_id (directly from NEW/OLD on `tasks`, via a join to `tasks` from
-- `artifacts`/`vcs_links`) and calls this. ON CONFLICT DO UPDATE means a
-- missing row (see backfill note above) self-heals on first bump instead of
-- erroring.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_task_list_revision(p_project_id UUID)
RETURNS void AS $$
BEGIN
    INSERT INTO task_list_revisions (project_id, revision, updated_at)
    VALUES (p_project_id, 1, NOW())
    ON CONFLICT (project_id) DO UPDATE
        SET revision = task_list_revisions.revision + 1,
            updated_at = NOW();
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Covers ADR Decision 1 rows #1-#5, #7: task create, field update (title,
-- description, priority, assignee, reviewer, delegation_level, human_gate,
-- custom_fields, labels, ...), move (status_id change), soft-delete
-- (UPDATE ... SET deleted_at), and subtask create/update (same INSERT/UPDATE
-- shape, same project -- see ADR row #7 for why no separate hook is needed).
-- Deliberately does NOT fire on comments (ADR row #12 -- comment writes never
-- touch `tasks`, and no list_tasks row field is comment-derived today) or on
-- label rename (ADR row #6 -- no such operation exists in the codebase; if
-- one is ever added it will be an UPDATE tasks over many rows and is already
-- covered by this same trigger with no new code).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_bump_task_list_revision_from_tasks()
RETURNS trigger AS $$
BEGIN
    PERFORM bump_task_list_revision(NEW.project_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_tasks_bump_task_list_revision
    AFTER INSERT OR UPDATE ON tasks
    FOR EACH ROW
    EXECUTE FUNCTION trg_bump_task_list_revision_from_tasks();

-- Covers ADR Decision 1 row #8-#9: artifact create/delete bumps
-- artifact_count on the owning task's list row without writing to `tasks`
-- itself, so it needs its own trigger + join to resolve project_id.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_bump_task_list_revision_from_artifacts()
RETURNS trigger AS $$
DECLARE
    v_project_id UUID;
BEGIN
    SELECT project_id INTO v_project_id
    FROM tasks
    WHERE id = COALESCE(NEW.task_id, OLD.task_id);

    IF v_project_id IS NOT NULL THEN
        PERFORM bump_task_list_revision(v_project_id);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_artifacts_bump_task_list_revision
    AFTER INSERT OR DELETE ON artifacts
    FOR EACH ROW
    EXECUTE FUNCTION trg_bump_task_list_revision_from_artifacts();

-- Covers ADR Decision 1 row #11: vcs_link create/delete bumps
-- vcs_link_count the same way artifacts bumps artifact_count.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION trg_bump_task_list_revision_from_vcs_links()
RETURNS trigger AS $$
DECLARE
    v_project_id UUID;
BEGIN
    SELECT project_id INTO v_project_id
    FROM tasks
    WHERE id = COALESCE(NEW.task_id, OLD.task_id);

    IF v_project_id IS NOT NULL THEN
        PERFORM bump_task_list_revision(v_project_id);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_vcs_links_bump_task_list_revision
    AFTER INSERT OR DELETE ON vcs_links
    FOR EACH ROW
    EXECUTE FUNCTION trg_bump_task_list_revision_from_vcs_links();

-- +goose Down

DROP TRIGGER IF EXISTS trg_vcs_links_bump_task_list_revision ON vcs_links;
DROP FUNCTION IF EXISTS trg_bump_task_list_revision_from_vcs_links();

DROP TRIGGER IF EXISTS trg_artifacts_bump_task_list_revision ON artifacts;
DROP FUNCTION IF EXISTS trg_bump_task_list_revision_from_artifacts();

DROP TRIGGER IF EXISTS trg_tasks_bump_task_list_revision ON tasks;
DROP FUNCTION IF EXISTS trg_bump_task_list_revision_from_tasks();

DROP FUNCTION IF EXISTS bump_task_list_revision(UUID);

DROP POLICY IF EXISTS rls_task_list_revisions ON task_list_revisions;
ALTER TABLE IF EXISTS task_list_revisions DISABLE ROW LEVEL SECURITY;

DROP TABLE IF EXISTS task_list_revisions;
