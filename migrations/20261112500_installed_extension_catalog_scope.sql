-- +goose Up
-- A catalog install taken from a Spark exact receipt
-- (catalog.json?scope=scoped&slug=&version=) is pinned to that version:
-- autoupdate and the default catalogue's latest never move it, and the
-- blocklist pass re-reads its exact receipt. Every existing row is 'default'.
ALTER TABLE installed_extensions
    ADD COLUMN catalog_scope TEXT NOT NULL DEFAULT 'default'
        CONSTRAINT installed_extensions_catalog_scope_check CHECK (catalog_scope IN ('default', 'scoped'));

-- +goose Down
-- Dropping the column would turn every pinned install into one that follows
-- the default catalogue's latest; refuse while any exists.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM installed_extensions WHERE catalog_scope = 'scoped') THEN
        RAISE EXCEPTION 'scoped catalog installs exist; choose a default version for them before downgrading';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE installed_extensions
    DROP COLUMN IF EXISTS catalog_scope;
