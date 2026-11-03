-- +goose Up

-- Strict existing-session metadata delivery (contract
-- runner-existing-session-metadata-v1). A receiver registers the identity of an
-- already running native session under its own agent key and runner bearer; a
-- sender the receiver listed may queue one bounded text for exactly that
-- identity. Nothing here is read by the ordinary chat claim/commands path, so
-- chat behaviour is unchanged, and an empty allow-list admits nobody.
--
-- Additive only: two new tables. Down drops them.
CREATE TABLE existing_session_receivers (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id           UUID NOT NULL,
    runner_id          UUID NOT NULL REFERENCES ext_agent_chat_runners (id) ON DELETE CASCADE,
    host_id            TEXT NOT NULL CHECK (char_length(host_id) BETWEEN 1 AND 128),
    hostname           TEXT NOT NULL CHECK (char_length(hostname) BETWEEN 1 AND 255),
    session_id         TEXT NOT NULL CHECK (char_length(session_id) BETWEEN 1 AND 128),
    capability         TEXT NOT NULL CHECK (capability = 'readonly_metadata'),
    admission_revision BIGINT NOT NULL CHECK (admission_revision > 0),
    allowed_senders    UUID[] NOT NULL,
    registered_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at         TIMESTAMPTZ,
    CONSTRAINT existing_session_receivers_agent FOREIGN KEY (agent_id, workspace_id)
        REFERENCES agents (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT existing_session_receivers_identity UNIQUE (workspace_id, agent_id, runner_id, host_id, session_id)
);

CREATE TABLE existing_session_deliveries (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    request_id         UUID NOT NULL,
    sender_agent_id    UUID NOT NULL,
    receiver_id        UUID NOT NULL REFERENCES existing_session_receivers (id) ON DELETE CASCADE,
    target             JSONB NOT NULL,
    expected_admission_revision BIGINT NOT NULL,
    capability         TEXT NOT NULL CHECK (capability = 'readonly_metadata'),
    text_sha256        TEXT NOT NULL CHECK (text_sha256 ~ '^[0-9a-f]{64}$'),
    -- Deleted (NULL) once the delivery is acknowledged or failed.
    body               TEXT CHECK (body IS NULL OR octet_length(body) <= 16384),
    state              TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'received', 'acknowledged', 'failed')),
    received_at        TIMESTAMPTZ,
    acknowledged_at    TIMESTAMPTZ,
    result_comment_url TEXT,
    error_code         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT existing_session_deliveries_request UNIQUE (workspace_id, sender_agent_id, request_id)
);

CREATE INDEX existing_session_deliveries_queue ON existing_session_deliveries (receiver_id, created_at) WHERE state = 'queued';

ALTER TABLE existing_session_receivers ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_existing_session_receivers ON existing_session_receivers
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE existing_session_deliveries ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_existing_session_deliveries ON existing_session_deliveries
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP TABLE IF EXISTS existing_session_deliveries;
DROP TABLE IF EXISTS existing_session_receivers;
