-- +goose Up
-- CONTRACT-2, step 1 of 2 (additive). The code now reads and conflicts on
-- extension_id; `type` stays as a derived column the previous image still reads,
-- so rolling the code back needs no rollback of this migration. DROP COLUMN
-- type / UNIQUE (project_id, type) and the fill trigger go in the next release.
--
-- extension_id is a function of type (team_relay -> team-relay, otherwise the
-- same string) and UNIQUE (project_id, type) already holds, so the new unique
-- index cannot find a duplicate. The trigger from 20261015005 filled every row.
ALTER TABLE project_integrations ALTER COLUMN type DROP NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_project_integrations_project_extension
    ON project_integrations (project_id, extension_id);

-- +goose Down
DROP INDEX IF EXISTS uq_project_integrations_project_extension;
-- type is still written by the code, so no row can hold NULL here.
ALTER TABLE project_integrations ALTER COLUMN type SET NOT NULL;
