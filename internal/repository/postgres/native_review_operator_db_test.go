package postgres

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// manifestFor derives a fresh, valid enrollment manifest from the fixture's
// workspace and actors (new enrollment, authority, tasks and transport source).
func (f *nrFixture) manifestFor() EnrollmentManifest {
	m := goodManifest()
	m.WorkspaceID, m.IssuerActorID, m.PublisherActorID, m.ConsumerActorID = f.ws, f.issuer, f.publisher, f.consumer
	m.AuthorityIssuerActorID, m.ResultAuthorActorID = f.authIssuer, f.author
	rcv := f.receiver()
	m.ReceiverTask, m.ReceiverAttempt, m.ReceiverSession, m.ReceiverGeneration = rcv.Task, rcv.Attempt, rcv.Session, rcv.Generation
	m.NativePackageSHA, m.PackageSHA, m.ManifestSHA, m.ConfigSHA = nrSHA("3"), nrSHA("3"), nrSHA("4"), nrSHA("5")
	m.CriteriaSHA, m.ContextSHA = nrSHA("1"), nrSHA("2")
	m.SourceRepo, m.SourceSHA = "evc-mesh", "0123456789abcdef0123456789abcdef01234567"
	m.ProductObservationID, m.ProductSessionID = nrSHA("6")[:32], nrSHA("7")[:32]
	m.ScopeTest = true
	b := domain.NativeLink{Binding: domain.NativeBinding{Producer: m.TransportSourceID.String(), Task: m.SourceTaskID.String(),
		Stage: m.NativeStage, Package: m.NativePackageSHA, Test: true}}
	reg, err := domain.DeriveNativeRegistration(b.Binding, rcv)
	require.NoError(f.t, err)
	m.NativeRegistration = reg
	return m
}

// Real adapter + operator path: enroll via manifest and host-local pubkey, load
// the key-file signer, issue through the repo, read back the signed packet.
func TestNativeReviewOperatorPathIssueReadback(t *testing.T) {
	f := newNRFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyFile := writeKeyFile(t, dir, "signer.key", base64.StdEncoding.EncodeToString(priv.Seed()), 0o600)
	pubFile := writeKeyFile(t, dir, "issuer.pub", base64.StdEncoding.EncodeToString(pub), 0o644)
	signer, err := LoadFileSigner(keyFile)
	require.NoError(t, err)
	repo := NewNativeReviewRepo(f.db, nil, signer)

	m := f.manifestFor()
	raw, _ := json.Marshal(m)
	parsed, err := ParseEnrollmentManifest(raw)
	require.NoError(t, err)
	issuerKey, err := ReadIssuerPublicKeyFile(pubFile)
	require.NoError(t, err)
	e, err := parsed.Enrollment(issuerKey)
	require.NoError(t, err)

	// negatives first, each leaving zero rows
	_, _, err = repo.EnrollAsOperator(ctx, uuid.New(), e) // not a workspace member
	requireRefusal(t, err, "not_authorized")
	_, _, err = repo.EnrollAsOperator(ctx, f.issuer, e) // enrolled identity cannot enroll itself
	requireRefusal(t, err, "not_authorized")
	require.Zero(t, f.n(`SELECT count(*) FROM native_review_enrollments WHERE id=$1`, m.ID))

	created, _, err := repo.EnrollAsOperator(ctx, f.operator, e) // positive control
	require.NoError(t, err)
	require.True(t, created)
	created, _, err = repo.EnrollAsOperator(ctx, f.operator, e) // idempotent replay
	require.NoError(t, err)
	require.False(t, created)
	changed := *e
	changed.ConfigSHA = nrSHA("9")
	_, _, err = repo.EnrollAsOperator(ctx, f.operator, &changed)
	requireRefusal(t, err, "enrollment_conflict")

	q := f.issueReq(domain.NativeVerdictShip, func(r *domain.NativeIssueRequest) {
		r.EnrollmentID = m.ID
		r.ReviewTaskID, r.SourceTaskID = m.ReviewTaskID, m.SourceTaskID
		r.Scope, r.Pins, r.Product, r.Receiver, r.Native = e.scope(), e.pins(), e.product(), e.receiver(), e.link()
	})
	// wrong key: the file signer's public key is not the enrolled pin of the fixture enrollment
	wrong := f.issueReq(domain.NativeVerdictShip)
	_, err = repo.IssueReceipt(ctx, f.issuer, wrong)
	requireRefusal(t, err, "issuer_signature_invalid")
	// publisher cannot issue
	_, err = repo.IssueReceipt(ctx, f.publisher, q)
	requireRefusal(t, err, "issuer_not_enrolled")

	pk, err := repo.IssueReceipt(ctx, f.issuer, q)
	require.NoError(t, err)
	back, err := repo.ReadReceipt(ctx, f.issuer, pk.ReceiptID)
	require.NoError(t, err)
	require.Equal(t, pk.ReceiptSHA, back.ReceiptSHA)
	require.NoError(t, domain.VerifyIssuerSignature(issuerKey, back.ReceiptSHA, back.IssuerSignature))

	// revoke: only the enrolling operator or authority issuer; then issue refused
	require.Error(t, repo.RevokeEnrollment(ctx, m.ID, f.publisher, "x"))
	require.NoError(t, repo.RevokeEnrollment(ctx, m.ID, f.operator, "key rotated"))
	_, err = repo.IssueReceipt(ctx, f.issuer, q)
	requireRefusal(t, err, "enrollment_revoked")
	_ = filepath.Join
}

// Concurrent identical enrollments are idempotent: exactly one creates, every
// caller gets the stored row, nobody sees an insert error.
func TestNativeReviewEnrollConcurrentReplay(t *testing.T) {
	f := newNRFixture(t)
	m := f.manifestFor()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	key := base64.StdEncoding.EncodeToString(pub)
	const n = 6
	type res struct {
		created bool
		err     error
	}
	out := make(chan res, n)
	for i := 0; i < n; i++ {
		go func() {
			e, eerr := m.Enrollment(key)
			if eerr != nil {
				out <- res{err: eerr}
				return
			}
			c, _, rerr := f.repo.EnrollAsOperator(context.Background(), f.operator, e)
			out <- res{created: c, err: rerr}
		}()
	}
	created := 0
	for i := 0; i < n; i++ {
		r := <-out
		require.NoError(t, r.err)
		if r.created {
			created++
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, 1, f.n(`SELECT count(*) FROM native_review_enrollments WHERE id=$1`, m.ID))
}
