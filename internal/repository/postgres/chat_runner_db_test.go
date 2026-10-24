package postgres

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func requireChatStatus(t *testing.T, err error, code int) {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, code, ae.Code)
}

// unaddressed drops the runner pin of a thread's commands so any runner of the
// agent may take them (the fixture's runtime carries a random runner_id).
func (f *chatDeleteFixture) unaddressed(t *testing.T, thread uuid.UUID) {
	t.Helper()
	_, err := f.db.Exec(`UPDATE ext_agent_chat_commands SET runner_id = NULL WHERE thread_id = $1`, thread)
	require.NoError(t, err)
}

// A deleted thread orders stop_session + delete_transcript; the ack closes each
// one and a closed command is never handed out again.
func TestChatRunnerCommandsAreDeliveredOnceAndAcked(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _ := f.threadWithTurn(t, "queued")
	require.NoError(t, NewChatThreadDeleteRepo(f.db, nil).SoftDeleteThread(ctx, userActor(f.userA), f.projA, thread))
	f.unaddressed(t, thread)

	repo := NewChatRunnerRepo(f.db)
	cmds, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, cmds, 2)
	kinds := map[string]bool{cmds[0].Kind: true, cmds[1].Kind: true}
	require.True(t, kinds["stop_session"] && kinds["delete_transcript"], "got %v", kinds)

	// at least once: listing again returns the same two until they are acked
	again, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, again, 2)

	state, err := repo.AckCommand(ctx, f.agentA, cmds[0].ID, nil, ChatAckDone, "")
	require.NoError(t, err)
	require.Equal(t, "done", state)
	state, err = repo.AckCommand(ctx, f.agentA, cmds[1].ID, nil, ChatAckNotFound, "no transcript here")
	require.NoError(t, err)
	require.Equal(t, "done", state)

	left, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Empty(t, left, "an acked command is not issued again")

	// acking twice is idempotent
	state, err = repo.AckCommand(ctx, f.agentA, cmds[0].ID, nil, ChatAckDone, "")
	require.NoError(t, err)
	require.Equal(t, "done", state)
}

func TestChatRunnerFailedAckCountsAttemptsThenDies(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _ := f.threadWithTurn(t, "queued")
	require.NoError(t, NewChatThreadDeleteRepo(f.db, nil).SoftDeleteThread(ctx, userActor(f.userA), f.projA, thread))
	f.unaddressed(t, thread)
	repo := NewChatRunnerRepo(f.db)
	cmds, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	id := cmds[0].ID
	for i := 1; i < ChatCommandMaxAttempts; i++ {
		state, ackErr := repo.AckCommand(ctx, f.agentA, id, nil, ChatAckFailed, "disk busy")
		require.NoError(t, ackErr)
		require.Equal(t, "pending", state, "attempt %d keeps the command", i)
	}
	state, err := repo.AckCommand(ctx, f.agentA, id, nil, ChatAckFailed, "disk busy")
	require.NoError(t, err)
	require.Equal(t, "dead", state)
	left, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, left, 1, "only the other command is left")
}

func TestChatRunnerExpiredCommandIsDropped(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _ := f.threadWithTurn(t, "queued")
	require.NoError(t, NewChatThreadDeleteRepo(f.db, nil).SoftDeleteThread(ctx, userActor(f.userA), f.projA, thread))
	f.unaddressed(t, thread)
	_, err := f.db.Exec(`UPDATE ext_agent_chat_commands SET expires_at = now() - interval '1 minute' WHERE thread_id = $1`, thread)
	require.NoError(t, err)
	cmds, err := NewChatRunnerRepo(f.db).PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Empty(t, cmds)
}

// The runner of another agent gets 403, never the commands or a way to ack them.
func TestChatRunnerOfAnotherAgentIsForbidden(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _ := f.threadWithTurn(t, "claimed")
	require.NoError(t, NewChatThreadDeleteRepo(f.db, nil).SoftDeleteThread(ctx, userActor(f.userA), f.projA, thread))
	f.unaddressed(t, thread)
	repo := NewChatRunnerRepo(f.db)
	cmds, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.NotEmpty(t, cmds)

	_, err = repo.PendingCommands(ctx, f.agentB, []uuid.UUID{f.agentA}, nil, 0)
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.AckCommand(ctx, f.agentB, cmds[0].ID, nil, ChatAckDone, "")
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.AckCommand(ctx, f.agentA, uuid.New(), nil, ChatAckDone, "")
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.AckCommand(ctx, f.agentA, cmds[0].ID, nil, "weird", "")
	requireChatStatus(t, err, http.StatusBadRequest)

	still, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, still, len(cmds), "a foreign ack closed nothing")
}

