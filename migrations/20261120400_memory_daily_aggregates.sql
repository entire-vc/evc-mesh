-- +goose Up

-- Daily memory aggregates (widget "Память Mesh", Stage 1 unit U4, plan doc
-- §3 formulas, §5 aggregate model v11). The widget's main endpoint reads only
-- these and, for the open day, memory_stats_rollup over the raw journal: it
-- never scans memory_read_events for a period longer than a day.
--
-- All days are UTC days: (ts AT TIME ZONE 'UTC')::date.
--
-- Rollback: revert the MR and run this file's Down. The U1 journal and the U3
-- usage rows are not touched; the aggregates are recomputed from them anew
-- (memory_base_daily is a snapshot and only covers days written after that).

-- Bounds of the mesh_memory_recall_top_score Prometheus histogram
-- (pkg/metrics memoryRecallTopScoreBuckets; a DB test keeps them equal).
-- +goose StatementBegin
CREATE FUNCTION memory_top_score_bounds() RETURNS double precision[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ARRAY[0.001, 0.002, 0.003, 0.004, 0.005,
                 0.008, 0.010, 0.011, 0.012, 0.013,
                 0.014, 0.015, 0.016, 0.017, 0.020, 0.030]::double precision[]
$$;
-- +goose StatementEnd

-- 1-based index of score in a NON-cumulative histogram over the bounds above:
-- index i holds bounds[i-1] < score <= bounds[i] (Prometheus `le`); the last
-- index (17) holds scores above the top bound.
-- +goose StatementBegin
CREATE FUNCTION memory_top_score_bucket(score double precision) RETURNS int
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT 1 + count(*)::int FROM unnest(memory_top_score_bounds()) AS b WHERE b < score
$$;
-- +goose StatementEnd

-- A served row is pinned when it carries the injected sentinel score 2.0
-- (plan §3 pinned(r); the same discriminator mesh_memory_recall_top_score
-- uses). Pinned rows are not retrieval: they count neither for hits nor for
-- served_rows, served_superseded or the top score.
-- +goose StatementBegin
CREATE FUNCTION memory_read_row_pinned(r jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE((r->>'score')::double precision >= 2.0, false)
$$;
-- +goose StatementEnd

-- memory_stats_daily: one row per (day, workspace, project, agent, caller_kind,
-- op, source). Written by the nightly job of mesh-api for closed days
-- (memory_daily_rollups marks which); the open day is read live from
-- memory_stats_rollup, the same formula.
--
-- op is the analytics op of plan §3, not the raw journal op:
--   recall, recall-final -> recall; recall_graph -> recall_with_graph;
--   get_memory, get_project_knowledge, get_project_updates as journaled.
--
-- Counter contract (doc §5 v11), so that SUM of any counter over all rows
-- never counts a call twice:
--   * recall ops: the call counters (reads, errors, hits, useful, misses,
--     empty_repeats, response_chars, top_score_hist) are on the row with
--     source='*', whose served_* are 0; served_rows / served_superseded are on
--     one row per source of the served rows (memory | project_knowledge | doc,
--     a row without a source -> unknown), whose call counters are 0. An empty
--     or failed recall has only its '*' row.
--   * other ops: one row per call, source by op: get_memory -> memory,
--     get_project_knowledge -> project_knowledge, get_project_updates ->
--     project_update; served_* of that call are on the same row.
--   * reads counts every call; errors the calls with error IS NOT NULL. All
--     other counters are over error-free calls only (R = reads - errors).
--   * hits: calls with at least one non-pinned row.
--   * useful: recall hits with at least one memory_read_usage row (U3),
--     bucketed by the read's own day.
--   * misses: filled by U7; until then 0 with misses_reason='not_collected'
--     (NULL once collected).
--   * empty_repeats: empty recall calls (no non-pinned row) whose agent got an
--     empty answer to the same query_hash in the 7 days before. Backward only,
--     so a closed day never changes.
--   * served_rows / served_superseded: non-pinned served rows, and those of
--     them with superseded=true. served_noise = served_superseded/served_rows
--     ("шум выдачи"), NULL when nothing was served.
--   * top_score_hist: 17 non-cumulative buckets (memory_top_score_bucket) of
--     the best non-pinned score of each error-free recall call; an empty or
--     pinned-only page observes 0 into bucket 1, as the Prometheus metric.
-- Grouping keys stay nullable (the raw journal may lack a workspace, project
-- or agent); the expression index makes NULL one bucket.
CREATE TABLE memory_stats_daily (
    day               DATE NOT NULL,
    workspace_id      UUID NULL,
    project_id        UUID NULL,
    agent_id          UUID NULL,
    caller_kind       TEXT NOT NULL,
    op                TEXT NOT NULL,
    source            TEXT NOT NULL,
    reads             BIGINT NOT NULL DEFAULT 0,
    errors            BIGINT NOT NULL DEFAULT 0,
    hits              BIGINT NOT NULL DEFAULT 0,
    useful            BIGINT NOT NULL DEFAULT 0,
    misses            BIGINT NOT NULL DEFAULT 0,
    misses_reason     TEXT NULL DEFAULT 'not_collected',
    empty_repeats     BIGINT NOT NULL DEFAULT 0,
    served_rows       BIGINT NOT NULL DEFAULT 0,
    served_superseded BIGINT NOT NULL DEFAULT 0,
    served_noise      DOUBLE PRECISION GENERATED ALWAYS AS
        (served_superseded::double precision / NULLIF(served_rows, 0)) STORED,
    response_chars    BIGINT NOT NULL DEFAULT 0,
    top_score_hist    INT[] NOT NULL DEFAULT array_fill(0, ARRAY[17])
);

CREATE UNIQUE INDEX uq_memory_stats_daily_bucket ON memory_stats_daily (
    day,
    (COALESCE(workspace_id, '00000000-0000-0000-0000-000000000000'::uuid)),
    (COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid)),
    (COALESCE(agent_id, '00000000-0000-0000-0000-000000000000'::uuid)),
    caller_kind, op, source
);
CREATE INDEX idx_memory_stats_daily_ws_day ON memory_stats_daily (workspace_id, day);

-- memory_stats_rollup(from_ts, to_ts): rows of memory_stats_daily computed
-- from the raw journal for reads with from_ts <= ts < to_ts. The nightly job
-- inserts its output for a closed day; the API calls it for "today". The
-- empty_repeats lookback reads the same agent's earlier reads by index
-- (agent_id, ts), never the whole journal.
-- +goose StatementBegin
CREATE FUNCTION memory_stats_rollup(from_ts timestamptz, to_ts timestamptz)
RETURNS TABLE (
    day               date,
    workspace_id      uuid,
    project_id        uuid,
    agent_id          uuid,
    caller_kind       text,
    op                text,
    source            text,
    reads             bigint,
    errors            bigint,
    hits              bigint,
    useful            bigint,
    misses            bigint,
    misses_reason     text,
    empty_repeats     bigint,
    served_rows       bigint,
    served_superseded bigint,
    served_noise      double precision,
    response_chars    bigint,
    top_score_hist    int[]
)
LANGUAGE sql STABLE AS $fn$
WITH ev AS (
    SELECT e.id, e.ts, e.workspace_id, e.project_id, e.agent_id, e.caller_kind,
           e.query_hash, e.response_chars, e.results,
           (e.ts AT TIME ZONE 'UTC')::date AS day,
           CASE e.op WHEN 'recall-final' THEN 'recall'
                     WHEN 'recall_graph' THEN 'recall_with_graph'
                     ELSE e.op END AS op,
           e.op IN ('recall', 'recall-final', 'recall_graph') AS is_recall,
           e.error IS NOT NULL AS failed,
           o.open_rows, o.open_superseded, o.top_score
    FROM memory_read_events e
    CROSS JOIN LATERAL (
        SELECT count(*) AS open_rows,
               count(*) FILTER (WHERE (r->>'superseded')::boolean IS TRUE) AS open_superseded,
               max((r->>'score')::double precision) AS top_score
        FROM jsonb_array_elements(e.results) AS r
        WHERE NOT memory_read_row_pinned(r)
    ) o
    WHERE e.ts >= from_ts AND e.ts < to_ts
),
calls AS (
    SELECT ev.day, ev.workspace_id, ev.project_id, ev.agent_id, ev.caller_kind, ev.op,
           CASE WHEN ev.is_recall THEN '*'
                WHEN ev.op = 'get_memory' THEN 'memory'
                WHEN ev.op = 'get_project_knowledge' THEN 'project_knowledge'
                WHEN ev.op = 'get_project_updates' THEN 'project_update'
                ELSE 'unknown' END AS source,
           1 AS reads,
           ev.failed::int AS errors,
           (NOT ev.failed AND ev.open_rows > 0)::int AS hits,
           (NOT ev.failed AND ev.is_recall AND ev.open_rows > 0
                AND EXISTS (SELECT 1 FROM memory_read_usage u WHERE u.read_event_id = ev.id))::int AS useful,
           (NOT ev.failed AND ev.is_recall AND ev.open_rows = 0
                AND ev.agent_id IS NOT NULL AND ev.query_hash IS NOT NULL
                AND EXISTS (
                    SELECT 1 FROM memory_read_events p
                    WHERE p.agent_id = ev.agent_id
                      AND p.ts >= ev.ts - interval '7 days' AND p.ts < ev.ts
                      AND p.query_hash = ev.query_hash
                      AND p.op IN ('recall', 'recall-final', 'recall_graph')
                      AND p.error IS NULL
                      AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(p.results) AS pr
                                      WHERE NOT memory_read_row_pinned(pr))))::int AS empty_repeats,
           CASE WHEN ev.is_recall OR ev.failed THEN 0 ELSE ev.open_rows END AS served_rows,
           CASE WHEN ev.is_recall OR ev.failed THEN 0 ELSE ev.open_superseded END AS served_superseded,
           CASE WHEN ev.failed THEN 0 ELSE ev.response_chars END AS response_chars,
           CASE WHEN ev.is_recall AND NOT ev.failed
                THEN memory_top_score_bucket(COALESCE(ev.top_score, 0)) END AS bucket
    FROM ev
),
served AS (
    SELECT ev.day, ev.workspace_id, ev.project_id, ev.agent_id, ev.caller_kind, ev.op,
           COALESCE(NULLIF(r->>'source', ''), 'unknown') AS source,
           0 AS reads, 0 AS errors, 0 AS hits, 0 AS useful, 0 AS empty_repeats,
           1 AS served_rows,
           ((r->>'superseded')::boolean IS TRUE)::int AS served_superseded,
           0 AS response_chars,
           NULL::int AS bucket
    FROM ev
    CROSS JOIN LATERAL jsonb_array_elements(ev.results) AS r
    WHERE ev.is_recall AND NOT ev.failed AND NOT memory_read_row_pinned(r)
),
x AS (
    SELECT * FROM calls
    UNION ALL
    SELECT * FROM served
)
SELECT x.day, x.workspace_id, x.project_id, x.agent_id, x.caller_kind, x.op, x.source,
       sum(x.reads)::bigint, sum(x.errors)::bigint, sum(x.hits)::bigint, sum(x.useful)::bigint,
       0::bigint, 'not_collected'::text,
       sum(x.empty_repeats)::bigint,
       sum(x.served_rows)::bigint, sum(x.served_superseded)::bigint,
       sum(x.served_superseded)::double precision / NULLIF(sum(x.served_rows), 0),
       sum(x.response_chars)::bigint,
       ARRAY[count(*) FILTER (WHERE x.bucket = 1),
             count(*) FILTER (WHERE x.bucket = 2),
             count(*) FILTER (WHERE x.bucket = 3),
             count(*) FILTER (WHERE x.bucket = 4),
             count(*) FILTER (WHERE x.bucket = 5),
             count(*) FILTER (WHERE x.bucket = 6),
             count(*) FILTER (WHERE x.bucket = 7),
             count(*) FILTER (WHERE x.bucket = 8),
             count(*) FILTER (WHERE x.bucket = 9),
             count(*) FILTER (WHERE x.bucket = 10),
             count(*) FILTER (WHERE x.bucket = 11),
             count(*) FILTER (WHERE x.bucket = 12),
             count(*) FILTER (WHERE x.bucket = 13),
             count(*) FILTER (WHERE x.bucket = 14),
             count(*) FILTER (WHERE x.bucket = 15),
             count(*) FILTER (WHERE x.bucket = 16),
             count(*) FILTER (WHERE x.bucket = 17)]::int[]
