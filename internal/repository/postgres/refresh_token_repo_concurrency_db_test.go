package postgres

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The auth service's one-shot refresh rotation rests entirely on a claim about
// PostgreSQL: that `UPDATE ... WHERE revoked_at IS NULL RETURNING id` can succeed for at
// most one of N concurrent callers. The service-level test next door cannot check that —
// its repo double enforces the property with a mutex, so it would stay green even if the
// SQL were not atomic at all. This asserts the claim against a real database, which is
// the only place it is actually true or false.
//
// Untagged, matching the other *_db_test.go files here: CI's plain `go test ./...` runs
// against a migrated DATABASE_URL, and this skips when no Postgres is reachable.
func refreshTokenTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func refreshTokenFixtureUser(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	handle := "rt-" + userID.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		 VALUES ($1, $2, 'not-a-real-hash', 'Refresh Test', $3, true)`,
		userID, handle+"@example.test", handle,
	)
	require.NoError(t, err)
	// refresh_tokens.user_id is ON DELETE CASCADE, so this clears the tokens too.
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, userID) })
	return userID
}

// TestRevokeByHashDB_ConcurrentCallersExactlyOneWins is the security invariant stated as
// the database sees it: one refresh token, many simultaneous rotations, exactly one
// winner. Every loser must be told it lost (false, nil) rather than receiving an error —
// the caller routes that into theft detection, and an error would instead surface as a
// 500 and lose the signal.
func TestRevokeByHashDB_ConcurrentCallersExactlyOneWins(t *testing.T) {
	const racers = 16

	db := refreshTokenTestDB(t)
	repo := NewRefreshTokenRepo(db)
	ctx := context.Background()
	userID := refreshTokenFixtureUser(t, db)

	tokenHash := "concurrency-probe-" + uuid.New().String()
	require.NoError(t, repo.Create(ctx, userID, tokenHash, time.Now().Add(time.Hour)))

	// Every caller gets its own connection and they are released together, so the
	// contention is real row-level contention in Postgres rather than queuing in the
	// pool or in goroutine start-up.
	db.SetMaxOpenConns(racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	won := make([]bool, racers)
	errs := make([]error, racers)

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			won[i], errs[i] = repo.RevokeByHash(ctx, tokenHash)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := 0; i < racers; i++ {
		require.NoError(t, errs[i], "racer %d errored; losing a race must be (false, nil)", i)
		if won[i] {
			winners++
		}
	}

	assert.Equal(t, 1, winners,
		"%d of %d concurrent callers were told they revoked the same one-shot token; "+
			"exactly 1 may win, or the auth service mints a token pair per winner", winners, racers)

	// The row must also actually be revoked — a run where nobody won would satisfy a
	// naive "not more than one" check while leaving the token replayable forever.
	var revokedAt *time.Time
	require.NoError(t, db.Get(&revokedAt,
		`SELECT revoked_at FROM refresh_tokens WHERE token_hash = $1`, tokenHash))
	assert.NotNil(t, revokedAt, "token still un-revoked after %d rotations", racers)
}

// TestRevokeByHashDB_AlreadyRevokedReportsFalse covers the sequential half of the same
// contract, and pins the distinction the caller depends on: "already consumed" is
// (false, nil), never an error and never a silent success.
func TestRevokeByHashDB_AlreadyRevokedReportsFalse(t *testing.T) {
	db := refreshTokenTestDB(t)
	repo := NewRefreshTokenRepo(db)
	ctx := context.Background()
	userID := refreshTokenFixtureUser(t, db)

	tokenHash := "sequential-probe-" + uuid.New().String()
	require.NoError(t, repo.Create(ctx, userID, tokenHash, time.Now().Add(time.Hour)))

	won, err := repo.RevokeByHash(ctx, tokenHash)
	require.NoError(t, err)
	assert.True(t, won, "first revoke of a fresh token must report the win")

	won, err = repo.RevokeByHash(ctx, tokenHash)
	require.NoError(t, err)
	assert.False(t, won, "second revoke of the same token must report the loss, not succeed")

	// An unknown hash is the same shape: no row matched, nothing revoked, no error.
	won, err = repo.RevokeByHash(ctx, "no-such-token-"+uuid.New().String())
	require.NoError(t, err)
	assert.False(t, won, "revoking a nonexistent token must report false, not true")
}
