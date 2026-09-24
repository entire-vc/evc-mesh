package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// OAuthClient is a registered MCP client — either via CIMD (client_id is the
// https:// metadata document URL, refetched periodically) or DCR (client_id
// is server-generated). Public clients only: there is no client secret
// anywhere on this struct, because token_endpoint_auth_method is always
// "none" and PKCE S256 is mandatory on every code exchange instead.
type OAuthClient struct {
	ID                      uuid.UUID       `json:"id" db:"id"`
	ClientID                string          `json:"client_id" db:"client_id"`
	RegistrationType        string          `json:"registration_type" db:"registration_type"`
	ClientName              string          `json:"client_name" db:"client_name"`
	RedirectURIs            pq.StringArray  `json:"redirect_uris" db:"redirect_uris"`
	TokenEndpointAuthMethod string          `json:"token_endpoint_auth_method" db:"token_endpoint_auth_method"`
	GrantTypes              pq.StringArray  `json:"grant_types" db:"grant_types"`
	Metadata                json.RawMessage `json:"metadata" db:"metadata"`
	MetadataFetchedAt       *time.Time      `json:"metadata_fetched_at,omitempty" db:"metadata_fetched_at"`
	CreatedAt               time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at" db:"updated_at"`
}

// IsCIMD reports whether this client was registered via a Client ID Metadata
// Document (client_id is itself the document's https:// URL) rather than DCR.
func (c *OAuthClient) IsCIMD() bool {
	return c.RegistrationType == "cimd"
}

// OAuthAuthorizationCode is a one-time RFC 6749 §4.1.2 authorization code.
// All identity (client/redirect/user/workspace/agent) beyond client_id and
// redirect_uri lives on the OAuthGrant it points at.
type OAuthAuthorizationCode struct {
	ID                  uuid.UUID  `json:"id" db:"id"`
	CodeHash            string     `json:"-" db:"code_hash"`
	ClientID            string     `json:"client_id" db:"client_id"`
	RedirectURI         string     `json:"redirect_uri" db:"redirect_uri"`
	CodeChallenge       string     `json:"code_challenge" db:"code_challenge"`
	CodeChallengeMethod string     `json:"code_challenge_method" db:"code_challenge_method"`
	GrantID             uuid.UUID  `json:"grant_id" db:"grant_id"`
	ExpiresAt           time.Time  `json:"expires_at" db:"expires_at"`
	UsedAt              *time.Time `json:"used_at,omitempty" db:"used_at"`
	// IssuedFamilyID is the token family this code produced, set atomically
	// with UsedAt at redemption. A replayed code reads this back to revoke
	// that family — see the migration's column comment.
	IssuedFamilyID *uuid.UUID `json:"-" db:"issued_family_id"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
}

// IsUsable reports whether this code can still be redeemed: not expired, and
// not already used (a replay attempt).
func (c *OAuthAuthorizationCode) IsUsable(now time.Time) bool {
	return c.UsedAt == nil && now.Before(c.ExpiresAt)
}

// OAuthGrant is a user's standing consent for one (client, workspace) pair —
// what /oauth/token mints tokens against, and what the user-facing "revoke
// access" API revokes. Revoking a grant does not delete or disable the
// connector agent it points at; it only stops new/refreshed tokens for it.
type OAuthGrant struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	UserID      uuid.UUID  `json:"user_id" db:"user_id"`
	ClientID    string     `json:"client_id" db:"client_id"`
	WorkspaceID uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	AgentID     uuid.UUID  `json:"agent_id" db:"agent_id"`
	Scope       string     `json:"scope" db:"scope"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
}

// IsRevoked reports whether this grant has been revoked.
func (g *OAuthGrant) IsRevoked() bool {
	return g.RevokedAt != nil
}

// OAuthTokenType distinguishes an access token row from a refresh token row
// in the shared oauth_tokens table.
type OAuthTokenType string

const (
	OAuthTokenTypeAccess  OAuthTokenType = "access"
	OAuthTokenTypeRefresh OAuthTokenType = "refresh"
)

// OAuthToken is one opaque bearer token (access or refresh), stored by
// SHA-256 digest of the raw value actually handed to the client.
//
// FamilyID ties every token descended from one authorization_code exchange
// together. Refresh rotation keeps FamilyID unchanged; presenting an
// already-revoked refresh token (proof of reuse — it was already rotated
// away) revokes the entire family, per RFC 6749's rotation-reuse-detection
// recommendation.
type OAuthToken struct {
	ID            uuid.UUID      `json:"id" db:"id"`
	GrantID       uuid.UUID      `json:"grant_id" db:"grant_id"`
	TokenType     OAuthTokenType `json:"token_type" db:"token_type"`
	TokenHash     string         `json:"-" db:"token_hash"`
	FamilyID      uuid.UUID      `json:"family_id" db:"family_id"`
	ParentTokenID *uuid.UUID     `json:"parent_token_id,omitempty" db:"parent_token_id"`
	ExpiresAt     time.Time      `json:"expires_at" db:"expires_at"`
	RevokedAt     *time.Time     `json:"revoked_at,omitempty" db:"revoked_at"`
	CreatedAt     time.Time      `json:"created_at" db:"created_at"`
}

// IsUsable reports whether this token is still valid for its purpose: not
// expired, not revoked.
func (t *OAuthToken) IsUsable(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// OAuthGrantWithDetails embeds OAuthGrant with the fields the user-facing
// "your connected apps" list needs, joined from oauth_clients + agents — the
// shape GET /api/v1/oauth/grants returns.
type OAuthGrantWithDetails struct {
	OAuthGrant
	ClientName string         `json:"client_name" db:"client_name"`
	AgentName  string         `json:"agent_name" db:"agent_name"`
	Workspace  WorkspaceBrief `json:"workspace"`
}
