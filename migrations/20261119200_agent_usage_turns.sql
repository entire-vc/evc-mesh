-- +goose Up

-- Agent usage journal (Stage 1 of the agent usage widget).
--
-- agent_usage_turns: one row = one runner turn (one harness spawn), written by
-- POST /agents/me/sessions/report next to the agent_sessions addition. NULL in
-- a measure means "no figure", never 0 (the contract of the *_unknown flags of
-- 20261113300). tokens_in keeps its old meaning (all input, cache included);
-- cache_read and cache_write are "of which", cache_write_1h is the part of
-- cache_write written with the 1 h TTL (priced apart from the 5 min one).
-- occurred_at is the moment the spend happened and picks the hour: ended_at
-- when the runner sent it, else the report time; a session_backfill row uses
-- the session's started_at.
--
-- agent_usage_hourly: the widget's only read path, kept by an UPSERT in the
-- same transaction as the journal row (no lag, no REFRESH). Sums hold only the
-- known figures; turns_*_known count the turns that had them, so a reader sees
-- coverage instead of a silent 0. nosplit_* are the same sums over the turns
-- without a cache split (history, old clients): cost by API price can only use
-- cost_reported for them. A complete split requires in/out/read/write;
-- partial cache figures are also retained in nosplit_cache_* so a reader can
-- subtract them before pricing the fully covered turns.

-- Provider of a model name, for reports that do not name it and for history.
-- One definition for the report path and the backfill.
CREATE FUNCTION agent_usage_provider(model TEXT) RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE
RETURN CASE
    WHEN model IS NULL OR btrim(model) = '' THEN 'unknown'
    WHEN lower(model) ~ '(claude|anthropic)' THEN 'anthropic'
    WHEN lower(model) ~ '(^|/)(gpt|o[0-9]|codex|chatgpt|openai)' THEN 'openai'
    WHEN lower(model) ~ '(kimi|moonshot)' THEN 'moonshot'
    WHEN lower(model) ~ '(glm|zhipu|zcode|z-ai|zai/)' THEN 'zhipu'
    WHEN lower(model) ~ 'gemini' THEN 'google'
    WHEN lower(model) ~ 'deepseek' THEN 'deepseek'
    WHEN lower(model) ~ 'qwen' THEN 'alibaba'
    WHEN lower(model) ~ 'grok' THEN 'xai'
    WHEN lower(model) ~ '(mistral|codestral)' THEN 'mistral'
    ELSE 'unknown'
END;

CREATE TABLE agent_usage_turns (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id       UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    task_id        UUID NULL REFERENCES tasks(id) ON DELETE SET NULL,
    -- Resolved from the task when the row is written.
    project_id     UUID NULL,
    session_id     UUID NULL REFERENCES agent_sessions(id) ON DELETE SET NULL,
    source         TEXT NOT NULL CHECK (source IN ('turn', 'session_backfill')),
    -- The runner's turn id; a repeat of the same turn adds nothing.
    turn_ref       TEXT NULL,
    started_at     TIMESTAMPTZ NULL,
    ended_at       TIMESTAMPTZ NULL,
    occurred_at    TIMESTAMPTZ NOT NULL,
    model          TEXT NOT NULL DEFAULT '',
    provider       TEXT NOT NULL,
    -- Opaque limit-pool label "<harness>/<account>" (claude/bob-team); never a secret or an email.
    account_ref    TEXT NULL,
    billing_mode   TEXT NULL CHECK (billing_mode IN ('subscription', 'api')),
    tokens_in      BIGINT NULL,
    tokens_out     BIGINT NULL,
    cache_read     BIGINT NULL,
    cache_write    BIGINT NULL,
    cache_write_1h BIGINT NULL,
    -- What the harness reported as estimated_cost; a fallback and a cross-check, not shown as spend.
    cost_reported  NUMERIC(14,6) NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_usage_turns_turn_ref_key UNIQUE (agent_id, turn_ref)
);
CREATE INDEX idx_agent_usage_turns_ws_occurred ON agent_usage_turns (workspace_id, occurred_at);
CREATE INDEX idx_agent_usage_turns_session ON agent_usage_turns (session_id) WHERE session_id IS NOT NULL;
CREATE INDEX idx_agent_usage_turns_task ON agent_usage_turns (task_id) WHERE task_id IS NOT NULL;

