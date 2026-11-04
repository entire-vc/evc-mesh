package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// NativeClock is the trusted clock/accounting protocol. It is consulted only
// AFTER the guard locks are held and is never selectable by a request. The
// production default is nil: reserve is refused until a protocol is proven and
// wired explicitly (release gate OPEN). A test harness injects a stub.
type NativeClock interface {
	Evidence(ctx context.Context, authorityID uuid.UUID, clockBindingSHA string) (domain.ClockEvidence, error)
}

// nativeHooks lets tests park a transaction at a deterministic point (after the
// guard locks, before any fresh read) so both commit orders can be driven
// without sleeps. Always nil in production.
type nativeHooks struct {
	afterLocks func(op string)
	// now and newID make issue deterministic for the cross-language golden
	// vector; always nil in production.
	now   func() time.Time
	newID func() uuid.UUID
}

// NativeSigner signs a receipt message with the enrolled issuer's key. It is
// never the publisher's and never a fixture in production: the production repo
// is constructed with nil, so issue refuses (signer_unavailable) until a trusted
// signer is deliberately wired (release gate).
type NativeSigner interface {
	Sign(ctx context.Context, message []byte) ([]byte, error)
}

// NativeReviewRepo owns enrollment, immutable receipts, authority guards and the
// single atomic reservation+marker.
type NativeReviewRepo struct {
	db     *sqlx.DB
	clock  NativeClock
	signer NativeSigner
	hooks  nativeHooks
}

func NewNativeReviewRepo(db *sqlx.DB, clock NativeClock, signer NativeSigner) *NativeReviewRepo {
	return &NativeReviewRepo{db: db, clock: clock, signer: signer}
}

// NativeEnrollment is the trusted enrollment row. Written by an operator only.
type NativeEnrollment struct {
	ID                     uuid.UUID `db:"id"`
	WorkspaceID            uuid.UUID `db:"workspace_id"`
	ProjectID              uuid.UUID `db:"project_id"`
	IssuerActorID          uuid.UUID `db:"issuer_actor_id"`
	PublisherActorID       uuid.UUID `db:"publisher_actor_id"`
	TransportSourceID      uuid.UUID `db:"transport_source_id"`
	ConsumerActorID        uuid.UUID `db:"consumer_actor_id"`
	AuthorityIssuerActorID uuid.UUID `db:"authority_issuer_actor_id"`
	ResultAuthorActorID    uuid.UUID `db:"result_author_actor_id"`
	OwnerActorID           uuid.UUID `db:"owner_actor_id"`
	AuthorityID            uuid.UUID `db:"authority_id"`
	IssuerKey              string    `db:"issuer_key"`
	ReceiverTask           uuid.UUID `db:"receiver_task"`
	ReceiverAttempt        uuid.UUID `db:"receiver_attempt"`
	ReceiverSession        uuid.UUID `db:"receiver_session"`
	ReceiverGeneration     int64     `db:"receiver_generation"`
	NativeRegistration     string    `db:"native_registration"`
	NativeStage            string    `db:"native_stage"`
	NativePackageSHA       string    `db:"native_package_sha256"`
	ReviewTaskID           uuid.UUID `db:"review_task_id"`
	SourceTaskID           uuid.UUID `db:"source_task_id"`
	ReviewerFamily         string    `db:"reviewer_family"`
	AuthorFamily           string    `db:"author_family"`
	ScopeDomain            string    `db:"scope_domain"`
	ScopeNamespace         uuid.UUID `db:"scope_namespace"`
	ScopeTest              bool      `db:"scope_test"`
	CriteriaSHA            string    `db:"criteria_sha256"`
	ContextSHA             string    `db:"context_sha256"`
	SourceRepo             string    `db:"source_repo"`
	SourceSHA              string    `db:"source_sha"`
	PackageSHA             string    `db:"package_sha256"`
	ManifestSHA            string    `db:"manifest_sha256"`
	ConfigSHA              string    `db:"config_sha256"`
	ProductTaskID          uuid.UUID `db:"product_task_id"`
	ProductObservationID   string    `db:"product_observation_id"`
	ProductSessionID       string    `db:"product_session_id"`
	AuthorityBytes         []byte    `db:"authority_bytes"`
	RootDispositionBytes   []byte    `db:"root_disposition_bytes"`
	AuthoritySHA           string    `db:"authority_sha256"`
	RootDispositionSHA     string    `db:"root_disposition_sha256"`
	CreatedBy              uuid.UUID `db:"created_by"`
	CreatedAt              time.Time `db:"created_at"`
}

func (e *NativeEnrollment) scope() domain.NativeScope {
	return domain.NativeScope{Domain: e.ScopeDomain, Namespace: e.ScopeNamespace, Test: e.ScopeTest, CriteriaSHA: e.CriteriaSHA, ContextSHA: e.ContextSHA}
}
func (e *NativeEnrollment) pins() domain.NativePins {
	return domain.NativePins{SourceRepo: e.SourceRepo, SourceSHA: e.SourceSHA, PackageSHA: e.PackageSHA, ManifestSHA: e.ManifestSHA, ConfigSHA: e.ConfigSHA}
}
func (e *NativeEnrollment) receiver() domain.NativeReceiver {
	return domain.NativeReceiver{Task: e.ReceiverTask, Attempt: e.ReceiverAttempt, Session: e.ReceiverSession, Generation: e.ReceiverGeneration}
}

