package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// LeadAcceptRepo stores lead-accept principals, their operator audit and the
// registry observations, and reads the evidence records a reader names
// (contract CH→Mesh lead-accept-context/v1, docs/lead-accept-context.md).
//
// Every instant is passed in by the caller (the service's clock): the
// freshness rules are tested with a controlled clock, so no query here uses
// now().
type LeadAcceptRepo struct {
	db *sqlx.DB
}

// NewLeadAcceptRepo creates a LeadAcceptRepo.
func NewLeadAcceptRepo(db *sqlx.DB) *LeadAcceptRepo { return &LeadAcceptRepo{db: db} }

// binding is NULL for a publisher: read as bytes, empty for none.
const leadAcceptPrincipalCols = `id, name, kind, workspace_id, evidence_project_id, convert_to(COALESCE(binding::text, ''), 'UTF8') AS binding, binding_sha256,
	token_prefix, previous_valid_until, created_at, issued_at, revoked_at, revoke_reason`

// leadAcceptAdvisoryLock serialises attestation intake: the current
// observation is global (the greatest observed_at of all publishers).
const leadAcceptAdvisoryLock int64 = 0x4c41_4f42_5356 // "LAOBSV"

func getLeadAcceptPrincipal(ctx context.Context, q sqlxQueryer, where string, args ...any) (*domain.LeadAcceptPrincipal, error) {
	var p domain.LeadAcceptPrincipal
	err := q.GetContext(ctx, &p, `SELECT `+leadAcceptPrincipalCols+` FROM lead_accept_principals WHERE `+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PrincipalByTokenHash returns the live principal of kind whose current token,
// or whose previous token still inside its grace window at now, hashes to
// tokenHash; nil when none does.
func (r *LeadAcceptRepo) PrincipalByTokenHash(ctx context.Context, kind, tokenHash string, now time.Time) (*domain.LeadAcceptPrincipal, error) {
	return getLeadAcceptPrincipal(ctx, r.db,
		`kind = $1 AND revoked_at IS NULL AND (token_hash = $2 OR (previous_token_hash = $2 AND previous_valid_until > $3))`,
		kind, tokenHash, now)
}

// PrincipalByName returns the principal called name (live or revoked), or nil.
func (r *LeadAcceptRepo) PrincipalByName(ctx context.Context, name string) (*domain.LeadAcceptPrincipal, error) {
	return getLeadAcceptPrincipal(ctx, r.db, `name = $1`, name)
}

// LeadAcceptIssue is what an operator issues: a publisher, or a reader bound
// to one request and to the workspace and evidence project it may read.
type LeadAcceptIssue struct {
	Name              string
	Kind              string
	WorkspaceID       uuid.UUID
	EvidenceProjectID uuid.UUID
	Request           *domain.LeadAcceptRequest
}

func (r *LeadAcceptRepo) audit(ctx context.Context, tx *sqlx.Tx, principalID, operator uuid.UUID, action, reason, prefix string, details map[string]any, now time.Time) error {
	if details == nil {
		details = map[string]any{}
	}
	d, _ := json.Marshal(details)
	var reasonArg, prefixArg any
	if reason != "" {
		reasonArg = reason
	}
	if prefix != "" {
		prefixArg = prefix
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO lead_accept_audit (principal_id, operator_id, action, reason, token_prefix, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, principalID, operator, action, reasonArg, prefixArg, d, now)
	return err
}

// checkReaderBinding refuses a reader whose request is not for the one v1
// project, whose evidence project is outside its workspace, or whose evidence
// tasks are missing, deleted or outside the evidence project.
func checkReaderBinding(ctx context.Context, tx *sqlx.Tx, in LeadAcceptIssue) error {
	if in.Request == nil {
		return operatorErr("a reader needs --request-file")
	}
	if in.Request.Project != domain.LeadAcceptProject {
		return operatorErr("request.project must be %s", domain.LeadAcceptProject)
	}
	var ok bool
	if err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2)`,
		in.EvidenceProjectID, in.WorkspaceID); err != nil {
		return err
	}
	if !ok {
		return operatorErr("evidence project %s is not in workspace %s", in.EvidenceProjectID, in.WorkspaceID)
	}
	for _, ref := range in.Request.EvidenceRefs() {
		if err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM tasks WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL)`,
			ref.TaskID, in.EvidenceProjectID); err != nil {
			return err
		}
		if !ok {
			return operatorErr("evidence %s: task is not a live task of project %s", ref.Ref, in.EvidenceProjectID)
		}
	}
	return nil
}

