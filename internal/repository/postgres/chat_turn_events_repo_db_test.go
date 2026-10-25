package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type turnFx struct {
	*chatDeleteFixture
	repo   *ChatTurnEventsRepo
	thread uuid.UUID
	runner uuid.UUID
	token  string
}

func newTurnFx(t *testing.T) *turnFx {
	t.Helper()
	f := newChatDeleteFixture(t)
	th, err := f.insertThread(t, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	return &turnFx{chatDeleteFixture: f, repo: NewChatTurnEventsRepo(f.db), thread: th, runner: uuid.New(), token: "tok-" + uuid.NewString()}
}

// turn inserts the one unfinished turn of the thread in the given state, held by
// the fixture's runner under the fixture's claim token.
func (f *turnFx) turn(t *testing.T, state string, expiresIn time.Duration) uuid.UUID {
	t.Helper()
	var msg uuid.UUID
	require.NoError(t, f.db.Get(&msg, `INSERT INTO ext_agent_chat_messages (workspace_id, thread_id, role, body, status)
		VALUES ($1, $2, 'user', 'hi', 'queued') RETURNING id`, f.wsA, f.thread))
	id := uuid.New()
	var runner any
	var token any
	if state != ChatTurnQueued {
		runner, token = f.runner, f.token
	}
	_, err := f.db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, user_message_id, state, runner_id, claim_token,
			execution_epoch, lease_until, expires_at, first_claimed_at, started_at, attempt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, now() + interval '60 seconds', now() + $9::interval, now(),
		        CASE WHEN $6 = 'streaming' THEN now() END, 1)`,
		id, f.wsA, f.thread, f.agentA, msg, state, runner, token, (expiresIn).String())
	require.NoError(t, err)
	return id
}

func (f *turnFx) in(turn uuid.UUID, events ...ChatTurnEvent) ChatEventsInput {
	return ChatEventsInput{TurnID: turn, RunnerID: f.runner, ClaimToken: f.token, Events: events, ServesAgent: func(a uuid.UUID) bool { return a == f.agentA }}
}

func chatS(v string) *string { return &v }
func chatI(v int) *int       { return &v }

func chatAPIErr(t *testing.T, err error) (status int, code string, ctx map[string]any) {
	t.Helper()
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "want an api error, got %v", err)
	return ae.Code, ae.ErrorCode, ae.Context
}

func (f *turnFx) row(t *testing.T, turn uuid.UUID) (state string, last int, terminal *int, body, msgStatus string) {
	t.Helper()
	var r struct {
		State     string `db:"state"`
		LastSeq   int    `db:"last_seq"`
		Terminal  *int   `db:"terminal_seq"`
		Body      string `db:"body"`
		MsgStatus string `db:"mstatus"`
	}
	require.NoError(t, f.db.Get(&r, `SELECT t.state, t.last_seq, t.terminal_seq, COALESCE(m.body, '') AS body, COALESCE(m.status, '') AS mstatus
		FROM ext_agent_chat_turns t LEFT JOIN ext_agent_chat_messages m ON m.id = t.agent_message_id WHERE t.id = $1`, turn))
	return r.State, r.LastSeq, r.Terminal, r.Body, r.MsgStatus
}

func TestChatEvents_StreamToDone_StoresBodyUsageAndReport(t *testing.T) {
	f := newTurnFx(t)
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	ctx := context.Background()

	res, fx, err := f.repo.ApplyEvents(ctx, f.in(turn,
		ChatTurnEvent{Seq: 1, Type: ChatEvSession, SessionID: "sess-1", Harness: "claude", Profile: "p"},
		ChatTurnEvent{Seq: 2, Type: ChatEvTextDelta, Text: chatS("Hel")},
		ChatTurnEvent{Seq: 3, Type: ChatEvToolStatus, ToolCallID: "t1", Label: "Reading", Status: "started"},
		ChatTurnEvent{Seq: 4, Type: ChatEvTextDelta, Text: chatS("lo")},
		ChatTurnEvent{Seq: 5, Type: ChatEvUsage, TokensIn: chatI(1200), TokensOut: chatI(340), CostUSD: func() *float64 { v := 0.0123; return &v }(), Model: "m1"},
		ChatTurnEvent{Seq: 6, Type: ChatEvFinal, StopReason: "end_turn"},
		ChatTurnEvent{Seq: 7, Type: ChatEvTextDelta, Text: chatS("late")},
	))
	require.NoError(t, err)
	assert.Equal(t, 6, res.AppliedSeq)
	assert.Equal(t, ChatTurnDone, res.State)
	assert.Equal(t, []ChatIgnoredEvent{{Seq: 7, Reason: "after_terminal"}}, res.Ignored)
	assert.Equal(t, f.userA, fx.OwnerID)
	assert.Equal(t, []string{"Hel", "lo"}, fx.Deltas)
	require.NotNil(t, fx.Created)
	require.NotNil(t, fx.Terminal)
	assert.Equal(t, "Hello", fx.Message.Body)

	state, last, term, body, ms := f.row(t, turn)
	assert.Equal(t, ChatTurnDone, state)
	assert.Equal(t, 6, last)
	require.NotNil(t, term)
	assert.Equal(t, 6, *term)
	assert.Equal(t, "Hello", body)
	assert.Equal(t, "done", ms)

	var userStatus string
	require.NoError(t, f.db.Get(&userStatus, `SELECT m.status FROM ext_agent_chat_messages m
		JOIN ext_agent_chat_turns t ON t.user_message_id = m.id WHERE t.id = $1`, turn))
	assert.Equal(t, "done", userStatus, "the user message leaves queued once the agent answered")

	var tu struct {
		In, Out int
		Cost    float64
		Model   string
		Session string
	}
	require.NoError(t, f.db.QueryRow(`SELECT tokens_in, tokens_out, cost_usd, model, session_id FROM ext_agent_chat_turns WHERE id = $1`, turn).
		Scan(&tu.In, &tu.Out, &tu.Cost, &tu.Model, &tu.Session))
	assert.Equal(t, 1200, tu.In)
	assert.Equal(t, 340, tu.Out)
	assert.InDelta(t, 0.0123, tu.Cost, 1e-9)
	assert.Equal(t, "m1", tu.Model)
	assert.Equal(t, "sess-1", tu.Session)

	var rep struct {
		In, Out int64
		Cost    float64
		Status  string
	}
	require.NoError(t, f.db.QueryRow(`SELECT tokens_in, tokens_out, estimated_cost, status FROM agent_sessions WHERE chat_thread_id = $1`, f.thread).
		Scan(&rep.In, &rep.Out, &rep.Cost, &rep.Status))
	assert.EqualValues(t, 1200, rep.In)
	assert.InDelta(t, 0.0123, rep.Cost, 1e-9)
	assert.Equal(t, "ended", rep.Status)

	var rt string
	require.NoError(t, f.db.Get(&rt, `SELECT runtime->>'session_id' FROM ext_agent_chat_threads WHERE id = $1`, f.thread))
	assert.Equal(t, "sess-1", rt)
}

func TestChatEvents_ReplayDoesNotDuplicate(t *testing.T) {
	f := newTurnFx(t)
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	ctx := context.Background()
	batch := f.in(turn,
		ChatTurnEvent{Seq: 1, Type: ChatEvTextDelta, Text: chatS("ab")},
		ChatTurnEvent{Seq: 2, Type: ChatEvToolStatus, Label: "x", Status: "started"})
	_, _, err := f.repo.ApplyEvents(ctx, batch)
	require.NoError(t, err)

	res, fx, err := f.repo.ApplyEvents(ctx, batch)
	require.NoError(t, err)
	assert.Nil(t, fx, "a replay changes nothing and publishes nothing")
	assert.Equal(t, 2, res.AppliedSeq)

	// A batch overlapping the old one applies only its new tail.
	_, fx, err = f.repo.ApplyEvents(ctx, f.in(turn,
		ChatTurnEvent{Seq: 2, Type: ChatEvToolStatus, Label: "x", Status: "started"},
		ChatTurnEvent{Seq: 3, Type: ChatEvTextDelta, Text: chatS("cd")}))
	require.NoError(t, err)
	assert.Equal(t, []string{"cd"}, fx.Deltas)

	_, _, _, body, _ := f.row(t, turn)
	assert.Equal(t, "abcd", body)
	var tools int
	require.NoError(t, f.db.Get(&tools, `SELECT count(*) FROM ext_agent_chat_tool_events WHERE turn_id = $1`, turn))
	assert.Equal(t, 1, tools)

	// Replay of an accepted final after the turn is terminal is still a 200 (N10) ...
	fin := f.in(turn, ChatTurnEvent{Seq: 4, Type: ChatEvFinal, StopReason: "end_turn"})
	_, _, err = f.repo.ApplyEvents(ctx, fin)
	require.NoError(t, err)
	res, fx, err = f.repo.ApplyEvents(ctx, fin)
	require.NoError(t, err)
	assert.Nil(t, fx)
	assert.Equal(t, ChatTurnDone, res.State)
	// ... while new data on it is refused with the terminal seq (N8).
	_, _, err = f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 5, Type: ChatEvTextDelta, Text: chatS("x")}))
	code, ec, cx := chatAPIErr(t, err)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "chat_turn_terminal", ec)
	assert.Equal(t, ChatTurnDone, cx["state"])
	assert.EqualValues(t, 4, cx["terminal_seq"])
}

func TestChatEvents_Refusals(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	text := func(seq int) ChatTurnEvent { return ChatTurnEvent{Seq: seq, Type: ChatEvTextDelta, Text: chatS("x")} }

	cases := []struct {
		name   string
		in     func() ChatEventsInput
		status int
		code   string
	}{
		{"unknown turn", func() ChatEventsInput { in := f.in(uuid.New(), text(1)); return in }, 404, "chat_turn_not_found"},
		{"agent not served", func() ChatEventsInput {
			in := f.in(turn, text(1))
			in.ServesAgent = func(uuid.UUID) bool { return false }
			return in
		}, 404, "chat_turn_not_found"},
		{"other runner", func() ChatEventsInput { in := f.in(turn, text(1)); in.RunnerID = uuid.New(); return in }, 403, "chat_not_claim_holder"},
		{"stale token", func() ChatEventsInput { in := f.in(turn, text(1)); in.ClaimToken = "old"; return in }, 409, "chat_stale_claim"},
		{"empty token", func() ChatEventsInput { in := f.in(turn, text(1)); in.ClaimToken = ""; return in }, 409, "chat_stale_claim"},
		{"seq gap", func() ChatEventsInput { return f.in(turn, text(3)) }, 409, "chat_seq_gap"},
		{"unknown stop reason", func() ChatEventsInput {
			return f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvFinal, StopReason: "bogus"})
		}, 400, "bad_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := f.repo.ApplyEvents(ctx, c.in())
			code, ec, _ := chatAPIErr(t, err)
			assert.Equal(t, c.status, code)
			assert.Equal(t, c.code, ec)
		})
	}
	_, last, _, body, _ := f.row(t, turn)
	assert.Equal(t, 0, last, "a refused batch applies nothing")
	assert.Empty(t, body)

	_, _, err := f.repo.ApplyEvents(ctx, f.in(turn, text(5)))
	_, _, cx := chatAPIErr(t, err)
	assert.EqualValues(t, 1, cx["expected_seq"])
}

func TestChatEvents_ClaimedTurnAcceptsOnlyErrorOrCancelledFinal(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()

	turn := f.turn(t, ChatTurnClaimed, 24*time.Hour)
	_, _, err := f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvTextDelta, Text: chatS("x")}))
	_, ec, _ := chatAPIErr(t, err)
	assert.Equal(t, "chat_not_started", ec)
	_, _, err = f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvFinal, StopReason: "end_turn"}))
	_, ec, _ = chatAPIErr(t, err)
	assert.Equal(t, "chat_not_started", ec)

	res, fx, err := f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvFinal, StopReason: "cancelled"}))
	require.NoError(t, err)
	assert.Equal(t, ChatTurnCancelled, res.State)
	assert.Equal(t, "cancelled", fx.Message.Status)

	// The same on a fresh thread: an error on a claimed turn fails it (T6).
	f2 := newTurnFx(t)
	t2 := f2.turn(t, ChatTurnClaimed, 24*time.Hour)
	res, fx, err = f2.repo.ApplyEvents(ctx, f2.in(t2, ChatTurnEvent{Seq: 1, Type: ChatEvError, Code: "session_failed"}))
	require.NoError(t, err)
	assert.Equal(t, ChatTurnFailed, res.State)
	require.NotNil(t, fx.Terminal.ErrorCode)
	assert.Equal(t, "session_failed", *fx.Terminal.ErrorCode)
	assert.Equal(t, "failed", fx.Message.Status)
}

func TestChatEvents_BodyCapKeepsHeadAndAcknowledges(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	big := strings.Repeat("я", ChatMessageBodyCap) // 2 bytes each: the cap falls on a rune boundary
	res, fx, err := f.repo.ApplyEvents(ctx, f.in(turn,
		ChatTurnEvent{Seq: 1, Type: ChatEvTextDelta, Text: &big},
		ChatTurnEvent{Seq: 2, Type: ChatEvTextDelta, Text: chatS("more")}))
	require.NoError(t, err)
	assert.Equal(t, 2, res.AppliedSeq, "deltas past the cap are acknowledged")
	assert.LessOrEqual(t, len(fx.Message.Body), ChatMessageBodyCap)
	assert.True(t, fx.Message.Truncated)
	assert.Len(t, fx.Deltas, 1, "only what is stored goes to the owner")
}

func TestChatCancel_VisibleToRunnerAndStopsQueued(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)

	var epochBefore int64
	require.NoError(t, f.db.Get(&epochBefore, `SELECT execution_epoch FROM ext_agent_chat_threads WHERE id = $1`, f.thread))

	res, _, err := f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvTextDelta, Text: chatS("a")}))
	require.NoError(t, err)
	assert.False(t, res.CancelRequested)

	o, err := f.repo.RequestCancel(ctx, f.userA, f.projA, f.thread)
	require.NoError(t, err)
	assert.Equal(t, ChatTurnStreaming, o.State, "a running turn only gets the flag; the runner confirms with final")

	res, _, err = f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 2, Type: ChatEvTextDelta, Text: chatS("b")}))
	require.NoError(t, err)
	assert.True(t, res.CancelRequested, "events answers with cancel_requested")
	// Replay answers carry it too.
	res, _, err = f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 2, Type: ChatEvTextDelta, Text: chatS("b")}))
	require.NoError(t, err)
	assert.True(t, res.CancelRequested)

	var epochAfter int64
	require.NoError(t, f.db.Get(&epochAfter, `SELECT execution_epoch FROM ext_agent_chat_threads WHERE id = $1`, f.thread))
	assert.Greater(t, epochAfter, epochBefore, "Stop moves the epoch on")

	// The runner then confirms with final(cancelled): partial text kept.
	res, fx, err := f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 3, Type: ChatEvFinal, StopReason: "cancelled"}))
	require.NoError(t, err)
	assert.Equal(t, ChatTurnCancelled, res.State)
	assert.Equal(t, "ab", fx.Message.Body)

	// A second Stop with nothing running is a no-op.
	o, err = f.repo.RequestCancel(ctx, f.userA, f.projA, f.thread)
	require.NoError(t, err)
	assert.Empty(t, o.State)

	// Stop of a queued turn is immediate (T10) and the user message shows cancelled.
	q := f.turn(t, ChatTurnQueued, 24*time.Hour)
	o, err = f.repo.RequestCancel(ctx, f.userA, f.projA, f.thread)
	require.NoError(t, err)
	assert.Equal(t, ChatTurnCancelled, o.State)
	require.NotNil(t, o.UserMessage)
	assert.Equal(t, "cancelled", o.UserMessage.Status)
	state, _, _, _, _ := f.row(t, q)
	assert.Equal(t, ChatTurnCancelled, state)

	// Another user's thread looks like a missing one.
	_, err = f.repo.RequestCancel(ctx, f.userB, f.projA, f.thread)
	require.Error(t, err)
	code, _, _ := chatAPIErr(t, err)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestChatExpiry_OfflineTurnExpiresAfterTTL(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnQueued, time.Hour)

	// The sweep is global and the database shared: look at this turn only, and
	// keep the shift small so no other test's 24 h turn is caught by it.
	mine := func(out []ChatTurnOutcome) *ChatTurnOutcome {
		for k := range out {
			if out[k].TurnID == turn {
				return &out[k]
			}
		}
		return nil
	}
	got, err := f.repo.ExpireQueued(ctx, 500)
	require.NoError(t, err)
	assert.Nil(t, mine(got), "within the TTL the turn stays queued")

	// Shift time past the TTL: the same pass now expires it.
	f.repo.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	got, err = f.repo.ExpireQueued(ctx, 500)
	require.NoError(t, err)
	o := mine(got)
	require.NotNil(t, o)
	assert.Equal(t, ChatTurnExpired, o.State)
	assert.Equal(t, f.userA, o.OwnerID)
	require.NotNil(t, o.ErrorCode)
	assert.Equal(t, "not_delivered", *o.ErrorCode)
	require.NotNil(t, o.UserMessage)
	assert.Equal(t, "failed", o.UserMessage.Status)

	state, _, _, _, _ := f.row(t, turn)
	assert.Equal(t, ChatTurnExpired, state)
	var code string
	require.NoError(t, f.db.Get(&code, `SELECT error_code FROM ext_agent_chat_messages WHERE id = $1`, o.UserMessage.ID))
	assert.Equal(t, "not_delivered", code)

	got, err = f.repo.ExpireQueued(ctx, 500)
	require.NoError(t, err)
	assert.Nil(t, mine(got), "a terminal turn never changes again")
}

func TestChatExpiry_ClaimedTurnIsNeverExpired(t *testing.T) {
	f := newTurnFx(t)
	turn := f.turn(t, ChatTurnClaimed, -time.Hour) // TTL long gone, but a runner holds it (N17)
	got, err := f.repo.ExpireQueued(context.Background(), 500)
	require.NoError(t, err)
	for _, o := range got {
		assert.NotEqual(t, turn, o.TurnID)
	}
	state, _, _, _, _ := f.row(t, turn)
	assert.Equal(t, ChatTurnClaimed, state)
}

// setChatOfflineExpiry stores offline_expiry_hours where the settings API keeps it:
// the workspace's agent-chat integration config.
func setChatOfflineExpiry(f *turnFx, hours int) (sql.Result, error) {
	if _, err := f.db.Exec(`DELETE FROM integration_configs WHERE workspace_id = $1 AND provider = 'agent-chat'`, f.wsA); err != nil {
		return nil, err
	}
	return f.db.Exec(`INSERT INTO integration_configs (id, workspace_id, provider, config, is_active)
		VALUES ($1, $2, 'agent-chat', jsonb_build_object('offline_expiry_hours', $3::int), true)`, uuid.New(), f.wsA, hours)
}

func TestChatOfflineTTL_WorkspaceSettingWithDefault(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	d, err := f.repo.OfflineTTL(ctx, f.wsA)
	require.NoError(t, err)
	assert.Equal(t, ChatDefaultOfflineTTL, d)

	_, err = setChatOfflineExpiry(f, 6)
	require.NoError(t, err)
	d, err = f.repo.OfflineTTL(ctx, f.wsA)
	require.NoError(t, err)
	assert.Equal(t, 6*time.Hour, d)
}

func TestChatEvents_LateLiveRunnerBumpsEpochButIsNotPunished(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	_, err := f.db.Exec(`UPDATE ext_agent_chat_turns SET lease_until = now() - interval '5 seconds' WHERE id = $1`, turn)
	require.NoError(t, err)
	res, _, err := f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvTextDelta, Text: chatS("x")}))
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.AppliedSeq)
	assert.EqualValues(t, 1, res.ExecutionEpoch-0, "epoch moved on from 1 to the thread counter")
	require.NotNil(t, res.LeaseUntil)
	assert.True(t, res.LeaseUntil.After(time.Now()), "lease renewed")
}

func TestChatExpiry_WorkspaceTTLSettingDecidesTheDeadline(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnQueued, 48*time.Hour) // stored expires_at is far away
	_, err := setChatOfflineExpiry(f, 1)
	require.NoError(t, err)

	got, err := f.repo.ExpireQueued(ctx, 500)
	require.NoError(t, err)
	for _, o := range got {
		assert.NotEqual(t, turn, o.TurnID, "inside the 1 h TTL")
	}
	f.repo.now = func() time.Time { return time.Now().Add(90 * time.Minute) }
	got, err = f.repo.ExpireQueued(ctx, 500)
	require.NoError(t, err)
	var seen bool
	for _, o := range got {
		seen = seen || o.TurnID == turn
	}
	assert.True(t, seen, "the setting, not the stored expires_at, expired it")
	state, _, _, _, _ := f.row(t, turn)
	assert.Equal(t, ChatTurnExpired, state)
}

func TestChatExpiry_NonPositiveTTLSettingFallsBackToStoredDeadline(t *testing.T) {
	f := newTurnFx(t)
	turn := f.turn(t, ChatTurnQueued, 24*time.Hour)
	for _, v := range []int{0, -3} {
		_, err := setChatOfflineExpiry(f, v)
		require.NoError(t, err)
		got, err := f.repo.ExpireQueued(context.Background(), 500)
		require.NoError(t, err)
		for _, o := range got {
			assert.NotEqual(t, turn, o.TurnID, "ttl %d must not expire a fresh turn", v)
		}
	}
}

func TestChatEvents_UsageFromEarlierBatchReachesSessionReport(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	_, _, err := f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvUsage, TokensIn: chatI(7), TokensOut: chatI(3), Model: "m2"}))
	require.NoError(t, err)
	_, _, err = f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 2, Type: ChatEvFinal, StopReason: "end_turn"}))
	require.NoError(t, err)
	var in, out int64
	var model string
	require.NoError(t, f.db.QueryRow(`SELECT tokens_in, tokens_out, model_used FROM agent_sessions WHERE chat_thread_id = $1`, f.thread).Scan(&in, &out, &model))
	assert.EqualValues(t, 7, in)
	assert.EqualValues(t, 3, out)
	assert.Equal(t, "m2", model)
}

func TestChatEvents_TurnLockSerialisesAcrossConnections(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := uuid.New()
	unlock, err := f.repo.LockTurn(ctx, turn)
	require.NoError(t, err)
	got := make(chan struct{})
	go func() {
		u2, err := f.repo.LockTurn(ctx, turn)
		if err == nil {
			u2()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second lock was granted while the first was held")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("second lock never granted after unlock")
	}
}

func TestChatEvents_NegativeUsageKeepsEarlierTotals(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	_, _, err := f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvUsage, TokensIn: chatI(7), TokensOut: chatI(3)}))
	require.NoError(t, err)
	_, _, err = f.repo.ApplyEvents(ctx, f.in(turn, ChatTurnEvent{Seq: 2, Type: ChatEvUsage, TokensIn: chatI(-1), TokensOut: chatI(-5)}))
	require.NoError(t, err)
	var in, out int64
	require.NoError(t, f.db.QueryRow(`SELECT tokens_in, tokens_out FROM ext_agent_chat_turns WHERE id = $1`, turn).Scan(&in, &out))
	assert.EqualValues(t, 7, in)
	assert.EqualValues(t, 3, out)
}

func TestChatCancel_StopRevokesSessionTokensOfRunningTurn(t *testing.T) {
	f := newTurnFx(t)
	ctx := context.Background()
	f.turn(t, ChatTurnStreaming, 24*time.Hour)
	session := uuid.New()
	_, err := f.db.Exec(`INSERT INTO chat_session_tokens (token_hash, user_id, agent_id, workspace_id, project_id, chat_thread_id, chat_session_id,
			runner_id, execution_epoch, issued_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'r', 1, now(), now() + interval '1 hour')`,
		"h-"+uuid.NewString(), f.userA, f.agentA, f.wsA, f.projA, f.thread, session)
	require.NoError(t, err)

	_, err = f.repo.RequestCancel(ctx, f.userA, f.projA, f.thread)
	require.NoError(t, err)

	var reason *string
	var revoked bool
	require.NoError(t, f.db.QueryRow(`SELECT revoked_at IS NOT NULL, revoked_reason FROM chat_session_tokens WHERE chat_session_id = $1`, session).Scan(&revoked, &reason))
	assert.True(t, revoked, "the token the runner already holds is dead after Stop")
	require.NotNil(t, reason)
	assert.Equal(t, "stop", *reason)
	var stops int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM chat_session_stops WHERE chat_session_id = $1`, session).Scan(&stops))
	assert.Equal(t, 1, stops, "the session is tombstoned so a late issue is refused")
}

