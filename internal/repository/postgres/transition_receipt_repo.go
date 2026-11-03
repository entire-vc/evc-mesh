package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TransitionReceiptRepo commits the receipt-bound product done transition.
// Validation that depends on stored state runs here, under the task row lock,
// so the state change, receipt, activity and outbox share one commit.
type TransitionReceiptRepo struct{ db *sqlx.DB }

func NewTransitionReceiptRepo(db *sqlx.DB) *TransitionReceiptRepo {
	return &TransitionReceiptRepo{db: db}
}

type policyRow struct {
	ProjectID         uuid.UUID       `db:"project_id"`
	Enabled           bool            `db:"enabled"`
	EnforceDone       bool            `db:"enforce_done"`
	PolicyVersion     int             `db:"policy_version"`
	ScopeID           string          `db:"scope_id"`
	AllowedProducers  json.RawMessage `db:"allowed_producers"`
	AllowedAcceptors  pq.StringArray  `db:"allowed_acceptors"`
	AllowedExemptions pq.StringArray  `db:"allowed_exemptions"`
	UpdatedBy         *uuid.UUID      `db:"updated_by"`
	UpdatedAt         time.Time       `db:"updated_at"`
}

const policyCols = `project_id, enabled, enforce_done, policy_version, scope_id, allowed_producers, allowed_acceptors::text[] AS allowed_acceptors, allowed_exemptions, updated_by, updated_at`

func (p policyRow) toDomain() (*domain.TransitionReceiptPolicy, error) {
	out := &domain.TransitionReceiptPolicy{
		ProjectID: p.ProjectID, Enabled: p.Enabled, EnforceDone: p.EnforceDone, PolicyVersion: p.PolicyVersion, ScopeID: p.ScopeID,
		AllowedExemptions: []string(p.AllowedExemptions), UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt,
		AllowedProducers: []domain.AllowedProducer{}, AllowedAcceptors: []uuid.UUID{},
	}
	if err := json.Unmarshal(p.AllowedProducers, &out.AllowedProducers); err != nil {
		return nil, err
	}
	for _, s := range p.AllowedAcceptors {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, err
		}
		out.AllowedAcceptors = append(out.AllowedAcceptors, id)
	}
	return out, nil
}

// GetPolicy returns the project's policy; a project without a row is policy off.
func (r *TransitionReceiptRepo) GetPolicy(ctx context.Context, projectID uuid.UUID) (*domain.TransitionReceiptPolicy, error) {
	return getPolicy(ctx, r.db, projectID, "")
}

