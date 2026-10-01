-- +goose Up

-- Agent "model" (#5548367d): the LLM an agent runs on (e.g. "gpt-6.1-sol"),
-- reported by the agent/dispatcher itself. agent_type stays the harness
-- (claude_code / codex / ...). Additive and nullable: NULL means "not reported",
-- deliberately not a default like 'claude' that would be wrong for every
-- non-Claude agent. An older binary never names the column, so a rollback of the
-- code alone is safe.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS model TEXT;

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS model;
