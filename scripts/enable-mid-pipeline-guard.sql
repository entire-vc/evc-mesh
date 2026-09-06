-- Enable the mid-pipeline guard (mid_pipeline block) on Lab and Mesh dev.
-- Task: 1.1 Mesh-сервер: сторож середины конвейера — 80182d18
--
-- WHAT THIS TURNS ON (both default OFF in code, so this file is the only thing
-- that enables them anywhere):
--   review_evidence_strict — moving to review needs an artifact, a VCS link, a
--                            PASSING dod_check, or a comment carrying a URL.
--                            Without it, ANY comment satisfies the gate, which
--                            in practice means every card does.
--   auto_park_stalled      — an in_progress card with no checkout and no sign of
--                            work is parked in backlog with due_date=+24h and a
--                            kind:monitor label, instead of being dropped back
--                            into todo to be re-fed immediately.
--
-- PRE-FLIGHT — take the snapshot BEFORE writing, not after:
--   docker exec evc-mesh-postgres-1 psql -U mesh -d mesh -tAc \
--     "SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3'; \
--      SELECT project_id, config FROM project_rules WHERE rule_type='workflow';" \
--     > rollback/project-rules-workflow-$(date +%Y%m%d-%H%M%S).json
--
-- Run: docker exec -i evc-mesh-postgres-1 psql -U mesh -d mesh < scripts/enable-mid-pipeline-guard.sql
--
-- NOTE ON THE MERGE. This uses `config || jsonb_build_object(...)`, a top-level
-- merge that touches exactly the mid_pipeline key and leaves transitions,
-- policies and enforcement_mode untouched. It is deliberately NOT a rewrite of
-- the whole config from a copy held in this file: a hardcoded full config
-- silently reverts whatever anyone else has changed since the copy was made, and
-- the mesh-dev transition matrix has already been wiped once that way
-- (2026-08-21). Rewriting only the key you own is the safe shape.
--
-- ROLLBACK (removes the key; every flag then reads as off, which is the
-- pre-change behaviour). Note it leaves an empty `{}` config row on a project
-- that had no row before — behaviourally identical, since an empty transitions
-- map is allow-all, but not literally the same rows. Delete the row instead if
-- you want the table back byte-for-byte.
--   SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';
--   UPDATE project_rules SET config = config - 'mid_pipeline', updated_at = now()
--    WHERE rule_type = 'workflow'
--      AND project_id IN ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0',
--                         'c6e35032-36d5-4045-b30d-6cf9e35c3dee');

SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';

BEGIN;

-- Lab and Mesh dev. Other projects follow after a week of observation; adding one
-- is another row here, not a release.
INSERT INTO project_rules (project_id, rule_type, config, enforcement_mode)
VALUES
  ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0', 'workflow',
   '{"mid_pipeline":{"review_evidence_strict":true,"auto_park_stalled":true,"auto_park_due_hours":24}}',
   'advisory'),
  ('c6e35032-36d5-4045-b30d-6cf9e35c3dee', 'workflow',
   '{"mid_pipeline":{"review_evidence_strict":true,"auto_park_stalled":true,"auto_park_due_hours":24}}',
   'advisory')
ON CONFLICT (project_id, rule_type) DO UPDATE
  SET config = project_rules.config || EXCLUDED.config,
      updated_at = now();

-- Read back from the server rather than trusting the statement above: on this
-- table a write that "succeeded" and a write that stored something other than
-- what was intended look identical from the client side.
SELECT
  p.slug,
  pr.config -> 'mid_pipeline'                       AS mid_pipeline,
  (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(pr.config) k) AS all_top_level_keys
FROM project_rules pr
JOIN projects p ON p.id = pr.project_id
WHERE pr.rule_type = 'workflow'
  AND pr.project_id IN ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0',
                        'c6e35032-36d5-4045-b30d-6cf9e35c3dee');

COMMIT;