// getPolicy reads the project's policy; lock is "" or " FOR SHARE". The commit
// path takes the share lock so a concurrent policy update (disable, producer
// removal) waits for the commit instead of landing between the check and it.
func getPolicy(ctx context.Context, q sqlx.QueryerContext, projectID uuid.UUID, lock string) (*domain.TransitionReceiptPolicy, error) {
	var row policyRow
	err := sqlx.GetContext(ctx, q, &row, `SELECT `+policyCols+` FROM transition_receipt_policies WHERE project_id=$1`+lock, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return &domain.TransitionReceiptPolicy{ProjectID: projectID, PolicyVersion: 1,
			AllowedProducers: []domain.AllowedProducer{}, AllowedAcceptors: []uuid.UUID{}, AllowedExemptions: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain()
}

// UpsertPolicy replaces the policy and advances policy_version on every change.
func (r *TransitionReceiptRepo) UpsertPolicy(ctx context.Context, p *domain.TransitionReceiptPolicy, actor uuid.UUID) (*domain.TransitionReceiptPolicy, error) {
	producers, err := json.Marshal(p.AllowedProducers)
	if err != nil {
		return nil, err
	}
	acceptors := make([]string, len(p.AllowedAcceptors))
	for i, a := range p.AllowedAcceptors {
		acceptors[i] = a.String()
	}
	exemptions := p.AllowedExemptions
	if exemptions == nil {
		exemptions = []string{}
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Same key as enforce_product_done_receipt(): a done write in flight on a
	// project with no policy row yet holds this shared, so the first-time
	// enable waits for it instead of committing between its check and commit.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('transition_receipt_policy:' || $1::text, 0))`, p.ProjectID.String()); err != nil {
		return nil, err
	}
	var row policyRow
	// scope_id: omitted in the request keeps the stored value ($8 NULL).
	var scope *string
	if p.ScopeIDSet {
		scope = &p.ScopeID
	}
	err = tx.GetContext(ctx, &row, `INSERT INTO transition_receipt_policies(project_id, enabled, enforce_done, allowed_producers, allowed_acceptors, allowed_exemptions, updated_by, scope_id)
 VALUES($1,$2,$7,$3,$4::uuid[],$5,$6,COALESCE($8,''))
 ON CONFLICT (project_id) DO UPDATE SET enabled=EXCLUDED.enabled, enforce_done=EXCLUDED.enforce_done, allowed_producers=EXCLUDED.allowed_producers,
  scope_id=COALESCE($8, transition_receipt_policies.scope_id),
  allowed_acceptors=EXCLUDED.allowed_acceptors, allowed_exemptions=EXCLUDED.allowed_exemptions,
  updated_by=EXCLUDED.updated_by, updated_at=now(), policy_version=transition_receipt_policies.policy_version+1
 RETURNING `+policyCols, p.ProjectID, p.Enabled, producers, pq.Array(acceptors), pq.Array(exemptions), actor, p.EnforceDone, scope)
	if err != nil {
		return nil, err
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, cerr
	}
	return row.toDomain()
}

// ListReceipts returns a task's receipts, oldest first.
func (r *TransitionReceiptRepo) ListReceipts(ctx context.Context, taskID uuid.UUID) ([]domain.TransitionReceipt, error) {
	out := []domain.TransitionReceipt{}
	err := r.db.SelectContext(ctx, &out, `SELECT `+receiptCols+` FROM task_transition_receipts WHERE task_id=$1 ORDER BY created_at, id`, taskID)
	return out, err
}

const receiptCols = `id, task_id, project_id, domain, task_class, operation, from_status_id, from_version, to_status_id, to_version,
 producer_id, producer_session, source_ref, source_hash, source_trust, credential_actor_id, credential_actor_type, policy_version,
 idempotency_key, payload_digest, accepting_actor_id, acceptance_ref, acceptance_intent_id, acting_role, evidence_kind, evidence, evidence_hash, reason, created_at,
 scope_id, acceptance_hash, payload_digest_v1, payload_digest_algorithm, wire_evidence, wire_evidence_hash`

func refuse(code, msg string) error { return &domain.TransitionRefusal{Code: code, Message: msg} }

// vcsSnapshot is the evidence stored in the receipt. Field order is fixed: the
// hash is taken over its JSON encoding.
type vcsSnapshot struct {
	LinkID     uuid.UUID `json:"vcs_link_id"`
	Provider   string    `json:"provider"`
	ExternalID string    `json:"external_id"`
	URL        string    `json:"url"`
	Status     string    `json:"status"`
	HeadSHA    string    `json:"head_sha"`
	ShipSHA    string    `json:"ship_sha"`
	// ShipTrust is always "claimed": ship_sha is writable by any task updater
	// through the metadata endpoint, so it is a consistency check against the
	// head and not proof of an independent verdict. That proof is the gate
	// receipt (later unit); here independence rests on the policy allow-lists.
	ShipTrust string `json:"ship_trust"`
}

type exemptionSnapshot struct {
	Type         string `json:"exemption_type"`
	ArtifactRef  string `json:"artifact_ref"`
	ArtifactHash string `json:"artifact_hash,omitempty"`
	Reason       string `json:"reason"`
}

// CommitProductDone validates and commits the transition. replayed is true when
// an earlier identical request already committed: that receipt is returned and
// nothing is written.
func (r *TransitionReceiptRepo) CommitProductDone(ctx context.Context, in domain.TransitionCommitInput) (receipt *domain.TransitionReceipt, replayed bool, err error) {
	req := in.Request
	// The foundation digest is taken over the request as received, before the
	// intent id is derived from acceptance_ref below.
	legacyDigest := req.Digest()
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	task, err := lockParkedTask(ctx, tx, in.TaskID)
	if err != nil {
		return nil, false, err
	}
	policy, err := getPolicy(ctx, tx, task.ProjectID, " FOR SHARE")
	if err != nil {
		return nil, false, err
	}
	if !policy.Enabled {
		return nil, false, refuse("transition_policy_disabled", "receipt-bound product done is not enabled for this project")
	}
	if !policy.ProducerAllowed(req.ProducerID, in.ActorID) {
		return nil, false, refuse("producer_not_authorized", "producer_id is not allowed for the calling credential")
	}

	// Replay precedes the CAS: the same request must answer with the same
	// receipt even though the commit it made advanced the version.
	var prior domain.TransitionReceipt
	err = tx.GetContext(ctx, &prior, `SELECT `+receiptCols+` FROM task_transition_receipts
 WHERE task_id=$1 AND operation=$2 AND producer_id=$3 AND idempotency_key=$4`, task.ID, req.Operation, req.ProducerID, req.IdempotencyKey)
	if err == nil {
		if prior.PayloadDigest != legacyDigest ||
			(prior.PayloadDigestV1 != "" && req.PayloadDigestV1 != "" && prior.PayloadDigestV1 != req.PayloadDigestV1) {
			return nil, false, domain.ErrReceiptKeyConflict
		}
		return &prior, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	if task.Version != *req.ExpectedVersion || task.StatusID != req.ExpectedStatusID {
		return nil, false, &domain.TaskConflict{CurrentVersion: task.Version, CurrentStatusID: task.StatusID, CurrentUpdatedAt: task.UpdatedAt, CurrentAssigneeID: task.AssigneeID}
	}
	if task.HumanGate {
		return nil, false, refuse("human_gate_active", "the task has an active human gate")
	}
	var waiting bool
	if werr := tx.GetContext(ctx, &waiting, `SELECT EXISTS(SELECT 1 FROM parked_waits WHERE task_id=$1 AND release_id IS NULL)`, task.ID); werr != nil {
		return nil, false, werr
	}
	if waiting {
		return nil, false, refuse("parked_wait_active", "the task has a registered wait")
	}
	if in.DoDBlocking != nil {
		// FOR SHARE holds the project row until commit: a concurrent settings
		// update waits instead of adding a required gate after this check.
		// Unreadable settings refuse the commit (fail closed).
		var raw []byte
		if gerr := tx.GetContext(ctx, &raw, `SELECT settings FROM projects WHERE id=$1 FOR SHARE`, task.ProjectID); gerr != nil {
			return nil, false, gerr
		}
		var settings domain.ProjectSettings
		if len(raw) > 0 {
			if jerr := json.Unmarshal(raw, &settings); jerr != nil {
				return nil, false, jerr
			}
		}
		if blocking := in.DoDBlocking(settings, task.DodChecks); len(blocking) > 0 {
			return nil, false, refuse("dod_gate_blocked", fmt.Sprintf("required DoD gates not passed: %v", blocking))
		}
	}
	var intent *domain.AcceptanceIntent
	// The acceptor comes from a stored intent when enforcement is on, or when the
	// caller names no acceptor at all (authenticated wire): never a claim.
	if policy.EnforceDone || req.AcceptingActorID == uuid.Nil {
		if req.AcceptanceIntentID == nil {
			if id, perr := uuid.Parse(strings.TrimSpace(req.AcceptanceRef)); perr == nil {
				req.AcceptanceIntentID = &id
			}
		} else if ref := strings.TrimSpace(req.AcceptanceRef); ref != "" {
			// A free-form legacy ref is just a note; a UUID ref must name the same intent.
			if id, perr := uuid.Parse(ref); perr == nil && id != *req.AcceptanceIntentID {
				return nil, false, refuse("acceptance_ref_mismatch", "acceptance_ref and acceptance_intent_id name different intents")
			}
		}
		in.Request = req
		if intent, err = loadIntentForCommit(ctx, tx, task, policy, in); err != nil {
			return nil, false, err
		}
	} else {
		if !policy.AcceptorAllowed(req.AcceptingActorID) {
			return nil, false, refuse("acceptor_not_authorized", "accepting_actor_id is not an allowed accepting identity")
		}
		if req.AcceptingActorID == in.ActorID {
			return nil, false, refuse("acceptor_not_independent", "the accepting identity must differ from the credential actor")
		}
	}

	var target struct {
		ProjectID uuid.UUID `db:"project_id"`
		Category  string    `db:"category"`
	}
	if err = tx.GetContext(ctx, &target, `SELECT project_id, category FROM task_statuses WHERE id=$1`, req.ToStatusID); errors.Is(err, sql.ErrNoRows) {
		return nil, false, apierror.NotFound("TaskStatus")
	} else if err != nil {
		return nil, false, err
	}
	if target.ProjectID != task.ProjectID || target.Category != string(domain.StatusCategoryDone) {
		return nil, false, refuse("target_not_done_status", "to_status_id must be a done-category status of the task's project")
	}

	evidenceKind, evidence, err := r.buildEvidence(ctx, tx, task.ID, req, policy)
	if err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256(evidence)
	evidenceHash := hex.EncodeToString(sum[:])
	if intent != nil && intent.EvidenceHash != evidenceHash {
		return nil, false, refuse("acceptance_evidence_mismatch", "the evidence changed after the acceptance intent was recorded")
	}

	// Wire contract v1: the request-shaped evidence must be exactly what the
	// intent pinned, and the acceptance snapshot hash is recomputed from the
	// stored intent (same function the consumer read it through).
	var wireEvidence json.RawMessage
	var wireEvidenceHash, acceptanceHash string
	if intent != nil {
		want, werr := domain.WireEvidenceFromSnapshot(intent.EvidenceKind, intent.Evidence)
		if werr != nil {
			return nil, false, werr
		}
		got := domain.WireEvidenceFromRequest(req.Evidence)
		wantB, _ := domain.CanonicalJSON(want)
		gotB, _ := domain.CanonicalJSON(got)
		if !bytes.Equal(wantB, gotB) {
			return nil, false, refuse("acceptance_evidence_mismatch", "the request evidence differs from the evidence the intent accepted")
		}
		wireEvidence = gotB
		if wireEvidenceHash, err = domain.CanonicalHash(got); err != nil {
			return nil, false, err
		}
		view, verr := domain.BuildAcceptanceView(intent, policy.ScopeID, req.ProducerID, in.ActorID)
		if verr != nil {
			return nil, false, verr
		}
		acceptanceHash, _ = view["snapshot_hash"].(string)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	var newVersion int64
	err = tx.GetContext(ctx, &newVersion, `UPDATE tasks SET status_id=$2, completed_at=$3, status_changed_at=$3, updated_at=$3,
 checked_out_by=NULL, checkout_token=NULL, checkout_expires=NULL, checkout_acquired_at=NULL
 WHERE id=$1 AND deleted_at IS NULL AND version=$4 AND status_id=$5 RETURNING version`,
		task.ID, req.ToStatusID, now, task.Version, task.StatusID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, &domain.TaskConflict{CurrentVersion: task.Version, CurrentStatusID: task.StatusID, CurrentUpdatedAt: task.UpdatedAt, CurrentAssigneeID: task.AssigneeID}
	}
	if err != nil {
		return nil, false, err
	}

	rc := domain.TransitionReceipt{
		ID: uuid.New(), TaskID: task.ID, ProjectID: task.ProjectID, Domain: req.Domain, Class: req.Class, Operation: req.Operation,
		FromStatusID: task.StatusID, FromVersion: task.Version, ToStatusID: req.ToStatusID, ToVersion: newVersion,
		ProducerID: req.ProducerID, ProducerSession: req.ProducerSession, SourceRef: req.SourceRef, SourceHash: req.SourceHash,
		SourceTrust: domain.SourceTrustClaimed, CredentialActorID: in.ActorID, CredentialActorType: string(in.ActorType),
		PolicyVersion: policy.PolicyVersion, IdempotencyKey: req.IdempotencyKey, PayloadDigest: legacyDigest,
		AcceptingActorID: req.AcceptingActorID, AcceptanceRef: req.AcceptanceRef, ActingRole: "producer",
		EvidenceKind: evidenceKind, Evidence: evidence, EvidenceHash: evidenceHash, Reason: req.Reason, CreatedAt: now,
	}
	if intent != nil {
		rc.ScopeID, rc.AcceptanceHash, rc.PayloadDigestV1, rc.PayloadDigestAlgorithm = policy.ScopeID, acceptanceHash, req.PayloadDigestV1, domain.PayloadDigestAlgorithmV1
		rc.WireEvidence, rc.WireEvidenceHash = wireEvidence, wireEvidenceHash
		rc.AcceptingActorID, rc.AcceptanceIntentID = intent.AcceptorActorID, &intent.ID
		if policy.ProducerRole(in.ActorID) == domain.ProducerRoleLead {
			rc.ActingRole = domain.ProducerRoleLead
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_transition_receipts(id, task_id, project_id, domain, task_class, operation, from_status_id, from_version,
 to_status_id, to_version, producer_id, producer_session, source_ref, source_hash, source_trust, credential_actor_id, credential_actor_type,
 policy_version, idempotency_key, payload_digest, accepting_actor_id, acceptance_ref, evidence_kind, evidence, evidence_hash, reason, created_at,
 acceptance_intent_id, acting_role, scope_id, acceptance_hash, payload_digest_v1, payload_digest_algorithm, wire_evidence, wire_evidence_hash)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35)`,
		rc.ID, rc.TaskID, rc.ProjectID, rc.Domain, rc.Class, rc.Operation, rc.FromStatusID, rc.FromVersion, rc.ToStatusID, rc.ToVersion,
		rc.ProducerID, rc.ProducerSession, rc.SourceRef, rc.SourceHash, rc.SourceTrust, rc.CredentialActorID, rc.CredentialActorType,
		rc.PolicyVersion, rc.IdempotencyKey, rc.PayloadDigest, rc.AcceptingActorID, rc.AcceptanceRef, rc.EvidenceKind, rc.Evidence, rc.EvidenceHash, rc.Reason, rc.CreatedAt, rc.AcceptanceIntentID, rc.ActingRole,
		rc.ScopeID, rc.AcceptanceHash, rc.PayloadDigestV1, rc.PayloadDigestAlgorithm, wireJSONOrNull(rc.WireEvidence), rc.WireEvidenceHash)
	if err != nil {
		return nil, false, fmt.Errorf("transition receipt: %w", err)
	}

	task.StatusID, task.UpdatedAt = req.ToStatusID, now
	audit := &domain.TransitionAudit{
		ActorID: in.ActorID, ActorType: in.ActorType, Source: "transition_receipt", Reason: req.Reason,
		Entries: []domain.TaskAuditEntry{{Action: "task.moved", Changes: map[string]any{
			"status":  map[string]any{"old": rc.FromStatusID, "new": rc.ToStatusID},
			"receipt": map[string]any{"id": rc.ID, "operation": rc.Operation, "class": rc.Class, "evidence_hash": rc.EvidenceHash, "policy_version": rc.PolicyVersion},
		}}},
	}
	if err := writeTransitionAudit(ctx, tx, task, task.Version, newVersion, audit); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &rc, false, nil
}

// buildEvidence reads the evidence from stored state, never from the request's
// description of it, and returns the canonical snapshot.
func (r *TransitionReceiptRepo) buildEvidence(ctx context.Context, tx *sqlx.Tx, taskID uuid.UUID, req domain.ProductDoneRequest, policy *domain.TransitionReceiptPolicy) (string, json.RawMessage, error) {
	if !req.NeedsVCSEvidence() {
		if !policy.ExemptionAllowed(req.Evidence.ExemptionType) {
			return "", nil, refuse("exemption_not_allowed", "the project policy does not allow this exemption type")
		}
		if policy.EnforceDone && strings.TrimSpace(req.Evidence.ArtifactHash) == "" {
			return "", nil, refuse("evidence_artifact_hash_required", "an exemption needs the immutable artifact hash under enforce_done")
		}
		b, err := json.Marshal(exemptionSnapshot{Type: req.Evidence.ExemptionType, ArtifactRef: req.Evidence.ArtifactRef, ArtifactHash: req.Evidence.ArtifactHash, Reason: req.Reason})
		return domain.ReceiptEvidenceExemption, b, err
	}
	var link struct {
		Provider   string          `db:"provider"`
		LinkType   string          `db:"link_type"`
		ExternalID string          `db:"external_id"`
		URL        string          `db:"url"`
		Status     string          `db:"status"`
		Metadata   json.RawMessage `db:"metadata"`
	}
	err := tx.GetContext(ctx, &link, `SELECT provider, link_type, external_id, url, status, metadata FROM vcs_links WHERE id=$1 AND task_id=$2`, *req.Evidence.VCSLinkID, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, refuse("evidence_missing", "vcs_link_id is not a link of this task")
	}
	if err != nil {
		return "", nil, err
	}
	var meta map[string]any
	_ = json.Unmarshal(link.Metadata, &meta)
	head, _ := meta[domain.VCSLinkMetaHeadSHA].(string)
	ship, _ := meta[domain.VCSLinkMetaShipSHA].(string)
	switch {
	case link.LinkType != string(domain.VCSLinkTypePR):
		return "", nil, refuse("evidence_not_pr", "the evidence link is not a pr/mr link")
	case link.Status != string(domain.VCSLinkStatusMerged):
		return "", nil, refuse("evidence_not_merged", "the evidence link is not merged")
	case head == "":
		return "", nil, refuse("evidence_missing_head", "the evidence link has no recorded head_sha")
	case head != req.Evidence.HeadSHA:
		return "", nil, refuse("evidence_head_changed", "evidence.head_sha does not match the link's current head_sha")
	case ship != head:
		return "", nil, refuse("evidence_not_shipped", "the link has no ship_sha recorded for this exact head")
	}
	b, err := json.Marshal(vcsSnapshot{LinkID: *req.Evidence.VCSLinkID, Provider: link.Provider, ExternalID: link.ExternalID, URL: link.URL, Status: link.Status, HeadSHA: head, ShipSHA: ship, ShipTrust: domain.SourceTrustClaimed})
	return domain.ReceiptEvidenceVCS, b, err
}

// wireJSONOrNull stores an absent wire evidence as the empty object default.
func wireJSONOrNull(b json.RawMessage) any {
	if len(b) == 0 {
		return []byte("{}")
	}
	return []byte(b)
}
