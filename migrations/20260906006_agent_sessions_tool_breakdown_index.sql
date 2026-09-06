-- +goose Up
-- Task #ce1bc187 (audit §1.6): agent_sessions.tool_breakdown has been a column since
-- 20260315042_create_agent_sessions.sql, but nothing has ever written to it — every one
-- of the 5,595 sessions on record shows it empty. Every R:W (recall-vs-remember), read-
-- before-action, and compliance measurement that wants "which tools did this session
-- call" has had to be reconstructed from Mac Mini transcripts, which are not retained
-- forever — losing them loses the history these measurements need.
--
-- This migration adds no new columns: tool_breakdown (JSONB) and tool_calls (INT) already
-- exist. What's missing is an index shaped for the query this fixes -- "R:W by agent over
-- the last N days" -- which filters agent_sessions by started_at and groups by agent_id:
--
--   SELECT agent_id,
--          SUM((tool_breakdown->>'recall')::bigint)   AS reads,
--          SUM((tool_breakdown->>'remember')::bigint) AS writes
--   FROM agent_sessions
--   WHERE started_at >= now() - interval '7 days'
--   GROUP BY agent_id;
--
-- 20260315042 already has single-column indexes on agent_id and on started_at
-- separately (idx_agent_sessions_agent, idx_agent_sessions_started), but the planner can
-- only use one of the two efficiently for a combined filter+group-by like the query
-- above -- a composite (agent_id, started_at) index lets it satisfy both in one scan.
-- The write path itself (internal/repository/postgres/session_repo.go
-- IncrementToolBreakdown) is a separate, application-level change; this migration is
-- only the index that makes reading the result back cheap.
CREATE INDEX IF NOT EXISTS idx_agent_sessions_agent_started
    ON agent_sessions (agent_id, started_at);

-- +goose Down
DROP INDEX IF EXISTS idx_agent_sessions_agent_started;
