-- +goose Up

-- MCP-OAuth hygiene (#23579e6b), additive only — two indexes, no data change.
--
-- ListGrantsByUser ("your connected apps") filters on user_id and sorts
-- created_at DESC over ALL of the user's grants, revoked ones included. The
-- only existing user_id indexes are uq_oauth_grant (user_id, client_id,
-- workspace_id) — right prefix, wrong sort — and the PARTIAL
-- idx_oauth_grants_user_active (revoked_at IS NULL), which the planner cannot
-- use for a query that also wants the revoked rows. So every listing sorted in
-- memory. (user_id, created_at DESC) serves the filter and the ORDER BY in one
-- index scan.
CREATE INDEX idx_oauth_grants_user_created ON oauth_grants(user_id, created_at DESC);

-- The periodic purge (OAuthService.PurgeExpired) deletes oauth_tokens by
-- expires_at; without an index every pass is a sequential scan of a table that
-- grows by two rows per refresh (access + refresh), for every connected client.
CREATE INDEX idx_oauth_tokens_expires ON oauth_tokens(expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_oauth_tokens_expires;
DROP INDEX IF EXISTS idx_oauth_grants_user_created;