FROM x
GROUP BY x.day, x.workspace_id, x.project_id, x.agent_id, x.caller_kind, x.op, x.source
$fn$;
-- +goose StatementEnd

-- memory_base_daily: snapshot of the memory base at the end of a UTC day, per
-- workspace and project (project_id NULL = workspace- and agent-scope rows).
-- Taken by the nightly job right after the day closes, once (a snapshot of
-- current memories cannot be recomputed for a past day later).
--   total              rows created before the end of the day
--   archived           archived flag or status 'archived'; active = the rest
--   superseded_active  active with status 'superseded'
--   conflicted         active with status 'conflicted'
--   no_kind            active without a kind: tag
--   statuslog_like     active, kind:session-checkpoint or a key like
--                      handoff-/checkpoint-/status-/progress- (§3 «статус-лог»)
--   dup_groups/rows    exact content_simhash duplicates among active rows of
--                      the workspace; a group is counted on the project of its
--                      oldest row, its rows on their own projects, so sums over
--                      projects give the workspace numbers
--   eligibleN          live rows (active, not superseded) created before
--                      day end - N days
--   deadN              of them, not served to an agent tool
--                      (caller_kind='agent_tool') in (day end - N d, day end],
--                      by the journal, not by last_accessed_at
-- A journal younger than N days: deadN and eligibleN are NULL and
-- deadN_reason = 'no_journal_before'.
CREATE TABLE memory_base_daily (
    day               DATE NOT NULL,
    workspace_id      UUID NOT NULL,
    project_id        UUID NULL,
    total             BIGINT NOT NULL DEFAULT 0,
    active            BIGINT NOT NULL DEFAULT 0,
    archived          BIGINT NOT NULL DEFAULT 0,
    superseded_active BIGINT NOT NULL DEFAULT 0,
    conflicted        BIGINT NOT NULL DEFAULT 0,
    no_kind           BIGINT NOT NULL DEFAULT 0,
    statuslog_like    BIGINT NOT NULL DEFAULT 0,
    dup_groups        BIGINT NOT NULL DEFAULT 0,
    dup_rows          BIGINT NOT NULL DEFAULT 0,
    dead30            BIGINT NULL,
    dead90            BIGINT NULL,
    eligible30        BIGINT NULL,
    eligible90        BIGINT NULL,
    dead30_reason     TEXT NULL,
    dead90_reason     TEXT NULL
);

