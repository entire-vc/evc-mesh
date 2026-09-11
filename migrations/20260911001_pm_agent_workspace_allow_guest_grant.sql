-- +goose Up

-- fk_pm_agent_workspace (migration 20260819094) enforces "an agent member's
-- workspace_id must equal agents.workspace_id" — correct when it landed, WRONG
-- now: task U1/U3 (agent_workspace_grants, migration 20260909001) added a
-- second, legitimate way for an agent to belong to a workspace other than the
-- one it started in (a "guest" grant), and a plain FOREIGN KEY has no way to
-- express that OR. Every guest-agent project invite (task #80dfb336's whole
-- point) hit this constraint with a bare foreign_key_violation
-- UNCONDITIONALLY, regardless of what internal/service.AddAgentMember decided
-- — the application-layer fix landing in the same change is necessary but not
-- sufficient; this migration is the other half. Found live, not by inspection:
-- the mock-backed unit tests for AddAgentMember passed with the FK still in
-- place (the mock has no schema), and only broke against a real Postgres.
--
-- Replace the static FK with a trigger expressing the actual invariant: home
-- OR active (non-revoked) grant. A trigger can express both the OR and the
-- "revoked_at IS NULL" clause that a FOREIGN KEY constraint structurally
-- cannot — the same shape as AgentIsInWorkspace (internal/middleware/workspace.go)
-- and AddAgentMember's own belongsAsGuest check, now also enforced against
-- direct SQL / a future backfill that bypasses the service layer entirely,
-- which is the exact protection 20260819094 was added for (task a0cf0c42) and
-- that this migration does not give up.

ALTER TABLE project_members DROP CONSTRAINT IF EXISTS fk_pm_agent_workspace;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION check_pm_agent_workspace() RETURNS trigger AS $$
BEGIN
    IF NEW.agent_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF EXISTS (
        SELECT 1 FROM agents
        WHERE id = NEW.agent_id AND workspace_id = NEW.workspace_id
    ) THEN
        RETURN NEW;
    END IF;
    IF EXISTS (
        SELECT 1 FROM agent_workspace_grants
        WHERE agent_id = NEW.agent_id
          AND workspace_id = NEW.workspace_id
          AND revoked_at IS NULL
    ) THEN
        RETURN NEW;
    END IF;
    -- Same SQLSTATE a real FK violation would raise, so the handler's
    -- existing pq.Error 23503 branch keeps producing the same
    -- "referenced entity not found" 400 for this now-rare
    -- direct-SQL-bypass case — no behavior change for that path, only for
    -- the legitimate guest-agent case this migration exists to unblock.
    RAISE EXCEPTION 'agent % does not belong to workspace % (no home match, no active grant)', NEW.agent_id, NEW.workspace_id
        USING ERRCODE = '23503';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_pm_agent_workspace ON project_members;
CREATE TRIGGER trg_pm_agent_workspace
    BEFORE INSERT OR UPDATE ON project_members
    FOR EACH ROW
    EXECUTE FUNCTION check_pm_agent_workspace();

-- +goose Down

DROP TRIGGER IF EXISTS trg_pm_agent_workspace ON project_members;
DROP FUNCTION IF EXISTS check_pm_agent_workspace();

-- Best-effort restore of the original FK. This FAILS TO VALIDATE if any
-- guest-agent (non-home) project_members row was created while the trigger
-- was active — which is not a bug in the down-migration, it is the down
-- migration correctly refusing to reinstate a constraint the live data
-- already violates. Cleaning up such rows (or leaving the trigger in place)
-- is a judgment call for whoever is rolling back, not something to guess here.
ALTER TABLE project_members
    ADD CONSTRAINT fk_pm_agent_workspace
    FOREIGN KEY (agent_id, workspace_id)
    REFERENCES agents (id, workspace_id);
