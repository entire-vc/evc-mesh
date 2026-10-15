-- +goose Up
-- W1.8 EXPAND: project_integrations becomes the shared per-project binding
-- table of the extension registry. `type` stays (readers and the old image
-- keep using it until the CONTRACT step); extension_id is added next to it.
--
-- Additive only: no DROP, no NOT NULL, no rename. agent_key, settings and the
-- last_sync_* / key_expires_at columns are not touched, so no credential value
-- changes (the encrypted-key trigger fires only when agent_key itself changes).
--
-- extension_id is the manifest id. For Team Relay the manifest id is
-- 'team-relay' (the id pattern has no underscore) while type stays 'team_relay'.
ALTER TABLE project_integrations ADD COLUMN extension_id TEXT NULL;

UPDATE project_integrations
SET extension_id = CASE type WHEN 'team_relay' THEN 'team-relay' ELSE type END
WHERE extension_id IS NULL;

CREATE INDEX ix_project_integrations_extension ON project_integrations(project_id, extension_id);

-- The previous image writes rows without extension_id. This keeps the column
-- filled for any writer, so rolling the image back never leaves NULLs behind.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION project_integrations_fill_extension_id()
RETURNS trigger AS $$
BEGIN
    IF NEW.extension_id IS NULL
       OR (TG_OP = 'UPDATE'
           AND NEW.type IS DISTINCT FROM OLD.type
           AND NEW.extension_id IS NOT DISTINCT FROM OLD.extension_id) THEN
        NEW.extension_id := CASE NEW.type WHEN 'team_relay' THEN 'team-relay' ELSE NEW.type END;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_project_integrations_fill_extension_id ON project_integrations;

CREATE TRIGGER trg_project_integrations_fill_extension_id
    BEFORE INSERT OR UPDATE ON project_integrations
    FOR EACH ROW
    EXECUTE FUNCTION project_integrations_fill_extension_id();

-- +goose Down
DROP TRIGGER IF EXISTS trg_project_integrations_fill_extension_id ON project_integrations;
DROP FUNCTION IF EXISTS project_integrations_fill_extension_id();
DROP INDEX IF EXISTS ix_project_integrations_extension;
ALTER TABLE project_integrations DROP COLUMN IF EXISTS extension_id;
