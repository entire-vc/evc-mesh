-- +goose Up
-- +goose StatementBegin

CREATE TABLE notification_preferences (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    agent_id UUID REFERENCES agents(id) ON DELETE CASCADE,
    channel VARCHAR(50) NOT NULL DEFAULT 'web_push',
    events TEXT[] NOT NULL DEFAULT '{task.assigned,task.status_changed,comment.created}',
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    config JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_single_actor CHECK (
        (user_id IS NOT NULL AND agent_id IS NULL) OR
        (user_id IS NULL AND agent_id IS NOT NULL)
    )
);

CREATE INDEX idx_notif_prefs_user ON notification_preferences(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX idx_notif_prefs_agent ON notification_preferences(agent_id) WHERE agent_id IS NOT NULL;
CREATE INDEX idx_notif_prefs_workspace ON notification_preferences(workspace_id);

CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    event_type VARCHAR(100) NOT NULL,
    title VARCHAR(500) NOT NULL,
    body TEXT DEFAULT '',
    metadata JSONB DEFAULT '{}',
    is_read BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_user_unread ON notifications(user_id, is_read) WHERE is_read = false;
CREATE INDEX idx_notifications_created ON notifications(created_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS notification_preferences;

-- +goose StatementEnd
