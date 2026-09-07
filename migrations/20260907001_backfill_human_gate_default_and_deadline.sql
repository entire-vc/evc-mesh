-- +goose Up
-- +goose StatementBegin
-- Task 1.4b (#4d61d877): one-shot backfill for gates armed BEFORE this fix landed.
-- ArmHumanGate (internal/service/task_service.go) now auto-fills recommended_default and
-- (through the same SQL this migration mirrors) gate_deadline for every NEW
-- marker-sourced arm that names no default — this migration gives the same treatment to
-- gates that are already live, so a gate armed before or after the fix reads identically.
--
-- Measured on prod 2026-09-07 before writing this migration (mesh_read, read-only):
-- human_gate = true — 97 rows; 94 with recommended_default NULL/empty, 96 with
-- gate_deadline NULL, 95 human_gate_class = 'hard', 0 authored directly by a human
-- (all self-armed by an agent through the legitimate ❓ Blocking marker).
--
-- Priority for recommended_default (matches the service's own regex,
-- internal/service/comment_service_human_gate_arming.go's recommendedDefaultRegex,
-- transliterated to a Postgres ARE — the 'n' flag gives ^/$ the per-line meaning Go's
-- (?m) does, 'i' is case-insensitive):
--   1. the LATEST non-system comment on the task naming an explicit
--      "по умолчанию:"/"дефолт:"/"recommended default:" line — the same field a live
--      marker would have supplied, just mined out of the thread after the fact.
--   2. a throwaway/probe/rotation card (recurring, or labelled/titled/described as a
--      canary/probe/throwaway) — "отменить / закрыть, если нет ответа": these were never
--      asking a human anything durable.
--   3. otherwise the same system default a live marker-sourced arm gets today,
--      domain.DefaultMarkerRecommendedDefault ("применить рекомендацию исполнителя /
--      закрыть как есть").
--
-- gate_deadline gets NOW() + 24h regardless of which branch above fired — the same
-- window ArmHumanGate uses for a fresh arm (HUMAN_GATE_DEFAULT_TIMEOUT_H, default 24h,
-- Pavel decision 2026-09-06). A hard-classified row getting a deadline here is SAFE, not
-- an auto-release risk: FindExpiredDefaultGates's `human_gate_class != 'hard'` predicate
-- (task_repo.go, a fixed literal, not derived from this column) makes a hard gate
-- structurally unreachable by the sweep this deadline feeds, regardless of whether the
-- column is set — the deadline is for escalation/visibility ("how long has this been
-- open") only.
--
-- NOTE on marker-mining's known limitation: this does not strip quoted/backticked spans
-- the way the Go extractor does (stripQuotedSpans), so a comment that QUOTES the
-- convention (e.g. an instructional post-mortem showing `По умолчанию: <текст>` as an
-- example) could in principle be harvested as if it were a real stated default.
-- Mitigated narrowly by excluding matches whose captured value itself starts with a
-- backtick (the exact quoting style this fleet's own docs use) and by requiring a
-- human/agent author, never 'system' (mirrors migration 20260906002's gate_author
-- backfill). Accepted as a one-shot backfill's residual imprecision, not a live code
-- path: a wrong recommended_default here is at worst a slightly-off closing line, never
-- a security or money decision — and a hard-classed row's deadline still never
-- auto-releases anything regardless of what recommended_default ends up saying.
WITH candidate_comments AS (
    SELECT
        c.task_id,
        c.created_at,
        (regexp_match(
            c.body,
            '^[ \t>*\-•]*\**\s*(?:recommended[ _-]?default|default(?:\s+if\s+no\s+answer)?|по\s+умолчанию|рекомендую\s+по\s+умолчанию|дефолт)\**\s*[:—-]\s*(.+)$',
            'ni'
        ))[1] AS extracted
    FROM comments c
    JOIN tasks t ON t.id = c.task_id
    WHERE t.human_gate = true
      AND t.deleted_at IS NULL
      AND c.author_type <> 'system'
),
marker_default AS (
    SELECT DISTINCT ON (task_id)
           task_id,
           NULLIF(btrim(btrim(extracted), '* '), '') AS value
    FROM candidate_comments
    WHERE extracted IS NOT NULL
      AND left(btrim(extracted), 1) <> '`'
    ORDER BY task_id, created_at DESC
),
throwaway_gate AS (
    SELECT t.id AS task_id
    FROM tasks t
    WHERE t.human_gate = true
      AND t.deleted_at IS NULL
      AND (
          t.recurring_schedule_id IS NOT NULL
          OR t.labels && ARRAY['kind:drift', 'kind:analytics']::text[]
          OR t.title ~* '(канареечн|canary|throwaway|probe)'
          OR t.description ~* '(канареечн|canary|throwaway|probe)'
      )
)
UPDATE tasks t
SET recommended_default = COALESCE(
        NULLIF(t.recommended_default, ''),
        md.value,
        CASE WHEN tw.task_id IS NOT NULL THEN 'отменить / закрыть, если нет ответа' END,
        'применить рекомендацию исполнителя / закрыть как есть'
    ),
    gate_deadline = COALESCE(t.gate_deadline, now() + interval '24 hours'),
    updated_at = now()
FROM (
    SELECT id FROM tasks
    WHERE human_gate = true
      AND deleted_at IS NULL
      AND (recommended_default IS NULL OR recommended_default = '' OR gate_deadline IS NULL)
) sel
LEFT JOIN marker_default md ON md.task_id = sel.id
LEFT JOIN throwaway_gate tw ON tw.task_id = sel.id
WHERE t.id = sel.id;
-- +goose StatementEnd

-- +goose Down
-- no-op: intentionally irreversible — the NULL recommended_default/gate_deadline this
-- migration replaces was itself the bug (task 1.4b, #4d61d877): every one of those rows
-- was a live gate resolvable only by a human finding it by hand. Reverting would
-- reintroduce that, not fix anything a rollback is supposed to fix.
