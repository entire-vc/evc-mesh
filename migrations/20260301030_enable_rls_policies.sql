-- +goose Up

-- Row Level Security (RLS) policies for multi-tenant isolation.
--
-- Architecture:
--   The Go backend connects as the 'mesh' user, which is the table owner.
--   PostgreSQL table owners bypass RLS by default (unless FORCE ROW LEVEL SECURITY
--   is set). We deliberately do NOT use FORCE RLS, so the backend user is never
--   blocked. RLS policies apply as defense-in-depth when any other role connects
--   directly to the database.
--
--   The middleware in internal/middleware/workspace.go sets the session variable
--   app.current_workspace_id via SET on every request. Policies use
--   current_setting('app.current_workspace_id', true)::uuid to read it.
--   The second argument (true) makes current_setting return NULL instead of
--   raising an error when the variable is not set, so admin/migration connections
--   are never blocked.
--
-- Table grouping:
--   Group A: direct workspace_id column → simple equality check
--   Group B: project_id → projects.workspace_id → one-level subquery
--   Group C: task_id → tasks.project_id → projects.workspace_id → two-level subquery
--   Group D: join table / delivery table → scoped through parent
--
-- Tables intentionally excluded from RLS:
--   workspaces   — top-level tenant; applying RLS would be recursive
--   users        — cross-tenant identity table
--   refresh_tokens — user-scoped, no workspace column


-- ---------------------------------------------------------------------------
-- Helper: reusable inline function for reading the current workspace UUID.
-- Returns NULL (not an error) when the variable is unset.
-- ---------------------------------------------------------------------------

-- We avoid a permanent function to keep the migration self-contained.
-- Each policy uses current_setting() inline.


-- ===========================================================================
-- GROUP A: tables with a direct workspace_id column
-- ===========================================================================

-- workspace_members
ALTER TABLE workspace_members ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_workspace_members ON workspace_members
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- projects
ALTER TABLE projects ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_projects ON projects
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- agents
ALTER TABLE agents ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_agents ON agents
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- event_bus_messages
ALTER TABLE event_bus_messages ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_event_bus_messages ON event_bus_messages
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- activity_log
ALTER TABLE activity_log ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_activity_log ON activity_log
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- webhook_configs
ALTER TABLE webhook_configs ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_webhook_configs ON webhook_configs
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- initiatives
ALTER TABLE initiatives ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_initiatives ON initiatives
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- integration_configs
ALTER TABLE integration_configs ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_integration_configs ON integration_configs
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- rules
ALTER TABLE rules ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_rules ON rules
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- workspace_rules
ALTER TABLE workspace_rules ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_workspace_rules ON workspace_rules
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );

-- rule_violation_logs
ALTER TABLE rule_violation_logs ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_rule_violation_logs ON rule_violation_logs
    USING (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    )
    WITH CHECK (
        workspace_id = current_setting('app.current_workspace_id', true)::uuid
    );


-- ===========================================================================
-- GROUP B: tables scoped via project_id → projects.workspace_id
-- ===========================================================================

-- task_statuses
ALTER TABLE task_statuses ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_task_statuses ON task_statuses
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = task_statuses.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = task_statuses.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- custom_field_definitions
ALTER TABLE custom_field_definitions ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_custom_field_definitions ON custom_field_definitions
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = custom_field_definitions.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = custom_field_definitions.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- saved_views
ALTER TABLE saved_views ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_saved_views ON saved_views
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = saved_views.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = saved_views.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- project_updates
ALTER TABLE project_updates ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_project_updates ON project_updates
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = project_updates.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = project_updates.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- project_rules
ALTER TABLE project_rules ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_project_rules ON project_rules
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = project_rules.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = project_rules.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );


-- ===========================================================================
-- GROUP C: tables scoped via project_id → projects.workspace_id (tasks)
--          and via task_id → tasks.project_id → projects.workspace_id
-- ===========================================================================

