package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Receipt-bound product done: the opt-in transition that commits a task to a
// done status only together with an immutable receipt naming the evidence it
// was accepted on. See docs/transition-receipts.md.

const (
	// TransitionDomainProduct is the only domain the foundation accepts. A
	// runner decision request or a gate resolution is a different domain with
	// its own receipt and must not be closed through this path.
	TransitionDomainProduct = "product"

	TransitionOpProductDone = "product_done"

	TransitionClassCode     = "code"
	TransitionClassConfig   = "config"
	TransitionClassVisible  = "visible"
	TransitionClassResearch = "research"
	TransitionClassTrivial  = "trivial"

	ReceiptEvidenceVCS       = "vcs"
	ReceiptEvidenceExemption = "exemption"

	// SourceTrustClaimed: the producer's source ref/hash comes from the request
	// and is not confirmed by a trusted registry or deploy receipt.
	SourceTrustClaimed = "claimed"
)

// TransitionReceiptPolicy is a project's opt-in. The zero value is "off".
type TransitionReceiptPolicy struct {
	ProjectID uuid.UUID `json:"project_id" db:"project_id"`
	Enabled   bool      `json:"enabled" db:"enabled"`
	// EnforceDone arms the database guard: in this project a task enters a
	// done-category status only with a receipt bound to an acceptance intent.
	// Separate from Enabled and default off.
	EnforceDone   bool `json:"enforce_done" db:"enforce_done"`
	PolicyVersion int  `json:"policy_version" db:"policy_version"`
	// ScopeID names the rollout scope the consumer pins (wire contract v1). Empty
	// until set; ScopeIDSet distinguishes "omitted, keep" from "set to empty".
	ScopeID           string            `json:"scope_id"`
	ScopeIDSet        bool              `json:"-"`
	AllowedProducers  []AllowedProducer `json:"allowed_producers"`
	AllowedAcceptors  []uuid.UUID       `json:"allowed_acceptors"`
	AllowedExemptions []string          `json:"allowed_exemptions"`
	UpdatedBy         *uuid.UUID        `json:"updated_by,omitempty" db:"updated_by"`
	UpdatedAt         time.Time         `json:"updated_at" db:"updated_at"`
}

// AllowedProducer binds a producer role to the credential actor that may use it.
type AllowedProducer struct {
	ProducerID string    `json:"producer_id"`
	ActorID    uuid.UUID `json:"actor_id"`
	// Role is "" (automatic executor) or "lead" (an explicitly audited lead/human
	// role whose own authenticated intent may stand as the acceptance).
	Role string `json:"role,omitempty"`
}

const (
	ProducerRoleLead = "lead"

	AcceptorRoleReviewer = "reviewer"
	AcceptorRoleLead     = "lead"
)

// ProducerRole returns the policy role bound to the actor ("" when none).
func (p *TransitionReceiptPolicy) ProducerRole(actor uuid.UUID) string {
	for _, ap := range p.AllowedProducers {
		if ap.ActorID == actor && ap.Role == ProducerRoleLead {
			return ProducerRoleLead
		}
	}
	return ""
}

// IsExecutor is true when the actor is bound to any producer entry.
func (p *TransitionReceiptPolicy) IsExecutor(actor uuid.UUID) bool {
	for _, ap := range p.AllowedProducers {
		if ap.ActorID == actor {
			return true
		}
	}
	return false
}

func (p *TransitionReceiptPolicy) ProducerAllowed(producerID string, actor uuid.UUID) bool {
	for _, ap := range p.AllowedProducers {
		if ap.ProducerID == producerID && ap.ActorID == actor {
			return true
		}
	}
	return false
}

func (p *TransitionReceiptPolicy) AcceptorAllowed(id uuid.UUID) bool {
	for _, a := range p.AllowedAcceptors {
		if a == id {
			return true
		}
	}
	return false
}

func (p *TransitionReceiptPolicy) ExemptionAllowed(kind string) bool {
	for _, e := range p.AllowedExemptions {
		if e == kind {
			return true
		}
	}
	return false
}

// ProductDoneEvidence is what the caller presents. For a vcs class the link and
// head are checked against the stored link; for an exemption the type, artifact
// and reason must be explicit and the policy must allow the type.
type ProductDoneEvidence struct {
	VCSLinkID *uuid.UUID `json:"vcs_link_id,omitempty"`
	HeadSHA   string     `json:"head_sha,omitempty"`

	ExemptionType string `json:"exemption_type,omitempty"`
	ArtifactRef   string `json:"artifact_ref,omitempty"`
	// ArtifactHash pins the immutable artifact version; required under enforce_done.
	ArtifactHash string `json:"artifact_hash,omitempty"`
}