// link is the native registration and closed binding the operator pinned: the
// producer is the enrolled transport source and the task the enrolled source task.
func (e *NativeEnrollment) link() domain.NativeLink {
	return domain.NativeLink{Registration: e.NativeRegistration, Binding: domain.NativeBinding{
		Producer: e.TransportSourceID.String(), Task: e.SourceTaskID.String(), Stage: e.NativeStage, Package: e.NativePackageSHA, Test: true}}
}

func (e *NativeEnrollment) product() domain.NativeProduct {
	return domain.NativeProduct{OwningTaskID: e.ProductTaskID, ObservationID: e.ProductObservationID, SessionID: e.ProductSessionID,
		AuthoritySHA: e.AuthoritySHA, RootDispSHA: e.RootDispositionSHA}
}

type nativeReceiptRow struct {
	ID                 uuid.UUID `db:"id"`
	EnrollmentID       uuid.UUID `db:"enrollment_id"`
	AuthorityID        uuid.UUID `db:"authority_id"`
	IssuerActorID      uuid.UUID `db:"issuer_actor_id"`
	Verdict            string    `db:"verdict"`
	ReceiverTask       uuid.UUID `db:"receiver_task"`
	ReceiverAttempt    uuid.UUID `db:"receiver_attempt"`
	ReceiverSession    uuid.UUID `db:"receiver_session"`
	ReceiverGeneration int64     `db:"receiver_generation"`
	NativeRegistration string    `db:"native_registration"`
	BodyBytes          []byte    `db:"body_bytes"`
	BodySHA            string    `db:"body_sha256"`
	Canonical          []byte    `db:"canonical"`
	ReceiptSHA         string    `db:"receipt_sha256"`
	IssuerSignature    string    `db:"issuer_signature"`
	CreatedAt          time.Time `db:"created_at"`
}

const nativeReceiptCols = `id, enrollment_id, authority_id, issuer_actor_id, verdict, receiver_task, receiver_attempt, receiver_session,
 receiver_generation, native_registration, body_bytes, body_sha256, canonical, receipt_sha256, issuer_signature, created_at`

type nativeAuthorityRow struct {
	ID                 uuid.UUID `db:"id"`
	EnrollmentID       uuid.UUID `db:"enrollment_id"`
	ReceiptID          uuid.UUID `db:"receipt_id"`
	CreatedBy          uuid.UUID `db:"created_by"`
	BudgetID           uuid.UUID `db:"budget_id"`
	NotBeforeNS        string    `db:"not_before_ns"`
	ExpiresNS          string    `db:"expires_ns"`
	ClockBindingSHA    string    `db:"clock_binding_sha256"`
	ReceiverTask       uuid.UUID `db:"receiver_task"`
	ReceiverAttempt    uuid.UUID `db:"receiver_attempt"`
	ReceiverSession    uuid.UUID `db:"receiver_session"`
	ReceiverGeneration int64     `db:"receiver_generation"`
	Revision           int64     `db:"revision"`
}

func refuseN(code, msg string) error { return domain.NativeRefuse(code, msg) }

func (r *NativeReviewRepo) begin(ctx context.Context) (*sqlx.Tx, error) {
	return r.db.BeginTxx(ctx, nil)
}

func (r *NativeReviewRepo) hook(op string) {
	if r.hooks.afterLocks != nil {
		r.hooks.afterLocks(op)
	}
}

const nativeEnrollmentCols = `id, workspace_id, project_id, issuer_actor_id, publisher_actor_id, transport_source_id, consumer_actor_id, authority_issuer_actor_id, result_author_actor_id, owner_actor_id, authority_id, issuer_key, receiver_task, receiver_attempt, receiver_session, receiver_generation, native_registration, native_stage, native_package_sha256, review_task_id, source_task_id, reviewer_family, author_family, scope_domain, scope_namespace, scope_test, criteria_sha256, context_sha256, source_repo, source_sha, package_sha256, manifest_sha256, config_sha256, product_task_id, product_observation_id, product_session_id, authority_bytes, root_disposition_bytes, authority_sha256, root_disposition_sha256, created_by, created_at`

// lockEnrollment takes the FIRST lock of the fixed order. shared=true for
// everything except enrollment revocation itself.
func lockEnrollment(ctx context.Context, tx *sqlx.Tx, id uuid.UUID, shared bool) (*NativeEnrollment, error) {
	mode := " FOR UPDATE"
	if shared {
		mode = " FOR SHARE"
	}
	var e NativeEnrollment
	if err := tx.GetContext(ctx, &e, `SELECT `+nativeEnrollmentCols+` FROM native_review_enrollments WHERE id=$1`+mode, id); errors.Is(err, sql.ErrNoRows) {
		return nil, refuseN("enrollment_not_found", "no such enrollment")
	} else if err != nil {
		return nil, err
	}
	return &e, nil
}