func TestChatRunnerCommandAddressedToOtherRunnerIsNotIssued(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _ := f.threadWithTurn(t, "queued") // runtime carries a random runner_id
	require.NoError(t, NewChatThreadDeleteRepo(f.db, nil).SoftDeleteThread(ctx, userActor(f.userA), f.projA, thread))
	repo := NewChatRunnerRepo(f.db)
	other := uuid.New()
	cmds, err := repo.PendingCommands(ctx, f.agentA, nil, &other, 0)
	require.NoError(t, err)
	require.Empty(t, cmds)
	// a caller that names no runner does not see an order addressed to one either
	cmds, err = repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Empty(t, cmds)
	// an order addressed only to the agent goes to any runner of it
	_, err = f.db.Exec(`UPDATE ext_agent_chat_commands SET runner_id = NULL WHERE thread_id = $1`, thread)
	require.NoError(t, err)
	cmds, err = repo.PendingCommands(ctx, f.agentA, nil, &other, 0)
	require.NoError(t, err)
	require.Len(t, cmds, 2)
}

// historyThread: one old exchange plus n more, then an active claimed turn.
func (f *chatDeleteFixture) historyThread(t *testing.T, pairs int) (thread, current uuid.UUID, ids []uuid.UUID) {
	t.Helper()
	thread, err := f.insertThread(t, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	base := time.Now().UTC().Add(-time.Hour)
	add := func(role, status, body string, n int) uuid.UUID {
		id := uuid.New()
		_, insErr := f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body, status, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, f.wsA, thread, role, body, status, base.Add(time.Duration(n)*time.Second))
		require.NoError(t, insErr)
		return id
	}
	n := 0
	for i := 0; i < pairs; i++ {
		ids = append(ids, add("user", "done", fmt.Sprintf("q%d", i), n))
		n++
		ids = append(ids, add("agent", "done", fmt.Sprintf("a%d", i), n))
		n++
	}
	current = add("user", "done", "current", n)
	agentMsg := add("agent", "streaming", "", n+1)
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_turns (workspace_id, thread_id, agent_id, user_message_id, agent_message_id, state, lease_until, expires_at)
		VALUES ($1,$2,$3,$4,$5,'claimed', now() + interval '1 minute', now() + interval '1 day')`, f.wsA, thread, f.agentA, current, agentMsg)
	require.NoError(t, err)
	return thread, current, ids
}

func TestChatRunnerHistoryReturnsExactlyLastNInOrder(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, current, ids := f.historyThread(t, 5) // 10 past messages
	repo := NewChatRunnerRepo(f.db)

	h, err := repo.History(ctx, f.agentA, thread, 4, nil)
	require.NoError(t, err)
	require.Len(t, h.Messages, 4)
	require.True(t, h.HasMore)
	for i, m := range h.Messages {
		require.Equal(t, ids[len(ids)-4+i], m.MessageID, "oldest to newest, the last four of ten")
		require.NotEqual(t, current, m.MessageID, "the current turn's user message is excluded")
	}

	// the cursor pages further back without overlap
	older, err := repo.History(ctx, f.agentA, thread, 4, &h.Messages[0].MessageID)
	require.NoError(t, err)
	require.Len(t, older.Messages, 4)
	require.Equal(t, ids[2], older.Messages[0].MessageID)
	require.Equal(t, ids[5], older.Messages[3].MessageID)
	require.True(t, older.HasMore)

	all, err := repo.History(ctx, f.agentA, thread, 0, nil)
	require.NoError(t, err)
	require.Len(t, all.Messages, 10)
	require.False(t, all.HasMore)
	big, err := repo.History(ctx, f.agentA, thread, 5000, nil)
	require.NoError(t, err)
	require.Len(t, big.Messages, 10, "limit is capped, not rejected")
}

func TestChatRunnerHistorySkipsBodylessFailuresAndListsAttachmentMetadata(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _, ids := f.historyThread(t, 2)
	_, err := f.db.Exec(`INSERT INTO ext_agent_chat_messages (workspace_id, thread_id, role, body, status, created_at)
		VALUES ($1,$2,'agent','','failed', now() - interval '59 minutes')`, f.wsA, thread)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_attachments (workspace_id, thread_id, message_id, s3_key, file_name, mime, size, uploaded_at)
		VALUES ($1,$2,$3,'chat/k.png','a.png','image/png',10, now())`, f.wsA, thread, ids[0])
	require.NoError(t, err)

	h, err := NewChatRunnerRepo(f.db).History(ctx, f.agentA, thread, 50, nil)
	require.NoError(t, err)
	require.Len(t, h.Messages, 4, "the empty failed message is left out")
	require.Equal(t, []ChatHistoryAttachment{{FileName: "a.png", Mime: "image/png", Size: 10}}, h.Messages[0].Attachments)
	require.Empty(t, h.Messages[1].Attachments)
}

