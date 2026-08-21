-- +goose Up

-- The history GET /api/v1/memories/:id/revisions guards is scoped to a
-- workspace (see memory_handler.go's Revisions), and until now that scope was
-- read by looking up the memory itself in `memories`. That works right up
-- until a `forget`: the row the check depends on is exactly the row the
-- delete just removed, so the endpoint 404s on the one case
-- memory_revisions.action = 'forgotten' exists to serve. The audit trail was
-- written and had no reader.
--
-- Denormalizing workspace_id onto memory_revisions gives the authorization
-- check a value that survives the memory it describes, matching how the
-- table's own memory_id column is deliberately NOT a cascading FK for the
-- same reason (see 20260821002's comment on that column).

ALTER TABLE memory_revisions
    ADD COLUMN workspace_id UUID NULL;

-- Backfill from the still-live memories row wherever one exists. This is
-- necessarily partial: a memory_revisions row whose memory was forgotten
-- BEFORE this migration ran has no live `memories` row left to join against,
-- and its workspace is unrecoverable — that row's workspace_id stays NULL
-- forever. Named here rather than discovered later: this backfill closes the
-- gap for every revision going forward (Upsert and Forget both populate
-- workspace_id directly from now on) and for every currently-live memory's
-- history, but not for revisions of memories already gone.
UPDATE memory_revisions mr
SET workspace_id = m.workspace_id
FROM memories m
WHERE mr.memory_id = m.id
  AND mr.workspace_id IS NULL;

-- Supports the authorization check in the Revisions handler (equality lookup
-- against the caller's workspace) and any future workspace-scoped audit query.
CREATE INDEX ix_memory_revisions_workspace ON memory_revisions (workspace_id);

-- +goose Down
DROP INDEX IF EXISTS ix_memory_revisions_workspace;
ALTER TABLE memory_revisions DROP COLUMN IF EXISTS workspace_id;
