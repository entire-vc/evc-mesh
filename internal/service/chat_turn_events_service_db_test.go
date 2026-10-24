package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// ChatTurnEventsService against the real schema: the service's own decisions
// (order of publication, who is told, the rate guard, Stop and expiry), each
// checked on the channel capture the owner's browser would read from.

type turnSvcEnv struct {
	svc                                     *ChatTurnEventsService
	ws                                      *chatCapture
	db                                      *sqlx.DB
	owner, ws_, proj, agent, thread, runner uuid.UUID
	token                                   string
	exec                                    func(q string, args ...any)
}

func (e *turnSvcEnv) scan(t *testing.T, dst any, q string, args ...any) {
	t.Helper()
	require.NoError(t, e.db.Get(dst, q, args...))
}

func newTurnSvcEnv(t *testing.T) *turnSvcEnv {
	t.Helper()
	db := oauthSvcTestDB(t)
	ctx := context.Background()
	w := &domain.Workspace{ID: uuid.New(), Name: "Turn svc", Slug: "ts-" + uuid.NewString(), OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, w))
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, w.ID) })
	p := &domain.Project{ID: uuid.New(), WorkspaceID: w.ID, Name: "P", Slug: "ts-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, p))
	owner := uuid.New()
	_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1, $2, $3, 'x', 'u')`, owner, owner.String()+"@ts.test", "ts-"+owner.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, owner) })
	env := &turnSvcEnv{db: db, ws: &chatCapture{}, owner: owner, ws_: w.ID, proj: p.ID, agent: uuid.New(), thread: uuid.New(),
		runner: uuid.New(), token: "claim-" + uuid.NewString()}
	env.exec = func(q string, args ...any) {
		t.Helper()
		_, err := db.Exec(q, args...)
		require.NoError(t, err)
	}
	env.exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, 'a', $3, 'h', 'p')`,
		env.agent, w.ID, "ts-agent-"+uuid.NewString()[:8])
	env.exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, title) VALUES ($1,$2,$3,$4,$5,'t')`,
		env.thread, w.ID, p.ID, env.agent, owner)
	env.svc = NewChatTurnEventsService(postgres.NewChatTurnEventsRepo(db), NewChatEventPublisher(env.ws, &countingSeq{}))
	return env
}

func (e *turnSvcEnv) turn(state string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, state, runner_id, claim_token, execution_epoch,
			lease_until, expires_at, first_claimed_at, started_at, attempt)
		VALUES ($1,$2,$3,$4,$5,$6,$7,1, now() + interval '60 seconds', now() + interval '24 hours', now(), now(), 1)`,
		id, e.ws_, e.thread, e.agent, state, e.runner, e.token)
	return id
}

func (e *turnSvcEnv) in(turn uuid.UUID, evs ...postgres.ChatTurnEvent) postgres.ChatEventsInput {
	return postgres.ChatEventsInput{TurnID: turn, RunnerID: e.runner, ClaimToken: e.token, Events: evs}
}

func TestChatTurnEventsService_StreamPublishesToOwnerDeltasBeforeCompleted(t *testing.T) {
	env := newTurnSvcEnv(t)
	turn := env.turn(postgres.ChatTurnStreaming)
	ctx := context.Background()

	res, err := env.svc.ApplyEvents(ctx, env.in(turn,
		postgres.ChatTurnEvent{Seq: 1, Type: postgres.ChatEvTextDelta, Text: chatStr("Hel")},
		postgres.ChatTurnEvent{Seq: 2, Type: postgres.ChatEvTextDelta, Text: chatStr("lo")},
		postgres.ChatTurnEvent{Seq: 3, Type: postgres.ChatEvFinal, StopReason: "end_turn"}))
	require.NoError(t, err)
	assert.Equal(t, postgres.ChatTurnDone, res.State)

	got := env.ws.on("ws:user:" + env.owner.String())
	require.NotEmpty(t, got)
	completedAt := -1
	for i, e := range got {
		switch e["event"] {
		case ChatEventMessageCompleted:
			completedAt = i
		case ChatEventMessageDelta:
			assert.Equal(t, -1, completedAt, "a delta after the completed message")
		}
	}
	assert.GreaterOrEqual(t, completedAt, 0, "completed message was published")

	// replay of the same seqs publishes nothing new
	n := len(got)
	_, err = env.svc.ApplyEvents(ctx, env.in(turn, postgres.ChatTurnEvent{Seq: 1, Type: postgres.ChatEvTextDelta, Text: chatStr("Hel")}))
	require.NoError(t, err)
	assert.Len(t, env.ws.on("ws:user:"+env.owner.String()), n)
}

func TestChatTurnEventsService_RefusesOversizedBatchAndStaleClaim(t *testing.T) {
	env := newTurnSvcEnv(t)
	turn := env.turn(postgres.ChatTurnStreaming)
	ctx := context.Background()

	big := make([]postgres.ChatTurnEvent, postgres.ChatMaxBatchEvents+1)
	for i := range big {
		big[i] = postgres.ChatTurnEvent{Seq: i + 1, Type: postgres.ChatEvTextDelta, Text: chatStr("x")}
	}
	_, err := env.svc.ApplyEvents(ctx, env.in(turn, big...))
	require.Error(t, err)

	in := env.in(turn, postgres.ChatTurnEvent{Seq: 1, Type: postgres.ChatEvTextDelta, Text: chatStr("x")})
	in.ClaimToken = "someone-else"
	_, err = env.svc.ApplyEvents(ctx, in)
	require.Error(t, err)
	assert.Empty(t, env.ws.on("ws:user:"+env.owner.String()), "a refused batch publishes nothing")
}