func TestChatRunnerHistoryForbiddenForForeignRunnerOrIdleThread(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _, _ := f.historyThread(t, 1)
	repo := NewChatRunnerRepo(f.db)

	_, err := repo.History(ctx, f.agentB, thread, 5, nil)
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.History(ctx, uuid.Nil, thread, 5, nil)
	requireChatStatus(t, err, http.StatusForbidden)

	// no active turn: the runner does not hold the thread any more
	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET state = 'done' WHERE thread_id = $1`, thread)
	require.NoError(t, err)
	_, err = repo.History(ctx, f.agentA, thread, 5, nil)
	requireChatStatus(t, err, http.StatusForbidden)

	_, err = repo.History(ctx, f.agentA, uuid.New(), 5, nil)
	requireChatStatus(t, err, http.StatusForbidden)
}

// A runner whose lease on the turn has lapsed no longer holds the thread.
func TestChatRunnerHistoryForbiddenAfterLeaseExpired(t *testing.T) {
	f := newChatDeleteFixture(t)
	thread, _, _ := f.historyThread(t, 1)
	repo := NewChatRunnerRepo(f.db)
	_, err := repo.History(context.Background(), f.agentA, thread, 5, nil)
	require.NoError(t, err)

	_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET lease_until = now() - interval '1 second' WHERE thread_id = $1`, thread)
	require.NoError(t, err)
	_, err = repo.History(context.Background(), f.agentA, thread, 5, nil)
	requireChatStatus(t, err, http.StatusForbidden)
}

func TestChatRunnerNilCallerIsForbidden(t *testing.T) {
	f := newChatDeleteFixture(t)
	_, err := NewChatRunnerRepo(f.db).PendingCommands(context.Background(), uuid.Nil, nil, nil, 0)
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = NewChatRunnerRepo(f.db).AckCommand(context.Background(), uuid.Nil, uuid.New(), nil, ChatAckDone, "")
	requireChatStatus(t, err, http.StatusForbidden)
}

// Two runners of one agent: an order addressed to runner B is never listed to
// runner A or to a caller with no runner credential, and A cannot ack it even
// by its id. B, the addressee, lists and acks it. Another agent's bearer and a
// revoked one are refused.
func TestChatRunnerOrderReachesOnlyItsAddressee(t *testing.T) {
	f := newChatDeleteFixture(t)
	ctx := context.Background()
	thread, _ := f.threadWithTurn(t, "claimed")
	require.NoError(t, NewChatThreadDeleteRepo(f.db, nil).SoftDeleteThread(ctx, userActor(f.userA), f.projA, thread))
	repo := NewChatRunnerRepo(f.db)

	rA, tokA, err := repo.RegisterRunner(ctx, f.agentA, "runner a")
	require.NoError(t, err)
	rB, tokB, err := repo.RegisterRunner(ctx, f.agentA, "runner b")
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE ext_agent_chat_commands SET runner_id = $2 WHERE thread_id = $1`, thread, rB.ID)
	require.NoError(t, err)

	idA, err := repo.AuthenticateRunner(ctx, f.agentA, tokA)
	require.NoError(t, err)
	require.Equal(t, rA.ID, idA)
	idB, err := repo.AuthenticateRunner(ctx, f.agentA, tokB)
	require.NoError(t, err)
	require.Equal(t, rB.ID, idB)

	all, err := repo.PendingCommands(ctx, f.agentA, nil, &idB, 0)
	require.NoError(t, err)
	require.Len(t, all, 2)

	seenByA, err := repo.PendingCommands(ctx, f.agentA, nil, &idA, 0)
	require.NoError(t, err)
	require.Empty(t, seenByA, "runner A must not see an order addressed to runner B")
	seenAnon, err := repo.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Empty(t, seenAnon, "no runner credential, no runner-addressed order")

	_, err = repo.AckCommand(ctx, f.agentA, all[0].ID, &idA, ChatAckDone, "")
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.AckCommand(ctx, f.agentA, all[0].ID, nil, ChatAckDone, "")
	requireChatStatus(t, err, http.StatusForbidden)
	still, err := repo.PendingCommands(ctx, f.agentA, nil, &idB, 0)
	require.NoError(t, err)
	require.Len(t, still, 2, "a sibling's ack closed nothing")

	state, err := repo.AckCommand(ctx, f.agentA, all[0].ID, &idB, ChatAckDone, "")
	require.NoError(t, err)
	require.Equal(t, "done", state)

	// another agent cannot use this agent's runner bearer
	_, err = repo.AuthenticateRunner(ctx, f.agentB, tokB)
	requireChatStatus(t, err, http.StatusForbidden)
	_, err = repo.AuthenticateRunner(ctx, f.agentA, "crt_nope")
	requireChatStatus(t, err, http.StatusForbidden)
	requireChatStatus(t, repo.RevokeRunner(ctx, f.agentB, rB.ID), http.StatusNotFound)

	// a revoked runner authenticates no more
	require.NoError(t, repo.RevokeRunner(ctx, f.agentA, rB.ID))
	_, err = repo.AuthenticateRunner(ctx, f.agentA, tokB)
	requireChatStatus(t, err, http.StatusForbidden)

	// an agent that does not exist cannot register a runner
	_, _, err = repo.RegisterRunner(ctx, uuid.New(), "ghost")
	requireChatStatus(t, err, http.StatusForbidden)
}
