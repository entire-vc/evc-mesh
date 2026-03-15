-- +goose Up
CREATE TABLE IF NOT EXISTS memories (
	id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	project_id      UUID REFERENCES projects(id) ON DELETE CASCADE,
	agent_id        UUID REFERENCES agents(id) ON DELETE SET NULL,
	key             TEXT NOT NULL,
	content         TEXT NOT NULL,
	scope           TEXT NOT NULL DEFAULT 'project'
	                CHECK (scope IN ('workspace', 'project', 'agent')),
	tags            TEXT[] DEFAULT '{}',
	source_type     TEXT NOT NULL DEFAULT 'agent'
	                CHECK (source_type IN ('agent', 'human', 'system')),
	source_event_id UUID REFERENCES event_bus_messages(id) ON DELETE SET NULL,
	relevance       REAL DEFAULT 1.0 CHECK (relevance >= 0 AND relevance <= 1),
	created_at      TIMESTAMPTZ DEFAULT now(),
	updated_at      TIMESTAMPTZ DEFAULT now(),
	expires_at      TIMESTAMPTZ,
	search_vector   TSVECTOR GENERATED ALWAYS AS (
		setweight(to_tsvector('simple', coalesce(key, '')), 'A') ||
		setweight(to_tsvector('english', coalesce(content, '')), 'B') ||
		setweight(to_tsvector('simple', coalesce(array_to_string(tags, ' '), '')), 'A')
	) STORED,
	CONSTRAINT uq_memory_key_scope UNIQUE (workspace_id, project_id, agent_id, key, scope)
);

CREATE INDEX idx_memories_search ON memories USING GIN(search_vector);
CREATE INDEX idx_memories_workspace_scope ON memories(workspace_id, scope);
CREATE INDEX idx_memories_project ON memories(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX idx_memories_agent ON memories(agent_id) WHERE agent_id IS NOT NULL;
CREATE INDEX idx_memories_tags ON memories USING GIN(tags);
CREATE INDEX idx_memories_relevance ON memories(relevance) WHERE relevance > 0;
CREATE INDEX idx_memories_expires ON memories(expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS memories;
