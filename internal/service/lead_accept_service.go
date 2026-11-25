package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// LeadAcceptStore is what the lead-accept context needs from storage
// (postgres.LeadAcceptRepo).
type LeadAcceptStore interface {
	PrincipalByTokenHash(ctx context.Context, kind, tokenHash string, now time.Time) (*domain.LeadAcceptPrincipal, error)
	AcceptObservation(ctx context.Context, principalID uuid.UUID, a *domain.LeadAcceptAttestation, now func() time.Time) (*postgres.LeadAcceptObservation, error)
	CurrentObservation(ctx context.Context) (*postgres.LeadAcceptObservation, error)
	TaskScope(ctx context.Context, taskID uuid.UUID) (*postgres.LeadAcceptTaskScope, error)
	Comment(ctx context.Context, id uuid.UUID) (*postgres.LeadAcceptComment, error)
	Artifact(ctx context.Context, id uuid.UUID) (*postgres.LeadAcceptArtifact, error)
	GateIntakeReceipts(ctx context.Context, approvalID uuid.UUID) ([]postgres.LeadAcceptReceipt, error)
}

// LeadAcceptObjects reads artifact bytes (storage.S3Client). GetObject stats
// eagerly, so a missing object is an error here, not a short read.
type LeadAcceptObjects interface {
	GetObject(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error)
}

// LeadAcceptService serves the lead-accept context (contract CH→Mesh
// lead-accept-context/v1, docs/lead-accept-context.md). It only reads: it
// never changes an approval, a task, a comment or an artifact, never merges
// and never publishes. The one thing it writes is an accepted registry
// observation of a publisher.
type LeadAcceptService struct {
	store   LeadAcceptStore
	objects LeadAcceptObjects
	now     func() time.Time
}

// NewLeadAcceptService creates a LeadAcceptService. objects may be nil (no
// object storage): an artifact evidence is then 503 evidence_unavailable.
// now nil means time.Now.
func NewLeadAcceptService(store LeadAcceptStore, objects LeadAcceptObjects, now func() time.Time) *LeadAcceptService {
	if now == nil {
		now = time.Now
	}
	return &LeadAcceptService{store: store, objects: objects, now: now}
}

// AuthenticateLeadAccept resolves a token to its live principal. Every token
// failure is the same domain.ErrLeadAcceptUnauthorized; a storage failure is
// returned as is.
func (s *LeadAcceptService) AuthenticateLeadAccept(ctx context.Context, token string) (*domain.LeadAcceptPrincipal, error) {
	if !domain.LeadAcceptTokenWellFormed(token) {
		return nil, domain.ErrLeadAcceptUnauthorized
	}
	p, err := s.store.PrincipalByTokenHash(ctx, domain.LeadAcceptTokenKind(token), domain.HashGateIntakeToken(token), s.now())
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, domain.ErrLeadAcceptUnauthorized
	}
	return p, nil
}

// LeadAcceptAttestationReceipt is the 201 body of an accepted attestation.
type LeadAcceptAttestationReceipt struct {
	ObservationID string `json:"observation_id"`
	ReceivedAt    string `json:"received_at"`
	InSync        bool   `json:"in_sync"`
	OK            bool   `json:"ok"`
}

// Attest accepts one observation of publisher p, or refuses it with a 409.
func (s *LeadAcceptService) Attest(ctx context.Context, p *domain.LeadAcceptPrincipal, a *domain.LeadAcceptAttestation) (*LeadAcceptAttestationReceipt, error) {
	if p.Kind != domain.LeadAcceptKindPublisher {
		return nil, leadAcceptErr(http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden, "only a publisher may attest", nil)
	}
	o, err := s.store.AcceptObservation(ctx, p.ID, a, s.now)
	if err != nil {
		var ref *postgres.LeadAcceptRefusal
		if errors.As(err, &ref) {
			return nil, leadAcceptErr(http.StatusConflict, ref.Code, ref.Message, ref.Context)
		}
		return nil, err
	}
	return &LeadAcceptAttestationReceipt{ObservationID: o.ObservationID.String(),
		ReceivedAt: domain.LeadAcceptTime(o.ReceivedAt), InSync: o.InSync, OK: o.OK}, nil
}

