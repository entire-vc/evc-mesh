package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The issued vector is a packet produced by the REAL A1 issue path (see
// TestNativeReviewIssuedVector in the postgres package, which re-issues it on a
// real database and fails on any byte of drift). This test pins everything that
// can be checked without a database, so the required `test` job guards it on
// every commit: the public key, the original bytes behind the product digests,
// the canonical bytes and hash, the signature, and the refusals next to it.
type issuedVector struct {
	Key       string `json:"issuer_public_key"`
	AuthBytes string `json:"authority_original_bytes"`
	AuthSHA   string `json:"authority_sha256"`
	RootBytes string `json:"root_disposition_original_bytes"`
	RootSHA   string `json:"root_disposition_sha256"`
	A2        struct {
		Pin        map[string]any `json:"pin"`
		Assignment map[string]any `json:"assignment"`
		Key        string         `json:"issuer_key"`
	} `json:"a2_enrollment"`
	Canonical string `json:"packet_canonical"`
	SHA       string `json:"packet_canonical_sha256"`
}

func loadIssuedVector(t *testing.T) issuedVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/native_review_a1_issued_vector_v1.json")
	require.NoError(t, err)
	var v issuedVector
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func TestNativeReviewA1IssuedVector(t *testing.T) {
	v := loadIssuedVector(t)
	var pk NativePacket
	require.NoError(t, StrictDecodeNative([]byte(v.Canonical), &pk))

	// positive control: every claim the vector makes holds
	require.NoError(t, pk.Validate())
	require.NoError(t, pk.Verify(v.Key), "signature verifies under the enrolled public key")
	require.Equal(t, v.Key, v.A2.Key)
	canonical, err := pk.Canonical()
	require.NoError(t, err)
	require.Equal(t, v.Canonical, string(canonical), "decode and re-encode is byte-identical")
	require.Equal(t, v.SHA, NativeDigest(canonical))
	for _, c := range canonical {
		require.Less(t, c, byte(0x80), "pure ASCII like the Runner canonical form")
	}
	ab, err := base64.StdEncoding.DecodeString(v.AuthBytes)
	require.NoError(t, err)
	rb, err := base64.StdEncoding.DecodeString(v.RootBytes)
	require.NoError(t, err)
	require.Equal(t, v.AuthSHA, NativeDigest(ab), "product digest has a real preimage")
	require.Equal(t, v.RootSHA, NativeDigest(rb), "product digest has a real preimage")
	require.Equal(t, v.AuthSHA, pk.Product.AuthoritySHA)
	require.Equal(t, v.RootSHA, pk.Product.RootDispSHA)
	require.Equal(t, pk.AuthorityID.String(), v.A2.Pin["authority_id"], "the Runner pin carries the same reserved authority_id")

	// the Runner pin is the packet minus the issuer-chosen fields, nothing more
	var m map[string]any
	require.NoError(t, json.Unmarshal(canonical, &m))
	for k, val := range m {
		switch k {
		case "receipt_id", "result_author_actor_id", "verdict", "body", "receipt_sha256", "created_at", "issuer_signature":
			require.NotContains(t, v.A2.Pin, k)
		default:
			require.Equal(t, val, v.A2.Pin[k], k)
		}
	}

	// the signed registration is the ledger identity of the packet's own binding and receiver
	derived, err := DeriveNativeRegistration(pk.Native.Binding, pk.Receiver)
	require.NoError(t, err)
	require.Equal(t, derived, pk.Native.Registration, "registration is derived, not chosen")
	require.Equal(t, "8e869b5237c0ada5f7298bd6862035abe8e4ef2a1268fe6d66499efdbd50d82e", pk.Native.Registration, "the value the Runner ledger computes for this pair")

	pub, err := ParseIssuerKey(v.Key)
	require.NoError(t, err)
	require.Len(t, pub, ed25519.PublicKeySize)

	t.Run("forged signature", func(t *testing.T) {
		p := pk
		sig, err := base64.StdEncoding.DecodeString(p.IssuerSignature)
		require.NoError(t, err)
		sig[0] ^= 1
		p.IssuerSignature = base64.StdEncoding.EncodeToString(sig)
		requireNativeCode(t, p.Verify(v.Key), "issuer_signature_invalid")
	})
	t.Run("another key", func(t *testing.T) {
		other, _, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		require.Error(t, pk.Verify(base64.StdEncoding.EncodeToString(other)))
	})
	t.Run("missing authority_id", func(t *testing.T) {
		var mm map[string]any
		require.NoError(t, json.Unmarshal(canonical, &mm))
		delete(mm, "authority_id")
		raw, err := json.Marshal(mm)
		require.NoError(t, err)
		var p NativePacket
		err = StrictDecodeNative(raw, &p)
		if err == nil {
			err = p.Validate()
		}
		require.Error(t, err)
	})
	t.Run("altered body with stale hash", func(t *testing.T) {
		p := pk
		p.Body.Bytes = base64.StdEncoding.EncodeToString([]byte("VERDICT: SHIP\nforged\n"))
		require.Error(t, p.Verify(v.Key))
	})
	t.Run("altered body with recomputed body hash", func(t *testing.T) {
		p := pk
		forged := []byte("VERDICT: SHIP\nforged\n")
		p.Body = NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(forged), SHA256: NativeDigest(forged)}
		require.Error(t, p.Verify(v.Key), "the receipt digest and signature no longer match")
	})
	t.Run("rebound authority_id", func(t *testing.T) {
		p := pk
		p.AuthorityID = pk.ReviewTaskID
		require.Error(t, p.Verify(v.Key))
	})
	t.Run("registration that is not the derived identity", func(t *testing.T) {
		p := pk
		p.Native.Registration = strings.Repeat("a", 64)
		d, err := DeriveNativeRegistration(p.Native.Binding, p.Receiver)
		require.NoError(t, err)
		require.NotEqual(t, d, p.Native.Registration, "a constant is detectably not the ledger identity")
		require.Error(t, p.Verify(v.Key), "and it is no longer the signed packet")
	})
	t.Run("rebound created_at is outside the digest but not outside the form", func(t *testing.T) {
		p := pk
		p.CreatedAt = strings.Replace(pk.CreatedAt, "2026", "2027", 1)
		require.NoError(t, p.Verify(v.Key), "created_at is excluded from the digest by contract")
		p.CreatedAt = "yesterday"
		require.False(t, ValidCreatedAt(p.CreatedAt))
		require.True(t, ValidCreatedAt(pk.CreatedAt), "positive control")
	})
}