func enrollmentLive(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) error {
	var revoked bool
	if err := tx.GetContext(ctx, &revoked, `SELECT EXISTS(SELECT 1 FROM native_review_enrollment_revocations WHERE enrollment_id=$1)`, id); err != nil {
		return err
	}
	if revoked {
		return refuseN("enrollment_revoked", "the enrollment was revoked")
	}
	return nil
}

// lockAuthority takes the SECOND lock (enrollment is already held).
func lockAuthority(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) (*nativeAuthorityRow, error) {
	var a nativeAuthorityRow
	err := tx.GetContext(ctx, &a, `SELECT id, enrollment_id, receipt_id, created_by, budget_id, not_before_ns, expires_ns, clock_binding_sha256,
 receiver_task, receiver_attempt, receiver_session, receiver_generation, revision FROM native_review_authorities WHERE id=$1 FOR UPDATE`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuseN("authority_not_found", "no such authority")
	}
	return &a, err
}

// authorityEnrollmentID reads the immutable enrollment link without a lock so the
// fixed lock order enrollment -> authority can be followed.
func authorityEnrollmentID(ctx context.Context, tx *sqlx.Tx, authority uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.GetContext(ctx, &id, `SELECT enrollment_id FROM native_review_authorities WHERE id=$1`, authority)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, refuseN("authority_not_found", "no such authority")
	}
	return id, err
}

