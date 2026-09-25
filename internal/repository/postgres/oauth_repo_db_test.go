package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No build tag — same convention as the other *_db_test.go files here. These
// run against the real migrated schema (agentDigestTestDB skips when no
// Postgres is reachable) so FK/UNIQUE constraints and the JOINs in
// ListGrantsByUser are exercised for real, not asserted against a mock's
// idea of them. Every fixture uses random ids/slugs: the DB is shared with
// other packages' tests.

type oauthFixture struct {
	db      *sqlx.DB
	repo    *OAuthRepo
	userID  uuid.UUID
	wsID    uuid.UUID
	wsSlug  string
	agent   *domain.Agent
	client  *domain.OAuthClient
	grantID uuid.UUID
}

func newOAuthTestClient(t *testing.T, repo *OAuthRepo, name string) *domain.OAuthClient {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := &domain.OAuthClient{
		ID:                      uuid.New(),
		ClientID:                "mcpc_test_" + uuid.New().String(),
		RegistrationType:        "dcr",
		ClientName:              name,
		RedirectURIs:            pq.StringArray{"https://client.example.com/callback"},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              pq.StringArray{"authorization_code", "refresh_token"},
		Metadata:                json.RawMessage(`{"client_name":"` + name + `"}`),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	require.NoError(t, repo.CreateClient(context.Background(), c))
	return c
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	db := agentDigestTestDB(t)
	repo := NewOAuthRepo(db)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	user := &domain.User{
		ID: uuid.New(), Email: "oauth-" + suffix + "@example.com", PasswordHash: "x",
		Name: "OAuth User", Username: "oauth-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, user))
	ws := &domain.Workspace{ID: uuid.New(), Name: "oauth-ws-" + suffix, Slug: "oauth-ws-" + suffix, OwnerID: user.ID}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))
	agent := seedGrantAgent(t, db, ws.ID)
	client := newOAuthTestClient(t, repo, "Test Client "+suffix)

	grant := &domain.OAuthGrant{
		ID: uuid.New(), UserID: user.ID, ClientID: client.ClientID, WorkspaceID: ws.ID,
		AgentID: agent.ID, Scope: "mesh offline_access", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateGrant(ctx, grant))

	return &oauthFixture{
		db: db, repo: repo, userID: user.ID, wsID: ws.ID, wsSlug: ws.Slug,
		agent: agent, client: client, grantID: grant.ID,
	}
}

func (f *oauthFixture) newToken(t *testing.T, typ domain.OAuthTokenType, family uuid.UUID, expiresAt time.Time) *domain.OAuthToken {
	t.Helper()
	tok := &domain.OAuthToken{
		ID: uuid.New(), GrantID: f.grantID, TokenType: typ, TokenHash: "h-" + uuid.New().String(),
		FamilyID: family, ExpiresAt: expiresAt, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, f.repo.CreateToken(context.Background(), tok))
	return tok
}

// closedOAuthRepo is a repo over a real, already-closed *sqlx.DB: every call
// fails at the driver with "sql: database is closed". That is the real
// failure mode of a dead pool, not a mock — it proves each method propagates
// the error instead of swallowing it into a (nil, nil) "not found".
func closedOAuthRepo(t *testing.T) *OAuthRepo {
	t.Helper()
	_ = agentDigestTestDB(t) // skip the same way the live tests do when no Postgres is reachable
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	// An independent pool, so closing it cannot affect any other test.
	db, err := sqlx.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return NewOAuthRepo(db)
}

func TestOAuthRepo_Client_CreateGetUpdate(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()

	got, err := f.repo.GetClientByClientID(ctx, f.client.ClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, f.client.ID, got.ID)
	assert.Equal(t, "dcr", got.RegistrationType)
	assert.False(t, got.IsCIMD())
	assert.Equal(t, []string{"https://client.example.com/callback"}, []string(got.RedirectURIs))
	assert.Equal(t, []string{"authorization_code", "refresh_token"}, []string(got.GrantTypes))
	assert.Nil(t, got.MetadataFetchedAt)

	fetched := time.Now().UTC().Truncate(time.Microsecond)
	got.ClientName = "Renamed"
	got.RedirectURIs = pq.StringArray{"https://a.example.com/cb", "https://b.example.com/cb"}
	got.GrantTypes = pq.StringArray{"authorization_code"}
	got.Metadata = json.RawMessage(`{"v":2}`)
	got.MetadataFetchedAt = &fetched
	got.UpdatedAt = fetched
	require.NoError(t, f.repo.UpdateClientMetadata(ctx, got))

	again, err := f.repo.GetClientByClientID(ctx, f.client.ClientID)
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.Equal(t, "Renamed", again.ClientName)
	assert.Equal(t, []string{"https://a.example.com/cb", "https://b.example.com/cb"}, []string(again.RedirectURIs))
	assert.Equal(t, []string{"authorization_code"}, []string(again.GrantTypes))
	assert.JSONEq(t, `{"v":2}`, string(again.Metadata))
	require.NotNil(t, again.MetadataFetchedAt)
	assert.WithinDuration(t, fetched, *again.MetadataFetchedAt, time.Millisecond)
}

