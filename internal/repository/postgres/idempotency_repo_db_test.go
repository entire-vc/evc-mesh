package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// DB-backed, no build tag: skipped without Postgres. The repo's own contract,
// claim by claim; the HTTP behaviour on top of it is pinned in
// internal/middleware/idempotency_db_test.go.
func TestIdempotencyRepo_ClaimCompleteRelease_DB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	repo := NewIdempotencyRepo(db)
	s := domain.IdempotencyScope{
		ActorType: domain.ActorTypeAgent, ActorID: uuid.New(),
		Method: "POST", Path: "/api/v1/tasks/" + uuid.New().String() + "/comments", Key: "k1",
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM idempotency_keys WHERE actor_id=$1`, s.ActorID) })

	rec, token, err := repo.Claim(ctx, s, "h1")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, token)
	assert.Nil(t, rec)

	// Held and unanswered: the second caller sees the in_flight claim.
	rec, other, err := repo.Claim(ctx, s, "h1")
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, other)
	require.NotNil(t, rec)
	assert.Equal(t, domain.IdempotencyInFlight, rec.State)
	assert.Equal(t, "h1", rec.RequestHash)

	// Complete under a foreign token touches nothing.
	require.NoError(t, repo.Complete(ctx, s, uuid.New(), 201, "application/json", []byte(`{}`)))
	rec, _, err = repo.Claim(ctx, s, "h1")
	require.NoError(t, err)
	assert.Equal(t, domain.IdempotencyInFlight, rec.State)

	require.NoError(t, repo.Complete(ctx, s, token, 201, "application/json", []byte(`{"id":"c1"}`)))
	rec, other, err = repo.Claim(ctx, s, "h1")
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, other)
	assert.Equal(t, domain.IdempotencyDone, rec.State)
	require.NotNil(t, rec.ResponseStatus)
	assert.Equal(t, 201, *rec.ResponseStatus)
	require.NotNil(t, rec.ResponseType)
	assert.Equal(t, "application/json", *rec.ResponseType)
	assert.JSONEq(t, `{"id":"c1"}`, string(rec.ResponseBody))

	// Release only drops an in_flight claim: a done record stays.
	require.NoError(t, repo.Release(ctx, s, token))
	rec, _, err = repo.Claim(ctx, s, "h1")
	require.NoError(t, err)
	assert.Equal(t, domain.IdempotencyDone, rec.State)

	// An in_flight claim that is released can be claimed again.
	s2 := s
	s2.Key = "k2"
	_, t2, err := repo.Claim(ctx, s2, "h2")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, t2)
	require.NoError(t, repo.Release(ctx, s2, t2))
	_, t2b, err := repo.Claim(ctx, s2, "h2")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, t2b)
}

// Expired records are forgotten (and purged), a stale in_flight claim is taken
// over under a new token, and the request that lost it can no longer complete
// or release the key.
func TestIdempotencyRepo_ExpiryAndStaleTakeover_DB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	repo := NewIdempotencyRepo(db)
	actor := uuid.New()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM idempotency_keys WHERE actor_id=$1`, actor) })
	scope := func(k string) domain.IdempotencyScope {
		return domain.IdempotencyScope{ActorType: domain.ActorTypeUser, ActorID: actor, Method: "POST", Path: "/x", Key: k}
	}
	age := func(k, col, by string) {
		t.Helper()
		_, err := db.Exec(`UPDATE idempotency_keys SET `+col+` = `+col+` - interval '`+by+`' WHERE actor_id=$1 AND idem_key=$2`, actor, k)
		require.NoError(t, err)
	}

	_, tok, err := repo.Claim(ctx, scope("exp"), "h")
	require.NoError(t, err)
	require.NoError(t, repo.Complete(ctx, scope("exp"), tok, 201, "application/json", []byte(`{}`)))
	age("exp", "expires_at", "25 hours")
	_, tok, err = repo.Claim(ctx, scope("exp"), "other-body")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, tok, "an expired key is free again, whatever the body")

	// A purge-only victim: expired, never touched again by its own scope.
	_, _, err = repo.Claim(ctx, scope("purge"), "h")
	require.NoError(t, err)
	age("purge", "expires_at", "25 hours")
	_, _, err = repo.Claim(ctx, scope("trigger"), "h")
	require.NoError(t, err)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM idempotency_keys WHERE actor_id=$1 AND idem_key='purge'`, actor))
	assert.Zero(t, n, "a later claim purges expired records")

	_, first, err := repo.Claim(ctx, scope("stale"), "h")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, first)
	_, none, err := repo.Claim(ctx, scope("stale"), "h")
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, none, "a fresh in_flight claim holds")
	age("stale", "claimed_at", "6 minutes")
	_, second, err := repo.Claim(ctx, scope("stale"), "h")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, second, "an in_flight claim past 5 minutes is taken over")
	require.NotEqual(t, first, second)

	// The slow original finishes late: its token is gone, it changes nothing.
	require.NoError(t, repo.Complete(ctx, scope("stale"), first, 201, "application/json", []byte(`{"from":"first"}`)))
	require.NoError(t, repo.Release(ctx, scope("stale"), first))
	rec, _, err := repo.Claim(ctx, scope("stale"), "h")
	require.NoError(t, err)
	require.NotNil(t, rec, "the takeover's claim survived the old request's release")
	assert.Equal(t, domain.IdempotencyInFlight, rec.State)

	require.NoError(t, repo.Complete(ctx, scope("stale"), second, 201, "application/json", []byte(`{"from":"second"}`)))
	rec, _, err = repo.Claim(ctx, scope("stale"), "h")
	require.NoError(t, err)
	assert.JSONEq(t, `{"from":"second"}`, string(rec.ResponseBody))
}
