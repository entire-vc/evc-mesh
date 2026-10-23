package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// chatDeleteFixture is a self-contained two-workspace fixture. Like the other
// DATABASE_URL tests in this package it skips without a database and carries no
// build tag, so the diff-coverage gate sees these lines run in the default job.
type chatDeleteFixture struct {
	db             *sqlx.DB
	wsA, wsB       uuid.UUID
	userA, userB   uuid.UUID
	agentA, agentB uuid.UUID
	projA, projB   uuid.UUID
}

func newChatDeleteFixture(t *testing.T) *chatDeleteFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the chat deletion tests against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	f := &chatDeleteFixture{db: db}
	mk := func(label string) (ws, user, agent, proj uuid.UUID) {
		w := &domain.Workspace{ID: uuid.New(), Name: "Chat " + label, Slug: "chat-" + uuid.NewString(), OwnerID: uuid.New()}
		require.NoError(t, NewWorkspaceRepo(db).Create(ctx, w))
		t.Cleanup(func() { _, err := db.Exec(`DELETE FROM workspaces WHERE id=$1`, w.ID); require.NoError(t, err) })
		p := &domain.Project{ID: uuid.New(), WorkspaceID: w.ID, Name: "Chat", Slug: "chat-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
		require.NoError(t, NewProjectRepo(db).Create(ctx, p))
		user, agent = uuid.New(), uuid.New()
		_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1, $2, $3, 'x', 'chat user')`, user, user.String()+"@chat.test", "chat-"+user.String()[:30])
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, user) })
		_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, 'chat agent', $3, 'h', 'p')`, agent, w.ID, "chat-agent-"+uuid.NewString()[:8])
		require.NoError(t, err)
		return w.ID, user, agent, p.ID
	}
	f.wsA, f.userA, f.agentA, f.projA = mk("A")
	f.wsB, f.userB, f.agentB, f.projB = mk("B")
	return f
}

func (f *chatDeleteFixture) insertThread(t *testing.T, ws, proj, agent, user uuid.UUID) (uuid.UUID, error) {
	t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, title, last_message_at) VALUES ($1,$2,$3,$4,$5,'t', now())`, id, ws, proj, agent, user)
	return id, err
}

type fakeChatStore struct {
	deleted []string
	failOn  map[string]error
}

func (s *fakeChatStore) Delete(_ context.Context, key string) error {
	if err := s.failOn[key]; err != nil {
		return err
	}
	s.deleted = append(s.deleted, key)
	return nil
}

func userActor(id uuid.UUID) UserRuntimeActor { return UserRuntimeActor{UserID: id} }

// threadWithTurn inserts a thread with one message, one active turn and two attachments.
func (f *chatDeleteFixture) threadWithTurn(t *testing.T, state string) (thread uuid.UUID, keys []string) {
	t.Helper()
	thread, err := f.insertThread(t, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE ext_agent_chat_threads SET runtime = $2::jsonb WHERE id = $1`, thread,
		fmt.Sprintf(`{"runner_id":%q,"harness":"claude","session_id":"s-1"}`, uuid.New()))
	require.NoError(t, err)
	msg, agentMsg := uuid.New(), uuid.New()
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body) VALUES ($1,$2,$3,'user','hi')`, msg, f.wsA, thread)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, status) VALUES ($1,$2,$3,'agent','streaming')`, agentMsg, f.wsA, thread)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_turns (workspace_id, thread_id, agent_id, user_message_id, agent_message_id, state, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6, now() + interval '1 day')`, f.wsA, thread, f.agentA, msg, agentMsg, state)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("chat/%s/%d.png", thread, i)
		keys = append(keys, key)
		_, err = f.db.Exec(`INSERT INTO ext_agent_chat_attachments (workspace_id, thread_id, message_id, s3_key, file_name, mime, size)
			VALUES ($1,$2,$3,$4,'a.png','image/png',10)`, f.wsA, thread, msg, key)
		require.NoError(t, err)
	}
	return thread, keys
}

func (f *chatDeleteFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, q, args...))
	return n
}

func TestChatSoftDeleteCancelsTurnAndQueuesCommands(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, &fakeChatStore{})
	thread, _ := f.threadWithTurn(t, "streaming")

	require.NoError(t, repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, thread))

	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL AND execution_epoch = 1`, thread))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_turns WHERE thread_id=$1 AND state='cancelled' AND stop_reason='thread_deleted'`, thread))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_messages WHERE thread_id=$1 AND role='agent' AND status='cancelled'`, thread))
	require.Equal(t, 2, f.count(t, `SELECT count(*) FROM ext_agent_chat_commands WHERE thread_id=$1 AND state='pending'
		AND session_id='s-1' AND harness='claude' AND runner_id IS NOT NULL AND kind IN ('stop_session','delete_transcript')`, thread))
	// Soft only: rows and the transcript stay until the purge.
	require.Equal(t, 2, f.count(t, `SELECT count(*) FROM ext_agent_chat_attachments WHERE thread_id=$1`, thread))

	err := repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, thread)
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Code, "a repeated DELETE is a 404")
}

