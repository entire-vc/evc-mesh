package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

const nrVectorPath = "../../domain/testdata/native_review_a1_issued_vector_v1.json"

func vid(n string) uuid.UUID { return uuid.MustParse("00000000-0000-4000-8000-" + n) }

// nrVectorEnrollment is the fixed operator enrollment behind the issued vector.
// Every id and pin is a constant so the issued packet is byte-reproducible.
func nrVectorEnrollment(pub ed25519.PublicKey) *NativeEnrollment {
	authBytes := []byte("A1 vector original authority bytes\n")
	rootBytes := []byte("A1 vector original root disposition bytes\n")
	e := &NativeEnrollment{
		ID: vid("00000000e001"), WorkspaceID: vid("00000000e002"), ProjectID: vid("00000000e003"),
		IssuerActorID: vid("00000000e011"), PublisherActorID: vid("00000000e012"), TransportSourceID: vid("00000000e013"),
		ConsumerActorID: vid("00000000e014"), AuthorityIssuerActorID: vid("00000000e015"), ResultAuthorActorID: vid("00000000e016"),
		OwnerActorID: vid("00000000e017"), AuthorityID: vid("00000000e021"), IssuerKey: base64.StdEncoding.EncodeToString(pub),
		ReceiverTask: vid("0000000000a1"), ReceiverAttempt: vid("0000000000a2"), ReceiverSession: vid("0000000000a3"), ReceiverGeneration: 1,
		NativeStage: "review-receipt", NativePackageSHA: nrSHA("3"),
		ReviewTaskID: vid("00000000e031"), SourceTaskID: vid("00000000e032"), ReviewerFamily: "codex", AuthorFamily: "claude",
		ScopeDomain: domain.NativeReviewScopeDomain, ScopeNamespace: vid("00000000e033"), ScopeTest: true, CriteriaSHA: nrSHA("1"), ContextSHA: nrSHA("2"),
		SourceRepo: "evc-mesh", SourceSHA: "0123456789abcdef0123456789abcdef01234567", PackageSHA: nrSHA("3"), ManifestSHA: nrSHA("4"), ConfigSHA: nrSHA("5"),
		ProductTaskID: vid("00000000e034"), ProductObservationID: nrSHA("6")[:32], ProductSessionID: nrSHA("7")[:32],
		AuthorityBytes: authBytes, RootDispositionBytes: rootBytes,
		AuthoritySHA: domain.NativeDigest(authBytes), RootDispositionSHA: domain.NativeDigest(rootBytes),
	}
	// the operator takes the registration identity from the ledger rule, never a constant
	reg, err := domain.DeriveNativeRegistration(e.link().Binding, e.receiver())
	if err != nil {
		panic(err)
	}
	e.NativeRegistration = reg
	return e
}

// nrVectorSigner derives a deterministic throwaway Ed25519 key from a public
// label. It exists only in this harness; Ed25519 signing is deterministic, so
// the same inputs always give the same packet. Production wires no signer.
func nrVectorSigner() (ed25519.PrivateKey, ed25519.PublicKey) {
	seed := sha256.Sum256([]byte("native-review-a1-issued-vector-v1 harness-only test key"))
	k := ed25519.NewKeyFromSeed(seed[:])
	return k, k.Public().(ed25519.PublicKey)
}

