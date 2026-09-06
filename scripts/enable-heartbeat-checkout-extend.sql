-- Enable heartbeat-extends-checkout (mid_pipeline.heartbeat_extends_checkout) on
-- Lab and Mesh dev.
-- Task: 1.8 Mesh-сервер: чекаут-лиз продлевается heartbeat'ом автоматически — 0f2528ec
--
-- WHAT THIS TURNS ON (default OFF in code, so this file is the only thing that
-- enables it anywhere):
--   heartbeat_extends_checkout      — a live agent heartbeat pushes checkout_expires
--                                     forward for every checkout that agent holds in
--                                     this project, instead of leaving expiry purely
--                                     TTL-based (audit §1.2: 98% of checkouts were
--                                     being reclaimed by the system, not released by
--                                     the agent that finished with them).
--   heartbeat_checkout_extend_minutes — how far ahead each heartbeat pushes
--                                        checkout_expires. 0/absent = default 30.
--
-- ⚠️ NESTED MERGE, NOT A TOP-LEVEL ONE — same landmine as
-- scripts/enable-triage-entry-gate.sql (task #2495b694), read that file's header
-- before touching this pattern. Both Lab and Mesh dev already carry a
-- `mid_pipeline` object with several keys from the two prior enablement scripts
-- (review_evidence_strict/auto_park_stalled/auto_park_due_hours from #80182d18,
-- triage_entry_strict/triage_park_due_hours from #2495b694); Mesh dev additionally
-- carries `transitions`/`enforcement_mode` at the top level of `config`. A
-- top-level `config || jsonb_build_object('mid_pipeline', {...})` would REPLACE
-- `mid_pipeline` wholesale, silently wiping every key the two prior scripts set.
-- This script does a jsonb_set that touches only the mid_pipeline sub-object,
-- merged with `||` at THAT level, so every other key — top-level or inside
-- mid_pipeline — survives. Verified against a live snapshot + dry run
-- (BEGIN...ROLLBACK) before running for real; see the read-back query at the
-- bottom for the actual key-count regression check.
--
-- PRE-FLIGHT — take the snapshot BEFORE writing, not after:
--   ssh mesh-vm "docker exec evc-mesh-postgres-1 psql -U mesh -d mesh -tAc \
--     \"SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3'; \
--      SELECT project_id, config FROM project_rules WHERE rule_type='workflow' \
--      AND project_id IN ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0', \
--                         'c6e35032-36d5-4045-b30d-6cf9e35c3dee');\"" \
--     > rollback/project-rules-workflow-heartbeat-extend-$(date +%Y%m%d-%H%M%S).json
--
-- Run: ssh mesh-vm "docker exec -i evc-mesh-postgres-1 psql -U mesh -d mesh" < scripts/enable-heartbeat-checkout-extend.sql
--
-- DRY RUN FIRST (sed the trailing COMMIT to ROLLBACK and pipe that instead) —
-- confirm the read-back shows every pre-existing mid_pipeline key still present
-- before running the real COMMIT version.
--
-- ROLLBACK (removes just the two new keys from mid_pipeline; every other
-- mid_pipeline key and every other top-level key is untouched; the flag reads
-- back as off, which is the pre-change behaviour):
--   SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';
--   UPDATE project_rules
--   SET config = jsonb_set(
--         config, '{mid_pipeline}',
--         (config->'mid_pipeline') - 'heartbeat_extends_checkout' - 'heartbeat_checkout_extend_minutes',
--         true
--       ),
--       updated_at = now()
--   WHERE rule_type = 'workflow'
--     AND project_id IN ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0',
--                        'c6e35032-36d5-4045-b30d-6cf9e35c3dee');

SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';

BEGIN;

-- Lab and Mesh dev. Other projects follow after a week of observation; adding one
-- is another row here, not a release — same rollout shape as #80182d18/#2495b694.
INSERT INTO project_rules (project_id, rule_type, config, enforcement_mode)
VALUES
  ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0', 'workflow',
   '{"mid_pipeline":{"heartbeat_extends_checkout":true,"heartbeat_checkout_extend_minutes":30}}',
   'advisory'),
  ('c6e35032-36d5-4045-b30d-6cf9e35c3dee', 'workflow',
   '{"mid_pipeline":{"heartbeat_extends_checkout":true,"heartbeat_checkout_extend_minutes":30}}',
   'advisory')
ON CONFLICT (project_id, rule_type) DO UPDATE
  SET config = jsonb_set(
        project_rules.config,
        '{mid_pipeline}',
        COALESCE(project_rules.config -> 'mid_pipeline', '{}'::jsonb)
          || (EXCLUDED.config -> 'mid_pipeline'),
        true
      ),
      updated_at = now();

-- Read back from the server rather than trusting the statement above. The key
-- count is the actual regression check for the landmine above: it must equal
-- (however many keys were already there) + 2, never fewer.
SELECT
  p.slug,
  pr.config -> 'mid_pipeline'                                          AS mid_pipeline,
  (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(pr.config) k)  AS all_top_level_keys,
  (SELECT count(*) FROM jsonb_object_keys(COALESCE(pr.config -> 'mid_pipeline', '{}'::jsonb))) AS mid_pipeline_key_count
FROM project_rules pr
JOIN projects p ON p.id = pr.project_id
WHERE pr.rule_type = 'workflow'
  AND pr.project_id IN ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0',
                        'c6e35032-36d5-4045-b30d-6cf9e35c3dee');

COMMIT;