// actorsExist is a fresh read of actor existence in the workspace. Writers of
// agents/grants outside this protocol do not take the guard locks: that gap is a
// documented release gate, not a claimed guarantee.
func actorsExist(ctx context.Context, tx *sqlx.Tx, workspace uuid.UUID, ids ...uuid.UUID) error {
	for _, id := range ids {
		var ok bool
		if err := tx.GetContext(ctx, &ok, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1 AND workspace_id=$2)`, id, workspace); err != nil {
			return err
		}
		if !ok {
			return refuseN("actor_not_current", "an enrolled identity is no longer a current member of the workspace")
		}
	}
	return nil
}

// CreateEnrollment records a trusted enrollment. The caller is an operator; the
// database rejects non-independent families/actors and digest mismatches.
func (r *NativeReviewRepo) CreateEnrollment(ctx context.Context, e *NativeEnrollment, actor uuid.UUID) (*NativeEnrollment, error) {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	e.CreatedBy = actor
	e.ScopeDomain = domain.NativeReviewScopeDomain
	if e.AuthorityID == uuid.Nil {
		return nil, refuseN("enrollment_invalid", "authority_id must be reserved by the operator before any receipt")
	}
	if _, err := domain.ParseIssuerKey(e.IssuerKey); err != nil {
		return nil, err
	}
	if !domain.ValidFamily(e.ReviewerFamily) || !domain.ValidFamily(e.AuthorFamily) || !domain.ValidRepo(e.SourceRepo) || !domain.ValidStage(e.NativeStage) {
		return nil, refuseN("enrollment_invalid", "family, repository or stage is not a closed token")
	}
	if derived, derr := domain.DeriveNativeRegistration(e.link().Binding, e.receiver()); derr != nil || e.NativeRegistration != derived {
		return nil, refuseN("enrollment_invalid", "native_registration must be the identity derived from the pinned binding and receiver")
	}
	if e.TransportSourceID == e.IssuerActorID {
		return nil, refuseN("enrollment_invalid", "transport source must differ from the issuer")
	}
	_, err := r.db.NamedExecContext(ctx, `INSERT INTO native_review_enrollments(id, workspace_id, project_id, issuer_actor_id, publisher_actor_id,
 transport_source_id, consumer_actor_id, authority_issuer_actor_id, result_author_actor_id, owner_actor_id, authority_id, issuer_key,
 receiver_task, receiver_attempt, receiver_session, receiver_generation, native_registration, native_stage, native_package_sha256,
 review_task_id, source_task_id,
 reviewer_family, author_family, scope_domain, scope_namespace, scope_test, criteria_sha256, context_sha256, source_repo, source_sha,
 package_sha256, manifest_sha256, config_sha256, product_task_id, product_observation_id, product_session_id, authority_bytes,
 root_disposition_bytes, authority_sha256, root_disposition_sha256, created_by)
 VALUES(:id,:workspace_id,:project_id,:issuer_actor_id,:publisher_actor_id,:transport_source_id,:consumer_actor_id,:authority_issuer_actor_id,
 :result_author_actor_id,:owner_actor_id,:authority_id,:issuer_key,:receiver_task,:receiver_attempt,:receiver_session,:receiver_generation,
 :native_registration,:native_stage,:native_package_sha256,:review_task_id,:source_task_id,:reviewer_family,:author_family,:scope_domain,:scope_namespace,:scope_test,
 :criteria_sha256,:context_sha256,:source_repo,:source_sha,:package_sha256,:manifest_sha256,:config_sha256,:product_task_id,:product_observation_id,
 :product_session_id,:authority_bytes,:root_disposition_bytes,:authority_sha256,:root_disposition_sha256,:created_by)`, e)
	if err != nil {
		return nil, refuseN("enrollment_invalid", err.Error())
	}
	return e, nil
}

// RevokeEnrollment takes the enrollment lock exclusively: it conflicts with every
// reserve/issue/revoke/turnover that holds it shared.
func (r *NativeReviewRepo) RevokeEnrollment(ctx context.Context, id, actor uuid.UUID, reason string) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	e, err := lockEnrollment(ctx, tx, id, false)
	if err != nil {
		return err
	}
	r.hook("revoke-enrollment")
	if actor != e.CreatedBy && actor != e.AuthorityIssuerActorID {
		return refuseN("not_authorized", "only the enrolling operator or the authority issuer may revoke")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_review_enrollment_revocations(enrollment_id, revoked_by, reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, actor, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// IssueReceipt stores the immutable signed receipt. Issuer is the authenticated
// actor and must be the enrolled issuer; every other fact (including the
// operator-reserved authority_id, the receiver and the native binding) comes from
// the enrollment and the request must restate it exactly. The signature is made
// by the configured trusted signer and must verify under the ENROLLED issuer key
// before anything is written; no signer means refusal, never an unsigned receipt.
func (r *NativeReviewRepo) IssueReceipt(ctx context.Context, actor uuid.UUID, req domain.NativeIssueRequest) (*domain.NativePacket, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	e, err := lockEnrollment(ctx, tx, req.EnrollmentID, true)
	if err != nil {
		return nil, err
	}
	r.hook("issue")
	err = enrollmentLive(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	switch {
	case actor != e.IssuerActorID:
		return nil, refuseN("issuer_not_enrolled", "the authenticated credential is not the enrolled issuer")
	case actor == e.ResultAuthorActorID || e.ReviewerFamily == e.AuthorFamily:
		return nil, refuseN("not_independent", "reviewer is not independent of the author")
	case req.ReviewTaskID != e.ReviewTaskID || req.SourceTaskID != e.SourceTaskID:
		return nil, refuseN("task_binding_mismatch", "review/source task differ from the enrollment")
	case req.Scope != e.scope():
		return nil, refuseN("scope_mismatch", "scope differs from the enrollment")
	case req.Pins != e.pins():
		return nil, refuseN("pins_mismatch", "pins differ from the enrollment")
	case req.Product != e.product():
		return nil, refuseN("product_mismatch", "product binding differs from the enrolled original authority")
	case req.Receiver != e.receiver():
		return nil, refuseN("receiver_mismatch", "receiver differs from the enrolled receiver")
	case req.Native != e.link():
		return nil, refuseN("native_mismatch", "native registration/binding differs from the enrollment")
	}
	if r.signer == nil {
		return nil, refuseN("signer_unavailable", "no trusted issuer signer is configured; receipts are not issued")
	}
	err = actorsExist(ctx, tx, e.WorkspaceID, e.IssuerActorID, e.PublisherActorID)
	if err != nil {
		return nil, err
	}
	id, now := uuid.New(), time.Now()
	if r.hooks.newID != nil {
		id = r.hooks.newID()
	}
	if r.hooks.now != nil {
		now = r.hooks.now()
	}
	pk := domain.NativePacket{NativeReceipt: domain.NativeReceipt{
		Schema: domain.NativeReviewSchema, ReceiptID: id, EnrollmentID: e.ID, AuthorityID: e.AuthorityID, Product: e.product(), IssuerActorID: actor,
		PublisherID: e.PublisherActorID, TransportSource: e.TransportSourceID, ResultAuthorID: e.ResultAuthorActorID,
		ReviewTaskID: e.ReviewTaskID, SourceTaskID: e.SourceTaskID, WorkspaceID: e.WorkspaceID, ProjectID: e.ProjectID,
		OwnerActorID: e.OwnerActorID, ReviewerFamily: e.ReviewerFamily, AuthorFamily: e.AuthorFamily, Verdict: req.Verdict,
		Scope: e.scope(), Pins: e.pins(), Receiver: e.receiver(), Native: e.link(), Body: req.Body,
	}}
	canonical, sum, err := pk.NativeReceipt.Canonical()
	if err != nil {
		return nil, err
	}
	sig, err := r.signer.Sign(ctx, domain.NativeSigningMessage(sum))
	if err != nil {
		return nil, refuseN("signer_unavailable", "the issuer signer failed; receipt not issued")
	}
	pk.ReceiptSHA, pk.IssuerSignature = sum, base64.StdEncoding.EncodeToString(sig)
	err = domain.VerifyIssuerSignature(e.IssuerKey, sum, pk.IssuerSignature)
	if err != nil {
		return nil, err
	}
	pk.CreatedAt = domain.FormatNativeCreatedAt(now)
	body, _ := req.Body.DecodedBytes()
	nb, _ := json.Marshal(e.link().Binding)
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_review_receipts(id, enrollment_id, authority_id, issuer_actor_id, verdict, receiver_task, receiver_attempt,
 receiver_session, receiver_generation, native_registration, native_binding, body_bytes, body_sha256, canonical, receipt_sha256, issuer_signature, created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$14,$15,$16,$17)`, pk.ReceiptID, e.ID, e.AuthorityID, actor, pk.Verdict, pk.Receiver.Task,
		pk.Receiver.Attempt, pk.Receiver.Session, pk.Receiver.Generation, pk.Native.Registration, string(nb), body, req.Body.SHA256, canonical, sum,
		pk.IssuerSignature, now.UTC().Truncate(time.Second)); err != nil {
		var pg *pq.Error
		if errors.As(err, &pg) && pg.Code == "23505" {
			return nil, refuseN("receipt_conflict", "the enrollment already has its one receipt")
		}
		return nil, err
	}
	return &pk, tx.Commit()
}

