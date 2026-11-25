-- +goose Up

-- Search misses (widget "Память Mesh", Stage 1 unit U7, plan doc §3
-- «Промах поиска»). Fills memory_stats_daily.misses, which U4 left at 0 with
-- misses_reason = 'not_collected'.
--
-- A recall read r (recall, recall-final, recall_graph; no error; with an agent
-- and a workspace) is a miss when there is a memory m that was active and
-- visible to r's agent at t_r, m is not among r's served rows, and later in the
-- same session S(r):
--   refound   (a) another recall r' returned m and r' was useful by m
--                 (a memory_read_usage row of r' for m, U3): found only on a
--                 later try;
--   rewritten (b) the agent created a memory m' whose content_simhash is within
--                 Hamming distance 3 of m's: wrote again what was already there.
--
-- "Later" is bounded: (t_r, t_r + 2 hours], the same window as W(r) of U3
-- (domain.MemoryMissWindow). §3 names no bound; without one a closed day would
-- never be final. S(r) follows U3 (domain sameSession):
--   (a) r' carries a session: r's session_id when r has one, else r's card
--       (task_id) when r has one, else the agent alone;
--   (b) a revision carries no session: the agent, and the card when both r
--       (task_id) and m' (source_task_id) name one.
-- m is active and visible at t_r when it is in r's workspace, is not another
-- agent's scope=agent row, is in r's project when r names one (that project's
-- rows and pinned workspace rows, as recall-final), was created before t_r,
-- had not expired, was not
-- archived at t_r (by its archive/unarchive revisions; the current flag when it
-- has none) and was not superseded by a memory created by t_r. Deleted memories
-- are not seen. m' is read with its current simhash (revisions keep none).
--
-- memory_search_misses(from_ts, to_ts) is also the miss detail for U5: one row
-- per (read in [from_ts, to_ts), missed memory, evidence), several evidence
-- rows per pair possible. It reads the raw journal of [from_ts, to_ts + 2h).
--
-- Rollback: revert the MR and run this file's Down: memory_stats_rollup goes
-- back to the U4 body, and the written days get misses = 0, misses_reason =
-- 'not_collected' again; the other counters are untouched.

-- +goose StatementBegin
CREATE FUNCTION memory_search_misses(from_ts timestamptz, to_ts timestamptz)
RETURNS TABLE (
    read_event_id uuid,
    read_ts       timestamptz,
    workspace_id  uuid,
    project_id    uuid,
    agent_id      uuid,
    session_id    text,
    task_id       uuid,
    caller_kind   text,
    op            text,
    memory_id     uuid,
    memory_key    text,
    reason        text,
    evidence_kind text,
    evidence_id   uuid,
    evidence_ts   timestamptz,
    distance      int
)
LANGUAGE sql STABLE AS $fn$
WITH r AS (
    SELECT e.id, e.ts, e.workspace_id, e.project_id, e.agent_id,
           NULLIF(e.session_id, '') AS session_id, e.task_id, e.caller_kind,
           CASE e.op WHEN 'recall-final' THEN 'recall'
                     WHEN 'recall_graph' THEN 'recall_with_graph'
                     ELSE e.op END AS op,
           e.results
    FROM memory_read_events e
    WHERE e.ts >= from_ts AND e.ts < to_ts
      AND e.op IN ('recall', 'recall-final', 'recall_graph')
      AND e.error IS NULL
      AND e.agent_id IS NOT NULL
      AND e.workspace_id IS NOT NULL
),
-- One row per (r, m, r'): several usage signals of r' by m are one evidence.
refound AS (
    SELECT DISTINCT r.id AS read_event_id, u.memory_id,
           'refound'::text AS reason, 'read_event'::text AS evidence_kind,
           r2.id AS evidence_id, r2.ts AS evidence_ts, NULL::int AS distance
    FROM r
    JOIN memory_read_events r2
      ON r2.agent_id = r.agent_id
     AND r2.ts > r.ts AND r2.ts <= r.ts + interval '2 hours'
     AND r2.op IN ('recall', 'recall-final', 'recall_graph')
     AND r2.error IS NULL
     AND CASE WHEN r.session_id IS NOT NULL THEN NULLIF(r2.session_id, '') = r.session_id
              WHEN r.task_id IS NOT NULL THEN r2.task_id = r.task_id
              ELSE true END
    JOIN memory_read_usage u ON u.read_event_id = r2.id
),
-- The memories the reads' agents created in [from_ts, to_ts + 2h), by the
-- actor index; each is compared with the workspace once (near), not once per
-- read.
writes AS MATERIALIZED (
    SELECT mr.memory_id, mr.created_at, mr.actor_agent_id, mn.workspace_id, mn.content_simhash, mn.source_task_id
    FROM (SELECT DISTINCT r.agent_id FROM r) ra
    JOIN memory_revisions mr
      ON mr.actor_agent_id = ra.agent_id
     AND mr.created_at > from_ts AND mr.created_at <= to_ts + interval '2 hours'
     AND mr.action = 'created'
    JOIN memories mn ON mn.id = mr.memory_id
    WHERE mn.content_simhash IS NOT NULL
),
near AS MATERIALIZED (
    SELECT w.memory_id AS new_id, m.id AS old_id,
           bit_count((m.content_simhash # w.content_simhash)::bit(64))::int AS distance
    FROM writes w
    JOIN memories m
      ON m.workspace_id = w.workspace_id
     AND m.id <> w.memory_id
     AND m.content_simhash IS NOT NULL
     AND bit_count((m.content_simhash # w.content_simhash)::bit(64)) <= 3
),
rewritten AS (
    SELECT DISTINCT r.id AS read_event_id, n.old_id AS memory_id,
           'rewritten'::text AS reason, 'memory'::text AS evidence_kind,
           w.memory_id AS evidence_id, w.created_at AS evidence_ts, n.distance
    FROM r
    JOIN writes w
      ON w.actor_agent_id = r.agent_id
     AND w.created_at > r.ts AND w.created_at <= r.ts + interval '2 hours'
     AND w.workspace_id = r.workspace_id
     AND (r.task_id IS NULL OR w.source_task_id IS NULL OR w.source_task_id = r.task_id)
    JOIN near n ON n.new_id = w.memory_id
),
cand AS (
    SELECT * FROM refound
    UNION ALL
    SELECT * FROM rewritten
)
SELECT r.id, r.ts, r.workspace_id, r.project_id, r.agent_id, r.session_id, r.task_id,
       r.caller_kind, r.op, m.id, m.key,
       c.reason, c.evidence_kind, c.evidence_id, c.evidence_ts, c.distance
FROM cand c
JOIN r ON r.id = c.read_event_id
JOIN memories m ON m.id = c.memory_id
WHERE m.workspace_id = r.workspace_id
  AND (m.scope <> 'agent' OR m.agent_id = r.agent_id)
  -- project of r: its rows and pinned workspace rows (finalEligible).
  AND (r.project_id IS NULL OR m.project_id = r.project_id
       OR (m.project_id IS NULL AND m.scope = 'workspace' AND m.tags @> ARRAY['kind:pinned']::text[]))
  AND m.created_at < r.ts
  AND (m.expires_at IS NULL OR m.expires_at > r.ts)
  AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(r.results) AS x WHERE x->>'id' = m.id::text)
  -- archived at t_r: the last archive/unarchive revision by t_r; else the
  -- opposite of the first one after t_r; else the current flag.
  AND NOT COALESCE(
        (SELECT a.action = 'archived' FROM memory_revisions a
          WHERE a.memory_id = m.id AND a.action IN ('archived', 'unarchived') AND a.created_at <= r.ts
          ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
        (SELECT a.action = 'unarchived' FROM memory_revisions a
          WHERE a.memory_id = m.id AND a.action IN ('archived', 'unarchived') AND a.created_at > r.ts
          ORDER BY a.created_at, a.version LIMIT 1),
        m.archived OR m.status = 'archived')
  -- superseded at t_r: superseded by a memory that existed by then (or by an
  -- unknown one).
  AND NOT (m.status = 'superseded'
           AND COALESCE((SELECT s.created_at <= r.ts FROM memories s WHERE s.id = m.superseded_by), true))
$fn$;
-- +goose StatementEnd

-- memory_stats_rollup as in U4, with misses counted: a recall call is a miss
-- when memory_search_misses has a row for it. misses_reason is NULL (collected).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION memory_stats_rollup(from_ts timestamptz, to_ts timestamptz)
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
missed AS (
    SELECT DISTINCT ms.read_event_id FROM memory_search_misses(from_ts, to_ts) AS ms
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
           (NOT ev.failed AND ev.is_recall
                AND EXISTS (SELECT 1 FROM missed WHERE missed.read_event_id = ev.id))::int AS misses,
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
           0 AS reads, 0 AS errors, 0 AS hits, 0 AS useful, 0 AS misses, 0 AS empty_repeats,
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
       sum(x.misses)::bigint, NULL::text,
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

-- Days written before U7 carry misses_reason = 'not_collected': the nightly job
-- writes them again with misses (the raw journal and the final usage are kept).
UPDATE memory_daily_rollups SET stats_written_at = NULL
WHERE day IN (SELECT DISTINCT day FROM memory_stats_daily WHERE misses_reason = 'not_collected');

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS memory_stats_rollup(timestamptz, timestamptz);
-- +goose StatementEnd
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
DROP FUNCTION IF EXISTS memory_search_misses(timestamptz, timestamptz);
UPDATE memory_stats_daily SET misses = 0, misses_reason = 'not_collected';