// ProductDoneRequest is the body of POST /tasks/:id/transitions/product-done.
// Everything except the credential actor is caller-claimed.
type ProductDoneRequest struct {
	Domain           string    `json:"domain"`
	Class            string    `json:"class"`
	Operation        string    `json:"operation"`
	ToStatusID       uuid.UUID `json:"to_status_id"`
	ExpectedStatusID uuid.UUID `json:"expected_status_id"`
	ExpectedVersion  *int64    `json:"expected_version"`
	IdempotencyKey   string    `json:"idempotency_key"`
	ProducerID       string    `json:"producer_id"`
	ProducerSession  string    `json:"producer_session,omitempty"`
	SourceRef        string    `json:"source_ref,omitempty"`
	SourceHash       string    `json:"source_hash,omitempty"`
	AcceptingActorID uuid.UUID `json:"accepting_actor_id"`
	AcceptanceRef    string    `json:"acceptance_ref,omitempty"`
	// AcceptanceIntentID names the stored acceptance intent; required under
	// enforce_done, where it replaces trust in AcceptingActorID.
	AcceptanceIntentID *uuid.UUID          `json:"acceptance_intent_id,omitempty"`
	Reason             string              `json:"reason"`
	Evidence           ProductDoneEvidence `json:"evidence"`

	// PayloadDigestV1 is set by the handler from the raw body; never decoded.
	PayloadDigestV1 string `json:"-"`
}

// Validate checks the shape only; authorization and evidence are checked
// against stored state inside the commit transaction.
func (r *ProductDoneRequest) Validate() error {
	switch {
	case r.Domain != TransitionDomainProduct:
		return fmt.Errorf("domain must be %q", TransitionDomainProduct)
	case r.Operation != TransitionOpProductDone:
		return fmt.Errorf("operation must be %q", TransitionOpProductDone)
	case !validTransitionClass(r.Class):
		return fmt.Errorf("class must be one of code, config, visible, research, trivial")
	case r.ToStatusID == uuid.Nil:
		return fmt.Errorf("to_status_id is required")
	case r.ExpectedStatusID == uuid.Nil:
		return fmt.Errorf("expected_status_id is required")
	case r.ExpectedVersion == nil:
		return fmt.Errorf("expected_version is required")
	case strings.TrimSpace(r.IdempotencyKey) == "" || len(r.IdempotencyKey) > 200:
		return fmt.Errorf("idempotency_key is required (1-200 chars)")
	case strings.TrimSpace(r.ProducerID) == "":
		return fmt.Errorf("producer_id is required")
	case r.AcceptingActorID == uuid.Nil && strings.TrimSpace(r.AcceptanceRef) == "" && r.AcceptanceIntentID == nil:
		// The authenticated wire names only the stored intent; the acceptor is
		// derived from it by the server, never claimed by the caller.
		return fmt.Errorf("accepting_actor_id or acceptance_ref is required")
	case strings.TrimSpace(r.Reason) == "":
		return fmt.Errorf("reason is required")
	}
	if r.NeedsVCSEvidence() {
		if r.Evidence.VCSLinkID == nil || r.Evidence.HeadSHA == "" || r.Evidence.ExemptionType != "" {
			return fmt.Errorf("class %s requires evidence.vcs_link_id and evidence.head_sha, and no exemption", r.Class)
		}
		return nil
	}
	if r.Evidence.ExemptionType != r.Class || strings.TrimSpace(r.Evidence.ArtifactRef) == "" || r.Evidence.VCSLinkID != nil {
		return fmt.Errorf("class %s requires evidence.exemption_type=%s and evidence.artifact_ref", r.Class, r.Class)
	}
	return nil
}

// NeedsVCSEvidence is true for the classes that change shipped behaviour.
func (r *ProductDoneRequest) NeedsVCSEvidence() bool {
	return r.Class == TransitionClassCode || r.Class == TransitionClassConfig || r.Class == TransitionClassVisible
}

func validTransitionClass(c string) bool {
	switch c {
	case TransitionClassCode, TransitionClassConfig, TransitionClassVisible, TransitionClassResearch, TransitionClassTrivial:
		return true
	}
	return false
}

