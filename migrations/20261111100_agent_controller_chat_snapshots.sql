-- +goose Up
-- The last chat-session snapshot a runner sent with POST /agents/me/controller:
-- which chat threads it holds a slot for and whether their process lives.
-- One row per (agent, runner), overwritten on every announce; no history.
-- received_at is the database clock at the write, so freshness does not
-- depend on the runner's clock. Additive: a runner that sends no snapshot
-- leaves no row, and the announce itself is unchanged.
CREATE TABLE agent_controller_chat_snapshots (
    agent_id      UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    runner_id     TEXT NOT NULL,
    sessions      JSONB NOT NULL DEFAULT '[]'::jsonb,
    slots_max     INT,
    slots_used    INT,
    reported_at   TIMESTAMPTZ NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, runner_id),
    CHECK (jsonb_typeof(sessions) = 'array')
);

CREATE INDEX idx_agent_controller_chat_snapshots_runner
    ON agent_controller_chat_snapshots (runner_id);

-- +goose Down
DROP TABLE IF EXISTS agent_controller_chat_snapshots;
