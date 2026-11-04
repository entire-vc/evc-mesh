package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Lifecycle writers (revoke / turnover / readback) refuse every actor that is not
// enrolled for the action and succeed for the one that is: each refusal sits next
// to its positive control, and no refusal changes the single-marker outcome.
func TestNativeReviewLifecycleAuthorization(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	c := f.chain()
	stranger := uuid.New()

	// readback / reservation of things that do not exist
	_, err := f.repo.ReadReceipt(ctx, f.issuer, uuid.New())
	requireRefusal(t, err, "receipt_not_found")
	_, err = f.repo.GetReservation(ctx, f.consumer, c.authority)
	requireRefusal(t, err, "reservation_not_found")

	// turnover: invalid receiver, unknown authority, wrong actor, not newer, positive
	requireRefusal(t, f.repo.TurnoverReceiver(ctx, c.authority, f.authIssuer, domain.NativeReceiver{}), "receiver_invalid")
	next := f.receiver()
	next.Generation++
	requireRefusal(t, f.repo.TurnoverReceiver(ctx, uuid.New(), f.authIssuer, next), "authority_not_found")
	requireRefusal(t, f.repo.TurnoverReceiver(ctx, c.authority, stranger, next), "not_authorized")
	requireRefusal(t, f.repo.TurnoverReceiver(ctx, c.authority, f.authIssuer, f.receiver()), "receiver_not_newer")
	other := next
	other.Task = uuid.New()
	requireRefusal(t, f.repo.TurnoverReceiver(ctx, c.authority, f.authIssuer, other), "receiver_not_newer")
	require.NoError(t, f.repo.TurnoverReceiver(ctx, c.authority, f.authIssuer, next))
	require.Equal(t, 2, f.n(`SELECT revision FROM native_review_authorities WHERE id=$1`, c.authority))

	// the old receiver can no longer reserve (receiver changed under the guard)
	_, err = f.repo.Reserve(ctx, f.consumer, f.reserveReq(c))
	require.Error(t, err)
	r, m := f.counts(c.authority)
	require.Zero(t, r+m)

	// authority revocation: unknown, stranger, positive (idempotent), then reserve refused
	requireRefusal(t, f.repo.RevokeAuthority(ctx, uuid.New(), f.authIssuer, "x"), "authority_not_found")
	requireRefusal(t, f.repo.RevokeAuthority(ctx, c.authority, stranger, "x"), "not_authorized")
	require.NoError(t, f.repo.RevokeAuthority(ctx, c.authority, f.authIssuer, "done"))
	require.NoError(t, f.repo.RevokeAuthority(ctx, c.authority, f.operator, "again"))
	_, err = f.repo.Reserve(ctx, f.consumer, f.reserveReq(c, func(q *domain.NativeReserveRequest) { q.Receiver = next }))
	require.Error(t, err)
	r, m = f.counts(c.authority)
	require.Zero(t, r+m, "revoked authority never reserves")
}

func TestNativeReviewEnrollmentRevocationBlocksEverything(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	c := f.chain()

	requireRefusal(t, f.repo.RevokeEnrollment(ctx, uuid.New(), f.operator, "x"), "enrollment_not_found")
	requireRefusal(t, f.repo.RevokeEnrollment(ctx, f.enr.ID, uuid.New(), "x"), "not_authorized")
	require.NoError(t, f.repo.RevokeEnrollment(ctx, f.enr.ID, f.authIssuer, "gone"))
	require.NoError(t, f.repo.RevokeEnrollment(ctx, f.enr.ID, f.operator, "again"))

	_, err := f.repo.Reserve(ctx, f.consumer, f.reserveReq(c))
	requireRefusal(t, err, "enrollment_revoked")
	_, err = f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	require.Error(t, err)
	// revoking is always allowed: it can only reduce authority
	require.NoError(t, f.repo.RevokeAuthority(ctx, c.authority, f.authIssuer, "x"))
	// turnover cannot grant anything either: the reserve above stays refused
	require.NoError(t, f.repo.TurnoverReceiver(ctx, c.authority, f.authIssuer, domain.NativeReceiver{
		Task: f.receiver().Task, Attempt: uuid.New(), Session: uuid.New(), Generation: f.receiver().Generation + 1}))
	requireRefusal(t, f.repo.RecordDelivery(ctx, f.publisher, f.registration, "ev-late", c.receipt.ReceiptID), "enrollment_revoked")
	r, m := f.counts(c.authority)
	require.Zero(t, r+m)
}

