-- +goose Up

-- Project reader (docs/project-reader.md): a service agent bound to exactly one
-- project. ProjectReaderGuard lets it make only the GET requests on an explicit
-- allowlist whose project is this one; everything else is 403.
--
-- The marker is a column, not a capabilities key: the capabilities blob has two
-- shapes and is rewritten by PATCH, profile and import, and a lost key would
-- turn the reader into a plain workspace viewer. Only the issuing INSERT writes
-- this column; no UPDATE in the code sets or clears it.
--
-- No ON DELETE action on purpose: a nulled marker would be exactly that silent
-- widening, so a project cannot be hard-deleted while a reader row points at it.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS reader_project_id UUID NULL REFERENCES projects(id);

CREATE INDEX IF NOT EXISTS idx_agents_reader_project
    ON agents (reader_project_id)
    WHERE reader_project_id IS NOT NULL;

-- +goose Down

-- A binary older than this migration does not know the column: every live
-- reader would authenticate as a plain viewer of its home workspace. Revoke
-- them first (DELETE /api/v1/agents/:id), then roll back.
-- +goose StatementBegin
DO $$
DECLARE
    live integer;
BEGIN
    SELECT count(*) INTO live FROM agents
    WHERE reader_project_id IS NOT NULL AND deleted_at IS NULL;
    IF live > 0 THEN
        RAISE EXCEPTION '% live project reader(s): DELETE /api/v1/agents/:id for each before rolling back', live;
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS idx_agents_reader_project;
ALTER TABLE agents DROP COLUMN IF EXISTS reader_project_id;