// Issue gives the principal called in.Name its token. A new name gets a new
// principal. A revoked name is re-armed only with the same kind and the same
// binding (a reader's binding never changes). A live name is refused.
func (r *LeadAcceptRepo) Issue(ctx context.Context, operator uuid.UUID, in LeadAcceptIssue, tokenHash, tokenPrefix string, now time.Time) (*domain.LeadAcceptPrincipal, string, error) {
	if !domain.LeadAcceptNameValid(in.Name) {
		return nil, "", operatorErr("--name must match ^[a-z0-9][a-z0-9-]{1,62}$")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()
	var binding []byte
	var bindingSHA *string
	var ws, evProj *uuid.UUID
	switch in.Kind {
	case domain.LeadAcceptKindReader:
		if berr := checkReaderBinding(ctx, tx, in); berr != nil {
			return nil, "", berr
		}
		binding = in.Request.CanonicalJSON()
		sha := in.Request.SHA256()
		bindingSHA = &sha
		w, p := in.WorkspaceID, in.EvidenceProjectID
		ws, evProj = &w, &p
	case domain.LeadAcceptKindPublisher:
		if in.Request != nil {
			return nil, "", operatorErr("--request-file is only for --kind reader")
		}
	default:
		return nil, "", operatorErr("--kind must be reader or publisher")
	}
	existing, err := getLeadAcceptPrincipal(ctx, tx, `name = $1 FOR UPDATE`, in.Name)
	if err != nil {
		return nil, "", err
	}
	action := "issue"
	var id uuid.UUID
	switch {
	case existing == nil:
		id = uuid.New()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO lead_accept_principals
				(id, name, kind, workspace_id, evidence_project_id, binding, binding_sha256, token_hash, token_prefix, created_at, issued_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			id, in.Name, in.Kind, ws, evProj, leadAcceptBindingArg(binding), bindingSHA, tokenHash, tokenPrefix, now)
	case existing.RevokedAt == nil:
		return nil, "", operatorErr("principal %s is live; use rotate to change its key", in.Name)
	default:
		if existing.Kind != in.Kind || !sameStrPtr(existing.BindingSHA256, bindingSHA) ||
			!sameUUIDPtr(existing.WorkspaceID, ws) || !sameUUIDPtr(existing.EvidenceProjectID, evProj) {
			return nil, "", operatorErr("principal %s was a %s with binding_sha256 %s; a re-issue keeps its kind and binding — "+
				"a new request needs a new principal name", in.Name, existing.Kind, strOr(existing.BindingSHA256, "none"))
		}
		action = "rearm"
		id = existing.ID
		_, err = tx.ExecContext(ctx, `
			UPDATE lead_accept_principals
			SET token_hash = $2, token_prefix = $3, previous_token_hash = NULL, previous_valid_until = NULL,
			    issued_at = $4, revoked_at = NULL, revoke_reason = NULL
			WHERE id = $1`, id, tokenHash, tokenPrefix, now)
	}
	if err != nil {
		return nil, "", err
	}
	details := map[string]any{"kind": in.Kind}
	if bindingSHA != nil {
		details["binding_sha256"] = *bindingSHA
		details["workspace_id"] = in.WorkspaceID
		details["evidence_project_id"] = in.EvidenceProjectID
	}
	if aerr := r.audit(ctx, tx, id, operator, action, "", tokenPrefix, details, now); aerr != nil {
		return nil, "", aerr
	}
	p, err := getLeadAcceptPrincipal(ctx, tx, `id = $1`, id)
	if err != nil {
		return nil, "", err
	}
	return p, action, tx.Commit()
}

// Rotate replaces the token of a live principal; the old one keeps working
// for grace (0 = not at all). Kind and binding never change.
func (r *LeadAcceptRepo) Rotate(ctx context.Context, operator uuid.UUID, name, tokenHash, tokenPrefix string, grace time.Duration, now time.Time) (*domain.LeadAcceptPrincipal, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := getLeadAcceptPrincipal(ctx, tx, `name = $1 FOR UPDATE`, name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, operatorErr("no principal %s", name)
	}
	if p.RevokedAt != nil {
		return nil, operatorErr("principal %s is revoked; issue it again", name)
	}
	if domain.LeadAcceptTokenPrefix(p.Kind) != tokenPrefix[:4] {
		return nil, operatorErr("token kind does not match principal kind %s", p.Kind)
	}
	if grace > 0 {
		_, err = tx.ExecContext(ctx, `
			UPDATE lead_accept_principals
			SET previous_token_hash = token_hash, previous_valid_until = $4,
			    token_hash = $2, token_prefix = $3, issued_at = $5
			WHERE id = $1`, p.ID, tokenHash, tokenPrefix, now.Add(grace), now)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE lead_accept_principals
			SET previous_token_hash = NULL, previous_valid_until = NULL,
			    token_hash = $2, token_prefix = $3, issued_at = $4
			WHERE id = $1`, p.ID, tokenHash, tokenPrefix, now)
	}
	if err != nil {
		return nil, err
	}
	if aerr := r.audit(ctx, tx, p.ID, operator, "rotate", "", tokenPrefix, map[string]any{
		"grace_seconds": int64(grace.Seconds()), "previous_token_prefix": p.TokenPrefix,
	}, now); aerr != nil {
		return nil, aerr
	}
	out, err := getLeadAcceptPrincipal(ctx, tx, `id = $1`, p.ID)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Revoke ends every token of the principal at once.
func (r *LeadAcceptRepo) Revoke(ctx context.Context, operator uuid.UUID, name, reason string, now time.Time) (*domain.LeadAcceptPrincipal, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := getLeadAcceptPrincipal(ctx, tx, `name = $1 FOR UPDATE`, name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, operatorErr("no principal %s", name)
	}
	if p.RevokedAt != nil {
		return nil, operatorErr("principal %s is already revoked", name)
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE lead_accept_principals
		SET token_hash = NULL, previous_token_hash = NULL, previous_valid_until = NULL,
		    revoked_at = $2, revoke_reason = $3
		WHERE id = $1`, p.ID, now, reason); err != nil {
		return nil, err
	}
	if aerr := r.audit(ctx, tx, p.ID, operator, "revoke", reason, "", nil, now); aerr != nil {
		return nil, aerr
	}
	out, err := getLeadAcceptPrincipal(ctx, tx, `id = $1`, p.ID)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Show reads a principal and records the operator's access before returning
// any metadata. Revoked principals remain inspectable by the host operator.
func (r *LeadAcceptRepo) Show(ctx context.Context, operator uuid.UUID, name string, now time.Time) (*domain.LeadAcceptPrincipal, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := getLeadAcceptPrincipal(ctx, tx, `name = $1 FOR SHARE`, name)
	if err != nil || p == nil {
		return nil, err
	}
	if err := r.audit(ctx, tx, p.ID, operator, "show", "", "", nil, now); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

// Audit returns the principal's operator audit, oldest first. The entry type
// is shared with gate intake: same columns, never a token.
func (r *LeadAcceptRepo) Audit(ctx context.Context, principalID uuid.UUID) ([]GateIntakeAuditEntry, error) {
	var out []GateIntakeAuditEntry
	err := r.db.SelectContext(ctx, &out, `
		SELECT action, operator_id, reason, token_prefix, details, created_at
		FROM lead_accept_audit WHERE principal_id = $1 ORDER BY id`, principalID)
	return out, err
}

// --- observations ----------------------------------------------------------

// LeadAcceptObservation is one stored registry observation.
type LeadAcceptObservation struct {
	ObservationID uuid.UUID       `db:"observation_id" json:"observation_id"`
	PrincipalID   uuid.UUID       `db:"principal_id" json:"principal_id"`
	ObservedAt    time.Time       `db:"observed_at" json:"observed_at"`
	ReceivedAt    time.Time       `db:"received_at" json:"received_at"`
	OK            bool            `db:"ok" json:"ok"`
	InSync        bool            `db:"in_sync" json:"in_sync"`
	ErrorStep     *string         `db:"error_step" json:"error_step,omitempty"`
	Body          json.RawMessage `db:"body" json:"-"`
}

const leadAcceptObservationCols = `observation_id, principal_id, observed_at, received_at, ok, in_sync, error_step, body`

// CurrentObservation returns the accepted observation with the greatest
// observed_at, or nil when none was ever accepted.
func (r *LeadAcceptRepo) CurrentObservation(ctx context.Context) (*LeadAcceptObservation, error) {
	return currentObservation(ctx, r.db)
}

func currentObservation(ctx context.Context, q sqlxQueryer) (*LeadAcceptObservation, error) {
	var o LeadAcceptObservation
	err := q.GetContext(ctx, &o, `SELECT `+leadAcceptObservationCols+`
		FROM lead_accept_observations ORDER BY observed_at DESC, received_at DESC LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// LatestObservationOf returns the principal's latest accepted observation, or nil.
func (r *LeadAcceptRepo) LatestObservationOf(ctx context.Context, principalID uuid.UUID) (*LeadAcceptObservation, error) {
	var o LeadAcceptObservation
	err := r.db.GetContext(ctx, &o, `SELECT `+leadAcceptObservationCols+`
		FROM lead_accept_observations WHERE principal_id = $1 ORDER BY observed_at DESC LIMIT 1`, principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// ObservationCount returns how many observations are stored.
func (r *LeadAcceptRepo) ObservationCount(ctx context.Context) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT count(*) FROM lead_accept_observations`)
	return n, err
}

// LeadAcceptRefusal is a refusal of an attestation (its error_code).
type LeadAcceptRefusal struct {
	Code    string
	Message string
	Context map[string]any
}

func (e *LeadAcceptRefusal) Error() string { return e.Code + ": " + e.Message }

// AcceptObservation runs the intake checks of one attestation in one
// transaction, serialised against every other intake, in the contract's
// order: clock skew, delay (ok=true only), replay, not newer. now is read
// after the lock is held and is the observation's received_at. On acceptance
// the observation becomes a new row (rows are never updated) and rows older
// than the retention are pruned, never the current one.
func (r *LeadAcceptRepo) AcceptObservation(ctx context.Context, principalID uuid.UUID, a *domain.LeadAcceptAttestation, now func() time.Time) (*LeadAcceptObservation, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, lerr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, leadAcceptAdvisoryLock); lerr != nil {
		return nil, lerr
	}
	received := now().UTC().Truncate(time.Microsecond)
	observed := a.ObservedAtTime.UTC().Truncate(time.Microsecond)
	if observed.After(received.Add(domain.LeadAcceptMaxSkew)) {
		return nil, &LeadAcceptRefusal{Code: domain.LeadAcceptCodeClockSkew,
			Message: "observed_at is more than 5 s ahead of the server clock",
			Context: map[string]any{"observed_at": domain.LeadAcceptTime(observed), "received_at": domain.LeadAcceptTime(received)}}
	}
	if a.OK && received.Sub(observed) > domain.LeadAcceptMaxDelay {
		return nil, &LeadAcceptRefusal{Code: domain.LeadAcceptCodeDelayed,
			Message: "an ok observation must arrive within 30 s of observed_at",
			Context: map[string]any{"observed_at": domain.LeadAcceptTime(observed), "received_at": domain.LeadAcceptTime(received)}}
	}
	var seen bool
	if gerr := tx.GetContext(ctx, &seen, `SELECT EXISTS (SELECT 1 FROM lead_accept_observations WHERE observation_id = $1)`,
		a.ObservationUID); gerr != nil {
		return nil, gerr
	}
	if seen {
		return nil, &LeadAcceptRefusal{Code: domain.LeadAcceptCodeReplayed,
			Message: "this observation_id was already accepted"}
	}
	cur, err := currentObservation(ctx, tx)
	if err != nil {
		return nil, err
	}
	if cur != nil && !observed.After(cur.ObservedAt) {
		return nil, &LeadAcceptRefusal{Code: domain.LeadAcceptCodeNotNewer,
			Message: "observed_at is not newer than the current observation",
			Context: map[string]any{"current_observed_at": domain.LeadAcceptTime(cur.ObservedAt)}}
	}
	body, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var step any
	if !a.OK {
		step = a.ErrorStep
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO lead_accept_observations (observation_id, principal_id, observed_at, received_at, ok, in_sync, error_step, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		a.ObservationUID, principalID, observed, received, a.OK, a.InSync(), step, body); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM lead_accept_observations WHERE received_at < $1 AND observation_id <> $2`,
		received.Add(-domain.LeadAcceptRetention), a.ObservationUID); err != nil {
		return nil, err
	}
	out := &LeadAcceptObservation{ObservationID: a.ObservationUID, PrincipalID: principalID, ObservedAt: observed,
		ReceivedAt: received, OK: a.OK, InSync: a.InSync(), Body: body}
	return out, tx.Commit()
}

// --- evidence --------------------------------------------------------------

// LeadAcceptTaskScope is where an evidence task lives.
type LeadAcceptTaskScope struct {
	ProjectID   uuid.UUID `db:"project_id"`
	WorkspaceID uuid.UUID `db:"workspace_id"`
}

// TaskScope returns the project and workspace of a live (not deleted) task,
// or nil.
func (r *LeadAcceptRepo) TaskScope(ctx context.Context, taskID uuid.UUID) (*LeadAcceptTaskScope, error) {
	var s LeadAcceptTaskScope
	err := r.db.GetContext(ctx, &s, `
		SELECT t.project_id, p.workspace_id FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1 AND t.deleted_at IS NULL`, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// LeadAcceptComment is a comment evidence record.
type LeadAcceptComment struct {
	ID         uuid.UUID `db:"id"`
	TaskID     uuid.UUID `db:"task_id"`
	AuthorID   uuid.UUID `db:"author_id"`
	AuthorType string    `db:"author_type"`
	AuthorName *string   `db:"author_name"`
	Body       string    `db:"body"`
	SystemCode *string   `db:"system_code"`
	IsInternal bool      `db:"is_internal"`
	CreatedAt  time.Time `db:"created_at"`
	UpdatedAt  time.Time `db:"updated_at"`
}

const leadAcceptActorName = `CASE
		WHEN %[1]s = 'agent' THEN (SELECT name FROM agents WHERE id = %[2]s)
		WHEN %[1]s = 'user' THEN
			(SELECT COALESCE(NULLIF(u.display_name, ''), SPLIT_PART(u.email, '@', 1)) FROM users u WHERE u.id = %[2]s)
		WHEN %[1]s = 'extension' THEN
			(SELECT COALESCE(NULLIF(ea.display_name, ''), ea.extension_id) FROM extension_actors ea WHERE ea.actor_id = %[2]s)
		ELSE NULL
	END`

// Comment returns a comment by id, or nil.
func (r *LeadAcceptRepo) Comment(ctx context.Context, id uuid.UUID) (*LeadAcceptComment, error) {
	var c LeadAcceptComment
	err := r.db.GetContext(ctx, &c, `
		SELECT c.id, c.task_id, c.author_id, c.author_type, c.body, c.system_code, c.is_internal, c.created_at, c.updated_at,
		`+sprintfActorName("c.author_type", "c.author_id")+` AS author_name
		FROM comments c WHERE c.id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// LeadAcceptArtifact is an artifact evidence record.
type LeadAcceptArtifact struct {
	ID             uuid.UUID `db:"id"`
	TaskID         uuid.UUID `db:"task_id"`
	Name           string    `db:"name"`
	StorageKey     string    `db:"storage_key"`
	SizeBytes      int64     `db:"size_bytes"`
	UploadedBy     uuid.UUID `db:"uploaded_by"`
	UploadedByType string    `db:"uploaded_by_type"`
	UploaderName   *string   `db:"uploader_name"`
	CreatedAt      time.Time `db:"created_at"`
}

// Artifact returns an artifact by id, or nil.
func (r *LeadAcceptRepo) Artifact(ctx context.Context, id uuid.UUID) (*LeadAcceptArtifact, error) {
	var a LeadAcceptArtifact
	err := r.db.GetContext(ctx, &a, `
		SELECT a.id, a.task_id, a.name, a.storage_key, a.size_bytes, a.uploaded_by, a.uploaded_by_type, a.created_at,
		`+sprintfActorName("a.uploaded_by_type", "a.uploaded_by")+` AS uploader_name
		FROM artifacts a WHERE a.id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// LeadAcceptReceipt is a gate-intake receipt of an approval with the
// description of the card it opened (empty when the card is deleted or gone).
type LeadAcceptReceipt struct {
	PrincipalID   uuid.UUID `db:"principal_id"`
	RequestSHA256 string    `db:"request_sha256"`
	TaskID        uuid.UUID `db:"task_id"`
	Description   string    `db:"description"`
}

// GateIntakeReceipts returns every gate-intake receipt of approvalID (one per
// gate-intake principal), oldest first.
func (r *LeadAcceptRepo) GateIntakeReceipts(ctx context.Context, approvalID uuid.UUID) ([]LeadAcceptReceipt, error) {
	var out []LeadAcceptReceipt
	err := r.db.SelectContext(ctx, &out, `
		SELECT r.principal_id, r.request_sha256, r.task_id, COALESCE(t.description, '') AS description
		FROM gate_intake_receipts r LEFT JOIN tasks t ON t.id = r.task_id AND t.deleted_at IS NULL
		WHERE r.approval_id = $1 ORDER BY r.created_at, r.principal_id`, approvalID)
	return out, err
}

func sameStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameUUIDPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func strOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func sprintfActorName(typeCol, idCol string) string {
	return fmt.Sprintf(leadAcceptActorName, typeCol, idCol)
}

func leadAcceptBindingArg(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}
