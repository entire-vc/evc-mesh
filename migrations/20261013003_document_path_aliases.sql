-- +goose Up
-- Former slug paths of documents that were moved in the tree (#ed10e6aa). A
-- document's id survives a move, but its path did not: cards and agents refer
-- to docs by path ("specs/threat-model") and by bare slug, and every
-- reorganisation turned those references into 404s. When a document (or a
-- folder with its subtree) changes parent, the old full path of each moved
-- document is recorded here, in the same transaction as the move.
--
-- One alias per (project, path): the newest move to vacate a path owns it. A
-- live document at the same path always wins — the resolver walks the live
-- tree first and only consults this table on a miss — so an alias can never
-- shadow a real page. Purely additive: nothing existing reads this table.
CREATE TABLE IF NOT EXISTS document_path_aliases (
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    document_id  UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, path)
);

-- Serves the ON DELETE CASCADE from documents and "every alias of this doc".
CREATE INDEX IF NOT EXISTS idx_document_path_aliases_document ON document_path_aliases (document_id);

-- Same tenant rule as documents: scoped through the alias's project.
ALTER TABLE document_path_aliases ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_document_path_aliases ON document_path_aliases
    USING (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = document_path_aliases.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM projects p
            WHERE p.id = document_path_aliases.project_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- +goose Down
DROP TABLE IF EXISTS document_path_aliases;
