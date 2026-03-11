-- +goose Up

-- Security hardening: RLS policies for tables added after the initial
-- enable_rls_policies migration (030), plus a unique index on
-- tasks(project_id, task_number) to prevent race-condition duplicates.
--
-- RLS architecture (same rules as migration 030):
--   The 'mesh' backend user owns the tables and bypasses RLS by default.
--   Policies are defense-in-depth for any direct DB connections.
--   current_setting('app.current_workspace_id', true) returns NULL (not an
--   error) when the variable is unset, so migration/admin connections are safe.
--
-- Group A: tables with a direct workspace_id column → equality check
--   - notification_preferences
--   - notifications
--   - recurring_schedules  (also has project_id, but workspace_id is simpler)
--
-- Group B: tables scoped via project_id → projects.workspace_id → subquery
--   - project_members
--   - task_templates
--   - auto_transition_rules


-- ===========================================================================
-- GROUP A: tables with a direct workspace_id column
-- ===========================================================================

-- notification_preferences
ALTER TABLE notification_preferences ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_notification_preferences ON notification_preferences
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- notifications
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_notifications ON notifications
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- recurring_schedules (has both workspace_id and project_id; workspace_id
-- is the authoritative tenant column and avoids a join)
ALTER TABLE recurring_schedules ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_recurring_schedules ON recurring_schedules
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );


-- ===========================================================================
-- GROUP B: tables scoped via project_id → projects.workspace_id
-- ===========================================================================

-- project_members
ALTER TABLE project_members ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_project_members ON project_members
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = project_members.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = project_members.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- task_templates
ALTER TABLE task_templates ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_task_templates ON task_templates
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = task_templates.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = task_templates.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- auto_transition_rules
ALTER TABLE auto_transition_rules ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_auto_transition_rules ON auto_transition_rules
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = auto_transition_rules.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = auto_transition_rules.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );


-- ===========================================================================
-- Unique index: prevent duplicate task_number within a project
--
-- task_number is assigned in application code. Under concurrent load two
-- requests can read the same max(task_number) before either commits, causing
-- duplicates. A partial unique index (excluding soft-deleted rows) is the
-- database-level guard against this race condition.
-- ===========================================================================

CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_project_task_number
    ON tasks (project_id, task_number)
    WHERE deleted_at IS NULL;


-- +goose Down

-- Remove unique index
DROP INDEX IF EXISTS idx_tasks_project_task_number;

-- Group B (reverse order)
DROP POLICY IF EXISTS rls_auto_transition_rules ON auto_transition_rules;
ALTER TABLE auto_transition_rules DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_task_templates ON task_templates;
ALTER TABLE task_templates DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_project_members ON project_members;
ALTER TABLE project_members DISABLE ROW LEVEL SECURITY;

-- Group A (reverse order)
DROP POLICY IF EXISTS rls_recurring_schedules ON recurring_schedules;
ALTER TABLE recurring_schedules DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_notifications ON notifications;
ALTER TABLE notifications DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_notification_preferences ON notification_preferences;
ALTER TABLE notification_preferences DISABLE ROW LEVEL SECURITY;
