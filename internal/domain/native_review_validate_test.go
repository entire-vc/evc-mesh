package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func nrU(n int) uuid.UUID {
	return uuid.MustParse("00000000-0000-4000-8000-" + strings.Repeat("0", 11) + strconv.Itoa(n%10))
}

func nrBody() NativeBody {
	b := []byte("SHIP exact head")
	return NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(b), SHA256: NativeDigest(b)}
}

func nrValidIssue() NativeIssueRequest {
	return NativeIssueRequest{
		Schema: NativeReviewSchema, EnrollmentID: nrU(1), ReviewTaskID: nrU(2), SourceTaskID: nrU(3), Verdict: NativeVerdictShip,
		Scope:    NativeScope{Domain: NativeReviewScopeDomain, Namespace: nrU(4), Test: true, CriteriaSHA: sha("1"), ContextSHA: sha("2")},
		Pins:     NativePins{SourceRepo: "evc/evc-mesh", SourceSHA: strings.Repeat("a", 40), PackageSHA: sha("3"), ManifestSHA: sha("4"), ConfigSHA: sha("5")},
		Product:  NativeProduct{OwningTaskID: nrU(5), ObservationID: strings.Repeat("a", 32), SessionID: strings.Repeat("b", 32), AuthoritySHA: sha("6"), RootDispSHA: sha("7")},
		Receiver: NativeReceiver{Task: nrU(6), Attempt: nrU(7), Session: nrU(8), Generation: 1},
		Native:   NativeLink{Registration: sha("8"), Binding: NativeBinding{Producer: nrU(1).String(), Task: nrU(2).String(), Stage: "review-receipt", Package: sha("9"), Test: true}},
		Body:     nrBody(),
	}
}

func nrReceiptOf(q NativeIssueRequest) NativeReceipt {
	return NativeReceipt{
		Schema: NativeReviewSchema, ReceiptID: nrU(1), EnrollmentID: nrU(2), AuthorityID: nrU(3), Product: q.Product, IssuerActorID: nrU(4), PublisherID: nrU(5),
		TransportSource: nrU(6), ResultAuthorID: nrU(7), ReviewTaskID: q.ReviewTaskID, SourceTaskID: q.SourceTaskID, WorkspaceID: nrU(8), ProjectID: nrU(9),
		OwnerActorID: nrU(1), ReviewerFamily: "codex", AuthorFamily: "claude", Verdict: q.Verdict, Scope: q.Scope, Pins: q.Pins, Receiver: q.Receiver, Native: q.Native, Body: q.Body,
	}
}

func requireNativeCodes(t *testing.T, err error, code string) {
	t.Helper()
	requireNativeCode(t, err, code)
}

