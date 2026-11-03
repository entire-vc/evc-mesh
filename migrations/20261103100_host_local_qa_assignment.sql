-- +goose Up

-- Host-local QA assignment v1 (capability host_local_qa_assignment_v1). A
-- separate, capability-scoped receiver admission and delivery ledger: nothing
-- here reuses or is read by the legacy existing_session_* tables, so the
-- readonly_metadata path keeps its strict validation untouched, and the new
-- routes stay refused while HOST_LOCAL_QA_ASSIGNMENT_ENABLED is not "true".
--
-- Additive only: two new tables. Down drops them (nothing else references them).
CREATE TABLE host_local_qa_receivers (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id           UUID NOT NULL,
    runner_id          UUID NOT NULL REFERENCES ext_agent_chat_runners (id) ON DELETE CASCADE,
    host_id            TEXT NOT NULL CHECK (char_length(host_id) BETWEEN 1 AND 128),
    hostname           TEXT NOT NULL CHECK (char_length(hostname) BETWEEN 1 AND 255),
    session_id         TEXT NOT NULL CHECK (char_length(session_id) BETWEEN 1 AND 128),
    capability         TEXT NOT NULL CHECK (capability = 'host_local_qa_assignment_v1'),
    policy_sha256      TEXT NOT NULL CHECK (policy_sha256 ~ '^[0-9a-f]{64}$'),
    producer           JSONB NOT NULL,
    expires_at         TIMESTAMPTZ NOT NULL,
    admission_revision BIGINT NOT NULL CHECK (admission_revision > 0),
    allowed_senders    UUID[] NOT NULL CHECK (cardinality(allowed_senders) >= 1),
    registered_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at         TIMESTAMPTZ,
    CONSTRAINT host_local_qa_receivers_agent FOREIGN KEY (agent_id, workspace_id)
        REFERENCES agents (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT host_local_qa_receivers_identity UNIQUE (workspace_id, agent_id, runner_id, host_id, session_id)
);

CREATE TABLE host_local_qa_deliveries (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    request_id         UUID NOT NULL,
    sender_agent_id    UUID NOT NULL,
    receiver_id        UUID NOT NULL REFERENCES host_local_qa_receivers (id) ON DELETE CASCADE,
    -- The immutable sender envelope without the text (the text is the frozen brief).
    envelope           JSONB NOT NULL,
    expected_admission_revision BIGINT NOT NULL CHECK (expected_admission_revision > 0),
    expires_at         TIMESTAMPTZ NOT NULL,
    state              TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'received', 'acknowledged', 'failed', 'uncertain')),
    received_at        TIMESTAMPTZ,
    acknowledged_at    TIMESTAMPTZ,
    observed_at        TIMESTAMPTZ,
    result_comment_url TEXT,
    error_code         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT host_local_qa_deliveries_request UNIQUE (workspace_id, sender_agent_id, request_id)
);

CREATE INDEX host_local_qa_deliveries_queue ON host_local_qa_deliveries (receiver_id, created_at) WHERE state = 'queued';

ALTER TABLE host_local_qa_receivers ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_host_local_qa_receivers ON host_local_qa_receivers
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE host_local_qa_deliveries ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_host_local_qa_deliveries ON host_local_qa_deliveries
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP TABLE IF EXISTS host_local_qa_deliveries;
DROP TABLE IF EXISTS host_local_qa_receivers;