-- tasks (project_id → projects.workspace_id, same as Group B but also anchor
-- for comments/artifacts/vcs_links/task_dependencies below)
ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_tasks ON tasks
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = tasks.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = tasks.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- comments (task_id → tasks → projects)
ALTER TABLE comments ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_comments ON comments
    USING (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = comments.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = comments.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- artifacts (task_id → tasks → projects)
ALTER TABLE artifacts ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_artifacts ON artifacts
    USING (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = artifacts.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = artifacts.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- vcs_links (task_id → tasks → projects)
ALTER TABLE vcs_links ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_vcs_links ON vcs_links
    USING (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = vcs_links.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = vcs_links.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );


-- ===========================================================================
-- GROUP D: join tables / delivery tables
-- ===========================================================================

-- task_dependencies — both task_id and depends_on_task_id must be in the same
-- workspace; checking task_id is sufficient because FK ensures both tasks exist
-- and the application always creates dependencies within one workspace.
ALTER TABLE task_dependencies ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_task_dependencies ON task_dependencies
    USING (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = task_dependencies.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM tasks t
            JOIN projects p ON p.id = t.project_id
            WHERE t.id = task_dependencies.task_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- webhook_deliveries (webhook_id → webhook_configs.workspace_id)
ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_webhook_deliveries ON webhook_deliveries
    USING (
        EXISTS (
            SELECT 1 FROM webhook_configs wc
            WHERE wc.id = webhook_deliveries.webhook_id
              AND wc.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM webhook_configs wc
            WHERE wc.id = webhook_deliveries.webhook_id
              AND wc.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- initiative_projects (initiative_id → initiatives.workspace_id)
ALTER TABLE initiative_projects ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_initiative_projects ON initiative_projects
    USING (
        EXISTS (
            SELECT 1 FROM initiatives i
            WHERE i.id = initiative_projects.initiative_id
              AND i.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM initiatives i
            WHERE i.id = initiative_projects.initiative_id
              AND i.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );


-- +goose Down

-- ===========================================================================
-- Drop all policies and disable RLS (reverse order, deepest dependencies first)
-- ===========================================================================

-- Group D
DROP POLICY IF EXISTS rls_initiative_projects ON initiative_projects;
ALTER TABLE initiative_projects DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_webhook_deliveries ON webhook_deliveries;
ALTER TABLE webhook_deliveries DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_task_dependencies ON task_dependencies;
ALTER TABLE task_dependencies DISABLE ROW LEVEL SECURITY;

-- Group C
DROP POLICY IF EXISTS rls_vcs_links ON vcs_links;
ALTER TABLE vcs_links DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_artifacts ON artifacts;
ALTER TABLE artifacts DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_comments ON comments;
ALTER TABLE comments DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_tasks ON tasks;
ALTER TABLE tasks DISABLE ROW LEVEL SECURITY;

-- Group B
DROP POLICY IF EXISTS rls_project_rules ON project_rules;
ALTER TABLE project_rules DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_project_updates ON project_updates;
ALTER TABLE project_updates DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_saved_views ON saved_views;
ALTER TABLE saved_views DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_custom_field_definitions ON custom_field_definitions;
ALTER TABLE custom_field_definitions DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_task_statuses ON task_statuses;
ALTER TABLE task_statuses DISABLE ROW LEVEL SECURITY;

-- Group A
DROP POLICY IF EXISTS rls_rule_violation_logs ON rule_violation_logs;
ALTER TABLE rule_violation_logs DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_workspace_rules ON workspace_rules;
ALTER TABLE workspace_rules DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_rules ON rules;
ALTER TABLE rules DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_integration_configs ON integration_configs;
ALTER TABLE integration_configs DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_initiatives ON initiatives;
ALTER TABLE initiatives DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_webhook_configs ON webhook_configs;
ALTER TABLE webhook_configs DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_activity_log ON activity_log;
ALTER TABLE activity_log DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_event_bus_messages ON event_bus_messages;
ALTER TABLE event_bus_messages DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_agents ON agents;
ALTER TABLE agents DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_projects ON projects;
ALTER TABLE projects DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS rls_workspace_members ON workspace_members;
ALTER TABLE workspace_members DISABLE ROW LEVEL SECURITY;
