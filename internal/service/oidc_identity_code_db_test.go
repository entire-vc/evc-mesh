package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Identity-flow codes against real Postgres: single use, and
// removed by the periodic purge once past the code retention.

func (env *oauthSvcEnv) staticClient(t *testing.T) string {
	t.Helper()
	clientID := "keep-test-" + uuid.New().String()[:8]
	require.NoError(t, env.svc.ConfigureOIDC(context.Background(), OIDCSettings{
		Issuer:  "https://mesh.example",
		Clients: []OIDCStaticClient{{ClientID: clientID, ClientName: "Keep", RedirectURIs: []string{"https://keep.example/cb"}}},
	}))
	t.Cleanup(func() { _, _ = env.db.Exec(`DELETE FROM oauth_clients WHERE client_id = $1`, clientID) })
	return clientID
}

func (env *oauthSvcEnv) insertIdentityCode(t *testing.T, clientID string, userID uuid.UUID, expires time.Time, used *time.Time) uuid.UUID {
	t.Helper()
	c := &domain.OAuthIdentityCode{
		ID: uuid.New(), CodeHash: "idc-test-" + uuid.New().String(), ClientID: clientID,
		RedirectURI: "https://keep.example/cb", CodeChallenge: "c", UserID: userID,
		Scope: "openid", ExpiresAt: expires, UsedAt: used, CreatedAt: time.Now(),
	}
	require.NoError(t, env.svc.repo.CreateIdentityCode(context.Background(), c))
	return c.ID
}

func TestIdentityCode_SingleUse(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	clientID := env.staticClient(t)
	userID, _ := env.createUser(t, "idc-once")
	id := env.insertIdentityCode(t, clientID, userID, time.Now().Add(time.Minute), nil)

	ok, err := env.svc.repo.MarkIdentityCodeUsed(ctx, id, time.Now())
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = env.svc.repo.MarkIdentityCodeUsed(ctx, id, time.Now())
	require.NoError(t, err)
	assert.False(t, ok, "a second redemption loses")
}

func TestIdentityCode_PurgeExpired(t *testing.T) {
	ctx := context.Background()
	env := newOAuthSvcEnv(t)
	clientID := env.staticClient(t)
	userID, _ := env.createUser(t, "idc-purge")
	now := time.Now()
	used := now.Add(-time.Minute)
	old := env.insertIdentityCode(t, clientID, userID, now.Add(-oauthCodeRetention-time.Hour), nil)
	oldUsed := env.insertIdentityCode(t, clientID, userID, now.Add(-oauthCodeRetention-time.Hour), &used)
	recent := env.insertIdentityCode(t, clientID, userID, now.Add(-time.Minute), nil)
	live := env.insertIdentityCode(t, clientID, userID, now.Add(time.Minute), &used)

	codes, _, err := env.svc.PurgeExpired(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, codes, int64(2))
	assert.False(t, env.rowExists(t, "oauth_identity_codes", old))
	assert.False(t, env.rowExists(t, "oauth_identity_codes", oldUsed))
	assert.True(t, env.rowExists(t, "oauth_identity_codes", recent), "kept until retention passes")
	assert.True(t, env.rowExists(t, "oauth_identity_codes", live), "a used, unexpired code stays so a replay is recognised")
}

func TestIdentityCode_DeletedWithUser(t *testing.T) {
	env := newOAuthSvcEnv(t)
	clientID := env.staticClient(t)
	userID, _ := env.createUser(t, "idc-cascade")
	id := env.insertIdentityCode(t, clientID, userID, time.Now().Add(time.Minute), nil)
	_, err := env.db.Exec(`DELETE FROM users WHERE id = $1`, userID)
	require.NoError(t, err)
	assert.False(t, env.rowExists(t, "oauth_identity_codes", id))
}
