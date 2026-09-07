-- Enable the triage-entry gate (mid_pipeline.triage_entry_strict) on wave 2:
-- Spark, Argus, Local Sync, Marketing, Content Marketing.
-- Task: 2.1a-prereq — aa2e3120
--
-- Wave 1 (Lab, Mesh dev) is scripts/enable-triage-entry-gate.sql (task 2495b694).
-- This is the deferred "other projects follow after a week of observation" step
-- that script's own header anticipated — brought forward because #2648d91c
-- (removing triage-drain.py) would otherwise strand orphan-triage cards on
-- every project that doesn't have this gate (incident class: 2026-06-21, 13 in
-- triage / 10 not on Pavel / 9 cards 99-137h stale before #aca99f88).
--
-- WHAT THIS TURNS ON — same two keys as wave 1, see that script's header for
-- the full explanation. Short version: a move into the `triage` status
-- category is refused (422 TriageEntryError) unless the task's human_gate was
-- authored by a human directly, or is hard-classed.
--
-- UNLIKE WAVE 1 — no existing `mid_pipeline` merge hazard here. Confirmed via a
-- live snapshot before writing (bob/rollback/project-rules-workflow-triage-
-- entry-wave2-*.json): none of these 5 projects has ANY row in `project_rules`
-- for rule_type='workflow' at all (config is NULL, not an empty object) — so
-- the ON CONFLICT branch's nested jsonb_set is defense-in-depth against a race,
-- not a real merge requirement today. Kept identical to wave 1's pattern
-- anyway: this file is exactly the kind of thing that gets copy-pasted for a
-- wave 3, and the version that's always safe is worth keeping over the version
-- that's only safe because of a fact checked once.
--
-- PRE-FLIGHT — snapshot already taken (see path above), BEFORE this write.
--
-- Run: ssh mesh-vm "docker exec -i evc-mesh-postgres-1 psql -U mesh -d mesh" < scripts/enable-triage-entry-gate-wave2.sql
--
-- ROLLBACK (removes just the two new keys from mid_pipeline; reads back off,
-- which is the pre-change behaviour — safe even though today mid_pipeline
-- would end up `{}` afterward, since there was nothing else in it to begin with):
--   SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';
--   UPDATE project_rules
--   SET config = jsonb_set(
--         config, '{mid_pipeline}',
--         (config->'mid_pipeline') - 'triage_entry_strict' - 'triage_park_due_hours',
--         true
--       ),
--       updated_at = now()
--   WHERE rule_type = 'workflow'
--     AND project_id IN ('ffb7d715-3716-4f57-b8d6-11c18057b00d',
--                        '17dbc243-4bc2-487d-82c1-2260244f25d0',
--                        '2d771317-5d34-4b79-ac4e-df22f9c06645',
--                        'b5ca3ae7-0a0e-4520-b228-c5560dd8feb0',
--                        '8673f589-efae-488a-9882-154680a6a9a2');

SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';

BEGIN;

INSERT INTO project_rules (project_id, rule_type, config, enforcement_mode)
VALUES
  ('ffb7d715-3716-4f57-b8d6-11c18057b00d', 'workflow',
   '{"mid_pipeline":{"triage_entry_strict":true,"triage_park_due_hours":48}}',
   'advisory'),
  ('17dbc243-4bc2-487d-82c1-2260244f25d0', 'workflow',
   '{"mid_pipeline":{"triage_entry_strict":true,"triage_park_due_hours":48}}',
   'advisory'),
  ('2d771317-5d34-4b79-ac4e-df22f9c06645', 'workflow',
   '{"mid_pipeline":{"triage_entry_strict":true,"triage_park_due_hours":48}}',
   'advisory'),
  ('b5ca3ae7-0a0e-4520-b228-c5560dd8feb0', 'workflow',
   '{"mid_pipeline":{"triage_entry_strict":true,"triage_park_due_hours":48}}',
   'advisory'),
  ('8673f589-efae-488a-9882-154680a6a9a2', 'workflow',
   '{"mid_pipeline":{"triage_entry_strict":true,"triage_park_due_hours":48}}',
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

-- Read back from the server rather than trusting the statement above (same
-- reasoning as wave 1) — five rows, five 2-key mid_pipeline objects, is the
-- actual regression check.
SELECT
  p.slug,
  pr.config -> 'mid_pipeline'                                          AS mid_pipeline,
  (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(pr.config) k)  AS all_top_level_keys,
  (SELECT count(*) FROM jsonb_object_keys(COALESCE(pr.config -> 'mid_pipeline', '{}'::jsonb))) AS mid_pipeline_key_count
FROM project_rules pr
JOIN projects p ON p.id = pr.project_id
WHERE pr.rule_type = 'workflow'
  AND pr.project_id IN ('ffb7d715-3716-4f57-b8d6-11c18057b00d',
                        '17dbc243-4bc2-487d-82c1-2260244f25d0',
                        '2d771317-5d34-4b79-ac4e-df22f9c06645',
                        'b5ca3ae7-0a0e-4520-b228-c5560dd8feb0',
                        '8673f589-efae-488a-9882-154680a6a9a2')
ORDER BY p.slug;

COMMIT;
