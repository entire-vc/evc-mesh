package postgres

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Same convention as agent_api_key_sha256_db_test.go: real Postgres via
// DATABASE_URL, skipped when unreachable, no build tag.

func hash64(seed string) string {
	return strings.Repeat(seed, 64)[:64]
}

func TestExtensionActorRepo_Lifecycle(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	agent := newDigestAgent(t, db, "d-"+uuid.New().String())
	wsID := agent.WorkspaceID

	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: wsID, Provider: domain.IntegrationProviderGitLab, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, NewIntegrationRepo(db).Upsert(ctx, conn))
	repo := NewExtensionActorRepo(db)

	h1 := hash64("a")
	row, err := repo.Issue(ctx, &domain.ExtensionActor{
		ConnectionID: conn.ID, WorkspaceID: wsID, ActorID: uuid.New(), ExtensionID: "gitlab",
		DisplayName: "GitLab", TokenHash: h1, Capabilities: []byte(`{"api":["tasks:read"]}`),
	})
	require.NoError(t, err)
	firstActor := row.ActorID

	got, err := repo.GetByConnection(ctx, conn.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "GitLab", got.DisplayName)
	assert.JSONEq(t, `{"api":["tasks:read"]}`, string(got.Capabilities))

	none, err := repo.GetByConnection(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, none)

	byHash, err := repo.GetByTokenHash(ctx, h1, time.Now())
	require.NoError(t, err)
	require.NotNil(t, byHash)
	assert.Equal(t, conn.ID, byHash.ConnectionID)
	miss, err := repo.GetByTokenHash(ctx, hash64("z"), time.Now())
	require.NoError(t, err)
	assert.Nil(t, miss)

	// Rotation: both hashes resolve inside the window; the old one stops after it.
	h2 := hash64("b")
	ok, err := repo.Rotate(ctx, conn.ID, h2, time.Now().Add(5*time.Minute))
	require.NoError(t, err)
	assert.True(t, ok)
	for _, h := range []string{h1, h2} {
		r, gerr := repo.GetByTokenHash(ctx, h, time.Now())
		require.NoError(t, gerr)
		assert.NotNil(t, r, h[:4])
	}
	late, err := repo.GetByTokenHash(ctx, h1, time.Now().Add(6*time.Minute))
	require.NoError(t, err)
	assert.Nil(t, late, "previous hash after its grace")
	cur, err := repo.GetByTokenHash(ctx, h2, time.Now().Add(6*time.Minute))
	require.NoError(t, err)
	assert.NotNil(t, cur)

	require.NoError(t, repo.RecordViolation(ctx, conn.ID, "POST /x requires api:tasks:write"))
	require.NoError(t, repo.RecordViolation(ctx, conn.ID, "GET /y"))
	got, err = repo.GetByConnection(ctx, conn.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.ViolationCount)
	assert.Equal(t, "GET /y", got.LastViolation)

	revoked, err := repo.Revoke(ctx, conn.ID)
	require.NoError(t, err)
	assert.True(t, revoked)
	again, err := repo.Revoke(ctx, conn.ID)
	require.NoError(t, err)
	assert.False(t, again, "already revoked")
	ok, err = repo.Rotate(ctx, conn.ID, hash64("c"), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, ok, "a revoked actor cannot rotate")
	ok, err = repo.Rotate(ctx, uuid.New(), hash64("d"), time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, ok, "unknown connection")

	// Re-issue after revoke: same actor id, fresh token, revoke cleared, counter reset.
	h3 := hash64("e")
	row, err = repo.Issue(ctx, &domain.ExtensionActor{
		ConnectionID: conn.ID, WorkspaceID: wsID, ActorID: uuid.New(), ExtensionID: "gitlab",
		DisplayName: "GitLab", TokenHash: h3, Capabilities: []byte(`{}`),
	})
	require.NoError(t, err)
	assert.Equal(t, firstActor, row.ActorID, "the actor identity survives a reconnect")
	assert.Nil(t, row.RevokedAt)
	for _, h := range []string{h1, h2} {
		r, gerr := repo.GetByTokenHash(ctx, h, time.Now())
		require.NoError(t, gerr)
		assert.Nil(t, r, "tokens of the previous issue are dead")
	}

	// A connection delete takes the actor with it.
	_, err = db.Exec(`DELETE FROM integration_configs WHERE id = $1`, conn.ID)
	require.NoError(t, err)
	gone, err := repo.GetByConnection(ctx, conn.ID)
	require.NoError(t, err)
	assert.Nil(t, gone)
}

