package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here. These pin what only the database can: the epoch/stop rules inside
// the issue transaction, the supersede deadline, and the attribution that lands
// in activity_log.changes.

type chatRepoFixture struct {
	repo                                  *ChatSessionTokenRepo
	user, agent, workspace, project, sess uuid.UUID
	thread                                uuid.UUID
}

func newChatRepoFixture(t *testing.T) *chatRepoFixture {
	t.Helper()
	db := agentDigestTestDB(t)
	agent := newDigestAgent(t, db, "")
	var owner uuid.UUID
	require.NoError(t, db.Get(&owner, `SELECT owner_id FROM workspaces WHERE id = $1`, agent.WorkspaceID))
	project := uuid.New()
	_, err := db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'chat repo', $3)`,
		project, agent.WorkspaceID, "chat-repo-"+project.String()[:8])
	require.NoError(t, err)
	return &chatRepoFixture{
		repo: NewChatSessionTokenRepo(db), user: owner, agent: agent.ID, workspace: agent.WorkspaceID,
		project: project, sess: uuid.New(), thread: uuid.New(),
	}
}

func (f *chatRepoFixture) token(epoch int64, ttl time.Duration) *domain.ChatSessionToken {
	now := time.Now()
	return &domain.ChatSessionToken{
		ID: uuid.New(), TokenHash: "h-" + uuid.New().String(),
		UserID: f.user, AgentID: f.agent, WorkspaceID: f.workspace, ProjectID: f.project,
		ChatThreadID: f.thread, ChatSessionID: f.sess, RunnerID: "runner-1", ExecutionEpoch: epoch,
		IssuedAt: now, ExpiresAt: now.Add(ttl),
	}
}

func TestChatSessionTokenRepo_RoundTripAndMisses(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	tok := f.token(1, 5*time.Minute)
	require.NoError(t, f.repo.Issue(ctx, tok, time.Now().Add(10*time.Second)))

	got, err := f.repo.GetByHash(ctx, tok.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, tok.ID, got.ID)
	assert.Equal(t, int64(1), got.ExecutionEpoch)
	assert.Nil(t, got.RevokedAt)

	latest, err := f.repo.LatestForSession(ctx, f.sess)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, tok.ID, latest.ID)

	miss, err := f.repo.GetByHash(ctx, "no-such-hash")
	require.NoError(t, err)
	assert.Nil(t, miss, "an unknown hash is (nil, nil), not an error")
	none, err := f.repo.LatestForSession(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, none, "a session that never had a token is (nil, nil)")
}

func TestChatSessionTokenRepo_RenewalSupersedesWithABoundedOverlap(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	first := f.token(3, 5*time.Minute)
	require.NoError(t, f.repo.Issue(ctx, first, time.Now().Add(10*time.Second)))

	deadline := time.Now().Add(10 * time.Second)
	second := f.token(3, 5*time.Minute)
	require.NoError(t, f.repo.Issue(ctx, second, deadline))

	old, err := f.repo.GetByHash(ctx, first.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, old.RevokedAt, "the older token must get a deadline, not live forever")
	assert.WithinDuration(t, deadline, *old.RevokedAt, time.Second)
	require.NotNil(t, old.RevokedReason)
	assert.Equal(t, domain.ChatSessionRevokedSuperseded, *old.RevokedReason)
	assert.False(t, old.SessionEnded(), "a supersede is not the end of the session")
	assert.True(t, old.IsUsable(time.Now()), "still usable inside the overlap")
	assert.False(t, old.IsUsable(time.Now().Add(11*time.Second)), "dead after the overlap")

	cur, err := f.repo.GetByHash(ctx, second.TokenHash)
	require.NoError(t, err)
	assert.Nil(t, cur.RevokedAt)
}

func TestChatSessionTokenRepo_EpochAndStopRulesInsideTheIssueTransaction(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	require.NoError(t, f.repo.Issue(ctx, f.token(5, time.Minute), time.Now().Add(10*time.Second)))

	assert.ErrorIs(t, f.repo.Issue(ctx, f.token(4, time.Minute), time.Now().Add(10*time.Second)),
		repository.ErrChatEpochStale, "an older execution must not issue")
	require.NoError(t, f.repo.Issue(ctx, f.token(6, time.Minute), time.Now().Add(10*time.Second)),
		"a newer execution takes the session over")

	n, err := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, time.Now())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))
	again, err := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, time.Now().Add(time.Second))
	require.NoError(t, err)
	assert.Zero(t, again, "a second stop has nothing left to revoke")

	assert.ErrorIs(t, f.repo.Issue(ctx, f.token(6, time.Minute), time.Now().Add(10*time.Second)),
		repository.ErrChatEpochStale, "a stopped session does not come back at the same epoch")
	assert.ErrorIs(t, f.repo.Issue(ctx, f.token(7, time.Minute), time.Now().Add(10*time.Second)),
		repository.ErrChatEpochStale, "nor under a higher one: a stop is final for the session id")
}

func TestChatSessionTokenRepo_StopPullsForwardAPendingSupersedeDeadline(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	first := f.token(1, 5*time.Minute)
	require.NoError(t, f.repo.Issue(ctx, first, time.Now().Add(10*time.Second)))
	require.NoError(t, f.repo.Issue(ctx, f.token(1, 5*time.Minute), time.Now().Add(10*time.Second)))

	stopAt := time.Now()
	_, err := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, stopAt)
	require.NoError(t, err)
	old, err := f.repo.GetByHash(ctx, first.TokenHash)
	require.NoError(t, err)
	assert.False(t, old.IsUsable(stopAt.Add(time.Second)), "Stop is immediate: the overlap must not outlive it")
	assert.True(t, old.SessionEnded())
}

func TestChatSessionTokenRepo_PurgeExpiredKeepsLiveRows(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	dead := f.token(1, -48*time.Hour)
	live := f.token(1, time.Hour)
	require.NoError(t, f.repo.Issue(ctx, dead, time.Now().Add(10*time.Second)))
	require.NoError(t, f.repo.Issue(ctx, live, time.Now().Add(10*time.Second)))

	n, err := f.repo.PurgeExpired(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))

	gone, err := f.repo.GetByHash(ctx, dead.TokenHash)
	require.NoError(t, err)
	assert.Nil(t, gone)
	kept, err := f.repo.GetByHash(ctx, live.TokenHash)
	require.NoError(t, err)
	assert.NotNil(t, kept)
}

// The attribution of a chat-session request is merged into activity_log.changes
// by the generic writer, and a handler-supplied key cannot shadow it.
func TestActivityLogRepo_CreateCarriesChatAttribution(t *testing.T) {
	f := newChatRepoFixture(t)
	repo := NewActivityLogRepo(f.repo.db)
	user, thread, session := f.user, f.thread, f.sess
	ctx := actorctx.WithChatAttribution(context.Background(), actorctx.ChatAttribution{
		InitiatedByUserID: user, ChatThreadID: thread, ChatSessionID: session, ExecutionEpoch: 9,
	})
	entry := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: f.workspace, EntityType: "project", EntityID: f.project,
		Action: "project.updated", ActorID: f.agent, ActorType: domain.ActorTypeAgent,
		Changes:   json.RawMessage(`{"initiated_by_user_id":"` + uuid.New().String() + `","name":"x"}`),
		CreatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(ctx, entry))

	var raw []byte
	require.NoError(t, f.repo.db.Get(&raw, `SELECT changes FROM activity_log WHERE id = $1`, entry.ID))
	var changes map[string]any
	require.NoError(t, json.Unmarshal(raw, &changes))
	assert.Equal(t, user.String(), changes["initiated_by_user_id"], "the verified user overwrites a supplied one")
	assert.Equal(t, thread.String(), changes["chat_thread_id"])
	assert.Equal(t, session.String(), changes["chat_session_id"])
	assert.EqualValues(t, 9, changes["execution_epoch"])
	assert.Equal(t, "x", changes["name"], "the handler's own keys survive")

	// Without a chat context nothing is added.
	plain := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: f.workspace, EntityType: "project", EntityID: f.project,
		Action: "project.updated", ActorID: f.agent, ActorType: domain.ActorTypeAgent, CreatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(context.Background(), plain))
	require.NoError(t, f.repo.db.Get(&raw, `SELECT changes FROM activity_log WHERE id = $1`, plain.ID))
	assert.JSONEq(t, `{}`, string(raw))
}

// Fencing: within one epoch the session belongs to one runner. A different
// runner at the same epoch is refused (and so is the stale one after a takeover
// under a newer epoch); the holder itself renews freely.
func TestChatSessionTokenRepo_SameEpochDifferentRunnerIsFenced(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	require.NoError(t, f.repo.Issue(ctx, f.token(2, time.Minute), time.Now().Add(10*time.Second)))
	require.NoError(t, f.repo.Issue(ctx, f.token(2, time.Minute), time.Now().Add(10*time.Second)), "the holder renews at its own epoch")

	intruder := f.token(2, time.Minute)
	intruder.RunnerID = "runner-2"
	assert.ErrorIs(t, f.repo.Issue(ctx, intruder, time.Now().Add(10*time.Second)), repository.ErrChatEpochStale)
	latest, err := f.repo.LatestForSession(ctx, f.sess)
	require.NoError(t, err)
	assert.Equal(t, "runner-1", latest.RunnerID, "the refused issue must not have replaced the active token")
	assert.Nil(t, latest.RevokedAt)

	takeover := f.token(3, time.Minute)
	takeover.RunnerID = "runner-2"
	require.NoError(t, f.repo.Issue(ctx, takeover, time.Now().Add(10*time.Second)), "a newer epoch hands the session to another runner")
	stale := f.token(3, time.Minute)
	assert.ErrorIs(t, f.repo.Issue(ctx, stale, time.Now().Add(10*time.Second)), repository.ErrChatEpochStale,
		"the previous runner cannot issue at the epoch it lost")
}

// The same runner at the same epoch cannot rebind the session: any change of
// user, agent, workspace, project or thread is refused and the holder is untouched.
func TestChatSessionTokenRepo_SameEpochSameRunnerRebindIsFenced(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	require.NoError(t, f.repo.Issue(ctx, f.token(2, time.Minute), time.Now().Add(10*time.Second)))

	rebinds := map[string]func(*domain.ChatSessionToken){
		"project":   func(tk *domain.ChatSessionToken) { tk.ProjectID = uuid.New() },
		"agent":     func(tk *domain.ChatSessionToken) { tk.AgentID = uuid.New() },
		"user":      func(tk *domain.ChatSessionToken) { tk.UserID = uuid.New() },
		"workspace": func(tk *domain.ChatSessionToken) { tk.WorkspaceID = uuid.New() },
		"thread":    func(tk *domain.ChatSessionToken) { tk.ChatThreadID = uuid.New() },
	}
	for name, mutate := range rebinds {
		tk := f.token(2, time.Minute)
		mutate(tk)
		assert.ErrorIs(t, f.repo.Issue(ctx, tk, time.Now().Add(10*time.Second)), repository.ErrChatEpochStale, name)
	}
	latest, err := f.repo.LatestForSession(ctx, f.sess)
	require.NoError(t, err)
	assert.Equal(t, f.project, latest.ProjectID, "a refused rebind must not replace the active token")
	assert.Nil(t, latest.RevokedAt)
}

// A takeover under a newer epoch ends the previous runner's tokens at once; the
// bounded overlap is only for the holder renewing at its own epoch.
func TestChatSessionTokenRepo_TakeoverRevokesOlderEpochImmediately(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	old := f.token(1, 5*time.Minute)
	require.NoError(t, f.repo.Issue(ctx, old, time.Now().Add(10*time.Second)))
	renewed := f.token(1, 5*time.Minute)
	require.NoError(t, f.repo.Issue(ctx, renewed, time.Now().Add(10*time.Second)))

	takeover := f.token(2, 5*time.Minute)
	takeover.RunnerID = "runner-2"
	takeover.IssuedAt = time.Now()
	require.NoError(t, f.repo.Issue(ctx, takeover, takeover.IssuedAt.Add(10*time.Second)))

	after := time.Now().Add(time.Second)
	for _, h := range []string{old.TokenHash, renewed.TokenHash} {
		got, err := f.repo.GetByHash(ctx, h)
		require.NoError(t, err)
		assert.False(t, got.IsUsable(after), "the previous epoch's token must not outlive the takeover by the overlap")
		assert.False(t, got.SessionEnded(), "a takeover is not a stop")
	}
	cur, err := f.repo.GetByHash(ctx, takeover.TokenHash)
	require.NoError(t, err)
	assert.True(t, cur.IsUsable(after))
}

// Stop is serialised with issuance: an issue that is mid-transaction when Stop
// arrives must not commit a live token after Stop returned.
func TestChatSessionTokenRepo_StopWaitsForAnInFlightIssue(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()
	tok := f.token(1, 5*time.Minute)

	// An "issue in flight": holds the session lock and has inserted its row, not committed.
	tx, err := f.repo.db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text))`, "chat_session:"+f.sess.String())
	require.NoError(t, err)
	_, err = tx.NamedExecContext(ctx, `
		INSERT INTO chat_session_tokens (`+chatSessionTokenCols+`)
		VALUES (:id, :token_hash, :user_id, :agent_id, :workspace_id, :project_id, :chat_thread_id, :chat_session_id, :runner_id, :execution_epoch, :issued_at, :expires_at, :revoked_at, :revoked_reason)`, tok)
	require.NoError(t, err)

	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, rerr := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, time.Now())
		done <- result{n, rerr}
	}()
	select {
	case <-done:
		t.Fatal("Stop returned while an issue held the session lock: it can leave a live token behind")
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())

	r := <-done
	require.NoError(t, r.err)
	assert.EqualValues(t, 1, r.n, "Stop must see and revoke the token the in-flight issue committed")
	got, err := f.repo.GetByHash(ctx, tok.TokenHash)
	require.NoError(t, err)
	assert.False(t, got.IsUsable(time.Now().Add(time.Second)))
	assert.True(t, got.SessionEnded())
}

