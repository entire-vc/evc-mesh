package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Rotation and code redemption must revoke/consume and insert as ONE unit.
// Every test here proves it against the real schema by making the INSERT half
// fail (token_hash is UNIQUE) and then reading back that the GUARD half did
// not stick — a transaction that quietly commits half is the bug.

func (f *oauthFixture) pair(family uuid.UUID) []*domain.OAuthToken {
	now := time.Now().UTC()
	return []*domain.OAuthToken{
		{ID: uuid.New(), GrantID: f.grantID, TokenType: domain.OAuthTokenTypeAccess, TokenHash: "a-" + uuid.New().String(), FamilyID: family, ExpiresAt: now.Add(time.Hour), CreatedAt: now},
		{ID: uuid.New(), GrantID: f.grantID, TokenType: domain.OAuthTokenTypeRefresh, TokenHash: "r-" + uuid.New().String(), FamilyID: family, ExpiresAt: now.Add(time.Hour), CreatedAt: now},
	}
}

func (f *oauthFixture) newCode(t *testing.T) *domain.OAuthAuthorizationCode {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	code := &domain.OAuthAuthorizationCode{
		ID: uuid.New(), CodeHash: "c-" + uuid.New().String(), ClientID: f.client.ClientID,
		RedirectURI: "https://client.example.com/callback", CodeChallenge: "x", CodeChallengeMethod: "S256",
		GrantID: f.grantID, ExpiresAt: now.Add(time.Minute), CreatedAt: now,
	}
	require.NoError(t, f.repo.CreateCode(context.Background(), code))
	return code
}

func (f *oauthFixture) liveCount(t *testing.T, family uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM oauth_tokens WHERE family_id=$1 AND revoked_at IS NULL`, family))
	return n
}

func TestOAuthRepo_RotateRefreshToken(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("revokes the old token and inserts the pair together", func(t *testing.T) {
		f := newOAuthFixture(t)
		fam := uuid.New()
		old := f.newToken(t, domain.OAuthTokenTypeRefresh, fam, now.Add(time.Hour))
		rows := f.pair(fam)

		ok, err := f.repo.RotateRefreshToken(ctx, old.ID, now, rows)
		require.NoError(t, err)
		require.True(t, ok)

		got, err := f.repo.GetTokenByHash(ctx, old.TokenHash)
		require.NoError(t, err)
		require.NotNil(t, got.RevokedAt, "the presented token must be revoked")
		assert.Equal(t, 2, f.liveCount(t, fam), "and exactly the new pair is live")
	})

	t.Run("a failing insert rolls the revoke back", func(t *testing.T) {
		f := newOAuthFixture(t)
		fam := uuid.New()
		old := f.newToken(t, domain.OAuthTokenTypeRefresh, fam, now.Add(time.Hour))
		rows := f.pair(fam)
		rows[1].TokenHash = rows[0].TokenHash // UNIQUE(token_hash) violation on the 2nd insert

		ok, err := f.repo.RotateRefreshToken(ctx, old.ID, now, rows)
		require.Error(t, err)
		assert.False(t, ok)

		got, err := f.repo.GetTokenByHash(ctx, old.TokenHash)
		require.NoError(t, err)
		assert.Nil(t, got.RevokedAt, "the client's token must still be good for a retry")
		assert.Equal(t, 1, f.liveCount(t, fam), "and the half-inserted pair must not survive")
	})

	t.Run("an already-revoked token loses without writing anything", func(t *testing.T) {
		f := newOAuthFixture(t)
		fam := uuid.New()
		old := f.newToken(t, domain.OAuthTokenTypeRefresh, fam, now.Add(time.Hour))
		ok, err := f.repo.RevokeToken(ctx, old.ID, now)
		require.NoError(t, err)
		require.True(t, ok)

		ok, err = f.repo.RotateRefreshToken(ctx, old.ID, now, f.pair(fam))
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Zero(t, f.liveCount(t, fam), "the loser must not mint a pair")
	})

	t.Run("concurrent rotations of one token: exactly one wins and only its pair exists", func(t *testing.T) {
		f := newOAuthFixture(t)
		fam := uuid.New()
		old := f.newToken(t, domain.OAuthTokenTypeRefresh, fam, now.Add(time.Hour))

		const n = 16
		var wg sync.WaitGroup
		start := make(chan struct{})
		var mu sync.Mutex
		wins := 0
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ok, err := f.repo.RotateRefreshToken(ctx, old.ID, now, f.pair(fam))
				assert.NoError(t, err)
				if ok {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.Equal(t, 1, wins)
		assert.Equal(t, 2, f.liveCount(t, fam), "one winner means one pair, not a forked family")
	})
}

func TestOAuthRepo_RedeemCode(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("marks the code used and inserts the tokens together", func(t *testing.T) {
		f := newOAuthFixture(t)
		code := f.newCode(t)
		fam := uuid.New()
		ok, err := f.repo.RedeemCode(ctx, code.ID, now, fam, f.pair(fam))
		require.NoError(t, err)
		require.True(t, ok)

		got, err := f.repo.GetCodeByHash(ctx, code.CodeHash)
		require.NoError(t, err)
		require.NotNil(t, got.UsedAt)
		require.NotNil(t, got.IssuedFamilyID)
		assert.Equal(t, fam, *got.IssuedFamilyID)
		assert.Equal(t, 2, f.liveCount(t, fam))
	})

	t.Run("a failing insert leaves the code unused", func(t *testing.T) {
		f := newOAuthFixture(t)
		code := f.newCode(t)
		fam := uuid.New()
		rows := f.pair(fam)
		rows[1].TokenHash = rows[0].TokenHash

		ok, err := f.repo.RedeemCode(ctx, code.ID, now, fam, rows)
		require.Error(t, err)
		assert.False(t, ok)

		got, err := f.repo.GetCodeByHash(ctx, code.CodeHash)
		require.NoError(t, err)
		assert.Nil(t, got.UsedAt, "a code that produced no tokens must not be burned")
		assert.Zero(t, f.liveCount(t, fam))
	})

	t.Run("a second redemption of the same code inserts nothing", func(t *testing.T) {
		f := newOAuthFixture(t)
		code := f.newCode(t)
		fam1, fam2 := uuid.New(), uuid.New()
		ok, err := f.repo.RedeemCode(ctx, code.ID, now, fam1, f.pair(fam1))
		require.NoError(t, err)
		require.True(t, ok)

		ok, err = f.repo.RedeemCode(ctx, code.ID, now, fam2, f.pair(fam2))
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Zero(t, f.liveCount(t, fam2))
	})
}

func TestOAuthRepo_RetargetGrant(t *testing.T) {
	ctx := context.Background()
	f := newOAuthFixture(t)
	other := seedGrantAgent(t, f.db, f.wsID)

	require.NoError(t, f.repo.RevokeGrant(ctx, f.grantID, time.Now().UTC()))
	require.NoError(t, f.repo.RetargetGrant(ctx, f.grantID, other.ID))

	g, err := f.repo.GetGrantByID(ctx, f.grantID)
	require.NoError(t, err)
	assert.Equal(t, other.ID, g.AgentID)
	assert.Nil(t, g.RevokedAt, "retargeting is a fresh consent: the grant is live again")
}