func TestAgentAPIKeyRepo_Lifecycle(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	agent := newDigestAgent(t, db, "d-"+uuid.New().String())
	repo := NewAgentAPIKeyRepo(db)

	mk := func(prefix string) *domain.AgentAPIKey {
		return &domain.AgentAPIKey{
			ID: uuid.New(), AgentID: agent.ID, WorkspaceID: agent.WorkspaceID, KeyPrefix: prefix,
			KeySHA256: "sha-" + uuid.New().String(), Label: "l", IssuedByType: domain.ActorTypeUser, IssuedByID: uuid.New(),
		}
	}
	k1, k2 := mk("pfx1"), mk("pfx2")
	require.NoError(t, repo.Create(ctx, k1))
	require.NoError(t, repo.Create(ctx, k2))
	assert.Error(t, repo.Create(ctx, mk("pfx1")), "prefix is unique per workspace")

	got, err := repo.GetByPrefix(ctx, agent.WorkspaceID, "pfx1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, k1.ID, got.ID)
	assert.Equal(t, k1.KeySHA256, got.KeySHA256)
	nope, err := repo.GetByPrefix(ctx, agent.WorkspaceID, "nope")
	require.NoError(t, err)
	assert.Nil(t, nope)

	byID, err := repo.GetByID(ctx, agent.ID, k2.ID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	wrongAgent, err := repo.GetByID(ctx, uuid.New(), k2.ID)
	require.NoError(t, err)
	assert.Nil(t, wrongAgent, "a key is only visible through its own agent")

	n, err := repo.CountActiveByAgent(ctx, agent.ID, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	revoked, err := repo.IsRevoked(ctx, k1.ID)
	require.NoError(t, err)
	assert.False(t, revoked)
	ok, err := repo.Revoke(ctx, uuid.New(), k1.ID)
	require.NoError(t, err)
	assert.False(t, ok, "wrong agent cannot revoke")
	ok, err = repo.Revoke(ctx, agent.ID, k1.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = repo.Revoke(ctx, agent.ID, k1.ID)
	require.NoError(t, err)
	assert.False(t, ok, "second revoke is a no-op")
	revoked, err = repo.IsRevoked(ctx, k1.ID)
	require.NoError(t, err)
	assert.True(t, revoked)
	revoked, err = repo.IsRevoked(ctx, uuid.New())
	require.NoError(t, err)
	assert.True(t, revoked, "an unknown key id counts as revoked")

	n, err = repo.CountActiveByAgent(ctx, agent.ID, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	list, err := repo.ListByAgent(ctx, agent.ID)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestAgentAPIKeyRepo_LastUsedDigestAndLock(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	agent := newDigestAgent(t, db, "lu-"+uuid.New().String())
	repo := NewAgentAPIKeyRepo(db)

	k := &domain.AgentAPIKey{
		ID: uuid.New(), AgentID: agent.ID, WorkspaceID: agent.WorkspaceID, KeyPrefix: "lu" + uuid.New().String()[:6],
		KeySHA256: "sha-" + uuid.New().String(), IssuedByType: domain.ActorTypeUser, IssuedByID: uuid.New(),
	}
	require.NoError(t, repo.Create(ctx, k))

	// Additional key: unset, then stamped, then throttled (second touch keeps the first value).
	before, err := repo.GetByID(ctx, agent.ID, k.ID)
	require.NoError(t, err)
	assert.Nil(t, before.LastUsedAt)
	require.NoError(t, repo.TouchLastUsed(ctx, k.ID))
	first, err := repo.GetByID(ctx, agent.ID, k.ID)
	require.NoError(t, err)
	require.NotNil(t, first.LastUsedAt)
	require.NoError(t, repo.TouchLastUsed(ctx, k.ID))
	second, err := repo.GetByID(ctx, agent.ID, k.ID)
	require.NoError(t, err)
	assert.True(t, first.LastUsedAt.Equal(*second.LastUsedAt), "touched at most once a minute")

	// Original key: same, and an unknown agent is just empty.
	got, err := repo.PrimaryLastUsed(ctx, agent.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
	require.NoError(t, repo.TouchPrimaryLastUsed(ctx, agent.ID))
	got, err = repo.PrimaryLastUsed(ctx, agent.ID)
	require.NoError(t, err)
	assert.NotNil(t, got)
	got, err = repo.PrimaryLastUsed(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)

	// New key material starts without a last-used stamp.
	require.NoError(t, NewAgentRepo(db).RotateHomeGrantKey(ctx, agent))
	got, err = repo.PrimaryLastUsed(ctx, agent.ID)
	require.NoError(t, err)
	assert.Nil(t, got, "rotating the original key clears its last-used stamp")

	d, err := repo.PrimaryDigest(ctx, agent.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, d)
	d, err = repo.PrimaryDigest(ctx, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, d, "unknown agent")

	// Expired keys are not active.
	_, err = db.Exec(`UPDATE agent_api_keys SET expires_at = now() - interval '1 hour' WHERE id = $1`, k.ID)
	require.NoError(t, err)
	n, err := repo.CountActiveByAgent(ctx, agent.ID, time.Now())
	require.NoError(t, err)
	assert.Zero(t, n)

	// The lock is exclusive per agent: a second holder waits for the first.
	var inside, overlap int32
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = repo.WithAgentLock(ctx, agent.ID, func() error {
				if atomic.AddInt32(&inside, 1) > 1 {
					atomic.AddInt32(&overlap, 1)
				}
				time.Sleep(30 * time.Millisecond)
				atomic.AddInt32(&inside, -1)
				return nil
			})
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	assert.Zero(t, overlap, "two holders ran at once")

	// fn's error comes back and the lock is released for the next caller.
	boom := errors.New("boom")
	assert.ErrorIs(t, repo.WithAgentLock(ctx, agent.ID, func() error { return boom }), boom)
	assert.NoError(t, repo.WithAgentLock(ctx, agent.ID, func() error { return nil }))
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	assert.Error(t, repo.WithAgentLock(cctx, agent.ID, func() error { return nil }), "cancelled context")
}

func TestExtensionActorRepo_RotateSelfAndDropPrevious(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	agent := newDigestAgent(t, db, "d-"+uuid.New().String())
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: agent.WorkspaceID, Provider: domain.IntegrationProviderGitLab, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, NewIntegrationRepo(db).Upsert(ctx, conn))
	repo := NewExtensionActorRepo(db)
	uniq := func() string { return strings.ReplaceAll(uuid.New().String()+uuid.New().String(), "-", "")[:64] }
	hA, hB, hC := uniq(), uniq(), uniq()
	_, err := repo.Issue(ctx, &domain.ExtensionActor{ConnectionID: conn.ID, WorkspaceID: agent.WorkspaceID, ActorID: uuid.New(), ExtensionID: "keep", TokenHash: hA, Capabilities: []byte(`{}`)})
	require.NoError(t, err)
	now := time.Now()

	// A stranger's hash rotates nothing.
	ok, err := repo.RotateSelf(ctx, conn.ID, uniq(), hB, now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.False(t, ok)

	// Current token rotates; it becomes the previous one.
	ok, err = repo.RotateSelf(ctx, conn.ID, hA, hB, now.Add(time.Minute), now)
	require.NoError(t, err)
	require.True(t, ok)
	got, err := repo.GetByTokenHash(ctx, hA, now)
	require.NoError(t, err)
	require.NotNil(t, got, "presented token valid as previous")

	// Previous still inside its window rotates too (retry), and stays previous.
	ok, err = repo.RotateSelf(ctx, conn.ID, hA, hC, now.Add(time.Minute), now)
	require.NoError(t, err)
	require.True(t, ok)
	gone, err := repo.GetByTokenHash(ctx, hB, now)
	require.NoError(t, err)
	assert.Nil(t, gone, "token the caller never saw is dropped")

	// An expired previous cannot rotate.
	ok, err = repo.RotateSelf(ctx, conn.ID, hA, uniq(), now.Add(time.Minute), now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.False(t, ok)

	// DropPrevious: only the current token may, idempotent.
	ok, err = repo.DropPrevious(ctx, conn.ID, hA)
	require.NoError(t, err)
	assert.False(t, ok, "previous token may not drop itself")
	for i := 0; i < 2; i++ {
		ok, err = repo.DropPrevious(ctx, conn.ID, hC)
		require.NoError(t, err)
		assert.True(t, ok)
	}
	dead, err := repo.GetByTokenHash(ctx, hA, now)
	require.NoError(t, err)
	assert.Nil(t, dead)

	// Revoked actor: neither works.
	_, err = repo.Revoke(ctx, conn.ID)
	require.NoError(t, err)
	ok, err = repo.RotateSelf(ctx, conn.ID, hC, uniq(), now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = repo.DropPrevious(ctx, conn.ID, hC)
	require.NoError(t, err)
	assert.False(t, ok)
}
