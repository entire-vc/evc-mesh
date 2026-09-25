package postgres

import (
	"context"
	"strings"
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
	now := time.Now().UTC()

	require.NoError(t, f.repo.RevokeGrant(ctx, f.grantID, now))
	swapped, err := f.repo.RetargetGrant(ctx, f.grantID, f.agent.ID, other.ID, now)
	require.NoError(t, err)
	require.True(t, swapped)

	g, err := f.repo.GetGrantByID(ctx, f.grantID)
	require.NoError(t, err)
	assert.Equal(t, other.ID, g.AgentID)
	assert.Nil(t, g.RevokedAt, "retargeting is a fresh consent: the grant is live again")

	t.Run("compare-and-swap: a stale old agent swaps nothing", func(t *testing.T) {
		third := seedGrantAgent(t, f.db, f.wsID)
		// The grant now points at `other`; a consent that still believes it
		// points at f.agent lost the race.
		swapped, err := f.repo.RetargetGrant(ctx, f.grantID, f.agent.ID, third.ID, now)
		require.NoError(t, err)
		assert.False(t, swapped)
		g, err := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, err)
		assert.Equal(t, other.ID, g.AgentID, "the winner's agent must not be overwritten")
	})
}

// codeCount is how many authorization codes (used or not) the grant holds.
func (f *oauthFixture) codeCount(t *testing.T, grantID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM oauth_authorization_codes WHERE grant_id=$1`, grantID))
	return n
}

func (f *oauthFixture) liveGrantTokens(t *testing.T, grantID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM oauth_tokens WHERE grant_id=$1 AND revoked_at IS NULL`, grantID))
	return n
}

// seedLiveCredentials gives the fixture's grant one unredeemed code and one
// live token pair — the state a grant is in when its standing changes under it.
func (f *oauthFixture) seedLiveCredentials(t *testing.T) {
	t.Helper()
	f.newCode(t)
	require.NoError(t, f.repo.CreateToken(context.Background(), f.pair(uuid.New())[0]))
	require.Positive(t, f.codeCount(t, f.grantID))
	require.Positive(t, f.liveGrantTokens(t, f.grantID))
}

// TestOAuthRepo_GrantStandingChangeKillsCredentials: a code or token minted
// before a retarget / reactivate / revoke must not survive it.
func TestOAuthRepo_GrantStandingChangeKillsCredentials(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("retarget", func(t *testing.T) {
		f := newOAuthFixture(t)
		other := seedGrantAgent(t, f.db, f.wsID)
		f.seedLiveCredentials(t)
		swapped, err := f.repo.RetargetGrant(ctx, f.grantID, f.agent.ID, other.ID, now)
		require.NoError(t, err)
		require.True(t, swapped)
		assert.Zero(t, f.codeCount(t, f.grantID), "a code issued before the retarget would redeem into tokens for the new agent")
		assert.Zero(t, f.liveGrantTokens(t, f.grantID))
	})

	t.Run("reactivate", func(t *testing.T) {
		f := newOAuthFixture(t)
		require.NoError(t, f.repo.RevokeGrant(ctx, f.grantID, now))
		f.seedLiveCredentials(t) // minted in the window before the tokens were revoked
		require.NoError(t, f.repo.ReactivateGrant(ctx, f.grantID, now))
		assert.Zero(t, f.codeCount(t, f.grantID))
		assert.Zero(t, f.liveGrantTokens(t, f.grantID), "a token from the revoke window must not come back to life")
		g, err := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, err)
		assert.False(t, g.IsRevoked())
	})

	t.Run("revoke", func(t *testing.T) {
		f := newOAuthFixture(t)
		f.seedLiveCredentials(t)
		require.NoError(t, f.repo.RevokeGrantWithCredentials(ctx, f.grantID, now))
		assert.Zero(t, f.codeCount(t, f.grantID))
		assert.Zero(t, f.liveGrantTokens(t, f.grantID))
		g, err := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, err)
		assert.True(t, g.IsRevoked())
	})

	t.Run("a redeemed code stays, so its replay is still detectable", func(t *testing.T) {
		f := newOAuthFixture(t)
		code := f.newCode(t)
		fam := uuid.New()
		ok, err := f.repo.RedeemCode(ctx, code.ID, now, fam, f.pair(fam))
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, f.repo.RevokeGrantWithCredentials(ctx, f.grantID, now))
		got, err := f.repo.GetCodeByHash(ctx, code.CodeHash)
		require.NoError(t, err)
		require.NotNil(t, got, "deleting used codes would turn a replay into an indistinguishable 'never existed'")
		assert.NotNil(t, got.UsedAt)
	})
}

