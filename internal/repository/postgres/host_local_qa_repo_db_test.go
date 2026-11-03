package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/hostlocalqa"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// hostLocalQAFixture runs the repository against a real database. The sender is a
// fresh agent listed in a copy of the fixed policy, so this package never touches
// the policy's own workspace and agent ids (the handler tests own those).
type hostLocalQAFixture struct {
	*chatDeleteFixture
	repo   *HostLocalQARepo
	policy *hostlocalqa.Policy
	sender uuid.UUID
	other  uuid.UUID
	runner uuid.UUID
	target hostlocalqa.Target
	prod   hostlocalqa.Producer
	offset atomic.Int64
}

func (f *hostLocalQAFixture) now() time.Time {
	return time.Now().Add(time.Duration(f.offset.Load()) * time.Second)
}

func newHostLocalQAFixture(t *testing.T) *hostLocalQAFixture {
	t.Helper()
	f := &hostLocalQAFixture{chatDeleteFixture: newChatDeleteFixture(t)}
	loaded, err := hostlocalqa.LoadPolicy()
	require.NoError(t, err)
	pol := *loaded
	mk := func() uuid.UUID {
		id := uuid.New()
		_, execErr := f.db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, 'qa agent', $3, 'h', 'p')`,
			id, f.wsA, "qa-agent-"+id.String()[:8])
		require.NoError(t, execErr)
		return id
	}
	f.sender, f.other = mk(), mk()
	pol.AllowedSenderAgents = []uuid.UUID{f.sender}
	f.policy = &pol
	f.repo = NewHostLocalQARepo(f.db, f.policy).WithClock(f.now)
	r, _, err := NewChatRunnerRepo(f.db).RegisterRunner(context.Background(), f.agentA, "mbp")
	require.NoError(t, err)
	f.runner = r.ID
	f.target = hostlocalqa.Target{WorkspaceID: f.wsA, AgentID: f.agentA, RunnerID: f.runner,
		HostID: "host-1", Hostname: pol.Hostname, SessionID: "native-1"}
	f.prod = hostlocalqa.Producer{ProducerPID: 4242, ProducerStarted: "2026-11-03T00:00:00Z",
		TerminalSessionID: "w0t0p0:ABC", Transport: hostlocalqa.Transport, TTY: "/dev/ttys004"}
	return f
}

func (f *hostLocalQAFixture) register(t *testing.T, senders ...uuid.UUID) *HostLocalQAReceiver {
	t.Helper()
	rc, err := f.repo.Register(context.Background(), f.wsA, f.agentA, f.runner, &hostlocalqa.Registration{
		Target: f.target, Producer: f.prod, Expires: f.now().Add(8 * time.Hour).Unix(), Senders: senders})
	require.NoError(t, err)
	return rc
}

func (f *hostLocalQAFixture) envelope(rc *HostLocalQAReceiver) *hostlocalqa.Envelope {
	p := f.policy
	return &hostlocalqa.Envelope{ArtifactID: p.ArtifactID, Candidate: p.Candidate, Capability: hostlocalqa.Capability,
		ExpectedRevision: rc.AdmissionRevision, Expires: f.now().Add(time.Hour).Unix(), OperationManifestSHA: p.OperationManifestSHA,
		PolicySHA256: p.SHA256, Producer: f.prod, QAPackageSHA: p.QAPackageSHA, ReceiverID: rc.ID, RequestID: uuid.New(),
		SchemaVersion: hostlocalqa.SchemaVersion, SourceTaskID: p.SourceTaskID, Target: f.target, TextSHA256: p.TextSHA256}
}

func (f *hostLocalQAFixture) binding(env *hostlocalqa.Envelope, delivery uuid.UUID, state string) *hostlocalqa.Binding {
	return &hostlocalqa.Binding{ReceiverID: env.ReceiverID, AdmissionRev: env.ExpectedRevision, DeliveryID: delivery,
		RequestID: env.RequestID, PolicySHA256: env.PolicySHA256, TextSHA256: env.TextSHA256, Target: env.Target,
		Producer: env.Producer, Expires: env.Expires, ObservedAt: f.now(), State: state}
}

func requireDetails(t *testing.T, err error, status int, details string) {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, status, ae.Code)
	require.Equal(t, details, ae.Details)
}

func TestHostLocalQARepoLifecycle(t *testing.T) {
	f := newHostLocalQAFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)
	require.EqualValues(t, 1, rc.AdmissionRevision)

	// registration refusals, each with the positive control above
	_, err := f.repo.Register(ctx, f.wsA, f.agentA, f.runner, &hostlocalqa.Registration{Target: f.target, Producer: f.prod,
		Expires: f.now().Add(9 * time.Hour).Unix(), Senders: []uuid.UUID{f.sender}})
	require.Error(t, err, "an admission longer than 8h is refused")
	_, err = f.repo.Register(ctx, f.wsA, f.agentA, f.runner, &hostlocalqa.Registration{Target: f.target, Producer: f.prod,
		Expires: f.now().Add(time.Hour).Unix(), Senders: []uuid.UUID{uuid.New()}})
	require.Error(t, err, "a sender that is not an agent of the workspace is refused")

	env := f.envelope(rc)
	first, created, err := f.repo.Send(ctx, f.wsA, f.sender, env)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, hostlocalqa.StateQueued, first.State)

	again, created, err := f.repo.Send(ctx, f.wsA, f.sender, env)
	require.NoError(t, err)
	require.False(t, created, "an identical replay is not queued twice")
	require.Equal(t, first.DeliveryID, again.DeliveryID)

	changed := *env
	changed.Candidate = "other"
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, &changed)
	requireDetails(t, err, 409, "request_conflict")

	// send refusals: each is a different broken input against the same live receiver
	stale := f.envelope(rc)
	stale.ExpectedRevision = 7
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, stale)
	requireDetails(t, err, 409, "admission_stale")
	unknown := f.envelope(rc)
	unknown.ReceiverID = uuid.New()
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, unknown)
	requireDetails(t, err, 403, "host_local_qa_target_denied")
	_, _, err = f.repo.Send(ctx, f.wsA, f.other, f.envelope(rc))
	requireDetails(t, err, 403, "host_local_qa_target_denied")
	swapped := f.envelope(rc)
	swapped.Producer.TTY = "/dev/ttys999"
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, swapped)
	requireDetails(t, err, 403, "host_local_qa_target_denied")
	late := f.envelope(rc)
	late.Expires = f.now().Add(-time.Minute).Unix()
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, late)
	require.Error(t, err)
	var queued int
	require.NoError(t, f.db.Get(&queued, `SELECT count(*) FROM host_local_qa_deliveries WHERE receiver_id = $1`, rc.ID))
	require.Equal(t, 1, queued, "no refused send queued anything")

	// the sender reads its own receipt; another agent and an unknown id are the same 404
	got, err := f.repo.Get(ctx, f.wsA, f.sender, first.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateQueued, got.State)
	_, err = f.repo.Get(ctx, f.wsA, f.other, first.DeliveryID)
	require.Error(t, err)
	_, err = f.repo.Get(ctx, f.wsA, f.sender, uuid.New())
	require.Error(t, err)

	// receiver side
	list, err := f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, "host-1", "native-1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, f.policy.Text, list[0].Text)
	_, err = f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, "host-1", "no-such-session")
	requireDetails(t, err, 403, "host_local_qa_target_denied")
	_, err = f.repo.Pending(ctx, f.wsA, f.agentA, uuid.New(), "host-1", "native-1")
	requireDetails(t, err, 403, "host_local_qa_target_denied")

	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.binding(env, first.DeliveryID, hostlocalqa.StateAcknowledged))
	requireDetails(t, err, 409, "delivery_state_conflict")
	bad := f.binding(env, first.DeliveryID, hostlocalqa.StateReceived)
	bad.TextSHA256 = f.policy.OperationManifestSHA
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, bad)
	requireDetails(t, err, 403, "host_local_qa_target_denied")
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, uuid.New(), first.DeliveryID, f.binding(env, first.DeliveryID, hostlocalqa.StateReceived))
	requireDetails(t, err, 403, "host_local_qa_target_denied")

	recv, err := f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.binding(env, first.DeliveryID, hostlocalqa.StateReceived))
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateReceived, recv.State)
	require.NotNil(t, recv.ReceivedAt)
	replay, err := f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.binding(env, first.DeliveryID, hostlocalqa.StateReceived))
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateReceived, replay.State, "repeating the current state is a no-op")
	list, err = f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, "host-1", "native-1")
	require.NoError(t, err)
	require.Empty(t, list, "a received delivery is never listed again")

	// the guard: active with the exact binding, 403 on any difference
	chk, err := f.repo.Check(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.binding(env, first.DeliveryID, ""))
	require.NoError(t, err)
	require.True(t, chk.Active)
	require.Equal(t, env.RequestID, chk.RequestID)
	diff := f.binding(env, first.DeliveryID, "")
	diff.Producer.ProducerPID++
	_, err = f.repo.Check(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, diff)
	requireDetails(t, err, 403, "host_local_qa_check_denied")
	_, err = f.repo.Check(ctx, f.wsA, f.agentA, uuid.New(), first.DeliveryID, f.binding(env, first.DeliveryID, ""))
	requireDetails(t, err, 403, "host_local_qa_check_denied")

	done, err := f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, func() *hostlocalqa.Binding {
		b := f.binding(env, first.DeliveryID, hostlocalqa.StateAcknowledged)
		b.ResultCommentURL = "https://mesh.example.invalid/t/x#c"
		return b
	}())
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateAcknowledged, done.State)
	require.NotNil(t, done.ResultCommentURL)
	_, err = f.repo.Check(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.binding(env, first.DeliveryID, ""))
	require.NoError(t, err, "an acknowledged assignment is still active until it expires")

	// the sender's allow-list is part of the live check: dropping it ends the assignment
	rc2 := f.register(t, f.other)
	require.EqualValues(t, 2, rc2.AdmissionRevision)
	_, err = f.repo.Check(ctx, f.wsA, f.agentA, f.runner, first.DeliveryID, f.binding(env, first.DeliveryID, ""))
	requireDetails(t, err, 403, "host_local_qa_check_denied")
}

func TestHostLocalQARepoReadmissionRevokeAndExpiry(t *testing.T) {
	f := newHostLocalQAFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)

	queuedEnv := f.envelope(rc)
	queued, _, err := f.repo.Send(ctx, f.wsA, f.sender, queuedEnv)
	require.NoError(t, err)
	receivedEnv := f.envelope(rc)
	received, _, err := f.repo.Send(ctx, f.wsA, f.sender, receivedEnv)
	require.NoError(t, err)
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, received.DeliveryID, f.binding(receivedEnv, received.DeliveryID, hostlocalqa.StateReceived))
	require.NoError(t, err)

	// re-admission: queued work of the older revision fails and is never listed
	rc2 := f.register(t, f.sender)
	require.EqualValues(t, 2, rc2.AdmissionRevision)
	require.Equal(t, rc.ID, rc2.ID)
	got, err := f.repo.Get(ctx, f.wsA, f.sender, queued.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateFailed, got.State)
	require.Equal(t, "admission_stale", *got.ErrorCode)
	list, err := f.repo.Pending(ctx, f.wsA, f.agentA, f.runner, "host-1", "native-1")
	require.NoError(t, err)
	require.Empty(t, list)

	// expiry on the server clock: a queued delivery past its time fails as expired
	soon := f.envelope(rc2)
	soon.Expires = f.now().Add(10 * time.Minute).Unix()
	pending, _, err := f.repo.Send(ctx, f.wsA, f.sender, soon)
	require.NoError(t, err)
	f.offset.Store(11 * 60)
	got, err = f.repo.Get(ctx, f.wsA, f.sender, pending.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateFailed, got.State)
	require.Equal(t, "expired", *got.ErrorCode)
	_, err = f.repo.Check(ctx, f.wsA, f.agentA, f.runner, pending.DeliveryID, f.binding(soon, pending.DeliveryID, ""))
	requireDetails(t, err, 403, "host_local_qa_check_denied")
	f.offset.Store(0)

	// a delivery that expires before the receiver reports it cannot become received
	lapsing := f.envelope(rc2)
	lapsing.Expires = f.now().Add(10 * time.Minute).Unix()
	l, _, err := f.repo.Send(ctx, f.wsA, f.sender, lapsing)
	require.NoError(t, err)
	f.offset.Store(11 * 60)
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, l.DeliveryID, f.binding(lapsing, l.DeliveryID, hostlocalqa.StateReceived))
	requireDetails(t, err, 409, "delivery_expired")
	f.offset.Store(0)

	// revoke: a received, never-acknowledged delivery may have been injected, so it
	// becomes uncertain; queued work fails; revoking twice and strangers are 404
	live := f.envelope(rc2)
	liveD, _, err := f.repo.Send(ctx, f.wsA, f.sender, live)
	require.NoError(t, err)
	require.Error(t, f.repo.Revoke(ctx, f.wsA, f.agentA, uuid.New(), rc.ID), "another runner cannot revoke")
	require.NoError(t, f.repo.Revoke(ctx, f.wsA, f.agentA, f.runner, rc.ID))
	require.Error(t, f.repo.Revoke(ctx, f.wsA, f.agentA, f.runner, rc.ID))
	got, err = f.repo.Get(ctx, f.wsA, f.sender, received.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateUncertain, got.State, "received, never acknowledged: it may have been injected")
	got, err = f.repo.Get(ctx, f.wsA, f.sender, liveD.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateFailed, got.State)
	require.Equal(t, "receiver_revoked", *got.ErrorCode)
	_, err = f.repo.Check(ctx, f.wsA, f.agentA, f.runner, liveD.DeliveryID, f.binding(live, liveD.DeliveryID, ""))
	requireDetails(t, err, 403, "host_local_qa_check_denied")
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, f.envelope(rc2))
	requireDetails(t, err, 403, "host_local_qa_target_denied")
}

func TestHostLocalQARepoRevokeMarksReceivedUncertain(t *testing.T) {
	f := newHostLocalQAFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)
	env := f.envelope(rc)
	d, _, err := f.repo.Send(ctx, f.wsA, f.sender, env)
	require.NoError(t, err)
	_, err = f.repo.Ack(ctx, f.wsA, f.agentA, f.runner, d.DeliveryID, f.binding(env, d.DeliveryID, hostlocalqa.StateReceived))
	require.NoError(t, err)
	require.NoError(t, f.repo.Revoke(ctx, f.wsA, f.agentA, f.runner, rc.ID))
	got, err := f.repo.Get(ctx, f.wsA, f.sender, d.DeliveryID)
	require.NoError(t, err)
	require.Equal(t, hostlocalqa.StateUncertain, got.State)
	require.Equal(t, "delivery_uncertain", *got.ErrorCode)
}

// The request id is one namespace across both capabilities, whichever side wrote first.
func TestHostLocalQARepoRequestIDAcrossCapabilities(t *testing.T) {
	f := newHostLocalQAFixture(t)
	ctx := context.Background()
	rc := f.register(t, f.sender)

	// legacy first
	legacy := NewExistingSessionRepo(f.db)
	lt := ExistingSessionTarget{WorkspaceID: f.wsA, AgentID: f.agentA, RunnerID: f.runner, HostID: "host-1",
		Hostname: f.policy.Hostname, SessionID: "legacy-1"}
	lrc, err := legacy.Register(ctx, f.wsA, f.agentA, f.runner, lt.HostID, lt.Hostname, lt.SessionID, []uuid.UUID{f.sender})
	require.NoError(t, err)
	text := "read the two files and report"
	req := ExistingSessionRequest{RequestID: uuid.New(), Target: lt, Expected: lrc.AdmissionRevision, Text: text, TextSHA256: sha256Hex(text)}
	_, created, err := legacy.Enqueue(ctx, f.wsA, f.sender, req)
	require.NoError(t, err)
	require.True(t, created)
	env := f.envelope(rc)
	env.RequestID = req.RequestID
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, env)
	requireDetails(t, err, 409, "request_conflict")

	// QA first
	env2 := f.envelope(rc)
	_, _, err = f.repo.Send(ctx, f.wsA, f.sender, env2)
	require.NoError(t, err)
	req2 := ExistingSessionRequest{RequestID: env2.RequestID, Target: lt, Expected: lrc.AdmissionRevision, Text: text, TextSHA256: sha256Hex(text)}
	_, _, err = legacy.Enqueue(ctx, f.wsA, f.sender, req2)
	requireDetails(t, err, 409, "request_conflict")
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