CREATE UNIQUE INDEX uq_memory_base_daily_bucket ON memory_base_daily (
    day, workspace_id, (COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid))
);
CREATE INDEX idx_memory_base_daily_ws_day ON memory_base_daily (workspace_id, day);

-- memory_daily_rollups: which days the nightly job has written. A day with no
-- reads has no stats rows, so the marker, not the rows, says "done".
CREATE TABLE memory_daily_rollups (
    day              DATE PRIMARY KEY,
    stats_written_at TIMESTAMPTZ NULL,
    base_written_at  TIMESTAMPTZ NULL
);

-- memory_writes_daily: one row per memory write, from memory_revisions
-- (created, updated, forgotten, archived, unarchived) and project_updates
-- (action 'project_update'). U5 counts rows.
--   kind  first kind: tag of the revision (value after "kind:"), NULL without;
--         'project-update' for project updates
--   scope scope of the memory now, NULL when the memory was deleted;
--         'project' for project updates
-- project_id comes from the memory (NULL when deleted); workspace_id from the
-- revision, else from the memory.
CREATE VIEW memory_writes_daily AS
SELECT (r.created_at AT TIME ZONE 'UTC')::date AS day,
       COALESCE(r.workspace_id, m.workspace_id) AS workspace_id,
       m.project_id,
       r.actor_agent_id AS agent_id,
       r.actor_user_id AS user_id,
       r.action,
       (r.reason IS NOT NULL) AS with_reason,
       (SELECT substr(t.tag, 6) FROM unnest(r.tags) WITH ORDINALITY AS t(tag, n)
         WHERE t.tag LIKE 'kind:%' ORDER BY t.n LIMIT 1) AS kind,
       m.scope,
       r.memory_id,
       r.created_at AS ts
