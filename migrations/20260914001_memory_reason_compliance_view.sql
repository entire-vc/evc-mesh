-- +goose Up

-- #40cc098e (follow-up to #620bd93a): two measurements three weeks apart found
-- fleet-wide `reason` compliance plateaued at 38-64%, nowhere near a bar that
-- would let MESH_MEMORY_REQUIRE_REASON flip to enforce without breaking most
-- of the fleet's remember() calls. Both measurements were done with ad-hoc SQL
-- typed fresh each time (Khan, 2026-08-21 and 2026-09-14) — this view is that
-- query, kept so the next check doesn't have to be re-derived, and so it can't
-- silently drift from what Khan actually ran.
--
-- The raw query without a workspace filter is misleading: 93% of
-- memory_revisions rows in the 2026-08-21 window belonged to `lme-bench-runner`
-- (the memory-bench CI load generator, .github/workflows/memory-bench.yml),
-- almost all with workspace_id NULL. Filtering those out is not optional
-- cleanup, it is the difference between "3%" and "45%" for the exact same
-- 12-hour window.
--
-- Deliberately a view, not a materialized one: memory_revisions is small
-- enough (tens of thousands of rows) that re-aggregating on every read costs
-- nothing worth caching, and a materialized view here would add a refresh
-- schedule nobody asked for and a second way for this number to go stale.

CREATE VIEW memory_reason_compliance_by_agent_daily AS
SELECT
    date_trunc('day', mr.created_at)                                          AS day,
    mr.actor_agent_id,
    a.name                                                                    AS agent_name,
    a.slug                                                                    AS agent_slug,
    count(*)                                                                  AS total_writes,
    count(*) FILTER (WHERE mr.reason IS NOT NULL)                             AS writes_with_reason,
    round(
        100.0 * count(*) FILTER (WHERE mr.reason IS NOT NULL) / count(*), 1
    )                                                                         AS pct_with_reason
FROM memory_revisions mr
LEFT JOIN agents a ON a.id = mr.actor_agent_id
LEFT JOIN workspaces w ON w.id = mr.workspace_id
WHERE mr.action IN ('created', 'updated')
    -- Excludes exactly the noise class Khan found: memory-bench's load
    -- generator writes with no workspace attached at all. A real remember()
    -- call always has a workspace_id (Remember() rejects uuid.Nil), so this
    -- is not filtering out any legitimate fleet write, past or future.
    AND mr.workspace_id IS NOT NULL
    -- Belt-and-suspenders against the other bench path: a load generator
    -- pointed at the dedicated lme-bench workspace (is_bench, see
    -- 20260906001) instead of leaving workspace_id NULL would otherwise slip
    -- through the filter above and re-introduce the exact same skew.
    AND COALESCE(w.is_bench, FALSE) = FALSE
GROUP BY 1, 2, 3, 4;

COMMENT ON VIEW memory_reason_compliance_by_agent_daily IS
    'Per-agent, per-day remember()/set_project_knowledge() reason-compliance, '
    'matching the methodology in Mesh #620bd93a / #40cc098e: excludes '
    'lme-bench-runner noise (workspace_id NULL or is_bench workspaces) so the '
    'percentage reflects real fleet activity. Query directly, e.g.: '
    'SELECT * FROM memory_reason_compliance_by_agent_daily '
    'WHERE day >= now() - interval ''7 days'' ORDER BY day DESC, pct_with_reason ASC;';

-- Supports the view's grouping + any ad-hoc "last N days per agent" query
-- without a sequential scan of the whole table. Partial on the same predicate
-- as ix_memory_revisions_missing_reason's WHERE clause would have narrowed
-- this further, but the view also needs compliant rows, so this stays
-- unfiltered.
CREATE INDEX ix_memory_revisions_actor_created ON memory_revisions (actor_agent_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS ix_memory_revisions_actor_created;
DROP VIEW IF EXISTS memory_reason_compliance_by_agent_daily;
