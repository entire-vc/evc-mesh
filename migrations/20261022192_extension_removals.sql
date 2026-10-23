-- +goose Up
-- Removal log for extensions. Additive: one new table and two triggers on
-- installed_extensions. Every way a row leaves installed_extensions (an
-- uninstall path, manual SQL) is recorded here by the trigger, so the purge job
-- (off by default) never depends on a caller remembering to mark the removal.
-- Retention is applied by the job (removed_at + retention), not stored per row.
CREATE TABLE extension_removals (
    extension_id VARCHAR(64) PRIMARY KEY,
    removed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    purged_at    TIMESTAMPTZ
);

-- +goose StatementBegin
CREATE FUNCTION record_extension_removal() RETURNS trigger AS $$
BEGIN
    INSERT INTO extension_removals (extension_id) VALUES (OLD.extension_id)
    ON CONFLICT (extension_id) DO UPDATE SET removed_at = now(), purged_at = NULL;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER installed_extensions_record_removal
    AFTER DELETE ON installed_extensions
    FOR EACH ROW EXECUTE FUNCTION record_extension_removal();

-- +goose StatementBegin
CREATE FUNCTION clear_extension_removal() RETURNS trigger AS $$
BEGIN
    -- The purge job holds this same per-id lock for its whole transaction, so an
    -- install either commits before the purge re-checks (the purge then skips) or
    -- waits until the purge has finished; it can never interleave with the deletes.
    PERFORM pg_advisory_xact_lock(hashtext('ext-purge:' || NEW.extension_id));
    DELETE FROM extension_removals WHERE extension_id = NEW.extension_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- A reinstall cancels the pending purge.
CREATE TRIGGER installed_extensions_clear_removal
    BEFORE INSERT ON installed_extensions
    FOR EACH ROW EXECUTE FUNCTION clear_extension_removal();

-- +goose Down
DROP TRIGGER IF EXISTS installed_extensions_clear_removal ON installed_extensions;
DROP TRIGGER IF EXISTS installed_extensions_record_removal ON installed_extensions;
DROP FUNCTION IF EXISTS clear_extension_removal();
DROP FUNCTION IF EXISTS record_extension_removal();
DROP TABLE IF EXISTS extension_removals;