func leadAcceptErr(status int, code, msg string, ctx map[string]any) *apierror.Error {
	return &apierror.Error{Code: status, ErrorCode: code, Message: msg, Context: ctx}
}

type leadAcceptEvidenceRec struct {
	ref      domain.LeadAcceptEvidenceRef
	scope    *postgres.LeadAcceptTaskScope
	comment  *postgres.LeadAcceptComment
	artifact *postgres.LeadAcceptArtifact
	bytes    []byte
}

// Context answers GET /api/v1/lead-accept-context for reader p and the
// validated request req. Checks run in the order of the contract's refusal
// table (LA6) and the first refusal is returned as an *apierror.Error. Nothing
// is cached: every call reads the current observation, every evidence record
// and every artifact's bytes again.
func (s *LeadAcceptService) Context(ctx context.Context, p *domain.LeadAcceptPrincipal, req *domain.LeadAcceptRequest) (*domain.LeadAcceptContext, error) {
	if p.Kind != domain.LeadAcceptKindReader || p.BindingSHA256 == nil || p.WorkspaceID == nil || p.EvidenceProjectID == nil {
		return nil, leadAcceptErr(http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden, "this credential does not read the lead-accept context", nil)
	}
	reqSHA := req.SHA256()
	// The binding check precedes every read of registry or evidence, and does
	// not say which field differs.
	if reqSHA != *p.BindingSHA256 {
		return nil, leadAcceptErr(http.StatusForbidden, domain.LeadAcceptCodeScopeForbidden,
			"the request is outside the scope bound to this credential", nil)
	}

	// Evidence scope: the task must exist (not deleted) inside the bound
	// evidence project and workspace. A missing task and a foreign one read
	// the same, so the answer is no oracle of existence.
	refs := req.EvidenceRefs()
	recs := make([]*leadAcceptEvidenceRec, 0, len(refs))
	for _, ref := range refs {
		sc, err := s.store.TaskScope(ctx, ref.TaskID)
		if err != nil {
			return nil, err
		}
		if sc == nil || sc.ProjectID != *p.EvidenceProjectID || sc.WorkspaceID != *p.WorkspaceID {
			return nil, leadAcceptErr(http.StatusForbidden, domain.LeadAcceptCodeEvidenceScope,
				"an evidence ref is outside the evidence scope of this credential", map[string]any{"ref": ref.Ref})
		}
		recs = append(recs, &leadAcceptEvidenceRec{ref: ref, scope: sc})
	}

	// The answer rests on one observation, current both before the evidence is
	// read and when the answer is built: an observation accepted meanwhile (a
	// revoked lead, a failed source) or the TTL running out during the reads
	// makes the whole evaluation run again on the new state.
	for attempt := 0; attempt < 3; attempt++ {
		out, changed, err := s.evaluate(ctx, req, reqSHA, recs)
		if err != nil || !changed {
			return out, err
		}
	}
	return nil, leadAcceptErr(http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryUnavailable,
		"the registry observation kept changing while the context was read", nil)
}

// registryState is the current observation and what the request takes from it.
type registryState struct {
	obs  *postgres.LeadAcceptObservation
	att  domain.LeadAcceptAttestation
	pol  domain.LeadAcceptProjectPolicy
	lead domain.LeadAcceptLead
}

