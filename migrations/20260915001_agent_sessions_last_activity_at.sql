-- +goose Up
-- Task #872f82a2 (audit #3bb599fc, finding 1): agent_sessions.EndStale ends any
-- session with status='active' whose started_at is older than the sweep's
-- timeout (6h, cmd/api/main.go), regardless of whether the session has had
-- ANY activity since. That's a dispatcher-shaped assumption (one spawn = one
-- short-lived session, closed by the reaper right after it reports cost) that
-- does not hold for fiddler's persistent, multi-task, hours-to-days-long
-- lanes: a task still being actively worked past the 6h mark gets its session
-- force-ended mid-work by the age sweep, and the very next tool call (via
-- IncrementToolBreakdown's create-on-miss) opens a brand new, empty successor
-- session for the same task — which only receives model/tokens/estimated_cost
-- if a session-report happens to land on THAT successor before it, in turn,
-- ages past 6h. Every earlier session in the chain is left with real
-- tool_calls and model_used='' / estimated_cost=0 forever. This is one of two
-- confirmed contributors to the 90-98%-of-sessions-uncosted regression dated
-- to 2026-09-06 (commit 2bc2e1e9, when IncrementToolBreakdown started
-- creating/touching agent_sessions rows on every MCP tool call instead of
-- only at session-report time) — see the task comment for the other
-- (structural) contributor, which needs a fiddler.py-side fix instead.
--
-- Fix: track last_activity_at (bumped by IncrementToolBreakdown on every tool
-- call, and by session-report on every cost update) and switch EndStale to
-- filter on THAT instead of started_at, so a session stays open for as long
-- as it keeps seeing activity, however long that turns out to be.
ALTER TABLE agent_sessions
    ADD COLUMN IF NOT EXISTS last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Backfill: seed every currently-active session to "now" rather than its
-- (possibly hours-old) started_at, so flipping EndStale's filter over in the
-- same deploy doesn't immediately force-end every session the fleet has open
-- at rollout time — genuinely stale ones will still age out on the next sweep
-- if nothing touches them.
UPDATE agent_sessions SET last_activity_at = now() WHERE status = 'active';

-- Ended sessions are never re-examined by EndStale (it only matches
-- status='active'), so backfilling them to started_at is enough to keep the
-- column non-null without a second now()-stamped write.
UPDATE agent_sessions SET last_activity_at = started_at WHERE status != 'active';

CREATE INDEX IF NOT EXISTS idx_agent_sessions_active_last_activity
    ON agent_sessions (last_activity_at) WHERE status = 'active';

-- +goose Down
DROP INDEX IF EXISTS idx_agent_sessions_active_last_activity;
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS last_activity_at;
