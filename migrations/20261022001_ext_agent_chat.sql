-- +goose Up

-- Chat with Agent extension (agent-chat). Tables follow the single DDL in
-- agent-chat-openapi §4 (v6), which already carries the runner-protocol deltas.
-- ext_agent_chat_commands (runner orders) is a separate unit, added later.
-- Naming rule: ext_<extension_id>_<name>, extension_id "agent-chat" -> agent_chat.
-- Additive only: new tables; projects and agents only gain a UNIQUE (id, workspace_id)
-- constraint, the target the workspace-aware foreign keys below need.
--
-- Deviations from the draft, all about tenancy:
--  * every child table carries workspace_id, and every parent reference is a
--    composite (parent_id, workspace_id) FK, so a row cannot point at a parent
--    of another workspace even though RLS only looks at the row's own column;
--  * the same holds for threads.project_id / agent_id and turns.agent_id: they are
--    composite (id, workspace_id) FKs to projects / agents, which get the matching
--    UNIQUE (id, workspace_id) first (the constraints are dropped again by down);
--  * RLS reads app.current_workspace_id, the setting the middleware actually
--    sets (the draft says app.workspace_id);
--  * the draft's ON DELETE SET NULL is kept on the three nullable references
--    (messages.turn_id, turns.user_message_id, turns.agent_message_id), written
--    as SET NULL (<column>): a plain SET NULL on a composite FK would also null
--    the NOT NULL workspace_id. The column list needs PostgreSQL 15+; CI and
--    run 16.

ALTER TABLE projects ADD CONSTRAINT uq_projects_ws_ext_agent_chat_fk UNIQUE (id, workspace_id);
ALTER TABLE agents ADD CONSTRAINT uq_agents_ws_ext_agent_chat_fk UNIQUE (id, workspace_id);

CREATE TABLE ext_agent_chat_threads (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id      UUID NOT NULL,
    agent_id        UUID NOT NULL,
    user_id         UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title           TEXT NOT NULL DEFAULT '' CHECK (char_length(title) <= 120),
    title_is_custom BOOLEAN NOT NULL DEFAULT false,
    last_message_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- soft delete; rows and S3 objects are purged 30 days later
    deleted_at      TIMESTAMPTZ,
    -- execution generation: +1 on every claim, requeue, Stop and delete (protocol §5.4)
    execution_epoch BIGINT NOT NULL DEFAULT 0,
    -- runner_id, harness, profile, session_id; never returned by the API
    runtime         JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT uq_ext_agent_chat_threads_ws UNIQUE (id, workspace_id),
    CONSTRAINT fk_ext_agent_chat_threads_project FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT fk_ext_agent_chat_threads_agent FOREIGN KEY (agent_id, workspace_id)
        REFERENCES agents (id, workspace_id)
);

CREATE INDEX ext_agent_chat_threads_list
    ON ext_agent_chat_threads (project_id, user_id, agent_id, last_message_at DESC NULLS LAST)
    WHERE deleted_at IS NULL;
CREATE INDEX ext_agent_chat_threads_purge
    ON ext_agent_chat_threads (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE ext_agent_chat_turns (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    thread_id           UUID NOT NULL,
    -- denormalised so a claim needs no join
    agent_id            UUID NOT NULL,
    -- FKs to messages are added below, after that table exists
    user_message_id     UUID,
    agent_message_id    UUID,
    state               TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'claimed', 'streaming', 'done', 'failed', 'cancelled', 'expired')),
    runner_id           UUID,
    attempt             INTEGER NOT NULL DEFAULT 0,
    -- rotated on every claim; server-only, in no user-facing schema
    claim_token         TEXT,
    -- copy of thread.execution_epoch at claim time
    execution_epoch     BIGINT,
    lease_until         TIMESTAMPTZ,
    last_seq            INTEGER NOT NULL DEFAULT 0,
    -- seq of the accepted terminal event (replay)
    terminal_seq        INTEGER,
    -- created_at + offline_turn_ttl
    expires_at          TIMESTAMPTZ NOT NULL,
    -- for max_turn_seconds (T15)
    first_claimed_at    TIMESTAMPTZ,
    cancel_requested    BOOLEAN NOT NULL DEFAULT false,
    cancel_requested_at TIMESTAMPTZ,
    stop_reason         TEXT,
    error_code          TEXT,
    -- accepted start (T3)
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ,
    cost_usd            NUMERIC(12, 6),
    tokens_in           INTEGER,
    tokens_out          INTEGER,
    session_id          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ext_agent_chat_turns_ws UNIQUE (id, workspace_id),
    CONSTRAINT fk_ext_agent_chat_turns_agent FOREIGN KEY (agent_id, workspace_id)
        REFERENCES agents (id, workspace_id),
    CONSTRAINT fk_ext_agent_chat_turns_thread FOREIGN KEY (thread_id, workspace_id)
        REFERENCES ext_agent_chat_threads (id, workspace_id) ON DELETE CASCADE
);

-- At most one unfinished turn per thread, queued included: a second turn cannot be created on send.
CREATE UNIQUE INDEX ext_agent_chat_turns_one_active
    ON ext_agent_chat_turns (thread_id) WHERE state IN ('queued', 'claimed', 'streaming');
CREATE INDEX ext_agent_chat_turns_claim
    ON ext_agent_chat_turns (agent_id, state, created_at) WHERE state = 'queued';
