-- +goose Up

-- keep_ref holds the ref Keep actually stores. 20261021001
-- allowed only keep:<uuid>, but the writer (a one-off migration tool via
-- HTTPKeepClient.PutValue) and the reader (secretstore.Open -> GetValue) both
-- use Keep's ref alphabet, e.g.
-- keep:mesh-secret.agent.<agent uuid>.<NAME>.<workspace uuid>, so every
-- migrated row was refused with 23514. The CHECK now mirrors keepRefRe in
-- internal/extensions/secretstore/keep_http.go: one leading letter or digit,
-- then up to 127 of [A-Za-z0-9._-], and no "..". A keep:<uuid> ref still fits.
ALTER TABLE secrets DROP CONSTRAINT IF EXISTS chk_secrets_keep_ref_shape;

ALTER TABLE secrets ADD CONSTRAINT chk_secrets_keep_ref_shape
    CHECK (keep_ref IS NULL OR (
        keep_ref ~ '^keep:[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
        AND position('..' in keep_ref) = 0
    ));

COMMENT ON COLUMN secrets.keep_ref IS
    'keep:<keep ref> when the current value has been migrated to Keep; NULL means '
    'the value is served from the builtin ciphertext, which is kept until the '
    'migration is confirmed, so clearing this column is the rollback.';

-- +goose Down

-- The old shape comes back NOT VALID: rows already pointed at Keep keep their
-- ref (clearing it is the migration tool's rollback, not a schema
-- step), and only new writes are held to keep:<uuid> again.
ALTER TABLE secrets DROP CONSTRAINT IF EXISTS chk_secrets_keep_ref_shape;

ALTER TABLE secrets ADD CONSTRAINT chk_secrets_keep_ref_shape
    CHECK (keep_ref IS NULL OR keep_ref ~ '^keep:[0-9a-fA-F-]{36}$') NOT VALID;

COMMENT ON COLUMN secrets.keep_ref IS
    'keep:<secret id> when the current value has been migrated to Keep; NULL means '
    'the value is served from the builtin ciphertext, which is kept until the '
    'migration is confirmed, so clearing this column is the rollback.';
