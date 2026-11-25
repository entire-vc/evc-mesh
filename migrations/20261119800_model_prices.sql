-- +goose Up

-- Public API list prices per model, the base of the "≈ $ by API price" figure
-- of the agent usage analytics (GET /workspaces/:ws_id/analytics/usage). The
-- figure is computed at query time from agent_usage_hourly: an hour takes the
-- row of its model with the greatest valid_from <= hour.
--
-- A price change is a NEW row with a new valid_from; existing rows are never
-- edited, so a past period keeps the price it was computed with.
--
-- Rates are USD per million tokens. A NULL cache rate means the provider list
-- gives none: cache tokens of that model are then not priced at the input rate
-- (that would overstate the sum), the hour stays without a sum instead.
-- cache_write_per_mtok is the 5-minute cache write, cache_write_1h_per_mtok the
-- 1-hour one. source_url names the public price list each row was taken from.
CREATE TABLE model_prices (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    model                   TEXT        NOT NULL,
    provider                TEXT        NOT NULL,
    valid_from              TIMESTAMPTZ NOT NULL,
    input_per_mtok          NUMERIC(12,6) NOT NULL CHECK (input_per_mtok >= 0),
    output_per_mtok         NUMERIC(12,6) NOT NULL CHECK (output_per_mtok >= 0),
    cache_read_per_mtok     NUMERIC(12,6) CHECK (cache_read_per_mtok >= 0),
    cache_write_per_mtok    NUMERIC(12,6) CHECK (cache_write_per_mtok >= 0),
    cache_write_1h_per_mtok NUMERIC(12,6) CHECK (cache_write_1h_per_mtok >= 0),
    currency                TEXT        NOT NULL DEFAULT 'USD' CHECK (currency = 'USD'),
    source_url              TEXT        NOT NULL CHECK (source_url LIKE 'https://%'),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (model, valid_from)
);


-- Initial valuation baseline for the 90-day reporting window, not a claim
-- about provider release dates. Later rate changes append a new valid_from.
-- Snapshot: official standard/global API price lists checked 2026-11-18.
-- Ambiguous harness labels (kimi, zcode, codex), probes and synthetic models
-- deliberately have no price; their reported cost can still be displayed.
INSERT INTO model_prices
    (model, provider, valid_from, input_per_mtok, output_per_mtok,
     cache_read_per_mtok, cache_write_per_mtok, cache_write_1h_per_mtok, source_url)
VALUES
    ('claude-opus-5-5', 'anthropic', '2026-08-20T00:00:00Z', 4, 20, 0.20, 5, 8, 'https://platform.claude.com/docs/en/about-claude/pricing'),
    ('claude-sonnet-5-5', 'anthropic', '2026-08-20T00:00:00Z', 2, 10, 0.10, 2.5, 4, 'https://platform.claude.com/docs/en/about-claude/pricing'),
    ('claude-opus-5', 'anthropic', '2026-08-20T00:00:00Z', 5, 25, 0.50, 6.25, 10, 'https://platform.claude.com/docs/en/about-claude/pricing'),
    ('claude-sonnet-5', 'anthropic', '2026-08-20T00:00:00Z', 2, 10, 0.20, 2.5, 4, 'https://platform.claude.com/docs/en/about-claude/pricing'),
    ('claude-opus-4-8', 'anthropic', '2026-08-20T00:00:00Z', 5, 25, 0.50, 6.25, 10, 'https://platform.claude.com/docs/en/about-claude/pricing'),
    ('claude-fable-5-1', 'anthropic', '2026-08-20T00:00:00Z', 10, 50, 0.25, 12.5, 20, 'https://platform.claude.com/docs/en/about-claude/pricing'),
    ('claude-sonnet-4-5', 'anthropic', '2026-08-20T00:00:00Z', 3, 15, 0.30, 3.75, 6, 'https://platform.claude.com/docs/en/about-claude/pricing'),
    ('gpt-6.1-sol', 'openai', '2026-08-20T00:00:00Z', 2, 10, 0.10, NULL, NULL, 'https://developers.openai.com/api/docs/models/gpt-6.1-sol'),
    ('gpt-6-astra', 'openai', '2026-08-20T00:00:00Z', 10, 50, 1, NULL, NULL, 'https://developers.openai.com/api/docs/models/gpt-6-astra'),
    ('gpt-6-luna', 'openai', '2026-08-20T00:00:00Z', 0.1, 0.5, 0.01, NULL, NULL, 'https://developers.openai.com/api/docs/models/gpt-6-luna'),
    ('glm-5.3', 'zai', '2026-08-20T00:00:00Z', 1.4, 4.4, 0.26, NULL, NULL, 'https://docs.z.ai/guides/overview/pricing');

-- +goose Down
DROP TABLE IF EXISTS model_prices;
