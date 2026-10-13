-- +goose Up

-- Team Relay embed attachments (#4e97f465 A4): images a mounted document
-- references must display in Mesh. The share's markdown keeps pointing at
-- share-relative paths; Mesh keeps its own copy of each referenced binary as a
-- document attachment, and the serve side rewrites the link at render time
-- (never in the stored source — WriteBack would push the rewrite to the
-- original).

-- documents.embed_synced_sha256: the source_sha256 of the body whose embeds
-- were last imported. NULL means "embeds not imported (yet)" — true for every
-- row created before this migration, which is exactly the population the mount
-- sync's backfill pass is written for. Nullable and never NOT NULL later: a
-- body with no embeds is still legitimately marked, so the column doubles as
-- "this body hash's embed pass ran" rather than "has embeds".
ALTER TABLE documents ADD COLUMN IF NOT EXISTS embed_synced_sha256 VARCHAR(64);

-- document_attachments.source_*: provenance of an attachment copied from a
-- Team Relay share, for the same dedup the document copies use. NULL for every
-- human-uploaded attachment (existing rows and future ones) — only the mount
-- sync's embed importer writes these.
ALTER TABLE document_attachments ADD COLUMN IF NOT EXISTS source_path VARCHAR(1000);
ALTER TABLE document_attachments ADD COLUMN IF NOT EXISTS source_sha256 VARCHAR(64);

-- One live imported embed per (document, share path): a re-import of a changed
-- image soft-deletes the old row before creating the new one, so the partial
-- index (deleted_at IS NULL) is what keeps the pair unique at any moment.
-- source_path IS NULL keeps human uploads out of the constraint entirely.
CREATE UNIQUE INDEX IF NOT EXISTS uq_document_attachments_source
    ON document_attachments (document_id, source_path)
    WHERE deleted_at IS NULL AND source_path IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_document_attachments_source;
ALTER TABLE document_attachments DROP COLUMN IF EXISTS source_sha256;
ALTER TABLE document_attachments DROP COLUMN IF EXISTS source_path;
ALTER TABLE documents DROP COLUMN IF EXISTS embed_synced_sha256;
