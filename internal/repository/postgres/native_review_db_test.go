package postgres

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

const (
	nrNB = uint64(1_000_000_000_000)
	nrEX = nrNB + 30_000_000_000
)

// stubClock is the TEST HARNESS clock provider. It is never reachable from a
// request and production wires none (reserve is then denied by default).
type stubClock struct {
	mu  sync.Mutex
	mut func(*domain.ClockEvidence)
}

func (s *stubClock) Evidence(_ context.Context, _ uuid.UUID, binding string) (domain.ClockEvidence, error) {
	ow, om := nrNB+1_000_000, uint64(5_000_000_000_000)
	ev := domain.ClockEvidence{BindingSHA: binding,
		OuterMono: strconv.FormatUint(om, 10), OuterWall: strconv.FormatUint(ow, 10),
		SampleMono: strconv.FormatUint(om+10_000_000, 10), SampleWall: strconv.FormatUint(ow+10_000_000, 10)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mut != nil {
		s.mut(&ev)
	}
	return ev, nil
}

// nrSigner is the TEST HARNESS issuer signer (a throwaway key per fixture).
// Production wires no signer, so issue is denied there by default.
type nrSigner struct{ key ed25519.PrivateKey }

func (s *nrSigner) Sign(_ context.Context, m []byte) ([]byte, error) {
	return ed25519.Sign(s.key, m), nil
}

type nrFixture struct {
	t     *testing.T
	db    *sqlx.DB
	repo  *NativeReviewRepo
	clock *stubClock
	ws    uuid.UUID

	operator, issuer, publisher, consumer, authIssuer, author uuid.UUID
	enr                                                       *NativeEnrollment
	signer                                                    *nrSigner
	registration                                              string
	body                                                      []byte
}

func nrSHA(c string) string {
	out := ""
	for len(out) < 64 {
		out += c
	}
	return out[:64]
}

func newNRFixture(t *testing.T) *nrFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the native review admission repo against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &nrFixture{t: t, db: db, clock: &stubClock{}, ws: uuid.New(), registration: nrSHA("a"), body: []byte("SHIP exact head, AC1-8 reviewed")}
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	f.signer = &nrSigner{key: priv}
	f.repo = NewNativeReviewRepo(db, f.clock, f.signer)
	f.exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'native review',$2,$3)`, f.ws, f.ws.String(), uuid.New())
	for _, p := range []*uuid.UUID{&f.operator, &f.issuer, &f.publisher, &f.consumer, &f.authIssuer, &f.author} {
		*p = uuid.New()
		f.exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'nr',$3,'h','p')`, *p, f.ws, p.String())
	}
	f.enr = &NativeEnrollment{
		WorkspaceID: f.ws, ProjectID: uuid.New(), IssuerActorID: f.issuer, PublisherActorID: f.publisher, TransportSourceID: uuid.New(),
		ConsumerActorID: f.consumer, AuthorityIssuerActorID: f.authIssuer, ResultAuthorActorID: f.author, OwnerActorID: uuid.New(),
		AuthorityID: uuid.New(), IssuerKey: base64.StdEncoding.EncodeToString(pub),
		ReceiverTask: f.receiver().Task, ReceiverAttempt: f.receiver().Attempt, ReceiverSession: f.receiver().Session, ReceiverGeneration: f.receiver().Generation,
		NativeRegistration: f.registration, NativeStage: "review-receipt", NativePackageSHA: nrSHA("3"),
		ReviewTaskID: uuid.New(), SourceTaskID: uuid.New(), ReviewerFamily: "codex", AuthorFamily: "claude",
		ScopeNamespace: uuid.New(), ScopeTest: true, CriteriaSHA: nrSHA("1"), ContextSHA: nrSHA("2"),
		SourceRepo: "evc-mesh", SourceSHA: "0123456789abcdef0123456789abcdef01234567", PackageSHA: nrSHA("3"), ManifestSHA: nrSHA("4"), ConfigSHA: nrSHA("5"),
		ProductTaskID: uuid.New(), ProductObservationID: nrSHA("6")[:32], ProductSessionID: nrSHA("7")[:32],
		AuthorityBytes: []byte("original authority bytes"), RootDispositionBytes: []byte("original root disposition"),
	}
	reg, rerr := domain.DeriveNativeRegistration(f.enr.link().Binding, f.enr.receiver())
	require.NoError(t, rerr)
	f.enr.NativeRegistration, f.registration = reg, reg
	f.enr.AuthoritySHA = domain.NativeDigest(f.enr.AuthorityBytes)
	f.enr.RootDispositionSHA = domain.NativeDigest(f.enr.RootDispositionBytes)
	_, err = f.repo.CreateEnrollment(context.Background(), f.enr, f.operator)
	require.NoError(t, err)
	return f
}

