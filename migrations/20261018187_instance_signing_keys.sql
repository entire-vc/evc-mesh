-- +goose Up
-- Instance signing keys for the context JWT of external-extension iframe
-- screens (published at /api/v1/.well-known/jwks.json). The private half is
-- encrypted at rest; a replaced key keeps its row (retired_at) so the JWKS can
-- publish it for the overlap.
CREATE TABLE instance_signing_keys (
    kid         VARCHAR(32) PRIMARY KEY,
    private_enc TEXT        NOT NULL CHECK (private_enc LIKE 'enc:v1:%'),
    public_jwk  JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at  TIMESTAMPTZ
);

-- At most one active key: a concurrent activation loses on this index.
CREATE UNIQUE INDEX instance_signing_keys_one_active
    ON instance_signing_keys ((true)) WHERE retired_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS instance_signing_keys;