CREATE INDEX ext_agent_chat_turns_lease
    ON ext_agent_chat_turns (lease_until) WHERE state IN ('claimed', 'streaming');
CREATE INDEX ext_agent_chat_turns_expiry
    ON ext_agent_chat_turns (expires_at) WHERE state = 'queued';

CREATE TABLE ext_agent_chat_messages (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    thread_id         UUID NOT NULL,
    role              TEXT NOT NULL CHECK (role IN ('user', 'agent', 'system')),
    body              TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'done'
        CHECK (status IN ('queued', 'streaming', 'done', 'failed', 'cancelled')),
    -- agent reply cut at 256 KiB
    truncated         BOOLEAN NOT NULL DEFAULT false,
    turn_id           UUID,
    -- not_delivered, runner_lost, turn_timeout, turn_undeliverable, runner_error, session_failed, ...
    error_code        TEXT,
    client_message_id TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ext_agent_chat_messages_ws UNIQUE (id, workspace_id),
    CONSTRAINT fk_ext_agent_chat_messages_thread FOREIGN KEY (thread_id, workspace_id)
        REFERENCES ext_agent_chat_threads (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT fk_ext_agent_chat_messages_turn FOREIGN KEY (turn_id, workspace_id)
        REFERENCES ext_agent_chat_turns (id, workspace_id) ON DELETE SET NULL (turn_id)
);

-- History page: keyset by (created_at, id) within a thread.
CREATE INDEX ext_agent_chat_messages_page
    ON ext_agent_chat_messages (thread_id, created_at DESC, id DESC);
-- Send idempotency: (thread, client_message_id) is unique.
CREATE UNIQUE INDEX ext_agent_chat_messages_idem
    ON ext_agent_chat_messages (thread_id, client_message_id) WHERE client_message_id IS NOT NULL;

ALTER TABLE ext_agent_chat_turns
    ADD CONSTRAINT fk_ext_agent_chat_turns_user_message FOREIGN KEY (user_message_id, workspace_id)
        REFERENCES ext_agent_chat_messages (id, workspace_id) ON DELETE SET NULL (user_message_id),
    ADD CONSTRAINT fk_ext_agent_chat_turns_agent_message FOREIGN KEY (agent_message_id, workspace_id)
        REFERENCES ext_agent_chat_messages (id, workspace_id) ON DELETE SET NULL (agent_message_id);

CREATE TABLE ext_agent_chat_attachments (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    thread_id    UUID NOT NULL,
    -- NULL = pending (uploaded or presigned, not yet sent)
    message_id   UUID,
    s3_key       TEXT NOT NULL,
    file_name    TEXT NOT NULL,
    mime         TEXT NOT NULL,
    size         BIGINT NOT NULL CHECK (size > 0),
    width        INTEGER,
    height       INTEGER,
    -- set when the object is confirmed in S3; NULL = not uploaded yet
    uploaded_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_ext_agent_chat_attachments_thread FOREIGN KEY (thread_id, workspace_id)
        REFERENCES ext_agent_chat_threads (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT fk_ext_agent_chat_attachments_message FOREIGN KEY (message_id, workspace_id)
        REFERENCES ext_agent_chat_messages (id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX ext_agent_chat_attachments_msg ON ext_agent_chat_attachments (message_id);
CREATE INDEX ext_agent_chat_attachments_pending
    ON ext_agent_chat_attachments (created_at) WHERE message_id IS NULL;

-- Optional: "agent is working: ..." lines, short labels without tool output.
CREATE TABLE ext_agent_chat_tool_events (
    turn_id      UUID NOT NULL,
    seq          INTEGER NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    label        TEXT NOT NULL CHECK (char_length(label) <= 200),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (turn_id, seq),
    CONSTRAINT fk_ext_agent_chat_tool_events_turn FOREIGN KEY (turn_id, workspace_id)
        REFERENCES ext_agent_chat_turns (id, workspace_id) ON DELETE CASCADE
);

-- Tenant isolation, same shape as agent_api_keys.
ALTER TABLE ext_agent_chat_threads ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_agent_chat_threads ON ext_agent_chat_threads
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE ext_agent_chat_turns ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_agent_chat_turns ON ext_agent_chat_turns
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE ext_agent_chat_messages ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_agent_chat_messages ON ext_agent_chat_messages
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE ext_agent_chat_attachments ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_agent_chat_attachments ON ext_agent_chat_attachments
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

ALTER TABLE ext_agent_chat_tool_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_ext_agent_chat_tool_events ON ext_agent_chat_tool_events
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
DROP TABLE IF EXISTS ext_agent_chat_tool_events;
DROP TABLE IF EXISTS ext_agent_chat_attachments;
-- messages and turns reference each other; drop the turns -> messages FKs first
ALTER TABLE IF EXISTS ext_agent_chat_turns
    DROP CONSTRAINT IF EXISTS fk_ext_agent_chat_turns_user_message,
    DROP CONSTRAINT IF EXISTS fk_ext_agent_chat_turns_agent_message;
DROP TABLE IF EXISTS ext_agent_chat_messages;
DROP TABLE IF EXISTS ext_agent_chat_turns;
DROP TABLE IF EXISTS ext_agent_chat_threads;
ALTER TABLE agents DROP CONSTRAINT IF EXISTS uq_agents_ws_ext_agent_chat_fk;
ALTER TABLE projects DROP CONSTRAINT IF EXISTS uq_projects_ws_ext_agent_chat_fk;