CREATE TABLE agent_usage_hourly (
    workspace_id              UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    hour                      TIMESTAMPTZ NOT NULL,
    agent_id                  UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- nil uuid = no project
    project_id                UUID NOT NULL,
    model                     TEXT NOT NULL,
    provider                  TEXT NOT NULL,
    -- '' = not given
    account_ref               TEXT NOT NULL,
    billing_mode              TEXT NOT NULL,
    turns                     BIGINT NOT NULL DEFAULT 0,
    turns_tokens_known        BIGINT NOT NULL DEFAULT 0,
    turns_cache_known         BIGINT NOT NULL DEFAULT 0,
    turns_duration_known      BIGINT NOT NULL DEFAULT 0,
    turns_cost_reported_known BIGINT NOT NULL DEFAULT 0,
    tokens_in                 BIGINT NOT NULL DEFAULT 0,
    tokens_out                BIGINT NOT NULL DEFAULT 0,
    cache_read                BIGINT NOT NULL DEFAULT 0,
    cache_write               BIGINT NOT NULL DEFAULT 0,
    cache_write_1h            BIGINT NOT NULL DEFAULT 0,
    cost_reported             NUMERIC(16,6) NOT NULL DEFAULT 0,
    duration_ms               BIGINT NOT NULL DEFAULT 0,
    nosplit_turns_cost_known  BIGINT NOT NULL DEFAULT 0,
    nosplit_cache_read        BIGINT NOT NULL DEFAULT 0,
    nosplit_cache_write       BIGINT NOT NULL DEFAULT 0,
    nosplit_cache_write_1h    BIGINT NOT NULL DEFAULT 0,
    nosplit_tokens_in         BIGINT NOT NULL DEFAULT 0,
    nosplit_tokens_out        BIGINT NOT NULL DEFAULT 0,
    nosplit_cost_reported     NUMERIC(16,6) NOT NULL DEFAULT 0,
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, hour, agent_id, project_id, model, provider, account_ref, billing_mode)
);

