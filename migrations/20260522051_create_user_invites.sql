-- +goose Up
CREATE TABLE user_invites (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email        TEXT        NOT NULL,
    role         TEXT        NOT NULL DEFAULT 'member',
    token        TEXT        NOT NULL UNIQUE,
    invited_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    expires_at   TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '7 days',
    accepted_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_user_invites_workspace ON user_invites(workspace_id);
CREATE INDEX idx_user_invites_token     ON user_invites(token);
CREATE INDEX idx_user_invites_email     ON user_invites(email);

-- +goose Down
DROP TABLE IF EXISTS user_invites;