// registry applies the registry and policy refusals (LA6) at now.
func (s *LeadAcceptService) registry(ctx context.Context, req *domain.LeadAcceptRequest, now time.Time) (*registryState, error) {
	obs, err := s.store.CurrentObservation(ctx)
	if err != nil {
		return nil, err
	}
	if obs == nil {
		return nil, leadAcceptErr(http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryUnavailable,
			"no registry observation has been accepted", nil)
	}
	obsCtx := map[string]any{"observation_id": obs.ObservationID.String(), "observed_at": domain.LeadAcceptTime(obs.ObservedAt)}
	if !obs.OK {
		if obs.ErrorStep != nil {
			obsCtx["error_step"] = *obs.ErrorStep
		}
		return nil, leadAcceptErr(http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistrySourceError,
			"the latest registry observation failed", obsCtx)
	}
	if now.Sub(obs.ObservedAt) > domain.LeadAcceptMaxAge {
		obsCtx["fresh_until"] = domain.LeadAcceptTime(obs.ObservedAt.Add(domain.LeadAcceptMaxAge))
		return nil, leadAcceptErr(http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryStale,
			"the latest registry observation is older than 150 s", obsCtx)
	}
	if !obs.InSync {
		return nil, leadAcceptErr(http.StatusServiceUnavailable, domain.LeadAcceptCodeRegistryOutOfSync,
			"the gate checkout differs from origin/main", obsCtx)
	}
	var att domain.LeadAcceptAttestation
	if err := json.Unmarshal(obs.Body, &att); err != nil {
		return nil, fmt.Errorf("lead accept: stored observation %s: %w", obs.ObservationID, err)
	}
	pol, ok := att.Projects[req.Project]
	if !ok || !pol.LeadAcceptAllowed {
		reason := pol.PolicyReason
		if !ok {
			reason = "project is not in the observation"
		}
		return nil, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeNotApplicable,
			"the gate policy closes lead-accept for this project", map[string]any{"policy_reason": reason})
	}
	var lead *domain.LeadAcceptLead
	for i := range pol.Leads {
		if pol.Leads[i].GitlabID == req.LeadGitlabID {
			lead = &pol.Leads[i]
			break
		}
	}
	if lead == nil {
		return nil, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeLeadNotAuthorized,
			"lead_gitlab_id is not a lead of the project in the latest registry observation", nil)
	}
	return &registryState{obs: obs, att: att, pol: pol, lead: *lead}, nil
}