func TestChatSoftDeleteQueuedTurnIsCancelledAtOnce(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, &fakeChatStore{})
	thread, _ := f.threadWithTurn(t, "queued")
	require.NoError(t, repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, thread))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_turns WHERE thread_id=$1 AND state='cancelled'`, thread))
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_turns WHERE thread_id=$1 AND state IN ('queued','claimed','streaming')`, thread))
}

// Red control: the same call by another user must change nothing.
func TestChatSoftDeleteOtherUsersThreadIsNotFound(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, &fakeChatStore{})
	thread, _ := f.threadWithTurn(t, "streaming")

	err := repo.SoftDeleteThread(context.Background(), userActor(f.userB), f.projA, thread)
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Code)
	require.Equal(t, "thread_not_found", ae.Details)
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, thread), "B must not delete A's thread")
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_commands WHERE thread_id=$1`, thread))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_turns WHERE thread_id=$1 AND state='streaming'`, thread))

	// An agent or connector principal is refused too.
	err = repo.SoftDeleteThread(context.Background(), UserRuntimeActor{UserID: f.userA, AgentID: f.agentA}, f.projA, thread)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 403, ae.Code)

	// Right user, wrong project: the thread is not found either.
	err = repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projB, thread)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Code)

	// The owner can: the 404 above came from ownership, not from the thread.
	require.NoError(t, repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, thread))
}

func TestChatPurgeRemovesRowsAndS3AfterThirtyDays(t *testing.T) {
	f := newChatDeleteFixture(t)
	store := &fakeChatStore{}
	repo := NewChatThreadDeleteRepo(f.db, store)
	thread, keys := f.threadWithTurn(t, "done")
	fresh, _ := f.threadWithTurn(t, "done")
	live, _ := f.threadWithTurn(t, "done")
	require.NoError(t, repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, thread))
	require.NoError(t, repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, fresh))

	// 29 days: nothing yet.
	_, err := f.db.Exec(`UPDATE ext_agent_chat_threads SET deleted_at = now() - interval '29 days' WHERE id IN ($1,$2)`, thread, fresh)
	require.NoError(t, err)
	res, err := repo.PurgeDeleted(context.Background(), 100)
	require.NoError(t, err)
	require.Zero(t, res.Threads)
	require.Empty(t, store.deleted)

	// 31 days for one of them.
	_, err = f.db.Exec(`UPDATE ext_agent_chat_threads SET deleted_at = now() - interval '31 days' WHERE id = $1`, thread)
	require.NoError(t, err)
	res, err = repo.PurgeDeleted(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, PurgeResult{Threads: 1, Objects: 2}, res)
	require.ElementsMatch(t, keys, store.deleted)
	for _, tbl := range []string{"threads WHERE id", "messages WHERE thread_id", "turns WHERE thread_id", "attachments WHERE thread_id"} {
		require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_`+tbl+`=$1`, thread), tbl)
	}
	// Other threads (29 days deleted, live) are untouched.
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1`, fresh))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NULL`, live))
	require.Equal(t, 2, f.count(t, `SELECT count(*) FROM ext_agent_chat_attachments WHERE thread_id=$1`, live))
	// The pending runner commands outlive the thread row.
	require.Equal(t, 2, f.count(t, `SELECT count(*) FROM ext_agent_chat_commands WHERE thread_id=$1`, thread))
}

func TestChatPurgeKeepsRowsWhenS3DeleteFails(t *testing.T) {
	f := newChatDeleteFixture(t)
	thread, keys := f.threadWithTurn(t, "done")
	store := &fakeChatStore{failOn: map[string]error{keys[1]: errors.New("storage down")}}
	repo := NewChatThreadDeleteRepo(f.db, store)
	require.NoError(t, repo.SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, thread))
	_, err := f.db.Exec(`UPDATE ext_agent_chat_threads SET deleted_at = now() - interval '31 days' WHERE id = $1`, thread)
	require.NoError(t, err)

	_, err = repo.PurgeDeleted(context.Background(), 100)
	require.Error(t, err)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1`, thread), "rows stay so the purge can retry")

	// An object already gone counts as deleted; the retry then completes.
	store.failOn[keys[1]] = fmt.Errorf("%w: gone", storage.ErrNotFound)
	res, err := repo.PurgeDeleted(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, res.Threads)
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1`, thread))
}

func TestChatExpireHistoryUsesWorkspaceRetention(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, &fakeChatStore{})
	old, _ := f.threadWithTurn(t, "done")
	recent, _ := f.threadWithTurn(t, "done")
	busy, _ := f.threadWithTurn(t, "streaming")
	_, err := f.db.Exec(`UPDATE ext_agent_chat_threads SET last_message_at = now() - interval '40 days' WHERE id IN ($1,$2,$3)`, old, recent, busy)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE ext_agent_chat_threads SET last_message_at = now() - interval '2 days' WHERE id = $1`, recent)
	require.NoError(t, err)

	// Setting absent: keep forever (red control for the setting).
	_, err = repo.ExpireHistory(context.Background(), 100)
	require.NoError(t, err)
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, old))

	_, err = f.db.Exec(`UPDATE workspaces SET settings = '{"agent_chat":{"history_retention_days":30}}'::jsonb WHERE id=$1`, f.wsA)
	require.NoError(t, err)
	_, err = repo.ExpireHistory(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, old))
	require.Equal(t, 2, f.count(t, `SELECT count(*) FROM ext_agent_chat_commands WHERE thread_id=$1 AND detail='retention'`, old))
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, recent), "within retention")
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, busy), "an active turn keeps the thread")
}

