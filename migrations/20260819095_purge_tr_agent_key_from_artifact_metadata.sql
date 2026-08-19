-- +goose Up
-- Removes the TeamRelay share agent key from artifacts.metadata.
--
-- artifact_service persisted tr_agent_key alongside tr_public_url so the UI could
-- build an authenticated open-URL without a server round-trip. The value is a
-- long-lived, non-rotating share credential that we encrypt at rest in
-- project_integrations — storing it in the clear here made the weakest copy the
-- one that defined its protection, and GET /tasks/:id/context served it to any
-- caller with workspace access (that read path missed stripSensitiveMetadata).
--
-- The writer is removed in the same change, so this is a one-shot cleanup rather
-- than a recurring sweep. Measured on prod before writing this: 186 of 573
-- artifact rows carried the key, spanning 10 distinct credentials written
-- 2026-06-08 through 2026-08-11.
--
-- Only the one key is dropped; tr_public_url and every other metadata field are
-- left untouched. The WHERE clause keeps the write off the other 387 rows — the
-- table has no updated_at to disturb (only created_at), but rewriting untouched
-- rows would still churn them for nothing.
--
-- Verified against a throwaway postgres:16 before merge, over rows carrying
-- key+url, url only, key only, empty metadata and NULL metadata: the key count
-- goes 2 -> 0, tr_public_url and unrelated fields survive, and the NULL row is
-- not touched (jsonb `-` on NULL would yield NULL, but `?` excludes it anyway).
UPDATE artifacts
SET metadata = metadata - 'tr_agent_key'
WHERE metadata ? 'tr_agent_key';

-- +goose Down
-- Irreversible by design: the down-migration cannot restore a secret it was the
-- point of this change to erase, and re-introducing it would be a regression
-- rather than a rollback. Reverting the application code is the rollback path.
SELECT 1;
