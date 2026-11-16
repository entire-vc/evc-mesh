package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestOIDCRepo_IdentityCodeLifecycle(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	c := *f.client
	c.ID, c.ClientID, c.RegistrationType = uuid.New(), "keep-"+uuid.New().String(), domain.OAuthRegistrationStatic
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM oauth_clients WHERE client_id=$1`, c.ClientID) })
	ok, err := f.repo.UpsertStaticClient(ctx, &c)
	require.NoError(t, err)
	require.True(t, ok)
	c.ClientName = "Updated Keep"
	ok, err = f.repo.UpsertStaticClient(ctx, &c)
	require.NoError(t, err)
	require.True(t, ok)
	gotClient, err := f.repo.GetClientByClientID(ctx, c.ClientID)
	require.NoError(t, err)
	assert.Equal(t, c.ClientName, gotClient.ClientName)

	collision := c
	collision.ClientID = f.client.ClientID
	ok, err = f.repo.UpsertStaticClient(ctx, &collision)
	require.NoError(t, err)
	assert.False(t, ok, "a DCR client cannot be overwritten by the static config")

	now := time.Now().UTC().Truncate(time.Microsecond)
	nonce := "repo-nonce"
	code := &domain.OAuthIdentityCode{
		ID: uuid.New(), CodeHash: uuid.NewString(), ClientID: c.ClientID, RedirectURI: c.RedirectURIs[0],
		CodeChallenge: "challenge", UserID: f.userID, Nonce: &nonce, Scope: "openid", ExpiresAt: now.Add(time.Minute), CreatedAt: now,
	}
	require.NoError(t, f.repo.CreateIdentityCode(ctx, code))
	got, err := f.repo.GetIdentityCodeByHash(ctx, code.CodeHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, code, got)
	require.Error(t, f.repo.CreateIdentityCode(ctx, code), "code hash must be unique")
	ok, err = f.repo.MarkIdentityCodeUsed(ctx, code.ID, now)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = f.repo.MarkIdentityCodeUsed(ctx, code.ID, now)
	require.NoError(t, err)
	assert.False(t, ok)
	got, err = f.repo.GetIdentityCodeByHash(ctx, code.CodeHash)
	require.NoError(t, err)
	require.NotNil(t, got.UsedAt)
	assert.WithinDuration(t, now, *got.UsedAt, time.Microsecond)
	// Use an old cutoff so concurrent packages' live codes are untouched.
	oldCode := *code
	oldCode.ID, oldCode.CodeHash = uuid.New(), uuid.NewString()
	oldCode.CreatedAt, oldCode.ExpiresAt = now.AddDate(-102, 0, 0), now.AddDate(-101, 0, 0)
	require.NoError(t, f.repo.CreateIdentityCode(ctx, &oldCode))
	deleted, err := f.repo.DeleteExpiredIdentityCodes(ctx, now.AddDate(-100, 0, 0))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, int64(1))
	got, err = f.repo.GetIdentityCodeByHash(ctx, oldCode.CodeHash)
	require.NoError(t, err)
	assert.Nil(t, got, "expired code is purged")
	got, err = f.repo.GetIdentityCodeByHash(ctx, code.CodeHash)
	require.NoError(t, err)
	require.NotNil(t, got, "recent code survives the purge")
}

func TestOIDCRepo_ClosedPoolErrors(t *testing.T) {
	r := closedOAuthRepo(t)
	ctx := context.Background()
	_, err := r.UpsertStaticClient(ctx, &domain.OAuthClient{})
	require.Error(t, err)
	err = r.CreateIdentityCode(ctx, &domain.OAuthIdentityCode{})
	require.Error(t, err)
	got, err := r.GetIdentityCodeByHash(ctx, "missing")
	require.Error(t, err)
	assert.Nil(t, got)
	ok, err := r.MarkIdentityCodeUsed(ctx, uuid.New(), time.Now())
	require.Error(t, err)
	assert.False(t, ok)
	n, err := r.DeleteExpiredIdentityCodes(ctx, time.Now())
	require.Error(t, err)
	assert.Zero(t, n)
}