// evaluate runs the registry, evidence and binding checks once. changed is
// true when the current observation is no longer the one the evaluation
// started from (or no longer fresh) once everything was read.
func (s *LeadAcceptService) evaluate(ctx context.Context, req *domain.LeadAcceptRequest, reqSHA string, scoped []*leadAcceptEvidenceRec) (*domain.LeadAcceptContext, bool, error) {
	st, err := s.registry(ctx, req, s.now().UTC())
	if err != nil {
		return nil, false, err
	}
	recs := make([]*leadAcceptEvidenceRec, len(scoped))
	for i, r := range scoped {
		recs[i] = &leadAcceptEvidenceRec{ref: r.ref, scope: r.scope}
	}

	// Evidence records: read all, then apply the refusals in table order.
	for _, r := range recs {
		switch r.ref.Kind {
		case "comment":
			c, cerr := s.store.Comment(ctx, r.ref.RecordID)
			if cerr != nil {
				return nil, false, cerr
			}
			r.comment = c
		case "artifact":
			a, aerr := s.store.Artifact(ctx, r.ref.RecordID)
			if aerr != nil {
				return nil, false, aerr
			}
			r.artifact = a
		}
	}
	for _, r := range recs {
		gone := (r.ref.Kind == "comment" && (r.comment == nil || r.comment.TaskID != r.ref.TaskID)) ||
			(r.ref.Kind == "artifact" && (r.artifact == nil || r.artifact.TaskID != r.ref.TaskID))
		if gone {
			return nil, false, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeEvidenceRevoked,
				"an evidence record no longer exists on its task", map[string]any{"ref": r.ref.Ref})
		}
	}
	for _, r := range recs {
		if r.comment != nil && !r.comment.UpdatedAt.Equal(r.comment.CreatedAt) {
			return nil, false, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeEvidenceChanged,
				"an evidence comment was edited", map[string]any{"ref": r.ref.Ref})
		}
	}
	for _, r := range recs {
		if r.artifact != nil && r.artifact.SizeBytes > domain.LeadAcceptMaxArtifactBytes {
			return nil, false, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeEvidenceUnsupported,
				"an evidence artifact is larger than 1 MiB", map[string]any{"ref": r.ref.Ref})
		}
	}
	for _, r := range recs {
		if r.artifact == nil {
			continue
		}
		b, tooBig, rerr := s.readArtifact(ctx, r.artifact.StorageKey)
		if tooBig {
			return nil, false, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeEvidenceUnsupported,
				"an evidence artifact is larger than 1 MiB", map[string]any{"ref": r.ref.Ref})
		}
		if rerr != nil {
			return nil, false, leadAcceptErr(http.StatusServiceUnavailable, domain.LeadAcceptCodeEvidenceUnavailable,
				"the bytes of an evidence artifact could not be read", map[string]any{"ref": r.ref.Ref})
		}
		r.bytes = b
	}

	binding, err := s.approvalBinding(ctx, req)
	if err != nil {
		return nil, false, err
	}

	// The observation must still be the current one, and fresh, now that
	// everything was read.
	cur, err := s.store.CurrentObservation(ctx)
	if err != nil {
		return nil, false, err
	}
	// Freshness is judged when the recheck has returned, never before it.
	now := s.now().UTC()
	if cur == nil || cur.ObservationID != st.obs.ObservationID || now.Sub(st.obs.ObservedAt) > domain.LeadAcceptMaxAge {
		return nil, true, nil
	}
	obs, att, pol, lead := st.obs, st.att, st.pol, st.lead

	out := &domain.LeadAcceptContext{
		Schema:        domain.LeadAcceptContextSchema,
		CheckedAt:     domain.LeadAcceptTime(now),
		Request:       req,
		RequestSHA256: reqSHA,
		Lead:          domain.LeadAcceptLeadOut{Username: lead.Username, GitlabID: lead.GitlabID, Lane: lead.Lane, Status: "active"},
		Scope:         domain.LeadAcceptScopeOut{Project: req.Project, LeadAcceptAllowed: true, PolicyReason: pol.PolicyReason},
		Registry: domain.LeadAcceptRegistryOut{
			ObservationID: obs.ObservationID.String(), OK: true,
			GitCommit: att.GitCommit, OriginMainCommit: att.OriginMainCommit, RegistryLastCommit: att.RegistryLastCommit,
			RegistryBlobSHA1: att.RegistryBlobSHA1, RegistrySHA256: att.RegistrySHA256,
			MirrorPolicyBlobSHA1: att.MirrorPolicyBlobSHA1, MirrorPolicySHA256: att.MirrorPolicySHA256,
			GateCodeSHA256: att.GateCodeSHA256, InSync: obs.InSync,
			ObservedAt: domain.LeadAcceptTime(obs.ObservedAt), ReceivedAt: domain.LeadAcceptTime(obs.ReceivedAt),
			MaxAgeSeconds: int(domain.LeadAcceptMaxAge / time.Second),
			FreshUntil:    domain.LeadAcceptTime(obs.ObservedAt.Add(domain.LeadAcceptMaxAge)),
		},
		ApprovalBinding: *binding,
	}
	for _, r := range recs {
		out.Evidence = append(out.Evidence, evidenceOut(req, r))
	}
	sum, err := out.ComputeContextSHA256()
	if err != nil {
		return nil, false, err
	}
	out.ContextSHA256 = sum
	return out, false, nil
}

// readArtifact reads at most 1 MiB + 1 byte of an object, whatever the
// database says its size is.
func (s *LeadAcceptService) readArtifact(ctx context.Context, key string) (data []byte, tooBig bool, err error) {
	if s.objects == nil || key == "" {
		return nil, false, errors.New("object storage is not configured")
	}
	rc, _, err := s.objects.GetObject(ctx, key)
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, domain.LeadAcceptMaxArtifactBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > domain.LeadAcceptMaxArtifactBytes {
		return nil, true, nil
	}
	return b, false, nil
}

