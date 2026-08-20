-- +goose Up

-- Project-scoped markdown documents. The body lives in object storage under
-- storage_key (see documentStorageKey in internal/service/document_service.go);
-- only the metadata needed to list, order and address a document is kept here,
-- so a project's document tree can be rendered without touching S3.
--
-- parent_id makes the hierarchy; the tree read model itself lands separately.
-- Versioning is deliberately not built here — when it arrives it gets its own
-- table keyed on document_id, which is why the body reference is a single
-- mutable storage_key on the row rather than anything version-shaped.
CREATE TABLE documents (
    id                  UUID PRIMARY KEY,
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_id           UUID REFERENCES documents(id) ON DELETE CASCADE,
    slug                VARCHAR(255) NOT NULL,
    title               VARCHAR(500) NOT NULL,
    storage_key         VARCHAR(1000) NOT NULL,
    position            INT NOT NULL DEFAULT 0,
    created_by          UUID NOT NULL,
    created_by_type     actor_type NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

-- Slug uniqueness among LIVE siblings only, hence the partial index: deletes here
-- are soft, so a plain unique constraint would let a deleted document keep its
-- slug reserved forever and refuse the obvious "delete it and make a new one with
-- the same name". The predicate also has to spell out both halves of "sibling" —
-- Postgres treats NULLs as distinct in a unique index, so a separate index with
-- parent_id IS NULL is what makes top-level slugs unique to each other rather than
-- unconstrained.
CREATE UNIQUE INDEX uq_documents_sibling_slug ON documents (project_id, parent_id, slug)
    WHERE deleted_at IS NULL AND parent_id IS NOT NULL;
CREATE UNIQUE INDEX uq_documents_root_slug ON documents (project_id, slug)
    WHERE deleted_at IS NULL AND parent_id IS NULL;

-- Serves "list this project's documents": the list endpoint filters on
-- project_id + deleted_at and orders by (position, created_at).
CREATE INDEX idx_documents_project ON documents (project_id, position, created_at)
    WHERE deleted_at IS NULL;

-- Serves the child lookup the tree read model will do per node.
CREATE INDEX idx_documents_parent ON documents (parent_id)
    WHERE parent_id IS NOT NULL AND deleted_at IS NULL;

-- Tenant isolation at the schema layer, same shape as saved_views and
-- project_updates: scoped through the document's project (Group B in
-- migrations/20260301030_enable_rls_policies.sql).
ALTER TABLE documents ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_documents ON documents
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = documents.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = documents.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- +goose Down
DROP TABLE IF EXISTS documents;