-- Adds the given journal rows onto their hours. The single definition of how a
-- turn counts: the report path calls it for its one row, the backfill and a
-- rebuild for many.
-- +goose StatementBegin
CREATE FUNCTION agent_usage_hourly_add(turn_ids UUID[]) RETURNS VOID
LANGUAGE sql AS $$
    INSERT INTO agent_usage_hourly AS h (
        workspace_id, hour, agent_id, project_id, model, provider, account_ref, billing_mode,
        turns, turns_tokens_known, turns_cache_known, turns_duration_known, turns_cost_reported_known,
        tokens_in, tokens_out, cache_read, cache_write, cache_write_1h, cost_reported, duration_ms,
        nosplit_tokens_in, nosplit_tokens_out, nosplit_cost_reported,
        nosplit_turns_cost_known, nosplit_cache_read, nosplit_cache_write, nosplit_cache_write_1h)
    SELECT t.workspace_id,
           date_trunc('hour', t.occurred_at, 'UTC'),
           t.agent_id,
           COALESCE(t.project_id, '00000000-0000-0000-0000-000000000000'::uuid),
           t.model,
           t.provider,
           COALESCE(t.account_ref, ''),
           COALESCE(t.billing_mode, ''),
           count(*),
           count(*) FILTER (WHERE t.tokens_in IS NOT NULL AND t.tokens_out IS NOT NULL),
           count(*) FILTER (WHERE t.tokens_in IS NOT NULL AND t.tokens_out IS NOT NULL
                                  AND t.cache_read IS NOT NULL AND t.cache_write IS NOT NULL),
           count(*) FILTER (WHERE t.started_at IS NOT NULL AND t.ended_at IS NOT NULL),
           count(*) FILTER (WHERE t.cost_reported IS NOT NULL),
           COALESCE(sum(t.tokens_in), 0),
           COALESCE(sum(t.tokens_out), 0),
           COALESCE(sum(t.cache_read), 0),
           COALESCE(sum(t.cache_write), 0),
           COALESCE(sum(t.cache_write_1h), 0),
           COALESCE(sum(t.cost_reported), 0),
           COALESCE(sum(round(extract(epoch FROM t.ended_at - t.started_at) * 1000)::bigint)
                    FILTER (WHERE t.started_at IS NOT NULL AND t.ended_at IS NOT NULL), 0),
           COALESCE(sum(t.tokens_in) FILTER (WHERE t.tokens_in IS NULL OR t.tokens_out IS NULL OR t.cache_read IS NULL OR t.cache_write IS NULL), 0),
           COALESCE(sum(t.tokens_out) FILTER (WHERE t.tokens_in IS NULL OR t.tokens_out IS NULL OR t.cache_read IS NULL OR t.cache_write IS NULL), 0),
           COALESCE(sum(t.cost_reported) FILTER (WHERE t.tokens_in IS NULL OR t.tokens_out IS NULL OR t.cache_read IS NULL OR t.cache_write IS NULL), 0),
           count(*) FILTER (WHERE t.cost_reported IS NOT NULL AND (t.tokens_in IS NULL OR t.tokens_out IS NULL OR t.cache_read IS NULL OR t.cache_write IS NULL)),
           COALESCE(sum(t.cache_read) FILTER (WHERE t.tokens_in IS NULL OR t.tokens_out IS NULL OR t.cache_read IS NULL OR t.cache_write IS NULL), 0),
           COALESCE(sum(t.cache_write) FILTER (WHERE t.tokens_in IS NULL OR t.tokens_out IS NULL OR t.cache_read IS NULL OR t.cache_write IS NULL), 0),
           COALESCE(sum(t.cache_write_1h) FILTER (WHERE t.tokens_in IS NULL OR t.tokens_out IS NULL OR t.cache_read IS NULL OR t.cache_write IS NULL), 0)
    FROM agent_usage_turns t
    WHERE t.id = ANY(turn_ids)
    GROUP BY 1, 2, 3, 4, 5, 6, 7, 8
    ON CONFLICT (workspace_id, hour, agent_id, project_id, model, provider, account_ref, billing_mode)
    DO UPDATE SET
        turns                     = h.turns + EXCLUDED.turns,
        turns_tokens_known        = h.turns_tokens_known + EXCLUDED.turns_tokens_known,
        turns_cache_known         = h.turns_cache_known + EXCLUDED.turns_cache_known,
        turns_duration_known      = h.turns_duration_known + EXCLUDED.turns_duration_known,
        turns_cost_reported_known = h.turns_cost_reported_known + EXCLUDED.turns_cost_reported_known,
        tokens_in                 = h.tokens_in + EXCLUDED.tokens_in,
        tokens_out                = h.tokens_out + EXCLUDED.tokens_out,
        cache_read                = h.cache_read + EXCLUDED.cache_read,
        cache_write               = h.cache_write + EXCLUDED.cache_write,
        cache_write_1h            = h.cache_write_1h + EXCLUDED.cache_write_1h,
        cost_reported             = h.cost_reported + EXCLUDED.cost_reported,
        duration_ms               = h.duration_ms + EXCLUDED.duration_ms,
        nosplit_turns_cost_known  = h.nosplit_turns_cost_known + EXCLUDED.nosplit_turns_cost_known,
        nosplit_cache_read        = h.nosplit_cache_read + EXCLUDED.nosplit_cache_read,
        nosplit_cache_write       = h.nosplit_cache_write + EXCLUDED.nosplit_cache_write,
        nosplit_cache_write_1h    = h.nosplit_cache_write_1h + EXCLUDED.nosplit_cache_write_1h,
        nosplit_tokens_in         = h.nosplit_tokens_in + EXCLUDED.nosplit_tokens_in,
        nosplit_tokens_out        = h.nosplit_tokens_out + EXCLUDED.nosplit_tokens_out,
        nosplit_cost_reported     = h.nosplit_cost_reported + EXCLUDED.nosplit_cost_reported,
        updated_at                = now();
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS agent_usage_hourly_add(UUID[]);
DROP TABLE IF EXISTS agent_usage_hourly;
DROP TABLE IF EXISTS agent_usage_turns;
DROP FUNCTION IF EXISTS agent_usage_provider(TEXT);
