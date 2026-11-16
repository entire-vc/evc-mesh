-- +goose Up

-- Mesh as an OpenID Connect provider for people signing in to other company
-- products (Keep W8). The same authorization server, a separate
-- identity flow: scope `openid` without `mesh` proves who the user is and
-- nothing else, so it needs no workspace, no grant, no connector agent and no
-- refresh token.
--
-- 1. registration_type gains 'static': clients the operator configures with
--    MESH_OIDC_CLIENTS and the API upserts at start. Only they may ask for
--    `openid`; a DCR or CIMD client never can.
ALTER TABLE oauth_clients DROP CONSTRAINT IF EXISTS oauth_clients_registration_type_check;
ALTER TABLE oauth_clients ADD CONSTRAINT oauth_clients_registration_type_check
    CHECK (registration_type IN ('cimd', 'dcr', 'static'));

-- 2. One-time codes of the identity flow. Not oauth_authorization_codes: that
--    table's grant_id is NOT NULL and its RLS policy reaches the workspace
--    through the grant, and an identity code has neither. A code is a 60 s,
--    single-use, PKCE-bound proof that user_id consented to client_id; it is
--    marked used (not deleted) so a replay is a provable invalid_grant, and the
--    periodic purge removes it after the same retention as other codes.
CREATE TABLE oauth_identity_codes (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash      TEXT NOT NULL UNIQUE,
    client_id      TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- OpenID Connect nonce from the authorize request, echoed into id_token.
    nonce          TEXT NULL,
    scope          TEXT NOT NULL DEFAULT 'openid',
    expires_at     TIMESTAMPTZ NOT NULL,
    used_at        TIMESTAMPTZ NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_oauth_identity_codes_expires ON oauth_identity_codes(expires_at);
CREATE INDEX idx_oauth_identity_codes_user ON oauth_identity_codes(user_id);

-- +goose Down
DROP TABLE IF EXISTS oauth_identity_codes;
-- Static clients cannot exist under the narrower CHECK; their codes went with
-- the table above, and they never own grants (the identity flow makes none).
DELETE FROM oauth_clients WHERE registration_type = 'static';
ALTER TABLE oauth_clients DROP CONSTRAINT IF EXISTS oauth_clients_registration_type_check;
ALTER TABLE oauth_clients ADD CONSTRAINT oauth_clients_registration_type_check
    CHECK (registration_type IN ('cimd', 'dcr'));