func TestNativeIssueRequestValidate(t *testing.T) {
	require.NoError(t, func() error { q := nrValidIssue(); return q.Validate() }(), "positive control")
	cases := []struct {
		name string
		code string
		mut  func(*NativeIssueRequest)
	}{
		{"schema", "schema_invalid", func(q *NativeIssueRequest) { q.Schema = "v0" }},
		{"enrollment id", "binding_missing", func(q *NativeIssueRequest) { q.EnrollmentID = uuid.Nil }},
		{"review task", "binding_missing", func(q *NativeIssueRequest) { q.ReviewTaskID = uuid.Nil }},
		{"source task", "binding_missing", func(q *NativeIssueRequest) { q.SourceTaskID = uuid.Nil }},
		{"verdict", "verdict_invalid", func(q *NativeIssueRequest) { q.Verdict = "ok" }},
		{"scope domain", "scope_domain", func(q *NativeIssueRequest) { q.Scope.Domain = "prod" }},
		{"scope namespace", "scope_namespace", func(q *NativeIssueRequest) { q.Scope.Namespace = uuid.Nil }},
		{"scope not test", "scope_not_test", func(q *NativeIssueRequest) { q.Scope.Test = false }},
		{"scope digest", "scope_digest", func(q *NativeIssueRequest) { q.Scope.ContextSHA = "zz" }},
		{"pins repo", "pins_invalid", func(q *NativeIssueRequest) { q.Pins.SourceRepo = "../x" }},
		{"pins short sha", "pins_invalid", func(q *NativeIssueRequest) { q.Pins.SourceSHA = "abc" }},
		{"pins config", "pins_invalid", func(q *NativeIssueRequest) { q.Pins.ConfigSHA = "" }},
		{"product task", "product_invalid", func(q *NativeIssueRequest) { q.Product.OwningTaskID = uuid.Nil }},
		{"product observation", "product_invalid", func(q *NativeIssueRequest) { q.Product.ObservationID = "short" }},
		{"product digest", "product_invalid", func(q *NativeIssueRequest) { q.Product.RootDispSHA = "" }},
		{"receiver attempt", "receiver_invalid", func(q *NativeIssueRequest) { q.Receiver.Attempt = uuid.Nil }},
		{"receiver generation", "receiver_invalid", func(q *NativeIssueRequest) { q.Receiver.Generation = 0 }},
		{"native registration", "native_invalid", func(q *NativeIssueRequest) { q.Native.Registration = "x" }},
		{"native producer not a uuid", "native_invalid", func(q *NativeIssueRequest) { q.Native.Binding.Producer = "reviewer" }},
		{"native producer not canonical", "native_invalid", func(q *NativeIssueRequest) { q.Native.Binding.Producer = "0000000A-0000-4000-8000-00000000000A" }},
		{"native stage", "native_invalid", func(q *NativeIssueRequest) { q.Native.Binding.Stage = "Review" }},
		{"native not test", "native_invalid", func(q *NativeIssueRequest) { q.Native.Binding.Test = false }},
		{"body missing", "body_missing", func(q *NativeIssueRequest) { q.Body = NativeBody{} }},
		{"body digest", "body_digest_mismatch", func(q *NativeIssueRequest) { q.Body.SHA256 = sha("0") }},
	}
	for _, x := range cases {
		t.Run(x.name, func(t *testing.T) {
			q := nrValidIssue()
			x.mut(&q)
			requireNativeCodes(t, q.Validate(), x.code)
		})
	}
}

func TestNativeReserveAndAuthorityRequestValidate(t *testing.T) {
	iq := nrValidIssue()
	reserve := func() NativeReserveRequest {
		return NativeReserveRequest{Schema: NativeReviewSchema, AuthorityID: nrU(1), ReceiptID: nrU(2), ReceiptSHA: sha("a"), Scope: iq.Scope, Pins: iq.Pins,
			Receiver: iq.Receiver, Registration: sha("b"), EventID: "ev", RequestID: nrU(3)}
	}
	q := reserve()
	require.NoError(t, q.Validate())
	for name, x := range map[string]struct {
		code string
		mut  func(*NativeReserveRequest)
	}{
		"schema":      {"schema_invalid", func(q *NativeReserveRequest) { q.Schema = "" }},
		"authority":   {"binding_missing", func(q *NativeReserveRequest) { q.AuthorityID = uuid.Nil }},
		"request id":  {"binding_missing", func(q *NativeReserveRequest) { q.RequestID = uuid.Nil }},
		"receipt sha": {"binding_missing", func(q *NativeReserveRequest) { q.ReceiptSHA = "x" }},
		"event":       {"binding_missing", func(q *NativeReserveRequest) { q.EventID = "" }},
		"scope":       {"scope_not_test", func(q *NativeReserveRequest) { q.Scope.Test = false }},
		"pins":        {"pins_invalid", func(q *NativeReserveRequest) { q.Pins.PackageSHA = "" }},
		"receiver":    {"receiver_invalid", func(q *NativeReserveRequest) { q.Receiver.Session = uuid.Nil }},
	} {
		t.Run("reserve "+name, func(t *testing.T) {
			r := reserve()
			x.mut(&r)
			requireNativeCodes(t, r.Validate(), x.code)
		})
	}
	d1, err := q.Digest()
	require.NoError(t, err)
	r2 := reserve()
	r2.EventID = "other"
	d2, err := r2.Digest()
	require.NoError(t, err)
	require.NotEqual(t, d1, d2, "the payload digest binds every field")
	require.Len(t, d1, 64)

	auth := func() NativeAuthorityRequest {
		return NativeAuthorityRequest{Schema: NativeReviewSchema, EnrollmentID: nrU(1), AuthorityID: nrU(2), ReceiptID: nrU(3), BudgetID: nrU(4), BudgetMS: 1000,
			MaxReservations: 1, NotBeforeNS: "1000000000000", ExpiresNS: "1030000000000", ClockBinding: sha("c")}
	}
	a := auth()
	require.NoError(t, a.Validate())
	for name, x := range map[string]struct {
		code string
		mut  func(*NativeAuthorityRequest)
	}{
		"schema":          {"schema_invalid", func(q *NativeAuthorityRequest) { q.Schema = "" }},
		"no authority":    {"binding_missing", func(q *NativeAuthorityRequest) { q.AuthorityID = uuid.Nil }},
		"no budget id":    {"binding_missing", func(q *NativeAuthorityRequest) { q.BudgetID = uuid.Nil }},
		"budget 2000":     {"budget_invalid", func(q *NativeAuthorityRequest) { q.BudgetMS = 2000 }},
		"two reserves":    {"budget_invalid", func(q *NativeAuthorityRequest) { q.MaxReservations = 2 }},
		"clock binding":   {"clock_binding_invalid", func(q *NativeAuthorityRequest) { q.ClockBinding = "" }},
		"not before fmt":  {"ns_not_canonical", func(q *NativeAuthorityRequest) { q.NotBeforeNS = "01" }},
		"expires fmt":     {"ns_not_canonical", func(q *NativeAuthorityRequest) { q.ExpiresNS = "1e9" }},
		"zero not_before": {"window_invalid", func(q *NativeAuthorityRequest) { q.NotBeforeNS = "0" }},
		"inverted":        {"window_invalid", func(q *NativeAuthorityRequest) { q.ExpiresNS = q.NotBeforeNS }},
		"over 60s":        {"window_invalid", func(q *NativeAuthorityRequest) { q.ExpiresNS = "1060000000001" }},
		"2^63": {"window_invalid", func(q *NativeAuthorityRequest) {
			q.NotBeforeNS = "9223372036854775000"
			q.ExpiresNS = "9223372036854775808"
		}},
	} {
		t.Run("authority "+name, func(t *testing.T) {
			r := auth()
			x.mut(&r)
			requireNativeCodes(t, r.Validate(), x.code)
		})
	}
}

