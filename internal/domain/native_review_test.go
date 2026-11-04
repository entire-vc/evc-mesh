package domain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func sha(c string) string { return strings.Repeat(c, 64) }

func TestNativeStrictDecodeClosed(t *testing.T) {
	var q NativeReserveRequest
	err := StrictDecodeNative([]byte(`{"schema":"x","issuer_actor_id":"`+uuid.NewString()+`"}`), &q)
	require.Error(t, err)
	require.Error(t, StrictDecodeNative([]byte(`{"schema":"x"} {"a":1}`), &q))
	var i NativeIssueRequest
	require.Error(t, StrictDecodeNative([]byte(`{"scope":{"test":1}}`), &i), "bool must be bool")
	require.Error(t, (&NativeReserveRequest{}).Validate(), "missing fields refuse")
}

func TestNativeParseNSCanonical(t *testing.T) {
	for _, bad := range []string{"", "-1", "+1", " 1", "1 ", "01", "1e9", "1.0", "9223372036854775808999", "0x1"} {
		_, err := ParseNS(bad)
		require.Error(t, err, bad)
	}
	for in, want := range map[string]uint64{"0": 0, "1": 1, "1000000000": 1e9} {
		v, err := ParseNS(in)
		require.NoError(t, err)
		require.Equal(t, want, v)
	}
}

func TestNativeBodyBounds(t *testing.T) {
	good := NativeBody{Encoding: "base64", Bytes: "U0hJUA==", SHA256: NativeDigest([]byte("SHIP"))}
	_, err := good.DecodedBytes()
	require.NoError(t, err)
	for _, b := range []NativeBody{
		{Encoding: "hex", Bytes: "U0hJUA==", SHA256: good.SHA256},
		{Encoding: "base64", Bytes: "U0hJUA", SHA256: good.SHA256},                                 // non-canonical padding
		{Encoding: "base64", Bytes: "U0hJUA==", SHA256: sha("0")},                                  // digest mismatch
		{Encoding: "base64", Bytes: strings.Repeat("QUFB", NativeReviewMaxBody), SHA256: sha("0")}, // oversize: refusal, not truncation
	} {
		_, err = b.DecodedBytes()
		require.Error(t, err)
	}
}

func TestClockAdmissionBoundaries(t *testing.T) {
	const nb, ex = uint64(1_000_000_000_000), uint64(1_000_000_000_000 + 30_000_000_000)
	ev := func(ow, om, sm, sw uint64) ClockEvidence {
		f := func(v uint64) string { return strconv.FormatUint(v, 10) }
		return ClockEvidence{BindingSHA: sha("c"), OuterMono: f(om), OuterWall: f(ow), SampleMono: f(sm), SampleWall: f(sw)}
	}
	nbS, exS := strconv.FormatUint(nb, 10), strconv.FormatUint(ex, 10)
	const om = uint64(5e12)
	ow := nb + 1
	require.NoError(t, ev(ow, om, om+10_000_000, ow+10_000_000).CheckAdmission(nbS, exS, sha("c")))
	// cutoff = om+900ms; last admissible sample is om+800ms (>=100ms before cutoff)
	require.NoError(t, ev(ow, om, om+800_000_000, ow+800_000_000).CheckAdmission(nbS, exS, sha("c")))
	require.Error(t, ev(ow, om, om+800_000_001, ow+800_000_001).CheckAdmission(nbS, exS, sha("c")))
	// expiry shortens the window: 300ms left => end = om+300ms, cutoff = om+200ms;
	// admission needs cutoff-sample >= 100ms, so the sample must be <= om+100ms
	ow2 := ex - 300_000_000
	require.NoError(t, ev(ow2, om, om+100_000_000, ow2+100_000_000).CheckAdmission(nbS, exS, sha("c")))
	require.Error(t, ev(ow2, om, om+100_000_001, ow2+100_000_001).CheckAdmission(nbS, exS, sha("c")))
	// <100ms to expiry: window shorter than the cutoff margin
	ow3 := ex - 50_000_000
	require.Error(t, ev(ow3, om, om+1, ow3+1).CheckAdmission(nbS, exS, sha("c")))
	require.Error(t, ev(nb-1, om, om+1, nb).CheckAdmission(nbS, exS, sha("c")))
	require.Error(t, ev(ow, om, om+1, ow+1).CheckAdmission(nbS, exS, sha("d")))
}