func TestOAuthRepo_Client_CIMDRegistrationType(t *testing.T) {
	db := agentDigestTestDB(t)
	repo := NewOAuthRepo(db)
	now := time.Now().UTC()
	c := &domain.OAuthClient{
		ID: uuid.New(), ClientID: "https://cimd-" + uuid.New().String() + ".example.com/client.json",
		RegistrationType: "cimd", RedirectURIs: pq.StringArray{"https://x.example.com/cb"},
		TokenEndpointAuthMethod: "none", GrantTypes: pq.StringArray{"authorization_code"},
		Metadata: json.RawMessage(`{}`), MetadataFetchedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateClient(context.Background(), c))
	got, err := repo.GetClientByClientID(context.Background(), c.ClientID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.IsCIMD())
}

func TestOAuthRepo_Client_DuplicateClientIDRejected(t *testing.T) {
	f := newOAuthFixture(t)
	dup := *f.client
	dup.ID = uuid.New()
	err := f.repo.CreateClient(context.Background(), &dup)
	require.Error(t, err, "client_id is UNIQUE — a second registration under the same id must fail")
}

func TestOAuthRepo_Client_NotFoundReturnsNilNil(t *testing.T) {
	db := agentDigestTestDB(t)
	got, err := NewOAuthRepo(db).GetClientByClientID(context.Background(), "mcpc_missing_"+uuid.New().String())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestOAuthRepo_Code_LifecycleAndSingleUse(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	code := &domain.OAuthAuthorizationCode{
		ID: uuid.New(), CodeHash: "code-" + uuid.New().String(), ClientID: f.client.ClientID,
		RedirectURI: "https://client.example.com/callback", CodeChallenge: "chal", CodeChallengeMethod: "S256",
		GrantID: f.grantID, ExpiresAt: time.Now().Add(5 * time.Minute), CreatedAt: time.Now(),
	}
	require.NoError(t, f.repo.CreateCode(ctx, code))

	got, err := f.repo.GetCodeByHash(ctx, code.CodeHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, code.ID, got.ID)
	assert.Equal(t, f.grantID, got.GrantID)
	assert.Nil(t, got.UsedAt)
	assert.Nil(t, got.IssuedFamilyID)
	assert.True(t, got.IsUsable(time.Now()))

	family := uuid.New()
	ok, err := f.repo.MarkCodeUsed(ctx, code.ID, time.Now(), family)
	require.NoError(t, err)
	assert.True(t, ok, "first redemption wins")

	ok, err = f.repo.MarkCodeUsed(ctx, code.ID, time.Now(), uuid.New())
	require.NoError(t, err)
	assert.False(t, ok, "second redemption of the same code must lose (replay)")

	used, err := f.repo.GetCodeByHash(ctx, code.CodeHash)
	require.NoError(t, err)
	require.NotNil(t, used)
	require.NotNil(t, used.UsedAt)
	require.NotNil(t, used.IssuedFamilyID)
	assert.Equal(t, family, *used.IssuedFamilyID, "the losing replay must not overwrite the family the winner recorded")
	assert.False(t, used.IsUsable(time.Now()))
}

func TestOAuthRepo_Code_NotFoundAndUnknownIDMark(t *testing.T) {
	db := agentDigestTestDB(t)
	repo := NewOAuthRepo(db)
	got, err := repo.GetCodeByHash(context.Background(), "nope-"+uuid.New().String())
	require.NoError(t, err)
	assert.Nil(t, got)

	ok, err := repo.MarkCodeUsed(context.Background(), uuid.New(), time.Now(), uuid.New())
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestOAuthRepo_Code_DeleteExpiredOnlyRemovesExpired(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	mk := func(exp time.Time) *domain.OAuthAuthorizationCode {
		c := &domain.OAuthAuthorizationCode{
			ID: uuid.New(), CodeHash: "exp-" + uuid.New().String(), ClientID: f.client.ClientID,
			RedirectURI: "https://client.example.com/callback", CodeChallenge: "c", CodeChallengeMethod: "S256",
			GrantID: f.grantID, ExpiresAt: exp, CreatedAt: time.Now(),
		}
		require.NoError(t, f.repo.CreateCode(ctx, c))
		return c
	}
	// Far-past expiry so the cutoff below touches only rows old enough that
	// no concurrently running test can own them.
	old := mk(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	fresh := mk(time.Now().Add(time.Hour))

	// No count assertion: another package's OAuthService.PurgeExpired test runs
	// against this same database and may sweep the 2001 row first. The two
	// row-level checks below are what proves the behaviour.
	_, err := f.repo.DeleteExpiredCodes(ctx, time.Date(2001, 1, 2, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	gone, err := f.repo.GetCodeByHash(ctx, old.CodeHash)
	require.NoError(t, err)
	assert.Nil(t, gone, "expired code must be deleted")
	kept, err := f.repo.GetCodeByHash(ctx, fresh.CodeHash)
	require.NoError(t, err)
	assert.NotNil(t, kept, "unexpired code must survive")
}

func TestOAuthRepo_Grant_GetRevokeReactivate(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()

	g, err := f.repo.GetGrantByID(ctx, f.grantID)
	require.NoError(t, err)
	require.NotNil(t, g)
	assert.Equal(t, f.userID, g.UserID)
	assert.Equal(t, f.agent.ID, g.AgentID)
	assert.False(t, g.IsRevoked())

	byTriple, err := f.repo.GetGrantByUserClientWorkspace(ctx, f.userID, f.client.ClientID, f.wsID)
	require.NoError(t, err)
	require.NotNil(t, byTriple)
	assert.Equal(t, f.grantID, byTriple.ID)

	first := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	require.NoError(t, f.repo.RevokeGrant(ctx, f.grantID, first))
	// A second revoke must not move revoked_at (WHERE revoked_at IS NULL).
	require.NoError(t, f.repo.RevokeGrant(ctx, f.grantID, time.Now().UTC()))
	g, err = f.repo.GetGrantByID(ctx, f.grantID)
	require.NoError(t, err)
	require.True(t, g.IsRevoked())
	assert.WithinDuration(t, first, *g.RevokedAt, time.Millisecond, "re-revoke must not overwrite the original revoked_at")

	require.NoError(t, f.repo.ReactivateGrant(ctx, f.grantID, time.Now().UTC()))
	g, err = f.repo.GetGrantByID(ctx, f.grantID)
	require.NoError(t, err)
	assert.False(t, g.IsRevoked())
}

func TestOAuthRepo_Grant_UniquePerUserClientWorkspace(t *testing.T) {
	f := newOAuthFixture(t)
	dup := &domain.OAuthGrant{
		ID: uuid.New(), UserID: f.userID, ClientID: f.client.ClientID, WorkspaceID: f.wsID,
		AgentID: f.agent.ID, Scope: "mesh", CreatedAt: time.Now(),
	}
	require.Error(t, f.repo.CreateGrant(context.Background(), dup))
}

func TestOAuthRepo_Grant_NotFound(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	g, err := f.repo.GetGrantByID(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, g)

	g, err = f.repo.GetGrantByUserClientWorkspace(ctx, f.userID, f.client.ClientID, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, g, "the lookup is scoped by workspace — another workspace's triple must miss")
}

func TestOAuthRepo_ListGrantsByUser_ScopedAndJoined(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()

	// A second grant for the same user (different client), created later.
	c2 := newOAuthTestClient(t, f.repo, "Second Client")
	g2 := &domain.OAuthGrant{
		ID: uuid.New(), UserID: f.userID, ClientID: c2.ClientID, WorkspaceID: f.wsID,
		AgentID: f.agent.ID, Scope: "mesh", CreatedAt: time.Now().UTC().Add(time.Second),
	}
	require.NoError(t, f.repo.CreateGrant(ctx, g2))
	require.NoError(t, f.repo.RevokeGrant(ctx, g2.ID, time.Now()))

	// Another user's grant must never appear in this user's list.
	other := newOAuthFixture(t)

	list, err := f.repo.ListGrantsByUser(ctx, f.userID)
	require.NoError(t, err)
	require.Len(t, list, 2, "both grants (active and revoked) of this user, nothing of the other user's")
	assert.Equal(t, g2.ID, list[0].ID, "ordered by created_at DESC")
	assert.Equal(t, f.grantID, list[1].ID)
	for _, g := range list {
		assert.Equal(t, f.userID, g.UserID)
		assert.NotEqual(t, other.grantID, g.ID)
	}
	assert.Equal(t, "Second Client", list[0].ClientName)
	assert.True(t, list[0].IsRevoked())
	assert.Equal(t, f.client.ClientName, list[1].ClientName)
	assert.Equal(t, f.agent.Name, list[1].AgentName)
	assert.Equal(t, f.wsID, list[1].Workspace.ID)
	assert.Equal(t, f.wsSlug, list[1].Workspace.Slug)
	assert.Equal(t, f.wsSlug, list[1].Workspace.Name)

	empty, err := f.repo.ListGrantsByUser(ctx, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestOAuthRepo_Token_CreateGetRevokeSemantics(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	family := uuid.New()
	tok := f.newToken(t, domain.OAuthTokenTypeAccess, family, time.Now().Add(time.Hour))

	got, err := f.repo.GetTokenByHash(ctx, tok.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, domain.OAuthTokenTypeAccess, got.TokenType)
	assert.Equal(t, family, got.FamilyID)
	assert.Nil(t, got.ParentTokenID)
	assert.True(t, got.IsUsable(time.Now()))

	ok, err := f.repo.RevokeToken(ctx, tok.ID, time.Now())
	require.NoError(t, err)
	assert.True(t, ok, "first revoke of a live token reports true")
	ok, err = f.repo.RevokeToken(ctx, tok.ID, time.Now())
	require.NoError(t, err)
	assert.False(t, ok, "revoking an already-revoked token reports false — this is the rotation race detector")
	ok, err = f.repo.RevokeToken(ctx, uuid.New(), time.Now())
	require.NoError(t, err)
	assert.False(t, ok, "unknown token id reports false")

	got, err = f.repo.GetTokenByHash(ctx, tok.TokenHash)
	require.NoError(t, err)
	assert.False(t, got.IsUsable(time.Now()))
}

func TestOAuthRepo_Token_ExpiredRowIsReturnedButNotUsable(t *testing.T) {
	f := newOAuthFixture(t)
	tok := f.newToken(t, domain.OAuthTokenTypeRefresh, uuid.New(), time.Now().Add(-time.Minute))
	got, err := f.repo.GetTokenByHash(context.Background(), tok.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, got, "an expired token is still returned so the caller can tell expired from unknown")
	assert.Nil(t, got.RevokedAt)
	assert.False(t, got.IsUsable(time.Now()))
}

func TestOAuthRepo_Token_NotFoundAndDuplicateHash(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	got, err := f.repo.GetTokenByHash(ctx, "missing-"+uuid.New().String())
	require.NoError(t, err)
	assert.Nil(t, got)

	tok := f.newToken(t, domain.OAuthTokenTypeAccess, uuid.New(), time.Now().Add(time.Hour))
	dup := *tok
	dup.ID = uuid.New()
	require.Error(t, f.repo.CreateToken(ctx, &dup), "token_hash is UNIQUE")
}

func TestOAuthRepo_RevokeFamily_OnlyThatFamily(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	famA, famB := uuid.New(), uuid.New()
	parent := f.newToken(t, domain.OAuthTokenTypeRefresh, famA, time.Now().Add(time.Hour))
	child := &domain.OAuthToken{
		ID: uuid.New(), GrantID: f.grantID, TokenType: domain.OAuthTokenTypeRefresh, TokenHash: "h-" + uuid.New().String(),
		FamilyID: famA, ParentTokenID: &parent.ID, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}
	require.NoError(t, f.repo.CreateToken(ctx, child))
	other := f.newToken(t, domain.OAuthTokenTypeAccess, famB, time.Now().Add(time.Hour))

	require.NoError(t, f.repo.RevokeFamily(ctx, famA, time.Now()))

	for _, h := range []string{parent.TokenHash, child.TokenHash} {
		got, err := f.repo.GetTokenByHash(ctx, h)
		require.NoError(t, err)
		require.NotNil(t, got.RevokedAt, "every token in the family must be revoked")
	}
	gotChild, err := f.repo.GetTokenByHash(ctx, child.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, gotChild.ParentTokenID)
	assert.Equal(t, parent.ID, *gotChild.ParentTokenID)

	gotOther, err := f.repo.GetTokenByHash(ctx, other.TokenHash)
	require.NoError(t, err)
	assert.Nil(t, gotOther.RevokedAt, "another family must be untouched")
}

func TestOAuthRepo_RevokeTokensByGrant_OnlyThatGrant(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	mine := f.newToken(t, domain.OAuthTokenTypeAccess, uuid.New(), time.Now().Add(time.Hour))
	other := newOAuthFixture(t)
	theirs := other.newToken(t, domain.OAuthTokenTypeAccess, uuid.New(), time.Now().Add(time.Hour))

	require.NoError(t, f.repo.RevokeTokensByGrant(ctx, f.grantID, time.Now()))

	got, err := f.repo.GetTokenByHash(ctx, mine.TokenHash)
	require.NoError(t, err)
	assert.NotNil(t, got.RevokedAt)
	got, err = f.repo.GetTokenByHash(ctx, theirs.TokenHash)
	require.NoError(t, err)
	assert.Nil(t, got.RevokedAt, "another grant's tokens must be untouched")
}

// TestOAuthRepo_HasAdminRevokedConnector proves the query this test file
// otherwise never drives at all: the connector-name footprint an admin's
// revoke leaves behind, read straight off the real schema (the JOIN, the
// LIKE-with-ESCAPE disambiguation match, and the exact-name match), not
// asserted against a mock's idea of the SQL.
func TestOAuthRepo_HasAdminRevokedConnector(t *testing.T) {
	ctx := context.Background()
	db := agentDigestTestDB(t)
	repo := NewOAuthRepo(db)
	wsID := seedGrantWorkspace(t, db)
	supervisor := seedGrantUser(t, db)
	revokedAt := time.Now().UTC()

	newNamedAgent := func(t *testing.T, supervisorID uuid.UUID, name string) *domain.Agent {
		t.Helper()
		suffix := uuid.New().String()[:8]
		agent := &domain.Agent{
			ID: uuid.New(), WorkspaceID: wsID, SupervisorUserID: &supervisorID, Name: name, Slug: "hac-" + suffix,
			AgentType: domain.AgentTypeCustom, APIKeyHash: "$2a$12$hash-" + suffix,
			APIKeyPrefix: "hac-" + suffix, Status: domain.AgentStatusOffline, Role: "developer",
		}
		require.NoError(t, NewAgentRepo(db).Create(ctx, agent))
		return agent
	}

	t.Run("no connector by this name: false", func(t *testing.T) {
		got, err := repo.HasAdminRevokedConnector(ctx, wsID, supervisor, "Nonexistent Tool "+uuid.New().String()[:6])
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("exact name match, revoked: true", func(t *testing.T) {
		agent := newNamedAgent(t, supervisor, "Exact Tool "+uuid.New().String()[:6])
		insertGrant(t, db, agent.ID, wsID, "member", "hacpfx-"+agent.ID.String()[:8], "$2a$12$h", &revokedAt)
		got, err := repo.HasAdminRevokedConnector(ctx, wsID, supervisor, agent.Name)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("disambiguated name match, revoked: true", func(t *testing.T) {
		base := "Disambig Tool " + uuid.New().String()[:6]
		agent := newNamedAgent(t, supervisor, base+" (ab12)")
		insertGrant(t, db, agent.ID, wsID, "member", "hacpfx-"+agent.ID.String()[:8], "$2a$12$h", &revokedAt)
		got, err := repo.HasAdminRevokedConnector(ctx, wsID, supervisor, base)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("live (not revoked) connector of the same name: false", func(t *testing.T) {
		agent := newNamedAgent(t, supervisor, "Live Tool "+uuid.New().String()[:6])
		insertGrant(t, db, agent.ID, wsID, "member", "hacpfx-"+agent.ID.String()[:8], "$2a$12$h", nil)
		got, err := repo.HasAdminRevokedConnector(ctx, wsID, supervisor, agent.Name)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("a different supervisor's revoked connector of the same name: false", func(t *testing.T) {
		other := seedGrantUser(t, db)
		agent := newNamedAgent(t, other, "Someone Else's Tool "+uuid.New().String()[:6])
		insertGrant(t, db, agent.ID, wsID, "member", "hacpfx-"+agent.ID.String()[:8], "$2a$12$h", &revokedAt)
		got, err := repo.HasAdminRevokedConnector(ctx, wsID, supervisor, agent.Name)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("a LIKE metacharacter in the queried name is treated literally, not as a wildcard", func(t *testing.T) {
		// The pattern is built from the queried baseName (escaped) and matched
		// against revoked connectors' disambiguated names. Unescaped, "Tool_A"
		// would wildcard-match "ToolXA (ab12)" since '_' matches any one char.
		agent := newNamedAgent(t, supervisor, "ToolXA (ab12)")
		insertGrant(t, db, agent.ID, wsID, "member", "hacpfx-"+agent.ID.String()[:8], "$2a$12$h", &revokedAt)
		got, err := repo.HasAdminRevokedConnector(ctx, wsID, supervisor, "Tool_A")
		require.NoError(t, err)
		assert.False(t, got, "an unescaped '_' in the queried name would wildcard-match 'ToolXA (ab12)'")

		t.Run("positive control: the literal underscore name does match its own disambiguated form", func(t *testing.T) {
			agent2 := newNamedAgent(t, supervisor, "Tool_A (cd34)")
			insertGrant(t, db, agent2.ID, wsID, "member", "hacpfx-"+agent2.ID.String()[:8], "$2a$12$h", &revokedAt)
			got, err := repo.HasAdminRevokedConnector(ctx, wsID, supervisor, "Tool_A")
			require.NoError(t, err)
			assert.True(t, got, "control: without the escaping bug, this exact-underscore name must still match")
		})
	})
}

// Every method must surface a driver error rather than collapse it into a
// "not found" nil — a dead pool reported as "unknown token" would turn an
// outage into a wave of invalid_grant answers.
func TestOAuthRepo_ClosedDBPropagatesErrors(t *testing.T) {
	repo := closedOAuthRepo(t)
	ctx := context.Background()
	id := uuid.New()
	now := time.Now()

	_, err := repo.GetClientByClientID(ctx, "x")
	assert.Error(t, err)
	_, err = repo.GetCodeByHash(ctx, "x")
	assert.Error(t, err)
	_, err = repo.MarkCodeUsed(ctx, id, now, id)
	assert.Error(t, err)
	_, err = repo.DeleteExpiredCodes(ctx, now)
	assert.Error(t, err)
	_, err = repo.DeleteExpiredTokens(ctx, now)
	assert.Error(t, err)
	_, err = repo.GetGrantByID(ctx, id)
	assert.Error(t, err)
	_, err = repo.GetGrantByUserClientWorkspace(ctx, id, "x", id)
	assert.Error(t, err)
	_, err = repo.ListGrantsByUser(ctx, id)
	assert.Error(t, err)
	_, err = repo.GetTokenByHash(ctx, "x")
	assert.Error(t, err)
	_, err = repo.RevokeToken(ctx, id, now)
	assert.Error(t, err)
	assert.Error(t, repo.ReactivateGrant(ctx, id, now))
	assert.Error(t, repo.RevokeGrant(ctx, id, now))
	assert.Error(t, repo.RevokeFamily(ctx, id, now))
	assert.Error(t, repo.RevokeTokensByGrant(ctx, id, now))
	assert.Error(t, repo.CreateClient(ctx, &domain.OAuthClient{}))
	assert.Error(t, repo.UpdateClientMetadata(ctx, &domain.OAuthClient{}))
	assert.Error(t, repo.CreateCode(ctx, &domain.OAuthAuthorizationCode{}))
	assert.Error(t, repo.CreateGrant(ctx, &domain.OAuthGrant{}))
	assert.Error(t, repo.CreateToken(ctx, &domain.OAuthToken{}))
	// BeginTxx itself is what fails on a closed pool for these three — none
	// of the other closed-DB calls above exercise that particular error path.
	_, err = repo.RetargetGrant(ctx, id, id, id, now)
	assert.Error(t, err)
	assert.Error(t, repo.RevokeGrantWithCredentials(ctx, id, now))
	_, err = repo.HasAdminRevokedConnector(ctx, id, id, "x")
	assert.Error(t, err)
}