func TestNativeReceiptValidateAndDecode(t *testing.T) {
	rc := nrReceiptOf(nrValidIssue())
	require.NoError(t, rc.Validate(), "positive control")
	for name, x := range map[string]struct {
		code string
		mut  func(*NativeReceipt)
	}{
		"schema":      {"schema_invalid", func(r *NativeReceipt) { r.Schema = "x" }},
		"authority":   {"binding_missing", func(r *NativeReceipt) { r.AuthorityID = uuid.Nil }},
		"publisher":   {"binding_missing", func(r *NativeReceipt) { r.PublisherID = uuid.Nil }},
		"same family": {"family_invalid", func(r *NativeReceipt) { r.AuthorFamily = r.ReviewerFamily }},
		"bad family":  {"family_invalid", func(r *NativeReceipt) { r.ReviewerFamily = "Codex 5" }},
		"verdict":     {"verdict_invalid", func(r *NativeReceipt) { r.Verdict = "" }},
		"scope":       {"scope_domain", func(r *NativeReceipt) { r.Scope.Domain = "x" }},
		"body":        {"body_missing", func(r *NativeReceipt) { r.Body = NativeBody{} }},
	} {
		t.Run(name, func(t *testing.T) {
			r := nrReceiptOf(nrValidIssue())
			x.mut(&r)
			requireNativeCodes(t, r.Validate(), x.code)
		})
	}
	c1, s1, err := rc.Canonical()
	require.NoError(t, err)
	require.Equal(t, NativeDigest(c1), s1)
	for _, b := range c1 {
		require.Less(t, b, byte(0x80), "canonical receipt is ASCII like the Runner form")
	}
}