func TestNativeReviewReserveRefusesDepartedActor(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	c := f.chain()
	// positive control first: the consumer is a member now
	f.exec(`DELETE FROM agents WHERE id=$1`, f.consumer)
	_, err := f.repo.Reserve(ctx, f.consumer, f.reserveReq(c))
	requireRefusal(t, err, "actor_not_current")
	r, m := f.counts(c.authority)
	require.Zero(t, r+m, "a departed identity never reserves")
}

func TestNativeReviewCreateAuthorityAndDeliveryGuards(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	rc, err := f.repo.IssueReceipt(ctx, f.issuer, f.issueReq(domain.NativeVerdictShip))
	require.NoError(t, err)
	base := domain.NativeAuthorityRequest{Schema: domain.NativeReviewSchema, EnrollmentID: f.enr.ID, AuthorityID: f.enr.AuthorityID, ReceiptID: rc.ReceiptID,
		BudgetID: uuid.New(), BudgetMS: 1000, MaxReservations: 1, NotBeforeNS: "1000000000000", ExpiresNS: "1030000000000", ClockBinding: nrSHA("c")}

	_, err = f.repo.CreateAuthority(ctx, f.authIssuer, func() domain.NativeAuthorityRequest { q := base; q.EnrollmentID = uuid.New(); return q }())
	requireRefusal(t, err, "enrollment_not_found")
	_, err = f.repo.CreateAuthority(ctx, uuid.New(), base)
	requireRefusal(t, err, "authority_issuer_not_enrolled")
	_, err = f.repo.CreateAuthority(ctx, f.authIssuer, func() domain.NativeAuthorityRequest { q := base; q.AuthorityID = uuid.New(); return q }())
	requireRefusal(t, err, "authority_id_mismatch")
	_, err = f.repo.CreateAuthority(ctx, f.authIssuer, func() domain.NativeAuthorityRequest { q := base; q.ReceiptID = uuid.New(); return q }())
	requireRefusal(t, err, "receipt_not_found")
	id, err := f.repo.CreateAuthority(ctx, f.authIssuer, base)
	require.NoError(t, err)
	require.Equal(t, f.enr.AuthorityID, id)
	// a second authority for the same reserved id is refused
	again := base
	again.BudgetID = uuid.New()
	_, err = f.repo.CreateAuthority(ctx, f.authIssuer, again)
	require.Error(t, err)

	requireRefusal(t, f.repo.RecordDelivery(ctx, f.publisher, f.registration, "ev-1", uuid.New()), "receipt_not_found")
	requireRefusal(t, f.repo.RecordDelivery(ctx, f.publisher, f.registration, "", rc.ReceiptID), "binding_missing")
}

func TestNativeReviewEnrollmentValidation(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	mk := func(mut func(e *NativeEnrollment)) error {
		e := *f.enr
		e.ID = uuid.New()
		e.AuthorityID = uuid.New()
		mut(&e)
		_, err := f.repo.CreateEnrollment(ctx, &e, f.operator)
		return err
	}
	require.NoError(t, mk(func(*NativeEnrollment) {}), "positive control")
	requireRefusal(t, mk(func(e *NativeEnrollment) { e.AuthorityID = uuid.Nil }), "enrollment_invalid")
	require.Error(t, mk(func(e *NativeEnrollment) { e.IssuerKey = "short" }))
	requireRefusal(t, mk(func(e *NativeEnrollment) { e.ReviewerFamily = "Bad Family" }), "enrollment_invalid")
	requireRefusal(t, mk(func(e *NativeEnrollment) {
		e.TransportSourceID = e.IssuerActorID
		e.NativeRegistration, _ = domain.DeriveNativeRegistration(e.link().Binding, e.receiver()) // only the independence rule fails
	}), "enrollment_invalid")
	// the registration is derived, never chosen: a constant and a derivation from another binding are refused
	requireRefusal(t, mk(func(e *NativeEnrollment) { e.NativeRegistration = nrSHA("a") }), "enrollment_invalid")
	requireRefusal(t, mk(func(e *NativeEnrollment) { e.NativeStage = "other-stage" }), "enrollment_invalid")
	requireRefusal(t, mk(func(e *NativeEnrollment) { e.ReceiverGeneration = 2 }), "enrollment_invalid")
	requireRefusal(t, mk(func(e *NativeEnrollment) { e.AuthoritySHA = nrSHA("0") }), "enrollment_invalid")
	requireRefusal(t, mk(func(e *NativeEnrollment) { e.AuthorFamily = e.ReviewerFamily }), "enrollment_invalid")
}