// Shared A2 golden fixture contains a public test key and signature only.
// The private signing seed is excluded from this repository.

const a2GoldenSHA = "acc1ce61f2811b5e92ede2fc2eb37269dc354b442c021f04c63cc5abeca71594"

func a2Golden(t *testing.T) (raw []byte, key string) {
	t.Helper()
	file, err := os.ReadFile("testdata/native_review_a2_fixture_v1.json")
	require.NoError(t, err)
	var doc struct {
		Enrollment struct {
			IssuerKey string `json:"issuer_key"`
		} `json:"enrollment"`
		Receipt json.RawMessage `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal(file, &doc))
	return doc.Receipt, doc.Enrollment.IssuerKey
}

func decodeGolden(t *testing.T, raw []byte) *NativePacket {
	t.Helper()
	var p NativePacket
	require.NoError(t, StrictDecodeNative(raw, &p))
	return &p
}

func TestNativeReviewA2GoldenPacket(t *testing.T) {
	raw, key := a2Golden(t)
	p := decodeGolden(t, raw)
	// positive control: the exact Runner packet verifies, byte for byte
	require.NoError(t, p.Verify(key))
	require.Equal(t, a2GoldenSHA, p.ReceiptSHA)
	_, sum, err := p.NativeReceipt.Canonical()
	require.NoError(t, err)
	require.Equal(t, a2GoldenSHA, sum, "Go canonical digest must equal the Python one")
	want, err := CanonicalJSONBytes(raw)
	require.NoError(t, err)
	got, err := p.Canonical()
	require.NoError(t, err)
	require.Equal(t, string(want), string(got), "packet must round-trip byte-identically")

	t.Run("forged signature", func(t *testing.T) {
		q := *p
		sig := []byte(q.IssuerSignature)
		if sig[0] == 'A' {
			sig[0] = 'B'
		} else {
			sig[0] = 'A'
		}
		q.IssuerSignature = string(sig)
		require.Error(t, q.Verify(key))
	})
	t.Run("wrong issuer key", func(t *testing.T) {
		require.Error(t, p.Verify("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="))
	})
	t.Run("altered body", func(t *testing.T) {
		q := *p
		body := []byte("VERDICT: SHIP\nhead=2222222222222222222222222222222222222222\nall criteria PASS\n")
		q.Body = NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(body), SHA256: NativeDigest(body)}
		err := q.Verify(key)
		require.Error(t, err)
		requireNativeCode(t, err, "receipt_digest_mismatch")
	})
	t.Run("altered body with recomputed digest still lacks the issuer signature", func(t *testing.T) {
		q := *p
		body := []byte("VERDICT: SHIP\nhead=2222222222222222222222222222222222222222\nall criteria PASS\n")
		q.Body = NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(body), SHA256: NativeDigest(body)}
		_, q.ReceiptSHA, _ = q.NativeReceipt.Canonical()
		requireNativeCode(t, q.Verify(key), "issuer_signature_invalid")
	})
	t.Run("missing authority_id", func(t *testing.T) {
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		delete(m, "authority_id")
		b, err := json.Marshal(m)
		require.NoError(t, err)
		requireNativeCode(t, decodeGolden(t, b).Verify(key), "binding_missing")
	})
	t.Run("unknown field", func(t *testing.T) {
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		m["extra"] = "x"
		b, err := json.Marshal(m)
		require.NoError(t, err)
		var q NativePacket
		requireNativeCode(t, StrictDecodeNative(b, &q), "schema_invalid")
	})
	t.Run("signature over a different digest", func(t *testing.T) {
		q := *p
		q.CreatedAt = "not-a-time"
		requireNativeCode(t, q.Verify(key), "created_at_invalid")
	})
}

func requireNativeCode(t *testing.T, err error, code string) {
	t.Helper()
	var r *NativeRefusal
	require.True(t, errors.As(err, &r), "want refusal %s, got %v", code, err)
	require.Equal(t, code, r.Code)
}
