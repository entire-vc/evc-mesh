package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// OAuth hygiene (#23579e6b): DeleteExpiredTokens, and the two indexes added by
// migration 20260924005.

func TestOAuthRepo_Token_DeleteExpiredOnlyRemovesExpired(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()

	// Far-past expiry, as in the codes test: the cutoff below only ever touches
	// rows old enough that no concurrently running test owns them.
	oldAccess := f.newToken(t, domain.OAuthTokenTypeAccess, uuid.New(), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	oldRefresh := f.newToken(t, domain.OAuthTokenTypeRefresh, uuid.New(), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	// A revoked-but-unexpired refresh token must stay: presenting it again is
	// how reuse is detected.
	revoked := f.newToken(t, domain.OAuthTokenTypeRefresh, uuid.New(), time.Now().Add(24*time.Hour))
	_, err := f.repo.RevokeToken(ctx, revoked.ID, time.Now())
	require.NoError(t, err)
	fresh := f.newToken(t, domain.OAuthTokenTypeAccess, uuid.New(), time.Now().Add(time.Hour))

	// No count assertion — the service-level purge test runs against the same
	// database and may sweep the 2001 rows first.
	_, err = f.repo.DeleteExpiredTokens(ctx, time.Date(2001, 1, 2, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	for name, tok := range map[string]*domain.OAuthToken{"expired access": oldAccess, "expired refresh": oldRefresh} {
		got, gerr := f.repo.GetTokenByHash(ctx, tok.TokenHash)
		require.NoError(t, gerr)
		assert.Nil(t, got, "%s token must be deleted", name)
	}
	for name, tok := range map[string]*domain.OAuthToken{"revoked but unexpired": revoked, "live": fresh} {
		got, gerr := f.repo.GetTokenByHash(ctx, tok.TokenHash)
		require.NoError(t, gerr)
		assert.NotNil(t, got, "%s token must survive", name)
	}
}

func TestOAuthRepo_HygieneIndexesExistAndServeTheirQueries(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()

	var have []string
	require.NoError(t, db.SelectContext(ctx, &have,
		`SELECT indexname FROM pg_indexes WHERE tablename IN ('oauth_grants','oauth_tokens')`))
	assert.Contains(t, have, "idx_oauth_grants_user_created")
	assert.Contains(t, have, "idx_oauth_tokens_expires")

	// The planner picks an index only if it can: with seq scans discouraged,
	// each query below must resolve to ITS index. Without the migration the
	// plan falls back to a sequential scan (or the unique index plus a Sort)
	// and the assertion names the plan it got.
	//
	// Bitmap scans are also discouraged: on a table with real rows and fresh
	// statistics (the shared CI database) the planner may pick a Bitmap Index
	// Scan on our index followed by a cheap Sort, which uses the index but says
	// nothing about the ORDER BY. Index Scan vs "unique index + Sort" is the
	// comparison that shows whether the index serves the ordering.
	explain := func(t *testing.T, q string, args ...interface{}) string {
		t.Helper()
		tx, err := db.BeginTxx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `SET LOCAL enable_bitmapscan = off`)
		require.NoError(t, err)
		var lines []string
		require.NoError(t, tx.SelectContext(ctx, &lines, `EXPLAIN `+q, args...))
		return strings.Join(lines, "\n")
	}

	t.Run("grants listing by user is index-ordered, no Sort node", func(t *testing.T) {
		plan := explain(t, `SELECT id FROM oauth_grants WHERE user_id = $1 ORDER BY created_at DESC`, uuid.New())
		assert.Contains(t, plan, "idx_oauth_grants_user_created", plan)
		assert.NotContains(t, plan, "Sort", plan)
	})

	t.Run("token purge by expires_at uses the index", func(t *testing.T) {
		plan := explain(t, `DELETE FROM oauth_tokens WHERE expires_at < $1`, time.Now())
		assert.Contains(t, plan, "idx_oauth_tokens_expires", plan)
	})
}