func TestNativeBodyEdgeCases(t *testing.T) {
	big := make([]byte, NativeReviewMaxBody+1)
	for i := range big {
		big[i] = 'a'
	}
	over := NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(big), SHA256: NativeDigest(big)}
	_, err := over.DecodedBytes()
	requireNativeCode(t, err, "body_invalid")
	huge := NativeBody{Encoding: "base64", Bytes: strings.Repeat("QUFB", NativeReviewMaxBody), SHA256: sha("0")}
	_, err = huge.DecodedBytes()
	requireNativeCode(t, err, "body_too_large")
	atLimit := big[:NativeReviewMaxBody]
	_, err = NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(atLimit), SHA256: NativeDigest(atLimit)}.DecodedBytes()
	require.NoError(t, err, "exactly the bound is accepted")
	_, err = NativeBody{Encoding: "base64", Bytes: "!!!!", SHA256: sha("0")}.DecodedBytes()
	requireNativeCode(t, err, "body_invalid")
}

func TestNativeIssuerKeyAndSignatureParsing(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	key := base64.StdEncoding.EncodeToString(pub)
	digest := sha("d")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, NativeSigningMessage(digest)))
	require.NoError(t, VerifyIssuerSignature(key, digest, sig), "positive control")
	for name, x := range map[string]struct {
		code, key, digest, sig string
	}{
		"short key":         {"issuer_key_invalid", key[:20], digest, sig},
		"non-base64 key":    {"issuer_key_invalid", strings.Repeat("!", 44), digest, sig},
		"non-canonical key": {"issuer_key_invalid", strings.TrimRight(key, "=") + "A", digest, sig},
		"short signature":   {"issuer_signature_invalid", key, digest, sig[:40]},
		"empty signature":   {"issuer_signature_invalid", key, digest, ""},
		"other digest":      {"issuer_signature_invalid", key, sha("e"), sig},
		"malformed digest":  {"issuer_signature_invalid", key, "xyz", sig},
	} {
		t.Run(name, func(t *testing.T) { requireNativeCode(t, VerifyIssuerSignature(x.key, x.digest, x.sig), x.code) })
	}
	_, err = ParseIssuerKey(key)
	require.NoError(t, err)
}

func TestNativeTokensTimestampsAndRefusalText(t *testing.T) {
	require.True(t, ValidFamily("codex"))
	require.False(t, ValidFamily("Codex"))
	require.True(t, ValidRepo("evc/evc-mesh"))
	require.False(t, ValidRepo("/etc"))
	require.True(t, ValidStage("review-receipt"))
	require.False(t, ValidStage("9x"))
	require.True(t, ValidCreatedAt("2026-11-03T17:45:00Z"))
	require.True(t, ValidCreatedAt("2026-11-03T17:45:00.123456789Z"))
	require.False(t, ValidCreatedAt("2026-11-03 17:45:00"))
	// whole seconds, UTC, whatever zone the clock was in
	loc := time.FixedZone("x", 3*3600)
	require.Equal(t, "2026-11-03T14:45:00Z", FormatNativeCreatedAt(time.Date(2026, 11, 3, 17, 45, 0, 999, loc)))
	require.Equal(t, "native_invalid: x", NativeRefuse("native_invalid", "x").Error())
	require.Contains(t, NativeReceiver{Task: nrU(1), Attempt: nrU(2), Session: nrU(3), Generation: 4}.String(), "/4")
	require.Equal(t, uint64(0), func() uint64 { v, _ := ParseNS("0"); return v }())
}

func TestNativeClockAdmissionRefusals(t *testing.T) {
	const nb, ex = "1000000000000", "1030000000000"
	ok := ClockEvidence{BindingSHA: sha("c"), OuterMono: "5000000000000", OuterWall: "1000001000000", SampleMono: "5000010000000", SampleWall: "1000011000000"}
	require.NoError(t, ok.CheckAdmission(nb, ex, sha("c")), "positive control")
	for name, mut := range map[string]func(*ClockEvidence){
		"unbound":             func(e *ClockEvidence) { e.BindingSHA = "" },
		"outer mono fmt":      func(e *ClockEvidence) { e.OuterMono = "-1" },
		"outer wall fmt":      func(e *ClockEvidence) { e.OuterWall = "1.0" },
		"sample mono fmt":     func(e *ClockEvidence) { e.SampleMono = "" },
		"sample wall fmt":     func(e *ClockEvidence) { e.SampleWall = "0x1" },
		"sample regressed":    func(e *ClockEvidence) { e.SampleMono = "4999999999999" },
		"wall ahead of mono":  func(e *ClockEvidence) { e.SampleWall = "1000051000000" },
		"mono ahead of wall":  func(e *ClockEvidence) { e.SampleMono = "5000050000000" },
		"outer wall overflow": func(e *ClockEvidence) { e.OuterWall = "9223372036854775809" },
	} {
		t.Run(name, func(t *testing.T) {
			e := ok
			mut(&e)
			require.Error(t, e.CheckAdmission(nb, ex, sha("c")))
		})
	}
	require.Error(t, ok.CheckAdmission("x", ex, sha("c")))
	require.Error(t, ok.CheckAdmission(nb, "x", sha("c")))
}

