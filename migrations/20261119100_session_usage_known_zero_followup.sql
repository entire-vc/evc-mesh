-- +goose Up
-- Follow-up to 20261118100: two groups of session rows still answer a cost they never had as a
-- known $0, so their task cost block keeps showing a zero instead of "unknown".
--   * Started before 20261113300 (on prod 2026-11-13 09:30:24 UTC), cost 0, flagged cost_unknown
--     yet still cost_known: the known flag is the default 20261113300 gave every old row, and a
--     known part of 0 next to an unknown cost carries nothing. 20261118100 skipped them on purpose
--     (cost_unknown already set); the sum still counted their 0. A non-zero figure stays known.
--   * Started on the day of 20261113300 after it, by writers still running the previous build:
--     a model's real tokens with cost 0 stored as known. The current build writes cost_unknown for
--     an unpriced report: on prod (read 2026-11-18) exactly 3 rows ever match after the cut, the
--     last started 2026-11-13 09:41:46 UTC, so the day bound takes them and nothing newer.
-- Only cost_known changes. The previous value of every touched row is kept in
-- agent_sessions_known_zero_followup_backup so Down puts it back exactly.
CREATE TABLE agent_sessions_known_zero_followup_backup (
    id         uuid PRIMARY KEY,
    cost_known boolean NOT NULL
);

INSERT INTO agent_sessions_known_zero_followup_backup (id, cost_known)
SELECT id, cost_known
FROM agent_sessions
WHERE cost_known
  AND COALESCE(estimated_cost, 0) = 0
  AND (
        (started_at < '2026-11-13 09:30:24+00' AND cost_unknown)
     OR (started_at >= '2026-11-13 09:30:24+00' AND started_at < '2026-11-14 00:00:00+00'
         AND NOT cost_unknown
         AND COALESCE(model_used, '') <> ''
         AND (COALESCE(tokens_in, 0) > 0 OR COALESCE(tokens_out, 0) > 0))
  );

UPDATE agent_sessions s
SET cost_known = false
FROM agent_sessions_known_zero_followup_backup b
WHERE s.id = b.id;

-- +goose Down
UPDATE agent_sessions s
SET cost_known = b.cost_known
FROM agent_sessions_known_zero_followup_backup b
WHERE s.id = b.id;

DROP TABLE agent_sessions_known_zero_followup_backup;