func TestChatSessionTokenRepo_StopBeforeTheFirstIssueEndsTheSession(t *testing.T) {
	f := newChatRepoFixture(t)
	ctx := context.Background()

	n, err := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, time.Now())
	require.NoError(t, err)
	assert.Zero(t, n, "a session without tokens has nothing to revoke")

	for _, epoch := range []int64{1, 2} {
		assert.ErrorIs(t, f.repo.Issue(ctx, f.token(epoch, time.Minute), time.Now().Add(10*time.Second)),
			repository.ErrChatEpochStale, "Stop-first: no token may be issued after the stop, at any epoch")
	}
	latest, err := f.repo.LatestForSession(ctx, f.sess)
	require.NoError(t, err)
	assert.Nil(t, latest, "the refused issue left no row behind")
}

func TestChatSessionTokenRepo_StopOrderingsAroundIssue(t *testing.T) {
	ctx := context.Background()
	soon := func() time.Time { return time.Now().Add(10 * time.Second) }

	t.Run("stop after issue", func(t *testing.T) {
		f := newChatRepoFixture(t)
		tok := f.token(1, time.Minute)
		require.NoError(t, f.repo.Issue(ctx, tok, soon()))
		n, err := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, time.Now())
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		got, err := f.repo.GetByHash(ctx, tok.TokenHash)
		require.NoError(t, err)
		assert.False(t, got.IsUsable(time.Now().Add(time.Second)))
		assert.ErrorIs(t, f.repo.Issue(ctx, f.token(1, time.Minute), soon()), repository.ErrChatEpochStale)
	})

	t.Run("double stop keeps the first tombstone", func(t *testing.T) {
		f := newChatRepoFixture(t)
		first := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
		_, err := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, first)
		require.NoError(t, err)
		_, err = f.repo.RevokeSession(ctx, f.sess, "manual", time.Now())
		require.NoError(t, err)
		var reason string
		var count int
		require.NoError(t, f.repo.db.Get(&reason, `SELECT reason FROM chat_session_stops WHERE chat_session_id = $1`, f.sess))
		require.NoError(t, f.repo.db.Get(&count, `SELECT COUNT(*) FROM chat_session_stops WHERE chat_session_id = $1`, f.sess))
		assert.Equal(t, domain.ChatSessionRevokedStop, reason)
		assert.Equal(t, 1, count)
	})

	t.Run("stop survives the purge of the expired tokens", func(t *testing.T) {
		f := newChatRepoFixture(t)
		require.NoError(t, f.repo.Issue(ctx, f.token(1, time.Millisecond), soon()))
		_, err := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, time.Now())
		require.NoError(t, err)
		_, err = f.repo.PurgeExpired(ctx, time.Now().Add(time.Hour))
		require.NoError(t, err)
		latest, err := f.repo.LatestForSession(ctx, f.sess)
		require.NoError(t, err)
		require.Nil(t, latest, "token rows are gone")
		assert.ErrorIs(t, f.repo.Issue(ctx, f.token(2, time.Minute), soon()), repository.ErrChatEpochStale,
			"a late issue after the purge is still refused")
	})

	t.Run("a stop does not leak to another session", func(t *testing.T) {
		f := newChatRepoFixture(t)
		_, err := f.repo.RevokeSession(ctx, uuid.New(), domain.ChatSessionRevokedStop, time.Now())
		require.NoError(t, err)
		require.NoError(t, f.repo.Issue(ctx, f.token(1, time.Minute), soon()))
	})

	t.Run("a stop waiting on the lock still beats a later issue", func(t *testing.T) {
		f := newChatRepoFixture(t)
		tx, err := f.repo.db.BeginTxx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text))`, "chat_session:"+f.sess.String())
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			_, rerr := f.repo.RevokeSession(ctx, f.sess, domain.ChatSessionRevokedStop, time.Now())
			done <- rerr
		}()
		select {
		case <-done:
			t.Fatal("Stop must wait for the session lock")
		case <-time.After(300 * time.Millisecond):
		}
		require.NoError(t, tx.Commit())
		require.NoError(t, <-done)
		assert.ErrorIs(t, f.repo.Issue(ctx, f.token(1, time.Minute), soon()), repository.ErrChatEpochStale)
	})
}
