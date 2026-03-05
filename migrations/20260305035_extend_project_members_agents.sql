-- +goose Up

-- Make user_id nullable so project_members can hold agent memberships.
ALTER TABLE project_members ALTER COLUMN user_id DROP NOT NULL;

-- Add agent_id column for agent memberships.
ALTER TABLE project_members ADD COLUMN agent_id UUID REFERENCES agents(id) ON DELETE CASCADE;

-- Ensure exactly one of user_id or agent_id is set.
ALTER TABLE project_members ADD CONSTRAINT chk_pm_user_or_agent
    CHECK (
        (user_id IS NOT NULL AND agent_id IS NULL)
        OR (user_id IS NULL AND agent_id IS NOT NULL)
    );

-- Drop the old unique constraint that assumed user_id is always present.
ALTER TABLE project_members DROP CONSTRAINT IF EXISTS uq_project_members_user;

-- Partial unique indices: one for user members, one for agent members.
CREATE UNIQUE INDEX uq_pm_project_user ON project_members (project_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX uq_pm_project_agent ON project_members (project_id, agent_id) WHERE agent_id IS NOT NULL;

-- Index for agent_id lookups.
CREATE INDEX idx_pm_agent ON project_members (agent_id) WHERE agent_id IS NOT NULL;

-- Backfill: add workspace owners and admins to all existing projects.
INSERT INTO project_members (id, project_id, user_id, role, created_at, updated_at)
SELECT
    gen_random_uuid(),
    p.id,
    wm.user_id,
    (CASE wm.role WHEN 'owner' THEN 'admin' ELSE 'member' END)::project_role,
    NOW(),
    NOW()
FROM projects p
JOIN workspace_members wm ON wm.workspace_id = p.workspace_id
WHERE wm.role IN ('owner', 'admin')
  AND p.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM project_members pm
      WHERE pm.project_id = p.id AND pm.user_id = wm.user_id
  );

-- +goose Down

-- Remove backfilled rows (best effort — cannot distinguish backfilled from manually added).
DELETE FROM project_members WHERE agent_id IS NOT NULL;

DROP INDEX IF EXISTS idx_pm_agent;
DROP INDEX IF EXISTS uq_pm_project_agent;
DROP INDEX IF EXISTS uq_pm_project_user;

ALTER TABLE project_members DROP CONSTRAINT IF EXISTS chk_pm_user_or_agent;
ALTER TABLE project_members DROP COLUMN IF EXISTS agent_id;

-- Restore original unique constraint.
ALTER TABLE project_members ADD CONSTRAINT uq_project_members_user UNIQUE (project_id, user_id);
ALTER TABLE project_members ALTER COLUMN user_id SET NOT NULL;
