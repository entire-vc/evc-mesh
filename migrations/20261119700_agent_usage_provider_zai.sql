-- +goose Up

CREATE OR REPLACE FUNCTION agent_usage_provider(model TEXT) RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE
RETURN CASE
    WHEN model IS NULL OR btrim(model) = '' THEN 'unknown'
    WHEN lower(model) ~ '(claude|anthropic)' THEN 'anthropic'
    WHEN lower(model) ~ '(^|/)(gpt|o[0-9]|codex|chatgpt|openai)' THEN 'openai'
    WHEN lower(model) ~ '(kimi|moonshot)' THEN 'moonshot'
    WHEN lower(model) ~ '(glm|zhipu|zcode|z-ai|zai/)' THEN 'zai'
    WHEN lower(model) ~ 'gemini' THEN 'google'
    WHEN lower(model) ~ 'deepseek' THEN 'deepseek'
    WHEN lower(model) ~ 'qwen' THEN 'alibaba'
    WHEN lower(model) ~ 'grok' THEN 'xai'
    WHEN lower(model) ~ '(mistral|codestral)' THEN 'mistral'
    ELSE 'unknown'
END;

-- Match the report write order and keep a concurrent old report from adding a
-- zhipu row after the rename. Rebuilding also merges existing zhipu/zai hours.
LOCK TABLE agent_usage_turns IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE agent_usage_hourly IN SHARE ROW EXCLUSIVE MODE;
UPDATE agent_usage_turns SET provider = 'zai' WHERE provider = 'zhipu';
DELETE FROM agent_usage_hourly;
SELECT agent_usage_hourly_add(ARRAY(SELECT id FROM agent_usage_turns));

-- +goose Down

CREATE OR REPLACE FUNCTION agent_usage_provider(model TEXT) RETURNS TEXT
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

LOCK TABLE agent_usage_turns IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE agent_usage_hourly IN SHARE ROW EXCLUSIVE MODE;
UPDATE agent_usage_turns SET provider = 'zhipu' WHERE provider = 'zai';
DELETE FROM agent_usage_hourly;
SELECT agent_usage_hourly_add(ARRAY(SELECT id FROM agent_usage_turns));
