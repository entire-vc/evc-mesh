-- +goose Up

-- History for the usage widget (plan §2.4): every agent session
-- with a model from the last 90 days becomes one journal row,
-- source='session_backfill'. Time = the session's started_at (sessions carry no
-- per-turn time), provider from the model, cost_reported = estimated_cost; no
-- pool, cache or duration. A measure the session has no known figure for stays
-- NULL. Sessions themselves are not touched. A session already in the journal
-- (re-run after a down) is skipped, and only the rows this run inserted are
-- added to the hours, so the step is safe to repeat. On such a re-run a session
-- may already have live 'turn' rows; the backfill row then carries only the
-- part of the session total those rows do not, so nothing counts twice.
CREATE TEMP TABLE agent_usage_backfilled (id UUID PRIMARY KEY) ON COMMIT DROP;

WITH ins AS (
INSERT INTO agent_usage_turns (
    workspace_id, agent_id, task_id, project_id, session_id, source,
    started_at, occurred_at, model, provider,
    tokens_in, tokens_out, cost_reported)
SELECT s.workspace_id, s.agent_id, s.task_id, t.project_id, s.id, 'session_backfill',
       s.started_at, s.started_at, s.model_used, agent_usage_provider(s.model_used),
       CASE WHEN s.tokens_in_known THEN s.tokens_in - live.tokens_in END,
       CASE WHEN s.tokens_out_known THEN s.tokens_out - live.tokens_out END,
       CASE WHEN s.cost_known THEN s.estimated_cost - live.cost_reported END
FROM agent_sessions s
LEFT JOIN tasks t ON t.id = s.task_id
CROSS JOIN LATERAL (
    SELECT COALESCE(sum(u.tokens_in), 0) AS tokens_in,
           COALESCE(sum(u.tokens_out), 0) AS tokens_out,
           COALESCE(sum(u.cost_reported), 0) AS cost_reported
    FROM agent_usage_turns u
    WHERE u.session_id = s.id AND u.source = 'turn') live
WHERE s.model_used IS NOT NULL AND s.model_used <> ''
  AND s.started_at >= now() - interval '90 days'
  AND NOT EXISTS (
      SELECT 1 FROM agent_usage_turns u
      WHERE u.session_id = s.id AND u.source = 'session_backfill')
RETURNING id)
INSERT INTO agent_usage_backfilled SELECT id FROM ins;

SELECT agent_usage_hourly_add(ARRAY(SELECT id FROM agent_usage_backfilled));

-- +goose Down
-- Remove the history rows and rebuild the hours from what stays in the journal.
-- The lock keeps a live report from adding onto an hour while it is rebuilt.
LOCK TABLE agent_usage_hourly IN SHARE ROW EXCLUSIVE MODE;
DELETE FROM agent_usage_turns WHERE source = 'session_backfill';
DELETE FROM agent_usage_hourly;
SELECT agent_usage_hourly_add(ARRAY(SELECT id FROM agent_usage_turns));