func TestChatTurnEventsService_RateGuardIsPerTurn(t *testing.T) {
	env := newTurnSvcEnv(t)
	turn := env.turn(postgres.ChatTurnStreaming)
	env2 := newTurnSvcEnv(t) // one unfinished turn per thread, so the other turn lives on its own thread
	other := env2.turn(postgres.ChatTurnStreaming)
	now := time.Now()
	env.svc.now = func() time.Time { return now } // one frozen second
	ctx := context.Background()

	var limited *ChatRateLimitedError
	seq := int64(0)
	for i := 0; i < 30 && !errors.As(errOrNil(env, ctx, turn, &seq), &limited); i++ {
	}
	require.NotNil(t, limited, "20 batches/s per turn is enforced")
	assert.Positive(t, limited.RetryAfter)

	_, err := env.svc.ApplyEvents(ctx, env2.in(other, postgres.ChatTurnEvent{Seq: 1, Type: postgres.ChatEvTextDelta, Text: chatStr("x")}))
	assert.NoError(t, err, "another turn is not throttled")
}

func errOrNil(env *turnSvcEnv, ctx context.Context, turn uuid.UUID, seq *int64) error {
	*seq++
	_, err := env.svc.ApplyEvents(ctx, env.in(turn, postgres.ChatTurnEvent{Seq: int(*seq), Type: postgres.ChatEvTextDelta, Text: chatStr("x")}))
	return err
}

func TestChatTurnEventsService_StopCancelsQueuedAndMarksRunning(t *testing.T) {
	env := newTurnSvcEnv(t)
	ctx := context.Background()

	_, err := env.svc.RequestCancel(ctx, env.owner, env.proj, uuid.New())
	require.Error(t, err, "unknown thread is a 404, not a silent no-op")

	state, err := env.svc.RequestCancel(ctx, env.owner, env.proj, env.thread)
	require.NoError(t, err)
	assert.Empty(t, state, "nothing running: no-op")

	queued := env.turn(postgres.ChatTurnQueued)
	state, err = env.svc.RequestCancel(ctx, env.owner, env.proj, env.thread)
	require.NoError(t, err)
	assert.Equal(t, postgres.ChatTurnCancelled, state)
	var got string
	env.scan(t, &got, `SELECT state FROM ext_agent_chat_turns WHERE id=$1`, queued)
	assert.Equal(t, postgres.ChatTurnCancelled, got)

	running := env.turn(postgres.ChatTurnStreaming)
	state, err = env.svc.RequestCancel(ctx, env.owner, env.proj, env.thread)
	require.NoError(t, err)
	assert.Equal(t, postgres.ChatTurnStreaming, state, "a running turn only gets cancel_requested")
	var flag bool
	env.scan(t, &flag, `SELECT cancel_requested FROM ext_agent_chat_turns WHERE id=$1`, running)
	assert.True(t, flag)
}

func TestChatTurnEventsService_ExpiryTellsOwnerAndStopsOnContext(t *testing.T) {
	env := newTurnSvcEnv(t)
	turn := env.turn(postgres.ChatTurnQueued)
	env.exec(`UPDATE ext_agent_chat_turns SET runner_id = NULL, claim_token = NULL, first_claimed_at = NULL, started_at = NULL, lease_until = NULL,
		created_at = now() - interval '48 hours', expires_at = now() - interval '24 hours' WHERE id = $1`, turn)

	assert.GreaterOrEqual(t, env.svc.ExpireOnce(context.Background()), 1)
	var state string
	env.scan(t, &state, `SELECT state FROM ext_agent_chat_turns WHERE id=$1`, turn)
	assert.Equal(t, postgres.ChatTurnExpired, state)
	assert.NotEmpty(t, env.ws.on("ws:user:"+env.owner.String()), "the owner is told the turn was not delivered")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { env.svc.RunExpiry(ctx, 0); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunExpiry did not stop with its context")
	}
}

func chatStr(s string) *string { return &s }

func TestChatTurnEventsService_StrangerCannotSpendTheHoldersBudget(t *testing.T) {
	env := newTurnSvcEnv(t)
	turn := env.turn(postgres.ChatTurnStreaming)
	now := time.Now()
	env.svc.now = func() time.Time { return now }
	ctx := context.Background()

	stranger := env.in(turn, postgres.ChatTurnEvent{Seq: 1, Type: postgres.ChatEvTextDelta, Text: chatStr("x")})
	stranger.ClaimToken = "not-the-claim"
	for i := 0; i < 3*postgres.ChatMaxBatchEvents; i++ {
		_, err := env.svc.ApplyEvents(ctx, stranger)
		var limited *ChatRateLimitedError
		require.False(t, errors.As(err, &limited), "a request without the claim is refused, never rate-limited")
	}
	_, err := env.svc.ApplyEvents(ctx, env.in(turn, postgres.ChatTurnEvent{Seq: 1, Type: postgres.ChatEvTextDelta, Text: chatStr("x")}))
	assert.NoError(t, err, "the holder still has its whole budget")
}
