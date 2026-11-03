package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Wire contract board-product-done.authenticated.v1.
//
// The fleet consumer (Board product-done adapter) pins these names and hashes.
// Everything here is additive: the foundation payload digest, the receipt
// columns and the legacy request shape (caller-supplied accepting_actor_id)
// keep working. Hashes are taken over canonical JSON: UTF-8, keys sorted,
// compact separators, no HTML escaping. That is byte-for-byte what Python's
// json.dumps(sort_keys=True, separators=(",", ":"), ensure_ascii=False) emits
// for the values used here (strings, integers, booleans, null, objects, arrays).
const (
	WireContractProductDoneV1 = "board-product-done.authenticated.v1"
	PayloadDigestAlgorithmV1  = "canonical-json-without-idempotency-key-v1"
)

// CanonicalJSON returns the canonical encoding of v.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return CanonicalJSONBytes(raw)
}

// CanonicalJSONBytes canonicalises an existing JSON document. Numbers keep the
// literal they arrived with (no float round trip).
func CanonicalJSONBytes(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CanonicalHash is the hex SHA-256 of the canonical encoding of v.
func CanonicalHash(v any) (string, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// CanonicalHashBytes is CanonicalHash for an already encoded document.
func CanonicalHashBytes(raw []byte) (string, error) {
	b, err := CanonicalJSONBytes(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// PayloadDigestV1 hashes a request body without its idempotency_key.
func PayloadDigestV1(raw []byte) (string, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return "", err
	}
	delete(m, "idempotency_key")
	return CanonicalHash(m)
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(t.String())
	case string:
		writeCanonicalString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonicalString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON type %T", v)
	}
	return nil
}

// writeCanonicalString mirrors Python's ensure_ascii=False escaping: quote,
// backslash and control characters only.
func writeCanonicalString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteString(strconv.FormatInt(int64(r)>>4, 16))
				buf.WriteString(strconv.FormatInt(int64(r)&0xf, 16))
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// WireEvidenceFromRequest is the request-shaped evidence the consumer carries:
// the same object an acceptance record exposes and a receipt reads back.
func WireEvidenceFromRequest(e ProductDoneEvidence) map[string]any {
	out := map[string]any{}
	if e.VCSLinkID != nil {
		out["vcs_link_id"] = e.VCSLinkID.String()
	}
	if e.HeadSHA != "" {
		out["head_sha"] = e.HeadSHA
	}
	if e.ExemptionType != "" {
		out["exemption_type"] = e.ExemptionType
	}
	if e.ArtifactRef != "" {
		out["artifact_ref"] = e.ArtifactRef
	}
	if e.ArtifactHash != "" {
		out["artifact_hash"] = e.ArtifactHash
	}
	return out
}

// WireEvidenceFromSnapshot derives the same object from a stored evidence
// snapshot (vcs or exemption), so an intent recorded before this contract
// still has a wire form.
func WireEvidenceFromSnapshot(kind string, snapshot json.RawMessage) (map[string]any, error) {
	var s map[string]any
	if err := json.Unmarshal(snapshot, &s); err != nil {
		return nil, err
	}
	keys := []string{"vcs_link_id", "head_sha"}
	if kind == ReceiptEvidenceExemption {
		keys = []string{"exemption_type", "artifact_ref", "artifact_hash"}
	}
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := s[k].(string); ok && v != "" {
			out[k] = v
		}
	}
	return out, nil
}

// BuildAcceptanceView is the consumer-facing acceptance record for one intent,
// read under the credential of the product-done producer. snapshot_hash covers
// every other field. The accepting actor comes from the stored intent; nothing
// in it is caller-claimed.
func BuildAcceptanceView(it *AcceptanceIntent, scopeID, producerID string, credentialActor uuid.UUID) (map[string]any, error) {
	ev, err := WireEvidenceFromSnapshot(it.EvidenceKind, it.Evidence)
	if err != nil {
		return nil, err
	}
	evHash, err := CanonicalHash(ev)
	if err != nil {
		return nil, err
	}
	var author any
	if it.ResultAuthorID != nil {
		author = it.ResultAuthorID.String()
	}
	rec := map[string]any{
		"id":                   it.ID.String(),
		"task_id":              it.TaskID.String(),
		"project_id":           it.ProjectID.String(),
		"domain":               it.Domain,
		"operation":            it.Operation,
		"class":                it.Class,
		"scope_id":             scopeID,
		"expected_status_id":   it.FromStatusID.String(),
		"expected_version":     it.FromVersion,
		"to_status_id":         it.ToStatusID.String(),
		"producer_id":          producerID,
		"credential_actor_id":  credentialActor.String(),
		"credential_client_id": nil,
		"accepting_actor_id":   it.AcceptorActorID.String(),
		"acceptor_role":        it.AcceptorRole,
		"result_author_id":     author,
		"authenticated":        true,
		"immutable":            true,
		"revoked":              false,
		"evidence":             ev,
		"evidence_hash":        evHash,
		"deploy_ref":           it.DeployRef,
		"live_ref":             it.LiveRef,
		"policy_version":       it.PolicyVersion,
		"reason":               it.Reason,
		"created_at":           it.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	hash, err := CanonicalHash(rec)
	if err != nil {
		return nil, err
	}
	rec["snapshot_hash"] = hash
	return rec, nil
}

// ProductDoneCapabilities is the discovery record the consumer checks before
// it will try a transition.
type ProductDoneCapabilities struct {
	WireContract            string  `json:"wire_contract"`
	ScopeID                 string  `json:"scope_id"`
	ProjectID               string  `json:"project_id"`
	ProducerID              string  `json:"producer_id"`
	CredentialActorID       string  `json:"credential_actor_id"`
	CredentialClientID      *string `json:"credential_client_id"`
	AuthenticatedAcceptance bool    `json:"authenticated_acceptance"`
	ImmutableReceipts       bool    `json:"immutable_receipts"`
	Enabled                 bool    `json:"enabled"`
	EnforceDone             bool    `json:"enforce_done"`
	EnforcementReady        bool    `json:"enforcement_ready"`
	PolicyVersion           int     `json:"policy_version"`
	PayloadDigestAlgorithm  string  `json:"payload_digest_algorithm"`
}

// Capabilities describes what the policy offers to one credential actor. It is
// ready only for an actor bound to a producer, in a scope that has a name.
func (p *TransitionReceiptPolicy) Capabilities(actor uuid.UUID) ProductDoneCapabilities {
	producer := p.ProducerFor(actor)
	return ProductDoneCapabilities{
		WireContract: WireContractProductDoneV1, ScopeID: p.ScopeID, ProjectID: p.ProjectID.String(),
		ProducerID: producer, CredentialActorID: actor.String(),
		AuthenticatedAcceptance: true, ImmutableReceipts: true,
		Enabled: p.Enabled, EnforceDone: p.EnforceDone,
		EnforcementReady: p.Enabled && p.EnforceDone && producer != "" && strings.TrimSpace(p.ScopeID) != "",
		PolicyVersion:    p.PolicyVersion, PayloadDigestAlgorithm: PayloadDigestAlgorithmV1,
	}
}

// ProducerFor returns the producer id bound to the actor ("" when none).
func (p *TransitionReceiptPolicy) ProducerFor(actor uuid.UUID) string {
	for _, ap := range p.AllowedProducers {
		if ap.ActorID == actor {
			return ap.ProducerID
		}
	}
	return ""
}