// Digest is the canonical payload digest used for idempotent replay. The
// idempotency key itself is excluded: it is the scope, not the payload.
func (r *ProductDoneRequest) Digest() string {
	c := *r
	c.IdempotencyKey = ""
	b, _ := json.Marshal(c) // struct field order is fixed, so the encoding is canonical
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TransitionReceipt is the immutable record of a committed transition.
type TransitionReceipt struct {
	ID                  uuid.UUID       `json:"id" db:"id"`
	TaskID              uuid.UUID       `json:"task_id" db:"task_id"`
	ProjectID           uuid.UUID       `json:"project_id" db:"project_id"`
	Domain              string          `json:"domain" db:"domain"`
	Class               string          `json:"class" db:"task_class"`
	Operation           string          `json:"operation" db:"operation"`
	FromStatusID        uuid.UUID       `json:"from_status_id" db:"from_status_id"`
	FromVersion         int64           `json:"from_version" db:"from_version"`
	ToStatusID          uuid.UUID       `json:"to_status_id" db:"to_status_id"`
	ToVersion           int64           `json:"to_version" db:"to_version"`
	ProducerID          string          `json:"producer_id" db:"producer_id"`
	ProducerSession     string          `json:"producer_session" db:"producer_session"`
	SourceRef           string          `json:"source_ref" db:"source_ref"`
	SourceHash          string          `json:"source_hash" db:"source_hash"`
	SourceTrust         string          `json:"source_trust" db:"source_trust"`
	CredentialActorID   uuid.UUID       `json:"credential_actor_id" db:"credential_actor_id"`
	CredentialActorType string          `json:"credential_actor_type" db:"credential_actor_type"`
	PolicyVersion       int             `json:"policy_version" db:"policy_version"`
	IdempotencyKey      string          `json:"idempotency_key" db:"idempotency_key"`
	PayloadDigest       string          `json:"payload_digest" db:"payload_digest"`
	AcceptingActorID    uuid.UUID       `json:"accepting_actor_id" db:"accepting_actor_id"`
	AcceptanceRef       string          `json:"acceptance_ref" db:"acceptance_ref"`
	AcceptanceIntentID  *uuid.UUID      `json:"acceptance_intent_id,omitempty" db:"acceptance_intent_id"`
	ActingRole          string          `json:"acting_role" db:"acting_role"`
	EvidenceKind        string          `json:"evidence_kind" db:"evidence_kind"`
	Evidence            json.RawMessage `json:"evidence" db:"evidence"`
	EvidenceHash        string          `json:"evidence_hash" db:"evidence_hash"`
	Reason              string          `json:"reason" db:"reason"`
	CreatedAt           time.Time       `json:"created_at" db:"created_at"`

	// Wire contract v1 (additive). Set when the receipt consumed an intent.
	ScopeID                string          `json:"scope_id" db:"scope_id"`
	AcceptanceHash         string          `json:"acceptance_hash" db:"acceptance_hash"`
	PayloadDigestV1        string          `json:"payload_digest_v1" db:"payload_digest_v1"`
	PayloadDigestAlgorithm string          `json:"payload_digest_algorithm" db:"payload_digest_algorithm"`
	WireEvidence           json.RawMessage `json:"-" db:"wire_evidence"`
	WireEvidenceHash       string          `json:"-" db:"wire_evidence_hash"`
}

// MarshalJSON keeps the legacy shape for receipts without a wire form. For a
// receipt that has one, evidence/evidence_hash are the request-shaped evidence
// the consumer sent and the stored snapshot moves to evidence_snapshot*.
func (r TransitionReceipt) MarshalJSON() ([]byte, error) {
	type plain TransitionReceipt
	if len(r.WireEvidence) == 0 || string(r.WireEvidence) == "{}" {
		// Shallower fields shadow the embedded wire-only ones and are omitted
		// when empty, so a legacy receipt serialises exactly as before.
		return json.Marshal(struct {
			plain
			ScopeID                string `json:"scope_id,omitempty"`
			AcceptanceHash         string `json:"acceptance_hash,omitempty"`
			PayloadDigestV1        string `json:"payload_digest_v1,omitempty"`
			PayloadDigestAlgorithm string `json:"payload_digest_algorithm,omitempty"`
		}{plain: plain(r)})
	}
	return json.Marshal(struct {
		plain
		Evidence           json.RawMessage `json:"evidence"`
		EvidenceHash       string          `json:"evidence_hash"`
		EvidenceSnapshot   json.RawMessage `json:"evidence_snapshot"`
		EvidenceSnapshotH  string          `json:"evidence_snapshot_hash"`
		CredentialClientID *string         `json:"credential_client_id"`
	}{plain: plain(r), Evidence: r.WireEvidence, EvidenceHash: r.WireEvidenceHash,
		EvidenceSnapshot: r.Evidence, EvidenceSnapshotH: r.EvidenceHash})
}

// TransitionCommitInput is the repository-side command: the request plus what
// the server derived from auth.
type TransitionCommitInput struct {
	TaskID    uuid.UUID
	Request   ProductDoneRequest
	ActorID   uuid.UUID
	ActorType ActorType
	// DoDBlocking names the required DoD gates that have not passed. The store
	// calls it inside the commit transaction with the project settings read
	// under a share lock and the locked task's checks, so a settings change
	// cannot slip in between the gate evaluation and the commit. A non-empty
	// result refuses the commit.
	DoDBlocking func(settings ProjectSettings, checks DodChecks) []string
}

// TransitionRefusal is a typed business refusal; Code is stable for clients.
type TransitionRefusal struct {
	Code    string
	Message string
}

func (e *TransitionRefusal) Error() string { return e.Code + ": " + e.Message }

// ReceiptKeyConflict: the idempotency key was used with a different payload.
var ErrReceiptKeyConflict = &TransitionRefusal{Code: "idempotency_key_payload_mismatch", Message: "idempotency_key was already used with a different payload"}

// AcceptanceIntentRequest is what an accepting identity posts under its own
// credential. It names the exact result it accepts; the server recomputes the
// evidence snapshot from stored state and pins its hash.
type AcceptanceIntentRequest struct {
	Domain           string              `json:"domain"`
	Class            string              `json:"class"`
	Operation        string              `json:"operation"`
	ToStatusID       uuid.UUID           `json:"to_status_id"`
	ExpectedStatusID uuid.UUID           `json:"expected_status_id"`
	ExpectedVersion  *int64              `json:"expected_version"`
	Evidence         ProductDoneEvidence `json:"evidence"`
	DeployRef        string              `json:"deploy_ref,omitempty"`
	LiveRef          string              `json:"live_ref,omitempty"`
	Reason           string              `json:"reason"`
}

// Validate checks the shape; it reuses the product-done request rules so the
// intent and the transition cannot disagree about what is valid evidence.
func (r *AcceptanceIntentRequest) Validate() error {
	probe := ProductDoneRequest{
		Domain: r.Domain, Class: r.Class, Operation: r.Operation, ToStatusID: r.ToStatusID,
		ExpectedStatusID: r.ExpectedStatusID, ExpectedVersion: r.ExpectedVersion, IdempotencyKey: "intent",
		ProducerID: "intent", AcceptingActorID: uuid.New(), Reason: r.Reason, Evidence: r.Evidence,
	}
	return probe.Validate()
}

// AcceptanceIntent is the immutable, authenticated acceptance record.
type AcceptanceIntent struct {
	ID                uuid.UUID       `json:"id" db:"id"`
	TaskID            uuid.UUID       `json:"task_id" db:"task_id"`
	ProjectID         uuid.UUID       `json:"project_id" db:"project_id"`
	Domain            string          `json:"domain" db:"domain"`
	Class             string          `json:"class" db:"task_class"`
	Operation         string          `json:"operation" db:"operation"`
	FromStatusID      uuid.UUID       `json:"from_status_id" db:"from_status_id"`
	FromVersion       int64           `json:"from_version" db:"from_version"`
	ToStatusID        uuid.UUID       `json:"to_status_id" db:"to_status_id"`
	EvidenceKind      string          `json:"evidence_kind" db:"evidence_kind"`
	Evidence          json.RawMessage `json:"evidence" db:"evidence"`
	EvidenceHash      string          `json:"evidence_hash" db:"evidence_hash"`
	DeployRef         string          `json:"deploy_ref" db:"deploy_ref"`
	LiveRef           string          `json:"live_ref" db:"live_ref"`
	ResultAuthorID    *uuid.UUID      `json:"result_author_id,omitempty" db:"result_author_id"`
	AcceptorActorID   uuid.UUID       `json:"acceptor_actor_id" db:"acceptor_actor_id"`
	AcceptorActorType string          `json:"acceptor_actor_type" db:"acceptor_actor_type"`
	AcceptorRole      string          `json:"acceptor_role" db:"acceptor_role"`
	PolicyVersion     int             `json:"policy_version" db:"policy_version"`
	Reason            string          `json:"reason" db:"reason"`
	CreatedAt         time.Time       `json:"created_at" db:"created_at"`
}

// AcceptanceIntentInput is the repository-side command for creating an intent.
type AcceptanceIntentInput struct {
	TaskID    uuid.UUID
	Request   AcceptanceIntentRequest
	ActorID   uuid.UUID
	ActorType ActorType
}
