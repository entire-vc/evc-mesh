-- +goose Up
-- GET /workspaces/:ws_id/events reads the newest events of a
-- whole workspace (every project the viewer may see) in (created_at, id) order.
-- The existing indexes all lead with project_id, task_id or agent_id, so that
-- query had no index matching its WHERE workspace_id + ORDER BY created_at.
-- The table is small (24h TTL on task events, ~11k rows on prod 13.11.2026),
-- so a plain CREATE INDEX holds its lock for milliseconds.
CREATE INDEX IF NOT EXISTS idx_events_workspace_created
    ON event_bus_messages (workspace_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_events_workspace_created;