// A repeated object key is parser-dependent (last wins in encoding/json, first in
// others), so the closed decoder refuses it at any depth; the same key in sibling
// objects is legitimate and stays accepted.
func TestNativeStrictDecodeRejectsDuplicateKeys(t *testing.T) {
	type inner struct {
		A int `json:"a"`
	}
	type doc struct {
		A int     `json:"a"`
		O inner   `json:"o"`
		L []inner `json:"l"`
	}
	var d doc
	require.NoError(t, StrictDecodeNative([]byte(`{"a":1,"o":{"a":2},"l":[{"a":3},{"a":4}]}`), &d), "positive control: same key in sibling objects")
	require.Equal(t, 4, d.L[1].A)
	for name, raw := range map[string]string{
		"top level": `{"a":1,"a":2}`,
		"nested":    `{"o":{"a":1,"a":2}}`,
		"in array":  `{"l":[{"a":1},{"a":1,"a":2}]}`,
		"after obj": `{"o":{"a":1},"o":{"a":2}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var x doc
			requireNativeCode(t, StrictDecodeNative([]byte(raw), &x), "schema_invalid")
		})
	}
	var x doc
	requireNativeCode(t, StrictDecodeNative([]byte(`{"a":`), &x), "schema_invalid")
}

// The registration identity is the Runner ledger rule, not a free value: the
// derivation is pinned to the value the ledger computes for the same binding and
// receiver, and every input that changes the pair changes the identity.
func TestNativeRegistrationDerivation(t *testing.T) {
	b := NativeBinding{Producer: "00000000-0000-4000-8000-00000000e013", Task: "00000000-0000-4000-8000-00000000e032",
		Stage: "review-receipt", Package: strings.Repeat("3", 64), Test: true}
	u := func(n string) uuid.UUID { return uuid.MustParse("00000000-0000-4000-8000-0000000000" + n) }
	r := NativeReceiver{Task: u("a1"), Attempt: u("a2"), Session: u("a3"), Generation: 1}
	got, err := DeriveNativeRegistration(b, r)
	require.NoError(t, err)
	// value computed by Runner native_result_ledger.identity({"binding","receiver"})
	require.Equal(t, "8e869b5237c0ada5f7298bd6862035abe8e4ef2a1268fe6d66499efdbd50d82e", got, "positive control")
	again, err := DeriveNativeRegistration(b, r)
	require.NoError(t, err)
	require.Equal(t, got, again, "deterministic")
	for name, mut := range map[string]func(*NativeBinding, *NativeReceiver){
		"producer":   func(b *NativeBinding, _ *NativeReceiver) { b.Producer = "00000000-0000-4000-8000-00000000e099" },
		"task":       func(b *NativeBinding, _ *NativeReceiver) { b.Task = "00000000-0000-4000-8000-00000000e099" },
		"stage":      func(b *NativeBinding, _ *NativeReceiver) { b.Stage = "other" },
		"package":    func(b *NativeBinding, _ *NativeReceiver) { b.Package = strings.Repeat("4", 64) },
		"test":       func(b *NativeBinding, _ *NativeReceiver) { b.Test = false },
		"attempt":    func(_ *NativeBinding, r *NativeReceiver) { r.Attempt = u("ff") },
		"generation": func(_ *NativeBinding, r *NativeReceiver) { r.Generation = 2 },
	} {
		bb, rr := b, r
		mut(&bb, &rr)
		other, err := DeriveNativeRegistration(bb, rr)
		require.NoError(t, err)
		require.NotEqual(t, got, other, name)
	}
}
