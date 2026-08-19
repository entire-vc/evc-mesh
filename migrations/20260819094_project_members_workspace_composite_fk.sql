-- +goose Up

-- Schema-level tenancy guard for project_members, complementing the application-level
-- fix in PR #527 (task a0cf0c42): a member row today carries no proof that its
-- agent/user actually belongs to the project's workspace. migrations/20260305035
-- added agent_id with a bare `REFERENCES agents(id)` (no workspace predicate); the
-- original user_id FK has never had one either. This closes both, at the schema
-- layer, so the invariant holds even against direct SQL, backfills, or a future
-- service that writes this table outside internal/service. Task e3723e99.

ALTER TABLE project_members ADD COLUMN workspace_id UUID;

UPDATE project_members pm
SET workspace_id = p.workspace_id
FROM projects p
WHERE p.id = pm.project_id;

ALTER TABLE project_members ALTER COLUMN workspace_id SET NOT NULL;

-- Parent-side unique indexes required for the composite FKs below. Both projects.id
-- and agents.id are already unique alone (PK); Postgres still requires an explicit
-- unique index on the exact (id, workspace_id) pair to be usable as an FK target.
CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_id_workspace ON projects (id, workspace_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agents_id_workspace ON agents (id, workspace_id);

-- FK 1: workspace_id must always be the real workspace of project_id. Backfilled
-- from projects.workspace_id above, so this holds for every existing row by
-- construction — validated immediately below.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_pm_project_workspace') THEN
        ALTER TABLE project_members
            ADD CONSTRAINT fk_pm_project_workspace
            FOREIGN KEY (project_id, workspace_id)
            REFERENCES projects (id, workspace_id)
            NOT VALID;
    END IF;
END
$$;
-- +goose StatementEnd

-- FK 2: an agent member must belong to the same workspace as the project. This is
-- the gap 20260305035 left open. MATCH SIMPLE (Postgres default) means a row with
-- agent_id IS NULL (a user member) auto-satisfies this — untouched.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_pm_agent_workspace') THEN
        ALTER TABLE project_members
            ADD CONSTRAINT fk_pm_agent_workspace
            FOREIGN KEY (agent_id, workspace_id)
            REFERENCES agents (id, workspace_id)
            NOT VALID;
    END IF;
END
$$;
-- +goose StatementEnd

-- FK 3: a user member must actually be a member of the project's workspace via
-- workspace_members. Reuses the existing uq_workspace_members_user
-- (workspace_id, user_id) unique constraint from 20260224004 — no new index needed.
-- MATCH SIMPLE: a row with user_id IS NULL (an agent member) auto-satisfies this.
--
-- This is a THIRD constraint, one more than the two the originating task estimated
-- ("два констрейнта") — added because the one real cross-tenant row the 08.08 audit
-- found (task a0cf0c42 §5: test@entire.vc, owner of workspace 004aaab2, enrolled in
-- project_members of project c6e35032 "Mesh dev", workspace df814cd2) was a USER,
-- not an agent, and the agent-only pair (FK 1 + FK 2) would not have caught it.
-- FK 1 alone cannot either: workspace_id is backfilled FROM the project, so it is
-- consistent with the project by construction even when the enrolled principal
-- itself does not belong there. Only a workspace_members-anchored check closes that.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_pm_user_workspace_member') THEN
        ALTER TABLE project_members
            ADD CONSTRAINT fk_pm_user_workspace_member
            FOREIGN KEY (workspace_id, user_id)
            REFERENCES workspace_members (workspace_id, user_id)
            NOT VALID;
    END IF;
END
$$;
-- +goose StatementEnd

-- FK 1 and FK 2 validate clean today on both live instances (verified 2026-08-19
-- against mesh.entire.host via the mesh_read role: 0 project/workspace_id
-- mismatches by construction, 0 agent cross-workspace project_members rows —
-- matches the a0cf0c42 audit). Validate them now; new violations were already
-- rejected the moment the NOT VALID constraints landed above.
ALTER TABLE project_members VALIDATE CONSTRAINT fk_pm_project_workspace;
ALTER TABLE project_members VALIDATE CONSTRAINT fk_pm_agent_workspace;

-- FK 3 is deliberately left NOT VALID / unvalidated by this migration. The one
-- known offending row (test@entire.vc, see above) would fail VALIDATE today, and
-- its cleanup is Pavel's call, tracked on Mesh task #35a1bada (question 1) per the
-- a0cf0c42 agreement ("чистку решаю я"). New violating rows are already rejected —
-- NOT VALID still enforces on every INSERT/UPDATE from this point on, it only skips
-- the retroactive scan of pre-existing rows. VALIDATE CONSTRAINT
-- fk_pm_user_workspace_member is a one-line follow-up migration once that row is
-- resolved (deleted, or legitimized via a workspace_members row).

-- +goose Down

ALTER TABLE project_members DROP CONSTRAINT IF EXISTS fk_pm_user_workspace_member;
ALTER TABLE project_members DROP CONSTRAINT IF EXISTS fk_pm_agent_workspace;
ALTER TABLE project_members DROP CONSTRAINT IF EXISTS fk_pm_project_workspace;
DROP INDEX IF EXISTS uq_agents_id_workspace;
DROP INDEX IF EXISTS uq_projects_id_workspace;
ALTER TABLE project_members DROP COLUMN IF EXISTS workspace_id;
