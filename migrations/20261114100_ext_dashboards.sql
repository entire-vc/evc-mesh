-- +goose Up

-- Dashboards extension (id "dashboards", PRD Dashboards §6.1). Additive only:
-- three new tables, nothing in the core schema changes.
--
-- Personal and shared dashboards (shared ones are designed in from the start)
-- live in one table:
--  * a personal dashboard has owner_user_id = its only reader and writer;
--  * a shared dashboard has owner_user_id NULL and is readable by every member
--    of the workspace; author_user_id remembers who made it.
-- Where a dashboard sits in a person's menu (group, order, hidden) is per
-- person, so it lives in ext_dashboards_placements, not on the dashboard.
--
-- Leaving the workspace is the cleanup trigger, not only deleting the user:
-- owner_user_id, author_user_id and placements.user_id reference the
-- membership row (workspace_members (workspace_id, user_id)), so removing the
-- member deletes their personal dashboards and placements (CASCADE) and keeps
-- their shared dashboards with author_user_id NULL (SET NULL on that column
-- only; the column list needs PostgreSQL 15+, CI and prod run 16).

CREATE TABLE ext_dashboards_dashboards (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    owner_user_id    UUID,
    author_user_id   UUID,
    scope            TEXT NOT NULL CHECK (scope IN ('personal', 'workspace')),
    members_can_edit BOOLEAN NOT NULL DEFAULT false,
    name             VARCHAR(60) NOT NULL CHECK (char_length(btrim(name)) >= 1),
    -- one emoji grapheme, icon:<name> or '' (domain.ValidateIconValue, §5.11)
    icon             VARCHAR(50) NOT NULL DEFAULT '' CHECK (octet_length(icon) <= 64),
    -- where a shared dashboard lands for members without their own placement
    default_placement TEXT NOT NULL DEFAULT 'bottom' CHECK (default_placement IN ('top', 'bottom')),
    -- saved from the virtual default dashboard (§5.3); "reset" applies only to it
    is_default       BOOLEAN NOT NULL DEFAULT false,
    template_id      TEXT,
    -- optimistic lock of the layout
    version          INT NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ext_dashboards_dashboards_ws UNIQUE (id, workspace_id),
    CONSTRAINT ck_ext_dashboards_dashboards_owner CHECK ((scope = 'workspace') = (owner_user_id IS NULL)),
    CONSTRAINT ck_ext_dashboards_dashboards_members_can_edit CHECK (scope = 'workspace' OR NOT members_can_edit),
    CONSTRAINT fk_ext_dashboards_dashboards_owner FOREIGN KEY (workspace_id, owner_user_id)
        REFERENCES workspace_members (workspace_id, user_id) ON DELETE CASCADE,
    CONSTRAINT fk_ext_dashboards_dashboards_author FOREIGN KEY (workspace_id, author_user_id)
        REFERENCES workspace_members (workspace_id, user_id) ON DELETE SET NULL (author_user_id)
);
CREATE INDEX ext_dashboards_dashboards_owner ON ext_dashboards_dashboards (workspace_id, owner_user_id);
CREATE INDEX ext_dashboards_dashboards_scope ON ext_dashboards_dashboards (workspace_id, scope);

CREATE TABLE ext_dashboards_placements (
    dashboard_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    user_id      UUID NOT NULL,
    placement    TEXT NOT NULL CHECK (placement IN ('top', 'bottom')),
    position     INT NOT NULL,
    hidden       BOOLEAN NOT NULL DEFAULT false,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (dashboard_id, user_id),
    CONSTRAINT fk_ext_dashboards_placements_dashboard FOREIGN KEY (dashboard_id, workspace_id)
        REFERENCES ext_dashboards_dashboards (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT fk_ext_dashboards_placements_member FOREIGN KEY (workspace_id, user_id)
        REFERENCES workspace_members (workspace_id, user_id) ON DELETE CASCADE
);
CREATE INDEX ext_dashboards_placements_user ON ext_dashboards_placements (workspace_id, user_id, placement, position);

CREATE TABLE ext_dashboards_widgets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dashboard_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    -- no foreign key: an unknown or vanished type shows a placeholder (§7.2)
    widget_type  TEXT NOT NULL CHECK (char_length(widget_type) BETWEEN 1 AND 128),
    x            INT NOT NULL CHECK (x >= 0),
    y            INT NOT NULL CHECK (y >= 0),
    w            INT NOT NULL CHECK (w BETWEEN 1 AND 12),
    h            INT NOT NULL CHECK (h BETWEEN 1 AND 12),
    settings     JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(settings) = 'object'),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_ext_dashboards_widgets_grid CHECK (x + w <= 12),
    CONSTRAINT fk_ext_dashboards_widgets_dashboard FOREIGN KEY (dashboard_id, workspace_id)
        REFERENCES ext_dashboards_dashboards (id, workspace_id) ON DELETE CASCADE
);
CREATE INDEX ext_dashboards_widgets_dashboard ON ext_dashboards_widgets (dashboard_id);

-- Tenant isolation, same shape as the agent-chat tables.
ALTER TABLE ext_dashboards_dashboards ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_dashboards_dashboards ON ext_dashboards_dashboards
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE ext_dashboards_placements ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_dashboards_placements ON ext_dashboards_placements
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE ext_dashboards_widgets ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_dashboards_widgets ON ext_dashboards_widgets
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP TABLE IF EXISTS ext_dashboards_widgets;
DROP TABLE IF EXISTS ext_dashboards_placements;
DROP TABLE IF EXISTS ext_dashboards_dashboards;
