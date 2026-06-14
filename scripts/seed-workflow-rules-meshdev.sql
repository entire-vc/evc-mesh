-- Seed default permissive workflow rules for mesh-dev project (advisory mode).
-- Task: [R-D-D] Seed дефолтной матрицы mesh-dev — c033f6a3
--
-- Run: docker exec evc-mesh-postgres-1 psql -U mesh -d mesh -f /tmp/seed-workflow-rules-meshdev.sql
--
-- ROLLBACK (restores the pre-seed config):
-- SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';
-- UPDATE project_rules
--   SET config = '{"transitions":{"Todo -> In Progress":{"allowed":["agent:684bd684-10e6-4329-9875-04846a1845c0"]}},"enforcement_mode":"advisory"}',
--       enforcement_mode = 'advisory',
--       updated_at = now()
-- WHERE project_id = 'c6e35032-36d5-4045-b30d-6cf9e35c3dee' AND rule_type = 'workflow';

SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';

INSERT INTO project_rules (project_id, rule_type, config, enforcement_mode)
VALUES (
  'c6e35032-36d5-4045-b30d-6cf9e35c3dee',
  'workflow',
  '{
    "enforcement_mode": "advisory",
    "statuses": ["backlog","todo","triage","in_progress","review","reject","done"],
    "transitions": {
      "backlog":     {"allowed": ["todo","triage","in_progress","review","reject","done"]},
      "todo":        {"allowed": ["backlog","triage","in_progress","review","reject","done"]},
      "triage":      {"allowed": ["backlog","todo","in_progress","review","reject","done"]},
      "in_progress": {"allowed": ["backlog","todo","triage","review","reject","done"]},
      "review":      {"allowed": ["backlog","todo","triage","in_progress","reject","done"]},
      "reject":      {"allowed": ["backlog","todo","triage","in_progress","review","done"]},
      "done":        {"allowed": ["backlog","todo","triage","in_progress","review","reject"]}
    }
  }',
  'advisory'
)
ON CONFLICT (project_id, rule_type) DO UPDATE
  SET config           = EXCLUDED.config,
      enforcement_mode = EXCLUDED.enforcement_mode,
      updated_at       = now();
