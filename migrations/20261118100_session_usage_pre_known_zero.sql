-- +goose Up
-- 20261113300 (on prod 2026-11-13 09:30:24 UTC) turned every existing session row into "known"
-- figures. Before it, interactive and codex turns and sessions with no usage report stored 0 for
-- "not reported", so those zeros became a "known $0" and the task cost block shows "<$0.01" for
-- them. Here the zeros of rows started before that moment go back to unknown:
--   * cost 0 that is known and not already flagged unknown -> cost_known = false;
--   * no model and tokens in and out both 0 (tool_breakdown only) -> all known flags false.
-- A non-zero figure of an old row stays known. The previous flags of every touched row are kept
-- in agent_sessions_pre_known_zero_backup so Down puts them back exactly.
CREATE TABLE agent_sessions_pre_known_zero_backup (
    id               uuid PRIMARY KEY,
    tokens_in_known  boolean NOT NULL,
    tokens_out_known boolean NOT NULL,
    cost_known       boolean NOT NULL
);

INSERT INTO agent_sessions_pre_known_zero_backup (id, tokens_in_known, tokens_out_known, cost_known)
SELECT id, tokens_in_known, tokens_out_known, cost_known
FROM agent_sessions
WHERE started_at < '2026-11-13 09:30:24+00'
  AND (
        (COALESCE(estimated_cost, 0) = 0 AND cost_known AND NOT cost_unknown)
     OR (COALESCE(model_used, '') = '' AND COALESCE(tokens_in, 0) = 0 AND COALESCE(tokens_out, 0) = 0
         AND (tokens_in_known OR tokens_out_known OR cost_known))
  );

UPDATE agent_sessions s
SET cost_known = CASE
        WHEN (COALESCE(s.estimated_cost, 0) = 0 AND NOT s.cost_unknown)
          OR (COALESCE(s.model_used, '') = '' AND COALESCE(s.tokens_in, 0) = 0
              AND COALESCE(s.tokens_out, 0) = 0) THEN false
        ELSE s.cost_known
    END,
    tokens_in_known = CASE
        WHEN COALESCE(s.model_used, '') = '' AND COALESCE(s.tokens_in, 0) = 0
          AND COALESCE(s.tokens_out, 0) = 0 THEN false
        ELSE s.tokens_in_known
    END,
    tokens_out_known = CASE
        WHEN COALESCE(s.model_used, '') = '' AND COALESCE(s.tokens_in, 0) = 0
          AND COALESCE(s.tokens_out, 0) = 0 THEN false
        ELSE s.tokens_out_known
    END
FROM agent_sessions_pre_known_zero_backup b
WHERE s.id = b.id;

-- +goose Down
UPDATE agent_sessions s
SET tokens_in_known  = b.tokens_in_known,
    tokens_out_known = b.tokens_out_known,
    cost_known       = b.cost_known
FROM agent_sessions_pre_known_zero_backup b
WHERE s.id = b.id;

DROP TABLE agent_sessions_pre_known_zero_backup;
