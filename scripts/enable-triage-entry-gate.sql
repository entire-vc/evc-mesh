-- Enable the triage-entry gate (mid_pipeline.triage_entry_strict) on Lab and Mesh dev.
-- Task: 1.5 Mesh-сервер: triage-exit / triage-entry — 2495b694
--
-- WHAT THIS TURNS ON (default OFF in code, so this file is the only thing that
-- enables it anywhere):
--   triage_entry_strict   — a move into the triage status category is refused
--                            (422 TriageEntryError) unless the task's human_gate
--                            was authored by a human directly, or is hard-classed
--                            (the fail-closed default for "❓ Blocking @user").
--                            Without it, anything can be moved into triage,
--                            including the dispatcher's un-gated count==3
--                            auto-triage path — the exact hole this closes.
--   triage_park_due_hours — how far ahead enforceBlockingTriage's backlog
--                            fallback sets due_date when a disqualified
--                            (soft-classed, agent-authored) gate is parked
--                            instead of moved to triage. 0/absent = default 48.
--
-- ⚠️ NESTED MERGE, NOT A TOP-LEVEL ONE — READ BEFORE COPY-PASTING THIS PATTERN.
-- Both Lab and Mesh dev already carry a `mid_pipeline` object from
-- scripts/enable-mid-pipeline-guard.sql (task #80182d18):
--   {"review_evidence_strict":true,"auto_park_stalled":true,"auto_park_due_hours":24}
-- Mesh dev additionally carries a 7-entry `transitions` matrix and
-- `enforcement_mode` at the TOP level of `config`. A top-level
-- `config || jsonb_build_object('mid_pipeline', {...})` — the pattern the
-- 80182d18 script used, safe there because it was the FIRST write to this key —
-- would REPLACE `mid_pipeline` wholesale here, silently wiping
-- review_evidence_strict/auto_park_stalled/auto_park_due_hours. `transitions`
-- and `enforcement_mode` are untouched either way (different top-level keys),
-- but that would not have been true one level down. This script does a
-- jsonb_set that touches only the mid_pipeline sub-object, merged with `||` at
-- THAT level, so every project's other keys — top-level or inside
-- mid_pipeline — survive unedited. Verified against a live snapshot before
-- writing: mesh-dev's `transitions`/`enforcement_mode` and both projects'
-- pre-existing three mid_pipeline keys are still present after this runs
-- (see the read-back query at the bottom).
--
-- PRE-FLIGHT — take the snapshot BEFORE writing, not after:
--   ssh mesh-vm "docker exec evc-mesh-postgres-1 psql -U mesh -d mesh -tAc \
--     \"SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3'; \
--      SELECT project_id, config FROM project_rules WHERE rule_type='workflow' \
--      AND project_id IN ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0', \
--                         'c6e35032-36d5-4045-b30d-6cf9e35c3dee');\"" \
--     > rollback/project-rules-workflow-triage-entry-$(date +%Y%m%d-%H%M%S).json
--
-- Run: ssh mesh-vm "docker exec -i evc-mesh-postgres-1 psql -U mesh -d mesh" < scripts/enable-triage-entry-gate.sql
--
-- ROLLBACK (removes just the two new keys from mid_pipeline; every other
-- mid_pipeline key and every other top-level key is untouched; the flags
-- read back as off, which is the pre-change behaviour):
--   SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';
--   UPDATE project_rules
--   SET config = jsonb_set(
--         config, '{mid_pipeline}',
--         (config->'mid_pipeline') - 'triage_entry_strict' - 'triage_park_due_hours',
--         true
--       ),
--       updated_at = now()
--   WHERE rule_type = 'workflow'
--     AND project_id IN ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0',
--                        'c6e35032-36d5-4045-b30d-6cf9e35c3dee');

SET app.current_workspace_id = 'df814cd2-ca4b-47d6-9522-820e4eb47dc3';

BEGIN;

-- Lab and Mesh dev. Other projects follow after a week of observation; adding one
-- is another row here, not a release — same rollout shape as #80182d18.
INSERT INTO project_rules (project_id, rule_type, config, enforcement_mode)
VALUES
  ('e93b8e1a-ee44-4399-8e8a-0bdda460b4a0', 'workflow',
   '{"mid_pipeline":{"triage_entry_strict":true,"triage_park_due_hours":48}}',
   'advisory'),
  ('c6e35032-36d5-4045-b30d-6cf9e35c3dee', 'workflow',
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

-- Read back from the server rather than trusting the statement above: on this
-- table a write that "succeeded" and a write that stored something other than
-- what was intended look identical from the client side. The two counts at the
-- end are the actual regression check for the landmine above — 3 and 3 (or
-- more, never fewer) proves nothing upstream was clobbered.
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
