-- +goose Up

-- Two-step uploads (docs/mcp-reference.md "Uploading files"): the caller
-- declares a file, PUTs its bytes straight to object storage on a presigned
-- URL, then confirms. A row is one declared upload. The bytes land under
-- staging_key; confirm checks them against size_bytes and sha256 and hands
-- them to the ordinary document-attachment or artifact upload, which writes
-- the real row and object. result_id is that row's id, so a repeated confirm
-- answers with the same result instead of writing a second one.
--
-- target_id is the document (kind document_attachment) or the task (kind
-- artifact). No foreign key: it points at one of two tables, and the upload
-- path re-checks the target in the caller's workspace on confirm anyway.
--
-- claimed_until is a lease taken by the confirm in progress, so two
-- concurrent confirms of one upload never both reach the upload path. The
-- sweeper skips a row while its lease runs.
--
-- pending_result_id is the id the attachment or artifact row will get, fixed
-- by the first confirm that takes the lease and reused by every later one. If
-- a confirm wrote the row but failed before confirmed_at was recorded, the
-- next confirm finds that row by this id and records it, instead of writing
-- the file a second time.
CREATE TABLE upload_sessions (
    id              UUID PRIMARY KEY,
    workspace_id    UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK (kind IN ('document_attachment', 'artifact')),
    target_id       UUID NOT NULL,
    name            TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 255),
    mime_type       TEXT NOT NULL,
    artifact_type   TEXT NULL,
    size_bytes      BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256          TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    staging_key     TEXT NOT NULL UNIQUE,
    created_by      UUID NOT NULL,
    created_by_type TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    confirmed_at    TIMESTAMPTZ NULL,
    result_id       UUID NULL,
    claimed_until   TIMESTAMPTZ NULL,
    pending_result_id UUID NULL,
    CHECK ((confirmed_at IS NULL) = (result_id IS NULL))
);

-- The sweeper's two scans: open uploads past expiry, confirmed ones past
-- their retention.
CREATE INDEX idx_upload_sessions_open_expiry
    ON upload_sessions (expires_at) WHERE confirmed_at IS NULL;
CREATE INDEX idx_upload_sessions_confirmed
    ON upload_sessions (confirmed_at) WHERE confirmed_at IS NOT NULL;

-- Tenant isolation, same shape as ext_agent_chat_attachments.
ALTER TABLE upload_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_upload_sessions ON upload_sessions
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down

-- Open uploads lose their session: their staged objects under uploads/ are
-- no longer swept by the API. Remove them with
-- `mc rm --recursive --force <alias>/<bucket>/uploads/` after the rollback.
DROP TABLE IF EXISTS upload_sessions;
