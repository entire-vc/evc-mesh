package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The golden hash below was produced by the consumer's own encoder:
// hashlib.sha256(json.dumps(v, sort_keys=True, separators=(",", ":"),
// ensure_ascii=False).encode()).hexdigest() for the same value. It covers
// non-ASCII keys and values, U+2028, HTML characters, control characters,
// DEL, an astral code point, empty containers and negative numbers.
func TestCanonicalJSON_MatchesConsumerEncoder(t *testing.T) {
	in := `{"b": [1, 2, {"z": null, "a": true}], "a": "é <>&\"\\\n\t\u0001\u001f\u007f\b\f😀", "n": -12, "o": {}, "e": [], "ключ": "значение", "f": false}`
	got, err := CanonicalJSONBytes([]byte(in))
	require.NoError(t, err)
	sum := sha256.Sum256(got)
	require.Equal(t, "dc10f0eca88f2555a65d0b11c2f2cfb27dfa680e00789509a12188cedebfea3c", hex.EncodeToString(sum[:]))

	// Control: a one-character change to the input moves the hash.
	other, err := CanonicalJSONBytes([]byte(`{"b": [1, 2, {"z": null, "a": true}], "a": "x", "n": -12, "o": {}, "e": [], "f": false}`))
	require.NoError(t, err)
	osum := sha256.Sum256(other)
	require.NotEqual(t, hex.EncodeToString(osum[:]), hex.EncodeToString(sum[:]))
}

func TestPayloadDigestV1_ExcludesIdempotencyKeyAndKeyOrder(t *testing.T) {
	a, err := PayloadDigestV1([]byte(`{"idempotency_key":"k1","class":"code","expected_version":7}`))
	require.NoError(t, err)
	b, err := PayloadDigestV1([]byte(`{"expected_version":7, "class":"code", "idempotency_key":"k2"}`))
	require.NoError(t, err)
	require.Equal(t, a, b)
	c, err := PayloadDigestV1([]byte(`{"expected_version":8,"class":"code","idempotency_key":"k1"}`))
	require.NoError(t, err)
	require.NotEqual(t, a, c)
}

func TestBuildAcceptanceView_SnapshotHashCoversEveryOtherField(t *testing.T) {
	author, acceptor, actor := uuid.New(), uuid.New(), uuid.New()
	link := uuid.New()
	snap, _ := json.Marshal(map[string]any{"vcs_link_id": link, "head_sha": "abc", "ship_sha": "abc", "url": "u"})
	it := &AcceptanceIntent{ID: uuid.New(), TaskID: uuid.New(), ProjectID: uuid.New(), Domain: "product", Class: "code",
		Operation: "product_done", FromStatusID: uuid.New(), FromVersion: 4, ToStatusID: uuid.New(), EvidenceKind: ReceiptEvidenceVCS,
		Evidence: snap, ResultAuthorID: &author, AcceptorActorID: acceptor, AcceptorRole: AcceptorRoleReviewer, PolicyVersion: 2,
		Reason: "ok", CreatedAt: time.Date(2026, 11, 2, 1, 2, 3, 456789000, time.UTC)}
	v, err := BuildAcceptanceView(it, "scope-1", "board", actor)
	require.NoError(t, err)
	require.Equal(t, acceptor.String(), v["accepting_actor_id"], "acceptor comes from the stored intent")
	require.Equal(t, map[string]any{"vcs_link_id": link.String(), "head_sha": "abc"}, v["evidence"], "request-shaped evidence, no snapshot-only fields")

	hash := v["snapshot_hash"].(string)
	rest := map[string]any{}
	for k, val := range v {
		if k != "snapshot_hash" {
			rest[k] = val
		}
	}
	want, err := CanonicalHash(rest)
	require.NoError(t, err)
	require.Equal(t, want, hash)

	// Control: another credential actor or scope changes the snapshot.
	v2, err := BuildAcceptanceView(it, "scope-2", "board", actor)
	require.NoError(t, err)
	require.NotEqual(t, hash, v2["snapshot_hash"])
}

func TestCapabilities_ReadyOnlyForBoundProducerWithScope(t *testing.T) {
	actor, other := uuid.New(), uuid.New()
	p := &TransitionReceiptPolicy{ProjectID: uuid.New(), Enabled: true, EnforceDone: true, PolicyVersion: 3, ScopeID: "s",
		AllowedProducers: []AllowedProducer{{ProducerID: "board", ActorID: actor}}}
	require.True(t, p.Capabilities(actor).EnforcementReady)
	require.Equal(t, "board", p.Capabilities(actor).ProducerID)
	require.False(t, p.Capabilities(other).EnforcementReady, "an unbound credential is never ready")
	p.ScopeID = ""
	require.False(t, p.Capabilities(actor).EnforcementReady, "no scope name, not ready")
	p.ScopeID, p.EnforceDone = "s", false
	require.False(t, p.Capabilities(actor).EnforcementReady, "enforcement off, not ready")
}