func (f *nrFixture) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.db.Exec(q, args...)
	require.NoError(f.t, err)
}

func (f *nrFixture) n(q string, args ...any) (n int) {
	f.t.Helper()
	require.NoError(f.t, f.db.Get(&n, q, args...))
	return n
}

// counts is reservation/marker rows for one authority.
func (f *nrFixture) counts(a uuid.UUID) (reservations, markers int) {
	return f.n(`SELECT count(*) FROM native_review_reservations WHERE authority_id=$1`, a),
		f.n(`SELECT count(*) FROM native_review_markers WHERE authority_id=$1`, a)
}

func (f *nrFixture) receiver() domain.NativeReceiver {
	return domain.NativeReceiver{Task: uuid.MustParse("00000000-0000-4000-8000-0000000000a1"), Attempt: uuid.MustParse("00000000-0000-4000-8000-0000000000a2"),
		Session: uuid.MustParse("00000000-0000-4000-8000-0000000000a3"), Generation: 1}
}

func (f *nrFixture) issueReq(verdict string, mut ...func(*domain.NativeIssueRequest)) domain.NativeIssueRequest {
	q := domain.NativeIssueRequest{
		Schema: domain.NativeReviewSchema, EnrollmentID: f.enr.ID, ReviewTaskID: f.enr.ReviewTaskID, SourceTaskID: f.enr.SourceTaskID, Verdict: verdict,
		Scope: f.enr.scope(), Pins: f.enr.pins(), Product: f.enr.product(), Receiver: f.enr.receiver(), Native: f.enr.link(),
		Body: domain.NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(f.body), SHA256: domain.NativeDigest(f.body)},
	}
	for _, m := range mut {
		m(&q)
	}
	return q
}

type nrChain struct {
	receipt   *domain.NativePacket
	sha       string
	authority uuid.UUID
	event     string
}

// chain runs the full ordinary setup: issue -> authority -> delivery link.
func (f *nrFixture) chain() *nrChain {
	f.t.Helper()
	ctx := context.Background()
	rc, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	require.NoError(f.t, err)
	pk, err := f.repo.ReadReceipt(ctx, f.issuer, rc.ReceiptID)
	require.NoError(f.t, err)
	sha := pk.ReceiptSHA
	a, err := f.repo.CreateAuthority(ctx, f.authIssuer, domain.NativeAuthorityRequest{Schema: domain.NativeReviewSchema, EnrollmentID: f.enr.ID, AuthorityID: f.enr.AuthorityID, ReceiptID: rc.ReceiptID,
		BudgetID: uuid.New(), BudgetMS: 1000, MaxReservations: 1, NotBeforeNS: strconv.FormatUint(nrNB, 10), ExpiresNS: strconv.FormatUint(nrEX, 10), ClockBinding: nrSHA("c")})
	require.NoError(f.t, err)
	ev := "ev-" + uuid.NewString()
	require.NoError(f.t, f.repo.RecordDelivery(ctx, f.publisher, f.registration, ev, rc.ReceiptID))
	return &nrChain{receipt: rc, sha: sha, authority: a, event: ev}
}

func (f *nrFixture) reserveReq(c *nrChain, mut ...func(*domain.NativeReserveRequest)) domain.NativeReserveRequest {
	q := domain.NativeReserveRequest{Schema: domain.NativeReviewSchema, AuthorityID: c.authority, ReceiptID: c.receipt.ReceiptID, ReceiptSHA: c.sha,
		Scope: f.enr.scope(), Pins: f.enr.pins(), Receiver: f.receiver(), Registration: f.registration, EventID: c.event, RequestID: uuid.New()}
	for _, m := range mut {
		m(&q)
	}
	return q
}

func requireRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var r *domain.NativeRefusal
	require.True(t, errors.As(err, &r), "want refusal %s, got %v", code, err)
	require.Equal(t, code, r.Code)
}