// ReadReceipt returns the stored signed packet exactly as issued, revalidating
// every stored digest and the issuer signature under the enrolled key. Readback
// is never permission, and it is not public: only the actors named by the
// trusted enrollment (issuer, publisher, consumer, authority issuer) or the
// enrolling operator may read. Anyone else gets the same refusal as for a
// missing receipt.
func (r *NativeReviewRepo) ReadReceipt(ctx context.Context, actor, id uuid.UUID) (*domain.NativePacket, error) {
	var row nativeReceiptRow
	err := r.db.GetContext(ctx, &row, `SELECT `+nativeReceiptCols+` FROM native_review_receipts WHERE id=$1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuseN("receipt_not_found", "no such receipt")
	}
	if err != nil {
		return nil, err
	}
	var enr struct {
		Allowed bool   `db:"allowed"`
		Key     string `db:"issuer_key"`
	}
	err = r.db.GetContext(ctx, &enr, `SELECT $2 IN (issuer_actor_id, publisher_actor_id, consumer_actor_id, authority_issuer_actor_id, created_by) AS allowed, issuer_key
 FROM native_review_enrollments WHERE id=$1`, row.EnrollmentID, actor)
	if err != nil {
		return nil, err
	}
	if !enr.Allowed {
		return nil, refuseN("receipt_not_found", "no such receipt")
	}
	if domain.NativeDigest(row.BodyBytes) != row.BodySHA || domain.NativeDigest(row.Canonical) != row.ReceiptSHA {
		return nil, refuseN("receipt_corrupt", "stored receipt does not match its digests")
	}
	var rc domain.NativeReceipt
	if err = domain.StrictDecodeNative(row.Canonical, &rc); err != nil {
		return nil, refuseN("receipt_corrupt", "stored receipt is not a closed receipt")
	}
	if err = rc.Validate(); err != nil {
		return nil, refuseN("receipt_corrupt", "stored receipt fails closed validation: "+err.Error())
	}
	if again, _, cerr := rc.Canonical(); cerr != nil || !bytes.Equal(again, row.Canonical) || rc.ReceiptID != row.ID || rc.AuthorityID != row.AuthorityID {
		return nil, refuseN("receipt_corrupt", "stored receipt is not canonical")
	}
	pk := &domain.NativePacket{NativeReceipt: rc, ReceiptSHA: row.ReceiptSHA, CreatedAt: domain.FormatNativeCreatedAt(row.CreatedAt), IssuerSignature: row.IssuerSignature}
	if err = pk.Verify(enr.Key); err != nil {
		return nil, refuseN("receipt_corrupt", "stored packet does not verify: "+err.Error())
	}
	return pk, nil
}

// CreateAuthority creates the mutable guard bound to one SHIP receipt.
func (r *NativeReviewRepo) CreateAuthority(ctx context.Context, actor uuid.UUID, req domain.NativeAuthorityRequest) (uuid.UUID, error) {
	if err := req.Validate(); err != nil {
		return uuid.Nil, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	e, err := lockEnrollment(ctx, tx, req.EnrollmentID, true)
	if err != nil {
		return uuid.Nil, err
	}
	err = enrollmentLive(ctx, tx, e.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if actor != e.AuthorityIssuerActorID {
		return uuid.Nil, refuseN("authority_issuer_not_enrolled", "the credential is not the enrolled authority issuer")
	}
	if req.AuthorityID != e.AuthorityID {
		return uuid.Nil, refuseN("authority_id_mismatch", "authority_id must be the one the operator reserved in the enrollment")
	}
	var rc nativeReceiptRow
	if err = tx.GetContext(ctx, &rc, `SELECT `+nativeReceiptCols+` FROM native_review_receipts WHERE id=$1`, req.ReceiptID); errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, refuseN("receipt_not_found", "no such receipt")
	} else if err != nil {
		return uuid.Nil, err
	}
	if rc.EnrollmentID != e.ID || rc.Verdict != domain.NativeVerdictShip {
		return uuid.Nil, refuseN("receipt_not_admissible", "receipt belongs to another enrollment or is not SHIP")
	}
	if rc.AuthorityID != e.AuthorityID {
		return uuid.Nil, refuseN("authority_id_mismatch", "the receipt does not carry the reserved authority_id; no rebind")
	}
	id := e.AuthorityID
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_review_authorities(id, enrollment_id, receipt_id, created_by, budget_id, budget_ms, max_reservations,
 not_before_ns, expires_ns, clock_binding_sha256, receiver_task, receiver_attempt, receiver_session, receiver_generation)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, id, e.ID, rc.ID, actor, req.BudgetID, req.BudgetMS, req.MaxReservations,
		req.NotBeforeNS, req.ExpiresNS, req.ClockBinding, rc.ReceiverTask, rc.ReceiverAttempt, rc.ReceiverSession, rc.ReceiverGeneration); err != nil {
		return uuid.Nil, refuseN("authority_conflict", err.Error())
	}
	return id, tx.Commit()
}

// RecordDelivery appends the receipt<->native event link. Only the enrolled
// publisher may record it, and only for the receipt's own registration.
func (r *NativeReviewRepo) RecordDelivery(ctx context.Context, actor uuid.UUID, registration, eventID string, receiptID uuid.UUID) error {
	if registration == "" || eventID == "" || receiptID == uuid.Nil {
		return refuseN("binding_missing", "registration, event and receipt required")
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var rc struct {
		Enrollment uuid.UUID `db:"enrollment_id"`
		Reg        string    `db:"native_registration"`
	}
	if err = tx.GetContext(ctx, &rc, `SELECT enrollment_id, native_registration FROM native_review_receipts WHERE id=$1`, receiptID); errors.Is(err, sql.ErrNoRows) {
		return refuseN("receipt_not_found", "no such receipt")
	} else if err != nil {
		return err
	}
	e, err := lockEnrollment(ctx, tx, rc.Enrollment, true)
	if err != nil {
		return err
	}
	err = enrollmentLive(ctx, tx, e.ID)
	if err != nil {
		return err
	}
	if actor != e.PublisherActorID {
		return refuseN("publisher_not_enrolled", "the credential is not the enrolled publisher")
	}
	if rc.Reg != registration {
		return refuseN("registration_mismatch", "registration differs from the receipt's native registration")
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO native_review_delivery_links(registration, event_id, receipt_id, recorded_by) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		registration, eventID, receiptID, actor)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var same bool
		err = tx.GetContext(ctx, &same, `SELECT receipt_id=$3 FROM native_review_delivery_links WHERE registration=$1 AND event_id=$2`, registration, eventID, receiptID)
		if err != nil {
			return err
		}
		if !same {
			return refuseN("delivery_conflict", "the native event is already linked to another receipt")
		}
	}
	return tx.Commit()
}

// RevokeAuthority appends a revocation under the fixed lock order.
func (r *NativeReviewRepo) RevokeAuthority(ctx context.Context, authorityID, actor uuid.UUID, reason string) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	eid, err := authorityEnrollmentID(ctx, tx, authorityID)
	if err != nil {
		return err
	}
	e, err := lockEnrollment(ctx, tx, eid, true)
	if err != nil {
		return err
	}
	if _, err = lockAuthority(ctx, tx, authorityID); err != nil {
		return err
	}
	r.hook("revoke-authority")
	if actor != e.AuthorityIssuerActorID && actor != e.CreatedBy {
		return refuseN("not_authorized", "only the authority issuer or enrolling operator may revoke")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_review_authority_revocations(authority_id, revoked_by, reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, authorityID, actor, reason); err != nil {
		return err
	}
	return tx.Commit()
}

// TurnoverReceiver replaces the current receiver under the guard locks. The old
// receiver's receipt no longer matches, so it cannot reserve; a new receipt for
// the new receiver is required. Never refunds a consumed reservation.
func (r *NativeReviewRepo) TurnoverReceiver(ctx context.Context, authorityID, actor uuid.UUID, next domain.NativeReceiver) error {
	if next.Task == uuid.Nil || next.Attempt == uuid.Nil || next.Session == uuid.Nil || next.Generation <= 0 {
		return refuseN("receiver_invalid", "receiver task/attempt/session/generation required")
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	eid, err := authorityEnrollmentID(ctx, tx, authorityID)
	if err != nil {
		return err
	}
	e, err := lockEnrollment(ctx, tx, eid, true)
	if err != nil {
		return err
	}
	a, err := lockAuthority(ctx, tx, authorityID)
	if err != nil {
		return err
	}
	r.hook("turnover")
	if actor != e.AuthorityIssuerActorID {
		return refuseN("not_authorized", "only the authority issuer may replace the receiver")
	}
	if next.Task != a.ReceiverTask || next.Generation <= a.ReceiverGeneration {
		return refuseN("receiver_not_newer", "a replacement keeps the task and raises the generation")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_review_authorities SET receiver_task=$2, receiver_attempt=$3, receiver_session=$4, receiver_generation=$5, revision=revision+1 WHERE id=$1`,
		authorityID, next.Task, next.Attempt, next.Session, next.Generation); err != nil {
		return err
	}
	return tx.Commit()
}