// The issued vector: a packet issued by the REAL A1 path (real DB, real enrollment
// checks, real signature verification under the enrolled key) from real test
// original bytes whose SHA-256 the database enforces. The committed fixture holds
// the public key, the original bytes and the expected canonical packet; the test
// fails if the real path drifts from it. NR_VECTOR_WRITE=1 regenerates the file.
func TestNativeReviewIssuedVector(t *testing.T) {
	f := newNRFixture(t)
	priv, pub := nrVectorSigner()
	enr := nrVectorEnrollment(pub)
	repo := NewNativeReviewRepo(f.db, nil, &nrSigner{key: priv})
	repo.hooks.now = func() time.Time { return time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC) }
	repo.hooks.newID = func() uuid.UUID { return vid("00000000e041") }
	ctx := context.Background()

	f.exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'native review vector',$2,$3) ON CONFLICT DO NOTHING`, enr.WorkspaceID, enr.WorkspaceID.String(), vid("00000000e0ff"))
	for _, a := range []uuid.UUID{enr.IssuerActorID, enr.PublisherActorID, enr.ConsumerActorID, enr.AuthorityIssuerActorID, enr.ResultAuthorActorID, vid("00000000e018")} {
		f.exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'nr-vec',$3,'h','p') ON CONFLICT DO NOTHING`, a, enr.WorkspaceID, a.String())
	}
	// the fixed ids are one-shot per database: issue once, read back thereafter
	if f.n(`SELECT count(*) FROM native_review_enrollments WHERE id=$1`, enr.ID) == 0 {
		_, err := repo.CreateEnrollment(ctx, enr, vid("00000000e018"))
		require.NoError(t, err)
	}
	if f.n(`SELECT count(*) FROM native_review_receipts WHERE enrollment_id=$1`, enr.ID) == 0 {
		req := domain.NativeIssueRequest{
			Schema: domain.NativeReviewSchema, EnrollmentID: enr.ID, ReviewTaskID: enr.ReviewTaskID, SourceTaskID: enr.SourceTaskID,
			Verdict: domain.NativeVerdictShip, Scope: enr.scope(), Pins: enr.pins(), Product: enr.product(), Receiver: enr.receiver(), Native: enr.link(),
			Body: domain.NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString([]byte("SHIP exact head, vector body\n")),
				SHA256: domain.NativeDigest([]byte("SHIP exact head, vector body\n"))},
		}
		_, err := repo.IssueReceipt(ctx, enr.IssuerActorID, req)
		require.NoError(t, err)
	}
	pk, err := repo.ReadReceipt(ctx, enr.IssuerActorID, vid("00000000e041"))
	require.NoError(t, err)
	canonical, err := pk.Canonical()
	require.NoError(t, err)
	require.NoError(t, pk.Verify(enr.IssuerKey))

	if os.Getenv("NR_VECTOR_WRITE") == "1" {
		var m map[string]any
		require.NoError(t, json.Unmarshal(canonical, &m))
		pin := map[string]any{}
		for k, v := range m {
			switch k {
			case "receipt_id", "result_author_actor_id", "verdict", "body", "receipt_sha256", "created_at", "issuer_signature":
			default:
				pin[k] = v
			}
		}
		out, merr := json.MarshalIndent(map[string]any{
			"vector": "native-review-a1-issued/v1",
			"note": "Issued by the real A1 issue path from the fixed enrollment below. The signing key is a harness-only test key and is NOT stored here; " +
				"only the public key is. Ed25519 is deterministic: re-issuing with the same inputs yields the same bytes. Regenerate: NR_VECTOR_WRITE=1.",
			"issuer_public_key":               enr.IssuerKey,
			"authority_original_bytes":        base64.StdEncoding.EncodeToString(enr.AuthorityBytes),
			"authority_sha256":                enr.AuthoritySHA,
			"root_disposition_original_bytes": base64.StdEncoding.EncodeToString(enr.RootDispositionBytes),
			"root_disposition_sha256":         enr.RootDispositionSHA,
			"a2_enrollment":                   map[string]any{"pin": pin, "assignment": map[string]any{"owner": vid("00000000e051").String(), "sequence": 1}, "issuer_key": enr.IssuerKey},
			"packet_canonical":                string(canonical),
			"packet_canonical_sha256":         domain.NativeDigest(canonical),
		}, "", "  ")
		require.NoError(t, merr)
		require.NoError(t, os.WriteFile(nrVectorPath, append(out, '\n'), 0o644))
		return
	}

	raw, err := os.ReadFile(nrVectorPath)
	require.NoError(t, err)
	var v struct {
		Canonical string `json:"packet_canonical"`
		SHA       string `json:"packet_canonical_sha256"`
	}
	require.NoError(t, json.Unmarshal(raw, &v))
	require.Equal(t, v.Canonical, string(canonical), "the real issue path drifted from the committed issued vector")
	require.Equal(t, v.SHA, domain.NativeDigest(canonical))
}
