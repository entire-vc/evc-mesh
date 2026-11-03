package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type existingSessionFixture struct {
	*chatDeleteFixture
	repo            *ExistingSessionRepo
	sender, outside uuid.UUID // agents of workspace A: listed sender and unlisted agent
	runner          uuid.UUID // registered runner of agentA
	target          ExistingSessionTarget
}

func newExistingSessionFixture(t *testing.T) *existingSessionFixture {
	t.Helper()
	f := &existingSessionFixture{chatDeleteFixture: newChatDeleteFixture(t)}
	f.repo = NewExistingSessionRepo(f.db)
	mk := func() uuid.UUID {
		id := uuid.New()
		_, err := f.db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, 'es agent', $3, 'h', 'p')`,
			id, f.wsA, "es-agent-"+uuid.NewString()[:8])
		require.NoError(t, err)
		return id
	}
	f.sender, f.outside = mk(), mk()
	r, _, err := NewChatRunnerRepo(f.db).RegisterRunner(context.Background(), f.agentA, "mbp")
	require.NoError(t, err)
	f.runner = r.ID
	f.target = ExistingSessionTarget{WorkspaceID: f.wsA, AgentID: f.agentA, RunnerID: f.runner,
		HostID: "host-1", Hostname: "host-a.example.invalid", SessionID: "native-session-1"}
	return f
}

func (f *existingSessionFixture) register(t *testing.T, senders ...uuid.UUID) *ExistingSessionReceiver {
	t.Helper()
	rc, err := f.repo.Register(context.Background(), f.wsA, f.agentA, f.runner, f.target.HostID, f.target.Hostname, f.target.SessionID, senders)
	require.NoError(t, err)
	return rc
}

func (f *existingSessionFixture) request(text string, rev int64) ExistingSessionRequest {
	sum := sha256.Sum256([]byte(text))
	return ExistingSessionRequest{RequestID: uuid.New(), Target: f.target, Expected: rev, Text: text, TextSHA256: hex.EncodeToString(sum[:])}
}

func (f *existingSessionFixture) ack(rc *ExistingSessionReceiver, state, code string) ExistingSessionAck {
	return ExistingSessionAck{HostID: rc.HostID, SessionID: rc.SessionID, Revision: rc.AdmissionRevision, State: state, ErrorCode: code}
}

// AC1: registration → send → receiver lists, marks received, acknowledges → sender GET;
// the same request id replays the same receipt, a changed target or hash conflicts.
func TestExistingSessionHappyPathAndIdempotency(t *testing.T) {
	f := newExistingSessionFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)
	require.EqualValues(t, 1, rc.AdmissionRevision)

	req := f.request("read the two files and report", rc.AdmissionRevision)
	first, created, err := f.repo.Enqueue(ctx, f.wsA, f.sender, req)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, ExistingSessionQueued, first.State)

	again, created, err := f.repo.Enqueue(ctx, f.wsA, f.sender, req)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.DeliveryID, again.DeliveryID)
	require.Equal(t, first.State, again.State)

	changed := req
	changed.Text, changed.TextSHA256 = "other", f.request("other", 1).TextSHA256
	_, _, err = f.repo.Enqueue(ctx, f.wsA, f.sender, changed)
	requireChatStatus(t, err, 409)
	moved := req
	moved.Target.SessionID = "native-session-2"
	_, _, err = f.repo.Enqueue(ctx, f.wsA, f.sender, moved)
	requireChatStatus(t, err, 409)

	pending, err := f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, rc.HostID, rc.SessionID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, req.Text, pending[0].Text)

	got, err := f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.ack(rc, ExistingSessionReceived, ""))
	require.NoError(t, err)
	require.Equal(t, ExistingSessionReceived, got.State)
	require.NotNil(t, got.ReceivedAt)

	// received is never listed again: no automatic second injection
	pending, err = f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, rc.HostID, rc.SessionID)
	require.NoError(t, err)
	require.Empty(t, pending)

	// a repeated state is a no-op; going straight back is refused
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.ack(rc, ExistingSessionReceived, ""))
	require.NoError(t, err)

	done := f.ack(rc, ExistingSessionAcknowledged, "")
	done.ResultCommentURL = "https://mesh.example.invalid/t/00000000#comment"
	got, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, done)
	require.NoError(t, err)
	require.Equal(t, ExistingSessionAcknowledged, got.State)
	require.Equal(t, done.ResultCommentURL, *got.ResultCommentURL)
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.ack(rc, ExistingSessionReceived, ""))
	requireChatStatus(t, err, 409)

	sent, err := f.repo.Get(ctx, f.wsA, f.sender, first.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, ExistingSessionAcknowledged, sent.State)
	raw, _ := json.Marshal(sent)
	require.NotContains(t, string(raw), req.Text, "a receipt never carries the text")
	var body *string
	require.NoError(t, f.db.Get(&body, `SELECT body FROM existing_session_deliveries WHERE id = $1`, first.DeliveryID))
	require.Nil(t, body, "the stored text is deleted once the delivery is acknowledged")

	// another sender cannot read it
	_, err = f.repo.Get(ctx, f.wsA, f.outside, first.DeliveryID)
	requireChatStatus(t, err, 404)
}

// AC2: nobody but the exact registered receiver gets a delivery, and a missing
// target is never replaced by another one.
func TestExistingSessionNegativeControls(t *testing.T) {
	f := newExistingSessionFixture(t)
	ctx := context.Background()

	// no registration at all: nothing is queued, nothing is invented
	_, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("x", 1))
	requireChatStatus(t, err, 403)

	rc := f.register(t, f.sender)
	req := f.request("hello", rc.AdmissionRevision)

	// sender not listed by the receiver: workspace membership is not enough
	_, _, err = f.repo.Enqueue(ctx, f.wsA, f.outside, req)
	requireChatStatus(t, err, 403)
	// foreign workspace caller and a target naming another workspace
	_, _, err = f.repo.Enqueue(ctx, f.wsB, f.agentB, req)
	requireChatStatus(t, err, 403)
	// wrong runner, host, hostname, session, agent
	for name, mut := range map[string]func(*ExistingSessionTarget){
		"runner":   func(t *ExistingSessionTarget) { t.RunnerID = uuid.New() },
		"host":     func(t *ExistingSessionTarget) { t.HostID = "host-2" },
		"hostname": func(t *ExistingSessionTarget) { t.Hostname = "host-b.example.invalid" },
		"session":  func(t *ExistingSessionTarget) { t.SessionID = "other" },
		"agent":    func(t *ExistingSessionTarget) { t.AgentID = f.outside },
	} {
		bad := f.request("hello "+name, rc.AdmissionRevision)
		mut(&bad.Target)
		_, _, err = f.repo.Enqueue(ctx, f.wsA, f.sender, bad)
		requireChatStatus(t, err, 403)
	}
	// stale revision
	_, _, err = f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("stale", rc.AdmissionRevision+1))
	requireChatStatus(t, err, 409)

	// the allow-list must hold agents of the receiver's own workspace
	_, err = f.repo.Register(ctx, f.wsA, f.agentA, f.runner, "host-1", "h", "s", []uuid.UUID{f.agentB})
	requireChatStatus(t, err, 400)

	ok, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, req)
	require.NoError(t, err)

	// wrong host/session/revision/agent/runner cannot list or ack
	_, err = f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, "host-2", rc.SessionID)
	requireChatStatus(t, err, 403)
	_, err = f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, rc.HostID, "other")
	requireChatStatus(t, err, 403)
	_, err = f.repo.Pending(ctx, f.wsA, f.agentA, uuid.New(), rc.HostID, rc.SessionID)
	requireChatStatus(t, err, 403)
	_, err = f.repo.Pending(ctx, f.wsB, f.agentB, f.runner, rc.HostID, rc.SessionID)
	requireChatStatus(t, err, 403)
	for name, a := range map[string]ExistingSessionAck{
		"host":     {HostID: "host-2", SessionID: rc.SessionID, Revision: rc.AdmissionRevision, State: ExistingSessionReceived},
		"session":  {HostID: rc.HostID, SessionID: "other", Revision: rc.AdmissionRevision, State: ExistingSessionReceived},
		"revision": {HostID: rc.HostID, SessionID: rc.SessionID, Revision: rc.AdmissionRevision + 1, State: ExistingSessionReceived},
	} {
		_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, ok.DeliveryID, a)
		requireChatStatus(t, err, 403)
		_ = name
	}
	_, err = f.repo.Ack(ctx, f.wsA, f.outside, f.runner, ok.DeliveryID, f.ack(rc, ExistingSessionReceived, ""))
	requireChatStatus(t, err, 403)
	_, err = f.repo.Ack(ctx, f.wsB, f.agentB, f.runner, ok.DeliveryID, f.ack(rc, ExistingSessionReceived, ""))
	requireChatStatus(t, err, 403)
	// queued cannot jump to acknowledged
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, ok.DeliveryID, f.ack(rc, ExistingSessionAcknowledged, ""))
	requireChatStatus(t, err, 409)
	still, err := f.repo.Get(ctx, f.wsA, f.sender, ok.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, ExistingSessionQueued, still.State, "no refused call changed the delivery")
}

// Re-registration and revocation fail queued work instead of delivering it to a
// receiver that was admitted for something else.
func TestExistingSessionStaleAndRevoked(t *testing.T) {
	f := newExistingSessionFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)
	d1, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("one", rc.AdmissionRevision))
	require.NoError(t, err)

	rc2 := f.register(t, f.sender)
	require.EqualValues(t, 2, rc2.AdmissionRevision)
	got, err := f.repo.Get(ctx, f.wsA, f.sender, d1.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, ExistingSessionFailed, got.State)
	require.Equal(t, "admission_stale", *got.ErrorCode)

	d2, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("two", rc2.AdmissionRevision))
	require.NoError(t, err)
	require.NoError(t, f.repo.Revoke(ctx, f.wsA, f.agentA, f.runner, rc2.ID))
	got, err = f.repo.Get(ctx, f.wsA, f.sender, d2.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, "receiver_revoked", *got.ErrorCode)
	_, _, err = f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("three", rc2.AdmissionRevision))
	requireChatStatus(t, err, 403)
	requireChatStatus(t, f.repo.Revoke(ctx, f.wsA, f.agentA, f.runner, rc2.ID), 404)

	// a received, unacknowledged delivery is settled as uncertain and its text dropped
	rc3 := f.register(t, f.sender)
	d3, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("four", rc3.AdmissionRevision))
	require.NoError(t, err)
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, d3.DeliveryID, f.ack(rc3, ExistingSessionReceived, ""))
	require.NoError(t, err)
	require.NoError(t, f.repo.Revoke(ctx, f.wsA, f.agentA, f.runner, rc3.ID))
	got, err = f.repo.Get(ctx, f.wsA, f.sender, d3.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, ExistingSessionFailed, got.State)
	require.Equal(t, "delivery_uncertain", *got.ErrorCode)
	var body *string
	require.NoError(t, f.db.Get(&body, `SELECT body FROM existing_session_deliveries WHERE id = $1`, d3.DeliveryID))
	require.Nil(t, body)
}

// A receiver that may have injected and then lost the session reports failed with
// its own error code; the delivery is not queued again.
func TestExistingSessionUncertainDeliveryStaysFailed(t *testing.T) {
	f := newExistingSessionFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)
	d, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("once", rc.AdmissionRevision))
	require.NoError(t, err)
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, d.DeliveryID, f.ack(rc, ExistingSessionReceived, ""))
	require.NoError(t, err)
	got, err := f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, d.DeliveryID, f.ack(rc, ExistingSessionFailed, "delivery_uncertain"))
	require.NoError(t, err)
	require.Equal(t, ExistingSessionFailed, got.State)
	pending, err := f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, rc.HostID, rc.SessionID)
	require.NoError(t, err)
	require.Empty(t, pending)
}

// Positive control: next to a registered receiver the ordinary chat runner path
// of the same agent still does real work: a deleted thread orders two commands,
// the runner lists them and the ack closes them.
func TestExistingSessionLeavesOrdinaryChatAlone(t *testing.T) {
	f := newExistingSessionFixture(t)
	ctx := context.Background()
	f.register(t, f.sender)
	thread, _ := f.threadWithTurn(t, "queued")
	require.NoError(t, NewChatThreadDeleteRepo(f.db, nil).SoftDeleteThread(ctx, userActor(f.userA), f.projA, thread))
	f.unaddressed(t, thread)

	chat := NewChatRunnerRepo(f.db)
	cmds, err := chat.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Len(t, cmds, 2)
	for _, c := range cmds {
		state, aerr := chat.AckCommand(ctx, f.agentA, c.ID, nil, ChatAckDone, "")
		require.NoError(t, aerr)
		require.Equal(t, "done", state)
	}
	left, err := chat.PendingCommands(ctx, f.agentA, nil, nil, 0)
	require.NoError(t, err)
	require.Empty(t, left)
}

// An empty allow-list is a valid receive-only registration that admits nobody.
func TestExistingSessionEmptyAllowListAdmitsNobody(t *testing.T) {
	f := newExistingSessionFixture(t)
	rc := f.register(t)
	require.Empty(t, rc.AllowedSenderIDs)
	_, _, err := f.repo.Enqueue(context.Background(), f.wsA, f.sender, f.request("x", rc.AdmissionRevision))
	requireChatStatus(t, err, 403)
}

// A receiver whose runner credential was revoked takes no new deliveries, even
// though its registration row is still active.
func TestExistingSessionRevokedRunnerTakesNothing(t *testing.T) {
	f := newExistingSessionFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)
	_, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("before", rc.AdmissionRevision))
	require.NoError(t, err)
	queued, _, err := f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("queued", rc.AdmissionRevision))
	require.NoError(t, err)
	require.NoError(t, NewChatRunnerRepo(f.db).RevokeRunner(ctx, f.agentA, f.runner))
	_, _, err = f.repo.Enqueue(ctx, f.wsA, f.sender, f.request("after", rc.AdmissionRevision))
	requireChatStatus(t, err, 403)
	// work queued before the revocation is settled and its text dropped
	got, err := f.repo.Get(ctx, f.wsA, f.sender, queued.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, ExistingSessionFailed, got.State)
	require.Equal(t, "receiver_revoked", *got.ErrorCode)
	var left int
	require.NoError(t, f.db.Get(&left, `SELECT count(*) FROM existing_session_deliveries WHERE receiver_id = $1 AND body IS NOT NULL`, rc.ID))
	require.Zero(t, left)
}
