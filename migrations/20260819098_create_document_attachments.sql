-- +goose Up

-- Files hung off a document: the images a markdown body references with
-- ![](/api/v1/document-attachments/<id>/download), and any other file uploaded
-- into the page. The bytes live in object storage under storage_key (see
-- documentAttachmentStorageKey in internal/service/document_attachment_service.go);
-- this table is the record that the object exists and whose it is.
--
-- Its own table rather than a column on documents, and rather than reusing
-- artifacts: artifacts hang off a task (artifacts.task_id NOT NULL) and a document
-- has no task, so an attachment stored there would have nothing to point at. Owning
-- the row here is also what lets the tenancy resolver answer "whose is this" from
-- the id alone — see the :att_id entry in internal/middleware/workspace.go.
--
-- Unlike a document body, an attachment IS handed to the browser as a presigned
-- URL: an <img> tag cannot attach an Authorization header, so the image has to be
-- fetched from storage directly. That is why mime_type and name are columns —
-- they are what the presigned URL's Content-Type and Content-Disposition are
-- built from.
CREATE TABLE document_attachments (
    id                  UUID PRIMARY KEY,
    document_id         UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    -- The original filename as uploaded. Display and Content-Disposition only:
    -- nothing addresses an attachment by name, so it carries no uniqueness
    -- constraint and two screenshots called "image.png" are fine.
    name                VARCHAR(500) NOT NULL,
    mime_type           VARCHAR(255) NOT NULL,
    size_bytes          BIGINT NOT NULL,
    storage_key         VARCHAR(1000) NOT NULL,
    uploaded_by         UUID NOT NULL,
    uploaded_by_type    actor_type NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

-- Serves "list this document's attachments": the list endpoint filters on
-- document_id + deleted_at and orders by created_at.
CREATE INDEX idx_document_attachments_document ON document_attachments (document_id, created_at)
    WHERE deleted_at IS NULL;

-- Two delete paths, deliberately different.
--
-- deleted_at is the one the API uses, and it mirrors documents on purpose: a
-- document delete is reversible (deleted_at, not DELETE), so its images have to
-- be recoverable too. Hard-deleting an attachment row when its document is soft
-- deleted would restore a document full of broken images — silent data loss the
-- restored row still claims to have content for. The service leaves the stored
-- object in place for the same reason.
--
-- ON DELETE CASCADE covers only the hard-delete path: a project purged for real
-- takes its documents, and the documents take their attachment rows, so no row is
-- left pointing at a document id that no longer exists. It is not the path any
-- endpoint takes.

-- Tenant isolation at the schema layer. Same shape as the documents policy, with
-- the extra join hop: an attachment names its tenant through its document, which
-- names it through its project. Both USING and WITH CHECK, so the policy governs
-- writes as well as reads — a WITH CHECK-less policy would let a session insert a
-- row it could not then see.
ALTER TABLE document_attachments ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_document_attachments ON document_attachments
    USING (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_attachments.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_attachments.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- +goose Down
DROP TABLE IF EXISTS document_attachments;