func TestChatEvents_FinalWithTopLevelUsageIsAccounted(t *testing.T) {
	f := newTurnFx(t)
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	_, _, err := f.repo.ApplyEvents(context.Background(), f.in(turn, ChatTurnEvent{Seq: 1, Type: ChatEvFinal, StopReason: "end_turn",
		TokensIn: chatI(11), TokensOut: chatI(4), Model: "m3"}))
	require.NoError(t, err)
	var in, out int64
	var model string
	require.NoError(t, f.db.QueryRow(`SELECT tokens_in, tokens_out, model_used FROM agent_sessions WHERE chat_thread_id = $1`, f.thread).Scan(&in, &out, &model))
	assert.EqualValues(t, 11, in)
	assert.EqualValues(t, 4, out)
	assert.Equal(t, "m3", model)
}

func TestChatEvents_HoldsClaim(t *testing.T) {
	f := newTurnFx(t)
	turn := f.turn(t, ChatTurnStreaming, 24*time.Hour)
	ctx := context.Background()
	assert.True(t, f.repo.HoldsClaim(ctx, turn, f.runner.String(), f.token))
	assert.False(t, f.repo.HoldsClaim(ctx, turn, f.runner.String(), "wrong"))
	assert.False(t, f.repo.HoldsClaim(ctx, turn, uuid.NewString(), f.token), "another runner")
	assert.False(t, f.repo.HoldsClaim(ctx, turn, f.runner.String(), ""))
	assert.False(t, f.repo.HoldsClaim(ctx, uuid.New(), f.runner.String(), f.token), "unknown turn")
}