// waitBlocked returns once some other backend is waiting on a lock: the
// ordering evidence comes from the database, never from a sleep.
func (f *nrFixture) waitBlocked() {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if f.n(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	f.t.Fatal("no backend ever blocked on the guard lock")
}

func TestNativeReview_PositiveReserveAndReplay(t *testing.T) {
	f := newNRFixture(t)
	c := f.chain()
	ctx := context.Background()
	q := f.reserveReq(c)
	res, err := f.repo.Reserve(ctx, f.consumer, q)
	require.NoError(t, err)
	require.True(t, res.Consumed)
	require.False(t, res.ExecuteAllowed)
	require.NotEqual(t, uuid.Nil, res.MarkerID)
	r, m := f.counts(c.authority)
	require.Equal(t, [2]int{1, 1}, [2]int{r, m})

	// Lost response: exact duplicate reads back the same marker, never a second.
	again, err := f.repo.Reserve(ctx, f.consumer, q)
	require.NoError(t, err)
	require.Equal(t, res.MarkerID, again.MarkerID)
	require.False(t, again.ExecuteAllowed)
	rb, err := f.repo.GetReservation(ctx, f.consumer, c.authority)
	require.NoError(t, err)
	require.Equal(t, res.ReservationID, rb.ReservationID)
	require.False(t, rb.ExecuteAllowed)

	// Different request_id on the same authority: the once-budget refuses.
	_, err = f.repo.Reserve(ctx, f.consumer, f.reserveReq(c))
	requireRefusal(t, err, "authority_consumed")
	// Changed payload under the used request_id: conflict.
	q2 := q
	q2.EventID = "other-event"
	_, err = f.repo.Reserve(ctx, f.consumer, q2)
	requireRefusal(t, err, "idempotency_payload_mismatch")
	r, m = f.counts(c.authority)
	require.Equal(t, [2]int{1, 1}, [2]int{r, m})

	// Revocation after the commit never refunds the consumed budget.
	require.NoError(t, f.repo.RevokeAuthority(ctx, c.authority, f.authIssuer, "late"))
	r, m = f.counts(c.authority)
	require.Equal(t, [2]int{1, 1}, [2]int{r, m})
	_, err = f.repo.GetReservation(ctx, f.publisher, c.authority)
	requireRefusal(t, err, "consumer_not_enrolled")
}

func TestNativeReview_ReserveRefusals(t *testing.T) {
	type tc struct {
		name  string
		code  string
		actor func(*nrFixture) uuid.UUID
		req   func(*domain.NativeReserveRequest)
		setup func(*nrFixture, *nrChain)
		clock func(*domain.ClockEvidence)
		noClk bool
	}
	win := func(ow, om, sm, sw uint64) func(*domain.ClockEvidence) {
		return func(e *domain.ClockEvidence) {
			e.OuterWall, e.OuterMono, e.SampleMono, e.SampleWall = strconv.FormatUint(ow, 10), strconv.FormatUint(om, 10), strconv.FormatUint(sm, 10), strconv.FormatUint(sw, 10)
		}
	}
	cases := []tc{
		{name: "no clock protocol wired (production default)", code: "clock_proof_unavailable", noClk: true},
		{name: "wrong consumer", code: "consumer_not_enrolled", actor: func(f *nrFixture) uuid.UUID { return f.publisher }},
		{name: "issuer as consumer", code: "consumer_not_enrolled", actor: func(f *nrFixture) uuid.UUID { return f.issuer }},
		{name: "wrong receipt digest", code: "receipt_mismatch", req: func(q *domain.NativeReserveRequest) { q.ReceiptSHA = nrSHA("e") }},
		{name: "wrong namespace", code: "scope_mismatch", req: func(q *domain.NativeReserveRequest) { q.Scope.Namespace = uuid.New() }},
		{name: "wrong criteria", code: "scope_mismatch", req: func(q *domain.NativeReserveRequest) { q.Scope.CriteriaSHA = nrSHA("9") }},
		{name: "wrong source sha", code: "pins_mismatch", req: func(q *domain.NativeReserveRequest) { q.Pins.SourceSHA = "ffffffffffffffffffffffffffffffffffffffff" }},
		{name: "wrong package", code: "pins_mismatch", req: func(q *domain.NativeReserveRequest) { q.Pins.PackageSHA = nrSHA("9") }},
		{name: "wrong receiver generation", code: "receiver_mismatch", req: func(q *domain.NativeReserveRequest) { q.Receiver.Generation = 2 }},
		{name: "wrong receiver session", code: "receiver_mismatch", req: func(q *domain.NativeReserveRequest) { q.Receiver.Session = uuid.New() }},
		{name: "wrong registration", code: "registration_mismatch", req: func(q *domain.NativeReserveRequest) { q.Registration = nrSHA("b") }},
		{name: "unlinked native event", code: "delivery_not_linked", req: func(q *domain.NativeReserveRequest) { q.EventID = "never-delivered" }},
		{name: "wrong receipt id", code: "receipt_mismatch", req: func(q *domain.NativeReserveRequest) { q.ReceiptID = uuid.New() }},
		{name: "revoked authority", code: "authority_revoked", setup: func(f *nrFixture, c *nrChain) {
			require.NoError(f.t, f.repo.RevokeAuthority(context.Background(), c.authority, f.authIssuer, "r"))
		}},
		{name: "revoked enrollment", code: "enrollment_revoked", setup: func(f *nrFixture, c *nrChain) {
			require.NoError(f.t, f.repo.RevokeEnrollment(context.Background(), f.enr.ID, f.operator, "r"))
		}},
		{name: "receiver turnover", code: "receiver_mismatch", setup: func(f *nrFixture, c *nrChain) {
			next := f.receiver()
			next.Generation, next.Session = 2, uuid.New()
			require.NoError(f.t, f.repo.TurnoverReceiver(context.Background(), c.authority, f.authIssuer, next))
		}},
		{name: "issuer left the workspace", code: "actor_not_current", setup: func(f *nrFixture, c *nrChain) {
			f.exec(`DELETE FROM agents WHERE id=$1`, f.issuer)
		}},
		{name: "before window", code: "window_closed", clock: win(nrNB-1, 5_000_000_000_000, 5_000_010_000_000, nrNB+9_999_999)},
		{name: "expired", code: "window_closed", clock: win(nrEX, 5_000_000_000_000, 5_000_010_000_000, nrEX+10_000_000)},
		{name: "wall/mono skew", code: "clock_skew", clock: win(nrNB+1_000_000, 5_000_000_000_000, 5_000_010_000_000, nrNB+1_000_000+10_000_000+6_000_000)},
		{name: "sample inside cutoff margin", code: "window_cutoff", clock: win(nrNB+1_000_000, 5_000_000_000_000, 5_000_000_000_000+950_000_000, nrNB+1_000_000+950_000_000)},
		{name: "sample before outer start", code: "clock_regressed", clock: win(nrNB+1_000_000, 5_000_000_000_000, 4_999_999_999_999, nrNB+1_000_000-1)},
		{name: "unbound clock evidence", code: "clock_binding_mismatch", clock: func(e *domain.ClockEvidence) { e.BindingSHA = nrSHA("d") }},
		{name: "non canonical ns", code: "ns_not_canonical", clock: func(e *domain.ClockEvidence) { e.SampleMono = "05000010000000" }},
	}
	// Positive control: the same fixture with no mutation reserves once.
	t.Run("positive control", func(t *testing.T) {
		f := newNRFixture(t)
		c := f.chain()
		_, err := f.repo.Reserve(context.Background(), f.consumer, f.reserveReq(c))
		require.NoError(t, err)
	})
	for _, x := range cases {
		t.Run(x.name, func(t *testing.T) {
			f := newNRFixture(t)
			c := f.chain()
			if x.setup != nil {
				x.setup(f, c)
			}
			if x.clock != nil {
				f.clock.mut = x.clock
			}
			if x.noClk {
				f.repo.clock = nil
			}
			actor := f.consumer
			if x.actor != nil {
				actor = x.actor(f)
			}
			var muts []func(*domain.NativeReserveRequest)
			if x.req != nil {
				muts = append(muts, x.req)
			}
			_, err := f.repo.Reserve(context.Background(), actor, f.reserveReq(c, muts...))
			requireRefusal(t, err, x.code)
			r, m := f.counts(c.authority)
			require.Equal(t, [2]int{0, 0}, [2]int{r, m}, "refusal must leave reservation0/marker0")
		})
	}
}

func TestNativeReview_IssueRefusals(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		actor func(*nrFixture) uuid.UUID
		mut   func(*domain.NativeIssueRequest)
	}{
		{name: "publisher cannot issue", code: "issuer_not_enrolled", actor: func(f *nrFixture) uuid.UUID { return f.publisher }},
		{name: "author cannot issue", code: "issuer_not_enrolled", actor: func(f *nrFixture) uuid.UUID { return f.author }},
		{name: "unknown enrollment", code: "enrollment_not_found", mut: func(q *domain.NativeIssueRequest) { q.EnrollmentID = uuid.New() }},
		{name: "wrong review task", code: "task_binding_mismatch", mut: func(q *domain.NativeIssueRequest) { q.ReviewTaskID = uuid.New() }},
		{name: "wrong source task", code: "task_binding_mismatch", mut: func(q *domain.NativeIssueRequest) { q.SourceTaskID = uuid.New() }},
		{name: "wrong namespace", code: "scope_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Scope.Namespace = uuid.New() }},
		{name: "wrong manifest", code: "pins_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Pins.ManifestSHA = nrSHA("9") }},
		{name: "wrong product authority digest", code: "product_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Product.AuthoritySHA = nrSHA("9") }},
		{name: "wrong product session", code: "product_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Product.SessionID = nrSHA("9")[:32] }},
		{name: "not a test scope", code: "scope_not_test", mut: func(q *domain.NativeIssueRequest) { q.Scope.Test = false }},
		{name: "body digest mismatch", code: "body_digest_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Body.SHA256 = nrSHA("9") }},
		{name: "missing body", code: "body_missing", mut: func(q *domain.NativeIssueRequest) { q.Body = domain.NativeBody{} }},
		{name: "receiver generation zero", code: "receiver_invalid", mut: func(q *domain.NativeIssueRequest) { q.Receiver.Generation = 0 }},
		{name: "wrong receiver generation", code: "receiver_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Receiver.Generation = 2 }},
		{name: "other native registration", code: "native_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Native.Registration = nrSHA("9") }},
		{name: "other native producer", code: "native_mismatch", mut: func(q *domain.NativeIssueRequest) { q.Native.Binding.Producer = uuid.NewString() }},
		{name: "unknown verdict", code: "verdict_invalid", mut: func(q *domain.NativeIssueRequest) { q.Verdict = "LGTM" }},
	}
	t.Run("positive control", func(t *testing.T) {
		f := newNRFixture(t)
		_, err := f.repo.IssueReceipt(context.Background(), f.issuer, f.issueReq(domain.NativeVerdictShip))
		require.NoError(t, err)
	})
	for _, x := range cases {
		t.Run(x.name, func(t *testing.T) {
			f := newNRFixture(t)
			actor := f.issuer
			if x.actor != nil {
				actor = x.actor(f)
			}
			var muts []func(*domain.NativeIssueRequest)
			if x.mut != nil {
				muts = append(muts, x.mut)
			}
			_, err := f.repo.IssueReceipt(context.Background(), actor, f.issueReq(domain.NativeVerdictShip, muts...))
			requireRefusal(t, err, x.code)
			require.Zero(t, f.n(`SELECT count(*) FROM native_review_receipts WHERE enrollment_id=$1`, f.enr.ID))
		})
	}
}

func TestNativeReview_DoNotShipNeverAdmits(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	rc, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictDoNotShip))
	require.NoError(t, err) // stored as data
	_, err = f.repo.CreateAuthority(ctx, f.authIssuer, domain.NativeAuthorityRequest{Schema: domain.NativeReviewSchema, EnrollmentID: f.enr.ID, AuthorityID: f.enr.AuthorityID, ReceiptID: rc.ReceiptID,
		BudgetID: uuid.New(), BudgetMS: 1000, MaxReservations: 1, NotBeforeNS: strconv.FormatUint(nrNB, 10), ExpiresNS: strconv.FormatUint(nrEX, 10), ClockBinding: nrSHA("c")})
	requireRefusal(t, err, "receipt_not_admissible")
	require.Zero(t, f.n(`SELECT count(*) FROM native_review_authorities WHERE enrollment_id=$1`, f.enr.ID))
}

func TestNativeReview_EnrollmentIndependenceAndImmutability(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	bad := *f.enr
	bad.ID = uuid.Nil
	bad.IssuerActorID = bad.ResultAuthorActorID // author as reviewer
	_, err := f.repo.CreateEnrollment(ctx, &bad, f.operator)
	requireRefusal(t, err, "enrollment_invalid")
	bad = *f.enr
	bad.ID = uuid.Nil
	bad.AuthorFamily = bad.ReviewerFamily // same family
	_, err = f.repo.CreateEnrollment(ctx, &bad, f.operator)
	requireRefusal(t, err, "enrollment_invalid")
	bad = *f.enr
	bad.ID = uuid.Nil
	bad.AuthoritySHA = nrSHA("0") // digest of other bytes
	_, err = f.repo.CreateEnrollment(ctx, &bad, f.operator)
	requireRefusal(t, err, "enrollment_invalid")

	c := f.chain()
	for _, q := range []string{
		`UPDATE native_review_receipts SET verdict='DO-NOT-SHIP' WHERE id=$1`,
		`DELETE FROM native_review_receipts WHERE id=$1`,
	} {
		_, err = f.db.Exec(q, c.receipt.ReceiptID)
		require.Error(t, err)
	}
	_, err = f.db.Exec(`UPDATE native_review_enrollments SET reviewer_family='x' WHERE id=$1`, f.enr.ID)
	require.Error(t, err)
	// stored receipt still verifies against its digests
	pk, err := f.repo.ReadReceipt(ctx, f.issuer, c.receipt.ReceiptID)
	require.NoError(t, err)
	require.Equal(t, c.sha, pk.ReceiptSHA)
}

func TestNativeReview_DeliveryLinkGuards(t *testing.T) {
	f := newNRFixture(t)
	c := f.chain()
	ctx := context.Background()
	requireRefusal(t, f.repo.RecordDelivery(ctx, f.issuer, f.registration, "ev-x", c.receipt.ReceiptID), "publisher_not_enrolled")
	requireRefusal(t, f.repo.RecordDelivery(ctx, f.publisher, nrSHA("b"), "ev-x", c.receipt.ReceiptID), "registration_mismatch")
	// Idempotent exact repeat is fine; the same event for another receipt is not.
	require.NoError(t, f.repo.RecordDelivery(ctx, f.publisher, f.registration, c.event, c.receipt.ReceiptID))
	// a second enrollment with the SAME binding and receiver (so the same derived
	// registration; one receipt per enrollment) issues its own receipt
	e2 := *f.enr
	e2.ID, e2.AuthorityID = uuid.New(), uuid.New()
	_, err := f.repo.CreateEnrollment(ctx, &e2, f.operator)
	require.NoError(t, err)
	rc2, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip, func(q *domain.NativeIssueRequest) { q.EnrollmentID = e2.ID }))
	require.NoError(t, err)
	require.Equal(t, f.registration, rc2.Native.Registration, "same binding and receiver derive the same registration")
	requireRefusal(t, f.repo.RecordDelivery(ctx, f.publisher, f.registration, c.event, rc2.ReceiptID), "delivery_conflict")
}

// Deterministic commit orders: the parked transaction holds the guard locks, the
// other backend is proven blocked by pg_stat_activity, then the first is released.
func TestNativeReview_RaceBothCommitOrders(t *testing.T) {
	park := func(f *nrFixture, op string) (reached, release chan struct{}) {
		reached, release = make(chan struct{}), make(chan struct{})
		var once sync.Once
		f.repo.hooks.afterLocks = func(got string) {
			if got != op {
				return
			}
			once.Do(func() { close(reached); <-release })
		}
		return
	}
	for _, revoke := range []string{"revoke-authority", "revoke-enrollment"} {
		revokeFn := func(f *nrFixture, c *nrChain) error {
			if revoke == "revoke-authority" {
				return f.repo.RevokeAuthority(context.Background(), c.authority, f.authIssuer, "race")
			}
			return f.repo.RevokeEnrollment(context.Background(), f.enr.ID, f.operator, "race")
		}
		t.Run(revoke+" commits first => 0/0", func(t *testing.T) {
			f := newNRFixture(t)
			c := f.chain()
			reached, release := park(f, revoke)
			revDone := make(chan error, 1)
			go func() { revDone <- revokeFn(f, c) }()
			<-reached // revoke holds its locks, uncommitted
			resDone := make(chan error, 1)
			go func() { _, err := f.repo.Reserve(context.Background(), f.consumer, f.reserveReq(c)); resDone <- err }()
			f.waitBlocked() // reserve is queued behind the revoke
			close(release)
			require.NoError(t, <-revDone)
			err := <-resDone
			if revoke == "revoke-authority" {
				requireRefusal(t, err, "authority_revoked")
			} else {
				requireRefusal(t, err, "enrollment_revoked")
			}
			r, m := f.counts(c.authority)
			require.Equal(t, [2]int{0, 0}, [2]int{r, m})
		})
		t.Run(revoke+" waits for reserve => 1/1, no refund", func(t *testing.T) {
			f := newNRFixture(t)
			c := f.chain()
			reached, release := park(f, "reserve")
			resDone := make(chan error, 1)
			go func() { _, err := f.repo.Reserve(context.Background(), f.consumer, f.reserveReq(c)); resDone <- err }()
			<-reached // reserve holds both locks, uncommitted
			revDone := make(chan error, 1)
			go func() { revDone <- revokeFn(f, c) }()
			f.waitBlocked() // revoke is queued behind the reserve
			close(release)
			require.NoError(t, <-resDone)
			require.NoError(t, <-revDone)
			r, m := f.counts(c.authority)
			require.Equal(t, [2]int{1, 1}, [2]int{r, m})
		})
	}
	t.Run("receiver turnover commits first => 0/0", func(t *testing.T) {
		f := newNRFixture(t)
		c := f.chain()
		reached, release := park(f, "turnover")
		next := f.receiver()
		next.Generation, next.Session = 2, uuid.New()
		done := make(chan error, 1)
		go func() { done <- f.repo.TurnoverReceiver(context.Background(), c.authority, f.authIssuer, next) }()
		<-reached
		resDone := make(chan error, 1)
		go func() { _, err := f.repo.Reserve(context.Background(), f.consumer, f.reserveReq(c)); resDone <- err }()
		f.waitBlocked()
		close(release)
		require.NoError(t, <-done)
		requireRefusal(t, <-resDone, "receiver_mismatch")
		r, m := f.counts(c.authority)
		require.Equal(t, [2]int{0, 0}, [2]int{r, m})
	})
}

func TestNativeReview_ConcurrentDifferentKeysSingleMarker(t *testing.T) {
	f := newNRFixture(t)
	c := f.chain()
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, consumed := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.repo.Reserve(context.Background(), f.consumer, f.reserveReq(c))
			mu.Lock()
			defer mu.Unlock()
			var r *domain.NativeRefusal
			switch {
			case err == nil:
				ok++
			case errors.As(err, &r) && r.Code == "authority_consumed":
				consumed++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, ok)
	require.Equal(t, n-1, consumed)
	r, m := f.counts(c.authority)
	require.Equal(t, [2]int{1, 1}, [2]int{r, m})
}

// Crash before commit: the transaction rolls back and nothing is consumed, so a
// later honest reserve still works once. After commit the budget is spent.
func TestNativeReview_CrashBoundaries(t *testing.T) {
	f := newNRFixture(t)
	c := f.chain()
	ctx, cancel := context.WithCancel(context.Background())
	f.repo.hooks.afterLocks = func(op string) {
		if op == "reserve" {
			cancel() // process dies after the locks, before any write
		}
	}
	_, err := f.repo.Reserve(ctx, f.consumer, f.reserveReq(c))
	require.Error(t, err)
	r, m := f.counts(c.authority)
	require.Equal(t, [2]int{0, 0}, [2]int{r, m}, "pre-commit crash consumes nothing")

	f.repo.hooks.afterLocks = nil
	q := f.reserveReq(c)
	res, err := f.repo.Reserve(context.Background(), f.consumer, q)
	require.NoError(t, err)
	// Response lost after commit: recovery is read-only and never permission.
	rb, err := f.repo.GetReservation(context.Background(), f.consumer, c.authority)
	require.NoError(t, err)
	require.Equal(t, res.MarkerID, rb.MarkerID)
	require.False(t, rb.ExecuteAllowed)
	_, err = f.repo.Reserve(context.Background(), f.consumer, f.reserveReq(c))
	requireRefusal(t, err, "authority_consumed")
	r, m = f.counts(c.authority)
	require.Equal(t, [2]int{1, 1}, [2]int{r, m})
}

func TestNativeReview_ReservationTablesAppendOnly(t *testing.T) {
	f := newNRFixture(t)
	c := f.chain()
	_, err := f.repo.Reserve(context.Background(), f.consumer, f.reserveReq(c))
	require.NoError(t, err)
	for _, q := range []string{
		`DELETE FROM native_review_reservations`,
		`UPDATE native_review_reservations SET consumer_actor_id=gen_random_uuid()`,
		`DELETE FROM native_review_markers`,
		`DELETE FROM native_review_authority_revocations`,
	} {
		_, err = f.db.Exec(q)
		require.Error(t, err, q)
	}
}

// Readback is not public: only the actors named by the enrollment (and the
// enrolling operator) may read; a stranger and the result author get the same
// answer as for a missing receipt.
func TestNativeReviewReadReceiptIsAuthorized(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	rc, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	require.NoError(t, err)
	for _, who := range []uuid.UUID{f.issuer, f.publisher, f.consumer, f.authIssuer, f.operator} {
		_, err = f.repo.ReadReceipt(ctx, who, rc.ReceiptID)
		require.NoError(t, err)
	}
	for _, who := range []uuid.UUID{uuid.New(), f.author} {
		_, err = f.repo.ReadReceipt(ctx, who, rc.ReceiptID)
		requireRefusal(t, err, "receipt_not_found")
	}
	_, err = f.repo.ReadReceipt(ctx, f.issuer, uuid.New())
	requireRefusal(t, err, "receipt_not_found")
}

// The issued packet is the flat A2 packet: readback is byte-identical, carries the
// operator-reserved authority_id, one persisted created_at and a signature that
// verifies under the ENROLLED key only.
func TestNativeReviewIssueReadbackSignedPacket(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	pk, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	require.NoError(t, err)
	require.Equal(t, f.enr.AuthorityID, pk.AuthorityID)
	require.NoError(t, domain.VerifyIssuerSignature(f.enr.IssuerKey, pk.ReceiptSHA, pk.IssuerSignature))
	back, err := f.repo.ReadReceipt(ctx, f.issuer, pk.ReceiptID)
	require.NoError(t, err)
	a, err := pk.Canonical()
	require.NoError(t, err)
	b, err := back.Canonical()
	require.NoError(t, err)
	require.Equal(t, a, b, "readback must be byte-identical to the issued packet")
	require.Equal(t, pk.CreatedAt, back.CreatedAt)
	for _, c := range a {
		require.Less(t, c, byte(0x80), "canonical packet must be pure ASCII like the Runner canonical form")
	}
	// Opt-in evidence hook: dump the issued packet and the matching Runner
	// enrollment so the Runner's own admit/receiver code can be run against it.
	if dir := os.Getenv("NR_A2_DUMP_DIR"); dir != "" {
		var m map[string]any
		require.NoError(t, json.Unmarshal(a, &m))
		pin := map[string]any{}
		for k, v := range m {
			switch k {
			case "receipt_id", "result_author_actor_id", "verdict", "body", "receipt_sha256", "created_at", "issuer_signature":
			default:
				pin[k] = v
			}
		}
		enr, err := json.Marshal(map[string]any{"pin": pin, "assignment": map[string]any{"owner": uuid.NewString(), "sequence": 1}, "issuer_key": f.enr.IssuerKey})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dir+"/packet.json", a, 0o600))
		require.NoError(t, os.WriteFile(dir+"/enrollment.json", enr, 0o600))
	}
	// the same packet under a different key does not verify
	other, _, _ := ed25519.GenerateKey(nil)
	require.Error(t, domain.VerifyIssuerSignature(base64.StdEncoding.EncodeToString(other), pk.ReceiptSHA, pk.IssuerSignature))
}

// No configured signer, or a signer whose key is not the enrolled one, issues
// nothing: reservation-side proof needs a receipt that only the enrolled key made.
func TestNativeReviewIssueNeedsEnrolledSigner(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	f.repo.signer = nil
	_, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	requireRefusal(t, err, "signer_unavailable")
	_, wrong, _ := ed25519.GenerateKey(nil)
	f.repo.signer = &nrSigner{key: wrong}
	_, err = f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	requireRefusal(t, err, "issuer_signature_invalid")
	require.Zero(t, f.n(`SELECT count(*) FROM native_review_receipts WHERE enrollment_id=$1`, f.enr.ID))
	f.repo.signer = f.signer // positive control
	_, err = f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	require.NoError(t, err)
	// one receipt per enrollment
	_, err = f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	requireRefusal(t, err, "receipt_conflict")
}

// The guard materialises only the operator-reserved authority_id and exactly the
// receipt that carries it: another id is refused, and the database itself refuses
// a guard row that rebinds.
func TestNativeReviewAuthorityIDIsOperatorReserved(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	rc, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	require.NoError(t, err)
	req := func(id uuid.UUID) domain.NativeAuthorityRequest {
		return domain.NativeAuthorityRequest{Schema: domain.NativeReviewSchema, EnrollmentID: f.enr.ID, AuthorityID: id, ReceiptID: rc.ReceiptID,
			BudgetID: uuid.New(), BudgetMS: 1000, MaxReservations: 1, NotBeforeNS: strconv.FormatUint(nrNB, 10), ExpiresNS: strconv.FormatUint(nrEX, 10), ClockBinding: nrSHA("c")}
	}
	_, err = f.repo.CreateAuthority(ctx, f.authIssuer, req(uuid.New()))
	requireRefusal(t, err, "authority_id_mismatch")
	require.Zero(t, f.n(`SELECT count(*) FROM native_review_authorities WHERE enrollment_id=$1`, f.enr.ID))
	// rebind attempt straight at the table: wrong id for this receipt
	_, err = f.db.Exec(`INSERT INTO native_review_authorities(id, enrollment_id, receipt_id, created_by, budget_id, budget_ms, max_reservations,
 not_before_ns, expires_ns, clock_binding_sha256, receiver_task, receiver_attempt, receiver_session, receiver_generation)
 VALUES($1,$2,$3,$4,$5,1000,1,'1000000000000','1000030000000000',$6,$7,$8,$9,1)`, uuid.New(), f.enr.ID, rc.ReceiptID, f.authIssuer, uuid.New(), nrSHA("c"),
		f.receiver().Task, f.receiver().Attempt, f.receiver().Session)
	require.Error(t, err)
	id, err := f.repo.CreateAuthority(ctx, f.authIssuer, req(f.enr.AuthorityID)) // positive control
	require.NoError(t, err)
	require.Equal(t, f.enr.AuthorityID, id)
}