FROM memory_revisions r
LEFT JOIN memories m ON m.id = r.memory_id
UNION ALL
SELECT (pu.created_at AT TIME ZONE 'UTC')::date AS day,
       p.workspace_id,
       pu.project_id,
       a.id AS agent_id,
       CASE WHEN a.id IS NULL THEN pu.created_by END AS user_id,
       'project_update' AS action,
       false AS with_reason,
       'project-update' AS kind,
       'project' AS scope,
       NULL::uuid AS memory_id,
       pu.created_at AS ts
FROM project_updates pu
JOIN projects p ON p.id = pu.project_id
LEFT JOIN agents a ON a.id = pu.created_by;

-- The view filters by day: index the same expression.
CREATE INDEX idx_memory_revisions_utc_day ON memory_revisions (((created_at AT TIME ZONE 'UTC')::date));
CREATE INDEX idx_project_updates_utc_day ON project_updates (((created_at AT TIME ZONE 'UTC')::date));

-- +goose Down
DROP INDEX IF EXISTS idx_project_updates_utc_day;
DROP INDEX IF EXISTS idx_memory_revisions_utc_day;
DROP VIEW IF EXISTS memory_writes_daily;
DROP TABLE IF EXISTS memory_daily_rollups;
DROP TABLE IF EXISTS memory_base_daily;
DROP FUNCTION IF EXISTS memory_stats_rollup(timestamptz, timestamptz);
DROP TABLE IF EXISTS memory_stats_daily;
DROP FUNCTION IF EXISTS memory_read_row_pinned(jsonb);
DROP FUNCTION IF EXISTS memory_top_score_bucket(double precision);
DROP FUNCTION IF EXISTS memory_top_score_bounds();