// failTokenUpdatesFor makes any UPDATE of oauth_tokens rows of one grant raise,
// for the life of the test — a fault injected INSIDE the transaction, which a
// wrapper around the repository cannot reach. The trigger is scoped to this
// grant's id so concurrently running tests are unaffected.
func (f *oauthFixture) failTokenUpdatesFor(t *testing.T, grantID uuid.UUID) {
	t.Helper()
	// fn/trg/grantID are not attacker-reachable: grantID is a uuid.UUID whose
	// .String() can only ever emit canonical lowercase hex (Go's type system
	// enforces the shape, no quote/SQL-metacharacter is representable), and
	// fn/trg are a hardcoded literal prefix plus the first 8 hex chars of
	// that same UUID. Nothing here is user input.
	suffix := strings.ReplaceAll(grantID.String()[:8], "-", "")
	fn, trg := "oauth_fail_tok_"+suffix, "oauth_fail_tok_trg_"+suffix
	_, err := f.db.Exec(`CREATE OR REPLACE FUNCTION ` + fn + `() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected token update failure'; END $$ LANGUAGE plpgsql`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- fn is a fixed prefix + hex slice of a uuid.UUID, not user input
	require.NoError(t, err)
	_, err = f.db.Exec(`CREATE TRIGGER ` + trg + ` BEFORE UPDATE ON oauth_tokens FOR EACH ROW WHEN (OLD.grant_id = '` + grantID.String() + `') EXECUTE FUNCTION ` + fn + `()`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- trg/fn are fixed-prefix+hex, grantID.String() is canonical UUID hex only, none are user input
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS ` + trg + ` ON oauth_tokens`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- trg is a fixed prefix + hex slice of a uuid.UUID, not user input
		_, _ = f.db.Exec(`DROP FUNCTION IF EXISTS ` + fn + `()`)               // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- fn is a fixed prefix + hex slice of a uuid.UUID, not user input
	})
}

// failGrantUpdatesFor makes any UPDATE of the oauth_grants row with this id
// raise, for the life of the test — same fault-injection shape as
// failTokenUpdatesFor above, just aimed at the grant row itself instead of
// its tokens. Reaches RetargetGrant/ReactivateGrant/RevokeGrantWithCredentials'
// own UPDATE-error branch, not the token/code cleanup inside
// killGrantCredentialsTx (that one is failTokenUpdatesFor's job). Scoped to
// this grant's id so concurrently running tests are unaffected.
func (f *oauthFixture) failGrantUpdatesFor(t *testing.T, grantID uuid.UUID) {
	t.Helper()
	// fn/trg/grantID: same non-attacker-reachable shape as failTokenUpdatesFor
	// above — grantID.String() can only ever emit canonical lowercase hex, and
	// fn/trg are a hardcoded literal prefix plus its first 8 hex chars.
	suffix := strings.ReplaceAll(grantID.String()[:8], "-", "")
	fn, trg := "oauth_fail_grant_"+suffix, "oauth_fail_grant_trg_"+suffix
	_, err := f.db.Exec(`CREATE OR REPLACE FUNCTION ` + fn + `() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected grant update failure'; END $$ LANGUAGE plpgsql`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- fn is a fixed prefix + hex slice of a uuid.UUID, not user input
	require.NoError(t, err)
	_, err = f.db.Exec(`CREATE TRIGGER ` + trg + ` BEFORE UPDATE ON oauth_grants FOR EACH ROW WHEN (OLD.id = '` + grantID.String() + `') EXECUTE FUNCTION ` + fn + `()`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- trg/fn are fixed-prefix+hex, grantID.String() is canonical UUID hex only, none are user input
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS ` + trg + ` ON oauth_grants`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- trg is a fixed prefix + hex slice of a uuid.UUID, not user input
		_, _ = f.db.Exec(`DROP FUNCTION IF EXISTS ` + fn + `()`)               // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- fn is a fixed prefix + hex slice of a uuid.UUID, not user input
	})
}

// failGrantCommitFor makes the eventual COMMIT of a transaction that UPDATEd
// this grant's row fail. A DEFERRABLE INITIALLY DEFERRED constraint trigger
// only fires at COMMIT, so the UPDATE itself — and any work the transaction
// does afterward, like killGrantCredentialsTx — succeeds normally; only
// tx.Commit() surfaces the injected error. Scoped to the grant so
// concurrently running tests are unaffected.
func (f *oauthFixture) failGrantCommitFor(t *testing.T, grantID uuid.UUID) {
	t.Helper()
	suffix := strings.ReplaceAll(grantID.String()[:8], "-", "")
	fn, trg := "oauth_fail_commit_"+suffix, "oauth_fail_commit_trg_"+suffix
	_, err := f.db.Exec(`CREATE OR REPLACE FUNCTION ` + fn + `() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected commit failure'; END $$ LANGUAGE plpgsql`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- fn is a fixed prefix + hex slice of a uuid.UUID, not user input
	require.NoError(t, err)
	_, err = f.db.Exec(`CREATE CONSTRAINT TRIGGER ` + trg + ` AFTER UPDATE ON oauth_grants DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.id = '` + grantID.String() + `') EXECUTE FUNCTION ` + fn + `()`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- trg/fn are fixed-prefix+hex, grantID.String() is canonical UUID hex only, none are user input
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS ` + trg + ` ON oauth_grants`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- trg is a fixed prefix + hex slice of a uuid.UUID, not user input
		_, _ = f.db.Exec(`DROP FUNCTION IF EXISTS ` + fn + `()`)               // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- fn is a fixed prefix + hex slice of a uuid.UUID, not user input
	})
}

// TestOAuthRepo_GrantRowUpdateFailure: the grant row's OWN UPDATE failing
// (as opposed to killGrantCredentialsTx's, covered above) must still roll
// back and surface the error, for every method that issues it.
func TestOAuthRepo_GrantRowUpdateFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("RetargetGrant: a failing agent-switch update surfaces the error and rolls back", func(t *testing.T) {
		f := newOAuthFixture(t)
		other := seedGrantAgent(t, f.db, f.wsID)
		f.failGrantUpdatesFor(t, f.grantID)

		swapped, err := f.repo.RetargetGrant(ctx, f.grantID, f.agent.ID, other.ID, now)
		require.Error(t, err)
		assert.False(t, swapped)
		g, gerr := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, gerr)
		assert.Equal(t, f.agent.ID, g.AgentID, "a failed switch must roll back")
	})

	t.Run("RetargetGrant: a failing commit surfaces the error and rolls back", func(t *testing.T) {
		f := newOAuthFixture(t)
		other := seedGrantAgent(t, f.db, f.wsID)
		f.failGrantCommitFor(t, f.grantID)

		swapped, err := f.repo.RetargetGrant(ctx, f.grantID, f.agent.ID, other.ID, now)
		require.Error(t, err)
		assert.False(t, swapped)
		g, gerr := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, gerr)
		assert.Equal(t, f.agent.ID, g.AgentID, "a failed commit must roll back the agent switch too")
	})

	t.Run("ReactivateGrant: a failing revoked_at clear surfaces the error and rolls back", func(t *testing.T) {
		f := newOAuthFixture(t)
		require.NoError(t, f.repo.RevokeGrant(ctx, f.grantID, now))
		f.failGrantUpdatesFor(t, f.grantID)

		err := f.repo.ReactivateGrant(ctx, f.grantID, now)
		require.Error(t, err)
		g, gerr := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, gerr)
		assert.True(t, g.IsRevoked(), "a failed clear must roll back")
	})

	t.Run("RevokeGrantWithCredentials: a failing revoke update surfaces the error and rolls back", func(t *testing.T) {
		f := newOAuthFixture(t)
		f.failGrantUpdatesFor(t, f.grantID)

		err := f.repo.RevokeGrantWithCredentials(ctx, f.grantID, now)
		require.Error(t, err)
		g, gerr := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, gerr)
		assert.False(t, g.IsRevoked(), "a failed revoke update must roll back")
	})
}

// TestOAuthRepo_GrantStandingChangeIsAtomic: when a later step fails, the
// earlier ones must not have stuck — no half-retargeted grant, no reactivated
// grant whose tokens survived, no revoked grant that still holds its codes.
func TestOAuthRepo_GrantStandingChangeIsAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("retarget", func(t *testing.T) {
		f := newOAuthFixture(t)
		other := seedGrantAgent(t, f.db, f.wsID)
		f.seedLiveCredentials(t)
		f.failTokenUpdatesFor(t, f.grantID)

		swapped, err := f.repo.RetargetGrant(ctx, f.grantID, f.agent.ID, other.ID, now)
		require.Error(t, err)
		assert.False(t, swapped)
		g, gerr := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, gerr)
		assert.Equal(t, f.agent.ID, g.AgentID, "the agent switch must roll back with the failed token revocation")
		assert.Positive(t, f.codeCount(t, f.grantID), "and the codes must still be there")
	})

	t.Run("reactivate", func(t *testing.T) {
		f := newOAuthFixture(t)
		require.NoError(t, f.repo.RevokeGrant(ctx, f.grantID, now))
		f.seedLiveCredentials(t)
		f.failTokenUpdatesFor(t, f.grantID)

		require.Error(t, f.repo.ReactivateGrant(ctx, f.grantID, now))
		g, gerr := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, gerr)
		assert.True(t, g.IsRevoked(), "a grant must not come back live while its old tokens are still live")
	})

	t.Run("revoke", func(t *testing.T) {
		f := newOAuthFixture(t)
		f.seedLiveCredentials(t)
		f.failTokenUpdatesFor(t, f.grantID)

		require.Error(t, f.repo.RevokeGrantWithCredentials(ctx, f.grantID, now))
		g, gerr := f.repo.GetGrantByID(ctx, f.grantID)
		require.NoError(t, gerr)
		assert.False(t, g.IsRevoked(), "revoked_at must roll back with the failed token revocation")
	})
}
