-- +goose Up

-- Memory read journal and daily tool-call counters (widget "Память Mesh",
-- Stage 1 unit U1). Both are written asynchronously in batches by the API
-- (middleware.MemoryReadLog, middleware.ToolBreakdownTracker), never on the
-- request path, and both are switched off together by MEMORY_READ_LOG=false.
--
-- memory_read_events: one row per memory read served over HTTP — recall
-- (/memories/search), recall-final (/memories/recall-final/v1), recall_graph,
-- get_memory (by key and by id), get_project_knowledge and
-- get_project_updates. results holds the served rows in rank order:
-- [{id, key, source, rank, score, superseded}]. Raw rows are kept 90 days;
-- query_text is cleared after 30 (the purge runs inside the writer).
-- No foreign keys on purpose: the journal must outlive a deleted agent, task
-- or memory, and an insert must never fail on a row that vanished between the
-- read and the flush.
CREATE TABLE memory_read_events (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ts               TIMESTAMPTZ NOT NULL DEFAULT now(),
    workspace_id     UUID NULL,
    project_id       UUID NULL,
    agent_id         UUID NULL,
    user_id          UUID NULL,
    session_id       TEXT NULL,
    task_id          UUID NULL,
    caller_kind      TEXT NOT NULL DEFAULT 'unknown'
        CHECK (caller_kind IN ('agent_tool', 'runner', 'script', 'ui', 'unknown')),
    route            TEXT NOT NULL,
    user_agent       TEXT NOT NULL DEFAULT '',
    op               TEXT NOT NULL,
    status_code      INT NOT NULL DEFAULT 200,
    query_hash       TEXT NULL,
    query_text       TEXT NULL,
    search_mode      TEXT NULL,
    result_count     INT NOT NULL DEFAULT 0,
    pinned_count     INT NOT NULL DEFAULT 0,
    superseded_count INT NOT NULL DEFAULT 0,
    top_score        DOUBLE PRECISION NULL,
    results          JSONB NOT NULL DEFAULT '[]'::jsonb,
    response_chars   INT NOT NULL DEFAULT 0,
    duration_ms      INT NOT NULL DEFAULT 0,
    error            TEXT NULL
);

CREATE INDEX idx_memory_read_events_ts ON memory_read_events (ts);
CREATE INDEX idx_memory_read_events_ws_ts ON memory_read_events (workspace_id, ts);
CREATE INDEX idx_memory_read_events_agent_ts ON memory_read_events (agent_id, ts) WHERE agent_id IS NOT NULL;

-- agent_tool_calls_daily: per-day call counts by agent and tool. Fixes the
-- single agent-wide agent_sessions.tool_breakdown row (no day window) and
-- separates get_task/get_task_context calls that actually returned comments
-- (tool = 'get_task+comments' / 'get_task_context+comments').
-- project_id and task-less calls share a row through the expression index:
-- NULL project_id is one bucket, not one row per flush.
CREATE TABLE agent_tool_calls_daily (
    day          DATE NOT NULL,
    workspace_id UUID NOT NULL,
    project_id   UUID NULL,
    agent_id     UUID NOT NULL,
    caller_kind  TEXT NOT NULL DEFAULT 'unknown'
        CHECK (caller_kind IN ('agent_tool', 'runner', 'script', 'ui', 'unknown')),
    tool         TEXT NOT NULL,
    calls        BIGINT NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_agent_tool_calls_daily_bucket ON agent_tool_calls_daily (
    day, workspace_id, (COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid)),
    agent_id, caller_kind, tool
);
CREATE INDEX idx_agent_tool_calls_daily_ws_day ON agent_tool_calls_daily (workspace_id, day);

-- +goose Down
DROP TABLE IF EXISTS agent_tool_calls_daily;
DROP TABLE IF EXISTS memory_read_events;