type nativeReservationRow struct {
	ID           uuid.UUID `db:"id"`
	AuthorityID  uuid.UUID `db:"authority_id"`
	ReceiptID    uuid.UUID `db:"receipt_id"`
	ReceiptSHA   string    `db:"receipt_sha256"`
	RequestID    uuid.UUID `db:"request_id"`
	Digest       string    `db:"payload_digest"`
	Registration string    `db:"native_registration"`
	EventID      string    `db:"native_event_id"`
	Consumer     uuid.UUID `db:"consumer_actor_id"`
	RTask        uuid.UUID `db:"receiver_task"`
	RAttempt     uuid.UUID `db:"receiver_attempt"`
	RSession     uuid.UUID `db:"receiver_session"`
	RGen         int64     `db:"receiver_generation"`
	CommittedAt  time.Time `db:"committed_at"`
	MarkerID     uuid.UUID `db:"marker_id"`
}

const nativeReservationSelect = `SELECT r.id, r.authority_id, r.receipt_id, r.receipt_sha256, r.request_id, r.payload_digest, r.native_registration,
 r.native_event_id, r.consumer_actor_id, r.receiver_task, r.receiver_attempt, r.receiver_session, r.receiver_generation, r.committed_at, m.marker_id
 FROM native_review_reservations r JOIN native_review_markers m ON m.reservation_id=r.id`

