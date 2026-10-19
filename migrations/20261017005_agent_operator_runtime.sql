-- +goose Up
-- Runner stage 1: operator-level runtime ("My Runner"). Purely additive; the
-- workspace-level runtime catalog (PUT /workspaces/:ws/runtime) is untouched.
--
-- operator_user_id: the user who runs the agent (sponsor = who issued the key).
-- NULL means "not known". Backfilled from sponsor_user_id.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS operator_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
UPDATE agents SET operator_user_id = sponsor_user_id
 WHERE operator_user_id IS NULL AND sponsor_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agents_operator_user_id ON agents (operator_user_id) WHERE operator_user_id IS NOT NULL;

-- One config (accounts as cred: refs, pools, profiles, per-agent settings) and
-- one revision line per user. No secrets are stored here.
CREATE TABLE IF NOT EXISTS user_runtime_configs (
    user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    revision   BIGINT NOT NULL DEFAULT 0,
    config     JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only history with the author of every change.
CREATE TABLE IF NOT EXISTS user_runtime_history (
    id          BIGSERIAL PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    revision    BIGINT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('config','limits','manager_granted','manager_revoked')),
    detail      JSONB NOT NULL DEFAULT '{}'::jsonb,
    author_type TEXT NOT NULL CHECK (author_type IN ('user','agent')),
    author_id   UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_user_runtime_history_user ON user_runtime_history (user_id, id DESC);

-- Assignment "runtime_manager": the operator lets own agents edit My Runner with
-- their own key. Revocable at any moment (revoked_at).
CREATE TABLE IF NOT EXISTS user_runtime_managers (
    operator_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id         UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    granted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at       TIMESTAMPTZ,
    PRIMARY KEY (operator_user_id, agent_id)
);

-- +goose Down
DROP TABLE IF EXISTS user_runtime_managers;
DROP TABLE IF EXISTS user_runtime_history;
DROP TABLE IF EXISTS user_runtime_configs;
DROP INDEX IF EXISTS idx_agents_operator_user_id;
ALTER TABLE agents DROP COLUMN IF EXISTS operator_user_id;