func TestProductDoneRequest_ValidateAcceptorOrRef(t *testing.T) {
	v := int64(1)
	link := uuid.New()
	base := ProductDoneRequest{Domain: TransitionDomainProduct, Operation: TransitionOpProductDone, Class: TransitionClassCode,
		ToStatusID: uuid.New(), ExpectedStatusID: uuid.New(), ExpectedVersion: &v, IdempotencyKey: "k", ProducerID: "board", Reason: "r",
		Evidence: ProductDoneEvidence{VCSLinkID: &link, HeadSHA: "h"}}
	require.Error(t, base.Validate(), "neither an acceptor nor a ref: refused")
	withRef := base
	withRef.AcceptanceRef = uuid.NewString()
	require.NoError(t, withRef.Validate())
	withActor := base
	withActor.AcceptingActorID = uuid.New()
	require.NoError(t, withActor.Validate(), "legacy shape still valid")
}

func TestTransitionReceiptJSON_LegacyShapeUnchangedWireShapeAdditive(t *testing.T) {
	legacy := TransitionReceipt{Evidence: json.RawMessage(`{"a":1}`), EvidenceHash: "snap"}
	b, err := json.Marshal(legacy)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, map[string]any{"a": float64(1)}, m["evidence"])
	require.Equal(t, "snap", m["evidence_hash"])
	for _, k := range []string{"credential_client_id", "evidence_snapshot", "scope_id", "acceptance_hash", "payload_digest_v1", "payload_digest_algorithm"} {
		require.NotContains(t, m, k, "legacy shape carries no wire-only field")
	}

	wire := TransitionReceipt{Evidence: json.RawMessage(`{"a":1}`), EvidenceHash: "snap",
		WireEvidence: json.RawMessage(`{"head_sha":"h"}`), WireEvidenceHash: "wire"}
	b, err = json.Marshal(wire)
	require.NoError(t, err)
	m = map[string]any{}
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, map[string]any{"head_sha": "h"}, m["evidence"])
	require.Equal(t, "wire", m["evidence_hash"])
	require.Equal(t, map[string]any{"a": float64(1)}, m["evidence_snapshot"])
	require.Equal(t, "snap", m["evidence_snapshot_hash"])
	require.Contains(t, m, "credential_client_id")
	require.Nil(t, m["credential_client_id"])
	require.Contains(t, m, "scope_id")
}

func TestCanonicalJSON_ErrorBranches(t *testing.T) {
	_, err := CanonicalJSONBytes([]byte(`{"a":`))
	require.Error(t, err)
	_, err = CanonicalJSONBytes([]byte(`{"a":1} {"b":2}`))
	require.Error(t, err)
	_, err = CanonicalJSON(make(chan int))
	require.Error(t, err)
	_, err = CanonicalHash(make(chan int))
	require.Error(t, err)
	_, err = CanonicalHashBytes([]byte(`not json`))
	require.Error(t, err)
	h, err := CanonicalHashBytes([]byte(`{"a":[1,"x"]}`))
	require.NoError(t, err)
	require.Len(t, h, 64)
	_, err = PayloadDigestV1([]byte(`[1]`))
	require.Error(t, err)

	// A type the encoder does not emit is refused, at top level and nested.
	var buf bytes.Buffer
	require.Error(t, writeCanonical(&buf, 1.5))
	require.Error(t, writeCanonical(&buf, []any{1.5}))
	require.Error(t, writeCanonical(&buf, map[string]any{"k": 1.5}))
}

func TestWireEvidence_Shapes(t *testing.T) {
	link := uuid.New()
	full := WireEvidenceFromRequest(ProductDoneEvidence{VCSLinkID: &link, HeadSHA: "abc", ExemptionType: "research", ArtifactRef: "r", ArtifactHash: "h"})
	require.Len(t, full, 5)
	require.Empty(t, WireEvidenceFromRequest(ProductDoneEvidence{}))

	_, err := WireEvidenceFromSnapshot(ReceiptEvidenceVCS, json.RawMessage(`nope`))
	require.Error(t, err)
	ex, err := WireEvidenceFromSnapshot(ReceiptEvidenceExemption, json.RawMessage(`{"exemption_type":"research","artifact_ref":"r","artifact_hash":"h","head_sha":"x"}`))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"exemption_type": "research", "artifact_ref": "r", "artifact_hash": "h"}, ex)
	vcs, err := WireEvidenceFromSnapshot(ReceiptEvidenceVCS, json.RawMessage(`{"vcs_link_id":"l","head_sha":"s"}`))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"vcs_link_id": "l", "head_sha": "s"}, vcs)

	it := &AcceptanceIntent{ID: uuid.New(), EvidenceKind: ReceiptEvidenceVCS, Evidence: json.RawMessage(`bad`)}
	_, err = BuildAcceptanceView(it, "scope", "prod", uuid.New())
	require.Error(t, err)
}

func TestCapabilities_UnboundActorNotReady(t *testing.T) {
	bound := uuid.New()
	p := &TransitionReceiptPolicy{ProjectID: uuid.New(), Enabled: true, EnforceDone: true, ScopeID: "s",
		AllowedProducers: []AllowedProducer{{ActorID: bound, ProducerID: "prod"}}}
	require.True(t, p.Capabilities(bound).EnforcementReady)
	require.False(t, p.Capabilities(uuid.New()).EnforcementReady)
	p.ScopeID = " "
	require.False(t, p.Capabilities(bound).EnforcementReady)
	require.Equal(t, "", p.ProducerFor(uuid.New()))
}
