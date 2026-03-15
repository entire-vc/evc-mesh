-- +goose Up
CREATE TABLE IF NOT EXISTS agent_sessions (
	id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	agent_id         UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	ended_at         TIMESTAMPTZ,
	status           TEXT DEFAULT 'active' CHECK (status IN ('active', 'idle', 'ended')),
	tool_calls       INT DEFAULT 0,
	tool_breakdown   JSONB DEFAULT '{}',
	tasks_touched    UUID[] DEFAULT '{}',
	events_published INT DEFAULT 0,
	memories_created INT DEFAULT 0,
	model_used       TEXT,
	tokens_in        BIGINT DEFAULT 0,
	tokens_out       BIGINT DEFAULT 0,
	estimated_cost   NUMERIC(10,4) DEFAULT 0,
	compliance_score REAL DEFAULT 0,
	compliance_detail JSONB DEFAULT '{}'
);

CREATE INDEX idx_agent_sessions_workspace ON agent_sessions(workspace_id);
CREATE INDEX idx_agent_sessions_agent ON agent_sessions(agent_id);
CREATE INDEX idx_agent_sessions_status ON agent_sessions(status) WHERE status = 'active';
CREATE INDEX idx_agent_sessions_started ON agent_sessions(started_at);

-- +goose Down
DROP TABLE IF EXISTS agent_sessions;