func TestChatPurgeWithoutObjectStoreKeepsRows(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, nil)
	thread, _ := f.threadWithTurn(t, "done")
	require.NoError(t, NewChatThreadDeleteRepo(f.db, &fakeChatStore{}).SoftDeleteThread(context.Background(), userActor(f.userA), f.projA, thread))
	_, err := f.db.Exec(`UPDATE ext_agent_chat_threads SET deleted_at = now() - interval '31 days' WHERE id = $1`, thread)
	require.NoError(t, err)
	res, err := repo.PurgeDeleted(context.Background(), 100)
	require.NoError(t, err)
	require.Zero(t, res.Threads)
	require.Equal(t, 2, f.count(t, `SELECT count(*) FROM ext_agent_chat_attachments WHERE thread_id=$1`, thread))
}

func TestChatExpireHistoryBusyThreadsDoNotStarveBatch(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, &fakeChatStore{})
	busy, _ := f.threadWithTurn(t, "streaming")
	old, _ := f.threadWithTurn(t, "done")
	_, err := f.db.Exec(`UPDATE ext_agent_chat_threads SET last_message_at = now() - interval '40 days' WHERE id IN ($1,$2)`, busy, old)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE workspaces SET settings = '{"agent_chat":{"history_retention_days":30}}'::jsonb WHERE id=$1`, f.wsA)
	require.NoError(t, err)
	// Batch of one: the busy thread must not occupy the only slot.
	n, err := repo.ExpireHistory(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, old))
}

// A cancelled context fails the first statement of every entry point; the
// errors must surface instead of being swallowed.
func TestChatDeleteRepoSurfacesDatabaseErrors(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, &fakeChatStore{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, repo.SoftDeleteThread(ctx, userActor(f.userA), f.projA, uuid.New()))
	_, err := repo.ExpireHistory(ctx, 10)
	require.Error(t, err)
	_, err = repo.expireOne(ctx, uuid.New())
	require.Error(t, err)
	_, err = repo.PurgeDeleted(ctx, 10)
	require.Error(t, err)
}

// expireOne re-checks eligibility under the lock: a thread that is not past its
// retention (or does not exist) is left alone.
func TestChatExpireOneSkipsIneligibleThread(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatThreadDeleteRepo(f.db, &fakeChatStore{})
	thread, _ := f.threadWithTurn(t, "done")
	ok, err := repo.expireOne(context.Background(), thread)
	require.NoError(t, err)
	require.False(t, ok, "no retention setting: not eligible")
	require.Zero(t, f.count(t, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, thread))
}