func evidenceOut(req *domain.LeadAcceptRequest, r *leadAcceptEvidenceRec) domain.LeadAcceptEvidenceOut {
	e := domain.LeadAcceptEvidenceOut{
		Ref: r.ref.Ref, Kind: r.ref.Kind, TaskID: r.ref.TaskID.String(),
		ProjectID: r.scope.ProjectID.String(), Status: "active",
	}
	if c := r.comment; c != nil {
		body := []byte(c.Body)
		sum := sha256.Sum256(body)
		e.Author = domain.LeadAcceptAuthor{Type: c.AuthorType, ID: c.AuthorID.String(), Name: strPtrOr(c.AuthorName)}
		e.SystemCode = c.SystemCode
		internal, edited := c.IsInternal, false
		e.IsInternal, e.Edited = &internal, &edited
		e.CreatedAt = domain.LeadAcceptTime(c.CreatedAt)
		upd := domain.LeadAcceptTime(c.UpdatedAt)
		e.UpdatedAt = &upd
		e.SHA256 = hex.EncodeToString(sum[:])
		e.Mentions = req.MentionsIn(body)
	}
	if a := r.artifact; a != nil {
		sum := sha256.Sum256(r.bytes)
		name, size := a.Name, a.SizeBytes
		e.Name, e.SizeBytes = &name, &size
		e.Author = domain.LeadAcceptAuthor{Type: a.UploadedByType, ID: a.UploadedBy.String(), Name: strPtrOr(a.UploaderName)}
		e.CreatedAt = domain.LeadAcceptTime(a.CreatedAt)
		e.SHA256 = hex.EncodeToString(sum[:])
		e.Mentions = req.MentionsIn(r.bytes)
	}
	return e
}

func strPtrOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// approvalBinding checks every gate-intake receipt of the approval. none: no
// receipt. match: each receipt's fields, recovered from its card and proven
// by the receipt's own request_sha256, equal the request. Anything else —
// fields that differ, or a receipt whose fields cannot be proven — is a 409.
func (s *LeadAcceptService) approvalBinding(ctx context.Context, req *domain.LeadAcceptRequest) (*domain.LeadAcceptApprovalBinding, error) {
	out := &domain.LeadAcceptApprovalBinding{ApprovalID: req.ApprovalID, ApprovalVersion: req.ApprovalVersion,
		PayloadSHA256: req.PayloadSHA256, GateIntakeReceipt: "none"}
	receipts, err := s.store.GateIntakeReceipts(ctx, uuid.MustParse(req.ApprovalID))
	if err != nil {
		return nil, err
	}
	for _, rec := range receipts {
		gi, ok := domain.GateIntakeRequestFromDescription(rec.Description, rec.RequestSHA256)
		if !ok {
			return nil, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeBindingConflict,
				"a gate-intake receipt exists for this approval but its fields cannot be verified",
				map[string]any{"receipt_request_sha256": rec.RequestSHA256, "request_sha256": req.SHA256(), "reason": "receipt_unverifiable"})
		}
		if gi.DraftVersion != req.ApprovalVersion || gi.PayloadSHA256 != req.PayloadSHA256 ||
			gi.MRIID != req.MRIID || gi.HeadSHA != req.HeadSHA || !bytes.Equal([]byte(gi.ApprovalID.String()), []byte(req.ApprovalID)) {
			return nil, leadAcceptErr(http.StatusConflict, domain.LeadAcceptCodeBindingConflict,
				"the gate-intake receipt of this approval differs from the request",
				map[string]any{"receipt_request_sha256": rec.RequestSHA256, "request_sha256": req.SHA256(), "reason": "fields_differ"})
		}
		out.GateIntakeReceipt = "match"
	}
	return out, nil
}
