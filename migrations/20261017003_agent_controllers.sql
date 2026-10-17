-- +goose Up
-- One controller per agent: a runner announces itself with the agent's key
-- (POST /agents/me/controller) and holds a lease that heartbeats renew. A
-- second process with the same key gets 409 until the lease lapses.
-- Additive: agents driven through MCP never announce and have no row here.
CREATE TABLE agent_controllers (
    agent_id         UUID PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    runner_id        TEXT NOT NULL,
    runner_name      TEXT NOT NULL,
    host             TEXT NOT NULL,
    version          TEXT NOT NULL DEFAULT '',
    announced_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS agent_controllers;
