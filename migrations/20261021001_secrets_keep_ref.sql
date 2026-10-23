-- +goose Up

-- Keep provider for the secrets table.
--
-- keep_ref is "keep:<secret id>" once a value has been copied into Keep by
-- a one-off migration tool. It is a NEW column rather than a rewrite of
-- the ciphertext column on purpose: the builtin ciphertext stays in place and is
-- the rollback (clear keep_ref and the row is served from it again). The
-- enc:v<N>: trigger on that column therefore keeps holding unchanged.
-- Additive and nullable, so the code that reads it can ship before or after.
ALTER TABLE secrets ADD COLUMN IF NOT EXISTS keep_ref TEXT;

ALTER TABLE secrets ADD CONSTRAINT chk_secrets_keep_ref_shape
    CHECK (keep_ref IS NULL OR keep_ref ~ '^keep:[0-9a-fA-F-]{36}$');

COMMENT ON COLUMN secrets.keep_ref IS
    'keep:<secret id> when the current value has been migrated to Keep; NULL means '
    'the value is served from the builtin ciphertext, which is kept until the '
    'migration is confirmed, so clearing this column is the rollback.';

-- +goose Down

ALTER TABLE secrets DROP CONSTRAINT IF EXISTS chk_secrets_keep_ref_shape;
ALTER TABLE secrets DROP COLUMN IF EXISTS keep_ref;