func (row *nativeReservationRow) view() *domain.NativeReservation {
	return &domain.NativeReservation{ReservationID: row.ID, AuthorityID: row.AuthorityID, ReceiptID: row.ReceiptID, ReceiptSHA: row.ReceiptSHA,
		RequestID: row.RequestID, Receiver: domain.NativeReceiver{Task: row.RTask, Attempt: row.RAttempt, Session: row.RSession, Generation: row.RGen},
		Registration: row.Registration, EventID: row.EventID, CommittedAt: row.CommittedAt.UTC(), MarkerID: row.MarkerID, Consumed: true, ExecuteAllowed: false}
}

// Reserve is the atomic check-and-reserve. Lock order is fixed: enrollment
// (shared), then authority (exclusive). Every authority/revocation/window/binding
// read happens AFTER both locks; the linearization point is the successful
// commit of reservation + marker. A lost response or uncertain commit is
// consumed: read back with GetReservation, never retry execution.
func (r *NativeReviewRepo) Reserve(ctx context.Context, actor uuid.UUID, req domain.NativeReserveRequest) (*domain.NativeReservation, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	digest, err := req.Digest()
	if err != nil {
		return nil, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	eid, err := authorityEnrollmentID(ctx, tx, req.AuthorityID)
	if err != nil {
		return nil, err
	}
	e, err := lockEnrollment(ctx, tx, eid, true)
	if err != nil {
		return nil, err
	}
	a, err := lockAuthority(ctx, tx, req.AuthorityID)
	if err != nil {
		return nil, err
	}
	r.hook("reserve")

	if actor != e.ConsumerActorID {
		return nil, refuseN("consumer_not_enrolled", "the authenticated credential is not the enrolled consumer")
	}
	// Replay / once-budget: a committed reservation answers before any
	// freshness check. It is readback, never a second permission.
	var prior nativeReservationRow
	err = tx.GetContext(ctx, &prior, nativeReservationSelect+` WHERE r.authority_id=$1 OR r.request_id=$2 ORDER BY (r.authority_id=$1) DESC LIMIT 1`, req.AuthorityID, req.RequestID)
	switch {
	case err == nil:
		if prior.RequestID != req.RequestID {
			return nil, refuseN("authority_consumed", "the once-budget of this authority is already consumed")
		}
		if prior.AuthorityID != req.AuthorityID || prior.Digest != digest {
			return nil, refuseN("idempotency_payload_mismatch", "request_id was already used with a different payload")
		}
		return prior.view(), nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}

	err = enrollmentLive(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	var revoked bool
	err = tx.GetContext(ctx, &revoked, `SELECT EXISTS(SELECT 1 FROM native_review_authority_revocations WHERE authority_id=$1)`, a.ID)
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, refuseN("authority_revoked", "the execution authority was revoked")
	}

	var rc nativeReceiptRow
	err = tx.GetContext(ctx, &rc, `SELECT id, enrollment_id, issuer_actor_id, verdict, receiver_task, receiver_attempt, receiver_session,
 receiver_generation, native_registration, body_bytes, body_sha256, canonical, receipt_sha256 FROM native_review_receipts WHERE id=$1`, a.ReceiptID)
	if err != nil {
		return nil, err
	}
	current := domain.NativeReceiver{Task: a.ReceiverTask, Attempt: a.ReceiverAttempt, Session: a.ReceiverSession, Generation: a.ReceiverGeneration}
	pinned := domain.NativeReceiver{Task: rc.ReceiverTask, Attempt: rc.ReceiverAttempt, Session: rc.ReceiverSession, Generation: rc.ReceiverGeneration}
	switch {
	case req.ReceiptID != rc.ID || req.ReceiptSHA != rc.ReceiptSHA:
		return nil, refuseN("receipt_mismatch", "receipt id/digest differ from the authority's receipt")
	case rc.Verdict != domain.NativeVerdictShip:
		return nil, refuseN("verdict_not_ship", "only SHIP admits")
	case domain.NativeDigest(rc.BodyBytes) != rc.BodySHA || domain.NativeDigest(rc.Canonical) != rc.ReceiptSHA:
		return nil, refuseN("receipt_corrupt", "stored receipt does not match its digests")
	case rc.EnrollmentID != e.ID || rc.IssuerActorID != e.IssuerActorID:
		return nil, refuseN("issuer_mismatch", "receipt issuer differs from the enrollment")
	case req.Scope != e.scope():
		return nil, refuseN("scope_mismatch", "scope differs from the enrollment")
	case req.Pins != e.pins():
		return nil, refuseN("pins_mismatch", "pins differ from the enrollment")
	case req.Receiver != current || current != pinned:
		return nil, refuseN("receiver_mismatch", "receiver is not the current guarded receiver of this receipt")
	case req.Registration != rc.NativeRegistration:
		return nil, refuseN("registration_mismatch", "native registration differs from the receipt")
	case e.IssuerActorID == e.ResultAuthorActorID || e.ReviewerFamily == e.AuthorFamily || e.ConsumerActorID == e.IssuerActorID:
		return nil, refuseN("not_independent", "independence no longer holds")
	}
	var linked bool
	err = tx.GetContext(ctx, &linked, `SELECT EXISTS(SELECT 1 FROM native_review_delivery_links WHERE registration=$1 AND event_id=$2 AND receipt_id=$3)`,
		req.Registration, req.EventID, rc.ID)
	if err != nil {
		return nil, err
	}
	if !linked {
		return nil, refuseN("delivery_not_linked", "no persisted native event links this receipt")
	}
	err = actorsExist(ctx, tx, e.WorkspaceID, e.IssuerActorID, e.PublisherActorID, e.ConsumerActorID)
	if err != nil {
		return nil, err
	}

	if r.clock == nil {
		return nil, refuseN("clock_proof_unavailable", "no proven clock protocol is wired; reserve is denied by default")
	}
	ev, err := r.clock.Evidence(ctx, a.ID, a.ClockBindingSHA)
	if err != nil {
		return nil, refuseN("clock_proof_unavailable", "clock evidence unavailable: "+err.Error())
	}
	err = ev.CheckAdmission(a.NotBeforeNS, a.ExpiresNS, a.ClockBindingSHA)
	if err != nil {
		return nil, err
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}

	rid, mid := uuid.New(), uuid.New()
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_review_reservations(id, authority_id, budget_id, receipt_id, receipt_sha256, request_id, payload_digest,
 native_registration, native_event_id, consumer_actor_id, receiver_task, receiver_attempt, receiver_session, receiver_generation, clock_evidence)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb)`, rid, a.ID, a.BudgetID, rc.ID, rc.ReceiptSHA, req.RequestID, digest,
		req.Registration, req.EventID, actor, current.Task, current.Attempt, current.Session, current.Generation, string(evJSON)); err != nil {
		return nil, mapNativeUnique(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_review_markers(marker_id, reservation_id, authority_id) VALUES($1,$2,$3)`, mid, rid, a.ID); err != nil {
		return nil, mapNativeUnique(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, refuseN("commit_uncertain", "commit outcome unknown: treat as consumed and read back; do not retry execution")
	}
	var out nativeReservationRow
	if err = r.db.GetContext(ctx, &out, nativeReservationSelect+` WHERE r.id=$1`, rid); err != nil {
		return nil, refuseN("commit_uncertain", "committed but readback failed: treat as consumed")
	}
	return out.view(), nil
}

// GetReservation is read-only recovery for a lost response: it proves the fact,
// it is never permission to execute (ExecuteAllowed is always false).
func (r *NativeReviewRepo) GetReservation(ctx context.Context, actor, authorityID uuid.UUID) (*domain.NativeReservation, error) {
	var row nativeReservationRow
	err := r.db.GetContext(ctx, &row, nativeReservationSelect+` WHERE r.authority_id=$1`, authorityID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuseN("reservation_not_found", "no reservation committed for this authority")
	}
	if err != nil {
		return nil, err
	}
	if row.Consumer != actor {
		return nil, refuseN("consumer_not_enrolled", "the credential is not the consumer that reserved")
	}
	return row.view(), nil
}

func mapNativeUnique(err error) error {
	var pg *pq.Error
	if errors.As(err, &pg) && pg.Code == "23505" {
		return refuseN("authority_consumed", "reservation uniqueness refused a second reservation")
	}
	return err
}
