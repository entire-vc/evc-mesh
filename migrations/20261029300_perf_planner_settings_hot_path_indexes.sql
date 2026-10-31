-- +goose NO TRANSACTION
-- +goose Up
-- Hot-path query fixes from the slow-query log.
--
-- 1. JIT off. The merged-MR close-on-merge sweep (vcs_link_repo) plans above
--    jit_above_cost, and on a 2-vCPU VM JIT compilation alone took
--    1.5s of a 1.9s run. Every OLTP query here is short; JIT never pays back.
-- 2. random_page_cost 1.1 (SSD). At the default 4 the planner seq-scanned
--    document_chunks for doc full-text recall instead of using the GIN index,
--    detoasting every search_vector (2.1s vs ~0.2s via the index).
-- Both are per-database defaults, so they reach new sessions only: the API
-- must reconnect (restart) to pick them up.
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('ALTER DATABASE %I SET jit = off', current_database());
    EXECUTE format('ALTER DATABASE %I SET random_page_cost = 1.1', current_database());
END
$$;
-- +goose StatementEnd

-- 3. The per-call tool_breakdown increment picks the agent's newest active
--    session with no task. idx_agent_sessions_agent_started is ascending on
--    (agent_id, started_at) without the status/task_id predicate, so the
--    planner walked idx_agent_sessions_started backward over ~88k rows.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_sessions_agent_active_untasked
    ON agent_sessions (agent_id, started_at DESC)
    WHERE status = 'active' AND task_id IS NULL;

-- 4. The close-on-merge sweep anti-joins activity_log on this one action; with
--    no index on action it was a parallel seq scan of the whole log per run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_activity_log_shadow_close_on_merge
    ON activity_log (entity_id)
    WHERE action = 'workflow.shadow_close_on_merge';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_activity_log_shadow_close_on_merge;
DROP INDEX CONCURRENTLY IF EXISTS idx_agent_sessions_agent_active_untasked;
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('ALTER DATABASE %I RESET jit', current_database());
    EXECUTE format('ALTER DATABASE %I RESET random_page_cost', current_database());
END
$$;
-- +goose StatementEnd
