-- +goose Up

-- MCP-OAuth 1/5 (Mesh Doc oauth-for-remote-mesh-mcp, epic #5b8aaf87): Mesh API
-- becomes an OAuth 2.0 Authorization Server (RFC 6749 authorization_code +
-- refresh_token, RFC 7591 dynamic client registration, RFC 7636 PKCE, plus
-- CIMD client_id-as-URL registration) so an MCP client (Claude.ai, Claude
-- Code) can obtain a token through user consent instead of a shared agent
-- key. Additive only — nothing existing reads or writes these tables.
--
-- Client auth is public-client-only (token_endpoint_auth_method 'none'):
-- PKCE S256 is mandatory on every authorization_code exchange, so there is no
-- client_secret column anywhere in this schema. Confidential clients are not
-- part of this feature.

CREATE TABLE oauth_clients (
    id                         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- For a CIMD client, client_id IS the https:// metadata document URL
    -- (RFC: Client ID Metadata Documents). For a DCR client, it's a
    -- server-generated opaque id ("mcpc_<random>").
    client_id                  TEXT NOT NULL UNIQUE,
    registration_type          TEXT NOT NULL CHECK (registration_type IN ('cimd', 'dcr')),
    client_name                TEXT NOT NULL DEFAULT '',
    redirect_uris              TEXT[] NOT NULL,
    token_endpoint_auth_method TEXT NOT NULL DEFAULT 'none',
    grant_types                TEXT[] NOT NULL DEFAULT ARRAY['authorization_code', 'refresh_token'],
    -- Raw registration/CIMD response, kept for debugging + re-serving GET
    -- /oauth/register/:client_id style lookups later; nothing parses this
    -- back out today, the typed columns above are the source of truth.
    metadata                   JSONB NOT NULL DEFAULT '{}',
    -- CIMD documents are re-fetched periodically (see oauthClientCacheTTL in
    -- the service) rather than trusted forever; NULL for a DCR client, which
    -- has no document to refetch.
    metadata_fetched_at        TIMESTAMPTZ NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One-time authorization codes (RFC 6749 §4.1.2). Bound to the grant that
-- produced them (client+redirect+challenge+user+workspace+agent all live on
-- oauth_grants) so /oauth/token only needs the code + client_id + verifier.
CREATE TABLE oauth_authorization_codes (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash             TEXT NOT NULL UNIQUE,
    client_id             TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    redirect_uri          TEXT NOT NULL,
    code_challenge        TEXT NOT NULL,
    code_challenge_method TEXT NOT NULL DEFAULT 'S256',
    grant_id              UUID NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    -- Marked used (not deleted) on redemption so a REPLAYED code is a
    -- provable invalid_grant, not an indistinguishable "never existed".
    used_at               TIMESTAMPTZ NULL,
    -- The token family this code produced, set atomically with used_at.
    -- A replay of an already-used code reads this back to revoke that
    -- family outright (RFC 6749 §10.5's recommendation: a replayed code is
    -- itself evidence the code leaked, so tokens it already produced are
    -- no longer trustworthy either), not just deny the replay itself.
    issued_family_id      UUID NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_oauth_codes_expires ON oauth_authorization_codes(expires_at);
CREATE INDEX idx_oauth_codes_grant ON oauth_authorization_codes(grant_id);

-- A user's standing consent for one (client, workspace) pair. Revoking this
-- is "revoke access" from the user's point of view (spec: "агент остаётся,
-- токены гаснут") — the connector agent itself is untouched, only its
-- ability to be authenticated via THIS grant's tokens dies.
CREATE TABLE oauth_grants (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id    TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The connector agent ("<client_name> — <username>") this grant's tokens
    -- authenticate as. Created-or-reused at consent time; agents.* remains
    -- the single source of truth for what the token can DO (existing RBAC),
    -- this table only decides whether a token for it may still be minted.
    agent_id     UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    scope        TEXT NOT NULL DEFAULT 'mesh offline_access',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ NULL,
    CONSTRAINT uq_oauth_grant UNIQUE (user_id, client_id, workspace_id)
);
CREATE INDEX idx_oauth_grants_user_active ON oauth_grants(user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_oauth_grants_agent ON oauth_grants(agent_id);

ALTER TABLE oauth_authorization_codes
    ADD CONSTRAINT fk_oauth_codes_grant FOREIGN KEY (grant_id) REFERENCES oauth_grants(id) ON DELETE CASCADE;

-- Opaque bearer tokens, stored by SHA-256 digest (not bcrypt: these are
-- 256-bit server-generated random values, not user-chosen secrets — a
-- lookup-by-exact-digest costs one indexed equality check instead of a
-- per-row bcrypt compare, the same tradeoff every "session token" scheme
-- makes). token_type 'access' rows carry the mot_ prefix the auth
-- middleware recognizes; 'refresh' rows are never presented to that
-- middleware, only to /oauth/token.
--
-- family_id ties every token descended from one authorization_code exchange
-- together: refresh rotation keeps the same family_id, and presenting an
-- already-revoked (i.e. already-rotated-away) refresh token revokes the
-- WHOLE family — RFC 6749's reuse-detection recommendation for rotating
-- refresh tokens.
CREATE TABLE oauth_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    grant_id        UUID NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
    token_type      TEXT NOT NULL CHECK (token_type IN ('access', 'refresh')),
    token_hash      TEXT NOT NULL UNIQUE,
    family_id       UUID NOT NULL,
    parent_token_id UUID NULL REFERENCES oauth_tokens(id) ON DELETE SET NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_oauth_tokens_family ON oauth_tokens(family_id);
CREATE INDEX idx_oauth_tokens_grant ON oauth_tokens(grant_id);

-- Same RLS posture as agent_workspace_grants (20260909001): the backend
-- connects as table owner and bypasses this without FORCE, so nothing
-- changes for the running app today; this only stops a future direct,
-- non-owner connection from reading another workspace's OAuth state.
ALTER TABLE oauth_authorization_codes ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_oauth_codes ON oauth_authorization_codes
    USING (
        grant_id IN (
            SELECT id FROM oauth_grants
            WHERE workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

ALTER TABLE oauth_grants ENABLE ROW LEVEL SECURITY;
CREATE POLICY rls_oauth_grants ON oauth_grants
    USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.current_workspace_id', true)::uuid);

-- +goose Down
-- Order matters beyond the FKs themselves: rls_oauth_codes's USING clause
-- references oauth_grants in a subquery, which Postgres tracks as a real
-- dependency of the POLICY object — oauth_authorization_codes (and the
-- policy on it) must go before oauth_grants, not just its FK constraint.
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_authorization_codes;
DROP TABLE IF EXISTS oauth_grants;
DROP TABLE IF EXISTS oauth_clients;
