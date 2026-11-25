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
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// GateIntakeRepo stores gate-intake principals, their receipts and the operator
// audit (contract CH→Mesh gate-create/v1, docs/gate-intake.md).
type GateIntakeRepo struct {
	db *sqlx.DB
}

// NewGateIntakeRepo creates a GateIntakeRepo.
func NewGateIntakeRepo(db *sqlx.DB) *GateIntakeRepo { return &GateIntakeRepo{db: db} }

const gateIntakePrincipalCols = `id, name, workspace_id, project_id, assignee_agent_id, labels, priority, schema,
	token_prefix, previous_valid_until, created_at, issued_at, revoked_at, revoke_reason`

type gateIntakePrincipalRow struct {
	domain.GateIntakePrincipal
	LabelsArr pq.StringArray `db:"labels"`
}

func (r gateIntakePrincipalRow) principal() *domain.GateIntakePrincipal {
	p := r.GateIntakePrincipal
	p.Labels = []string(r.LabelsArr)
	if p.Labels == nil {
		p.Labels = []string{}
	}
	return &p
}

type sqlxQueryer interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
}

func getGateIntakePrincipal(ctx context.Context, q sqlxQueryer, where string, args ...any) (*domain.GateIntakePrincipal, error) {
	var row gateIntakePrincipalRow
	err := q.GetContext(ctx, &row, `SELECT `+gateIntakePrincipalCols+` FROM gate_intake_principals WHERE `+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row.principal(), nil
}

// PrincipalByTokenHash returns the live principal whose current token, or whose
// previous token still inside its grace window, hashes to tokenHash; nil when
// none does (unknown, revoked, or rotated out after the grace).
func (r *GateIntakeRepo) PrincipalByTokenHash(ctx context.Context, tokenHash string) (*domain.GateIntakePrincipal, error) {
	return getGateIntakePrincipal(ctx, r.db,
		`revoked_at IS NULL AND (token_hash = $1 OR (previous_token_hash = $1 AND previous_valid_until > now()))`,
		tokenHash)
}

// PrincipalByName returns the principal called name (live or revoked), or nil.
func (r *GateIntakeRepo) PrincipalByName(ctx context.Context, name string) (*domain.GateIntakePrincipal, error) {
	return getGateIntakePrincipal(ctx, r.db, `name = $1`, name)
}

// GetReceipt returns the receipt of (principal, approval), or nil.
func (r *GateIntakeRepo) GetReceipt(ctx context.Context, principalID, approvalID uuid.UUID) (*domain.GateIntakeReceipt, error) {
	var rec domain.GateIntakeReceipt
	err := r.db.GetContext(ctx, &rec, `
		SELECT principal_id, approval_id, request_sha256, task_id, response, created_at
		FROM gate_intake_receipts WHERE principal_id = $1 AND approval_id = $2`, principalID, approvalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// InsertReceiptTx writes the receipt inside tx (the card's own creation
// transaction) and binds the card's external ref. inserted=false means a
// receipt for (principal, approval) already exists — committed by another
// request, which this INSERT waited for — and nothing was written: the caller
// rolls the card back and answers from the stored receipt.
func (r *GateIntakeRepo) InsertReceiptTx(ctx context.Context, tx *sql.Tx, rec *domain.GateIntakeReceipt, setBy uuid.UUID) (bool, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO gate_intake_receipts (principal_id, approval_id, request_sha256, task_id, response, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (principal_id, approval_id) DO NOTHING`,
		rec.PrincipalID, rec.ApprovalID, rec.RequestSHA256, rec.TaskID, []byte(rec.Response), rec.CreatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO task_external_refs (task_id, ref, hash, set_by) VALUES ($1, $2, $3, $4)`,
		rec.TaskID, domain.GateIntakeExternalRefPrefix+rec.ApprovalID.String(), rec.RequestSHA256, setBy)
	if err != nil {
		return false, err
	}
	return true, nil
}

// GateIntakeBinding is the destination an operator binds a principal to.
type GateIntakeBinding struct {
	Name            string
	WorkspaceID     uuid.UUID
	ProjectID       uuid.UUID
	AssigneeAgentID uuid.UUID
	Labels          []string
	Priority        domain.Priority
	Schema          string
}

// GateIntakeOperatorError is a refusal of an operator command (bad binding,
// wrong state). Its message is safe to print: it never carries a token.
type GateIntakeOperatorError struct{ Msg string }

func (e *GateIntakeOperatorError) Error() string { return e.Msg }

func operatorErr(format string, a ...any) error {
	return &GateIntakeOperatorError{Msg: fmt.Sprintf(format, a...)}
}

func (r *GateIntakeRepo) audit(ctx context.Context, tx *sqlx.Tx, principalID, operator uuid.UUID, action, reason, prefix string, details map[string]any) error {
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
		INSERT INTO gate_intake_audit (principal_id, operator_id, action, reason, token_prefix, details)
		VALUES ($1, $2, $3, $4, $5, $6)`, principalID, operator, action, reasonArg, prefixArg, d)
	return err
}

// checkBinding refuses a binding whose project is outside its workspace or
// whose assignee is not an agent of that workspace, so a bad binding fails once
// at issue time and not on every intake.
func checkBinding(ctx context.Context, tx *sqlx.Tx, b GateIntakeBinding) error {
	switch b.Priority {
	case domain.PriorityUrgent, domain.PriorityHigh, domain.PriorityMedium, domain.PriorityLow, domain.PriorityNone:
	default:
		return operatorErr("priority %q is not a task priority", b.Priority)
	}
	if b.Schema != domain.GateIntakeSchemaContentHub {
		return operatorErr("schema must be %s", domain.GateIntakeSchemaContentHub)
	}
	var ok bool
	if err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2)`,
		b.ProjectID, b.WorkspaceID); err != nil {
		return err
	}
	if !ok {
		return operatorErr("project %s is not in workspace %s", b.ProjectID, b.WorkspaceID)
	}
	if err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1 AND workspace_id = $2)`,
		b.AssigneeAgentID, b.WorkspaceID); err != nil {
		return err
	}
	if !ok {
		return operatorErr("agent %s is not in workspace %s", b.AssigneeAgentID, b.WorkspaceID)
	}
	var hasDefault bool
	if err := tx.GetContext(ctx, &hasDefault, `SELECT EXISTS (SELECT 1 FROM task_statuses WHERE project_id = $1 AND is_default = TRUE)`,
		b.ProjectID); err != nil {
		return err
	}
	if !hasDefault {
		// The same default status Intake starts every item in
		// (TaskStatusRepo.GetDefaultForProject): without it every request is a 500.
		return operatorErr("project %s has no default task status", b.ProjectID)
	}
	return nil
}

func sameBinding(p *domain.GateIntakePrincipal, b GateIntakeBinding) bool {
	if p.WorkspaceID != b.WorkspaceID || p.ProjectID != b.ProjectID || p.AssigneeAgentID != b.AssigneeAgentID ||
		p.Priority != b.Priority || p.Schema != b.Schema || len(p.Labels) != len(b.Labels) {
		return false
	}
	for i := range p.Labels {
		if p.Labels[i] != b.Labels[i] {
			return false
		}
	}
	return true
}

// Issue gives the principal called b.Name its token (tokenHash/tokenPrefix).
// A new name gets a new principal. A revoked name is re-armed: the same id,
// so its receipts keep answering; its binding must be the one it had. A live
// name is refused: change its key with Rotate.
func (r *GateIntakeRepo) Issue(ctx context.Context, operator uuid.UUID, b GateIntakeBinding, tokenHash, tokenPrefix string) (*domain.GateIntakePrincipal, string, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if berr := checkBinding(ctx, tx, b); berr != nil {
		return nil, "", berr
	}
	existing, err := getGateIntakePrincipal(ctx, tx, `name = $1 FOR UPDATE`, b.Name)
	if err != nil {
		return nil, "", err
	}
	action := "issue"
	var id uuid.UUID
	switch {
	case existing == nil:
		id = uuid.New()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO gate_intake_principals
				(id, name, workspace_id, project_id, assignee_agent_id, labels, priority, schema, token_hash, token_prefix, issued_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())`,
			id, b.Name, b.WorkspaceID, b.ProjectID, b.AssigneeAgentID, pq.StringArray(b.Labels), b.Priority, b.Schema,
			tokenHash, tokenPrefix)
	case existing.RevokedAt == nil:
		return nil, "", operatorErr("principal %s is live; use rotate to change its key", b.Name)
	default:
		if !sameBinding(existing, b) {
			return nil, "", operatorErr("principal %s was bound to workspace %s project %s assignee %s labels %v priority %s; "+
				"a re-issue keeps that binding", b.Name, existing.WorkspaceID, existing.ProjectID, existing.AssigneeAgentID,
				existing.Labels, existing.Priority)
		}
		action = "rearm"
		id = existing.ID
		_, err = tx.ExecContext(ctx, `
			UPDATE gate_intake_principals
			SET token_hash = $2, token_prefix = $3, previous_token_hash = NULL, previous_valid_until = NULL,
			    issued_at = now(), revoked_at = NULL, revoke_reason = NULL
			WHERE id = $1`, id, tokenHash, tokenPrefix)
	}
	if err != nil {
		return nil, "", err
	}
	if aerr := r.audit(ctx, tx, id, operator, action, "", tokenPrefix, map[string]any{
		"workspace_id": b.WorkspaceID, "project_id": b.ProjectID, "assignee_agent_id": b.AssigneeAgentID,
		"labels": b.Labels, "priority": b.Priority, "schema": b.Schema,
	}); aerr != nil {
		return nil, "", aerr
	}
	p, err := getGateIntakePrincipal(ctx, tx, `id = $1`, id)
	if err != nil {
		return nil, "", err
	}
	return p, action, tx.Commit()
}

// Rotate replaces the token of a live principal; the old one keeps working for
// grace (0 = not at all).
func (r *GateIntakeRepo) Rotate(ctx context.Context, operator uuid.UUID, name, tokenHash, tokenPrefix string, grace time.Duration) (*domain.GateIntakePrincipal, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := getGateIntakePrincipal(ctx, tx, `name = $1 FOR UPDATE`, name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, operatorErr("no principal %s", name)
	}
	if p.RevokedAt != nil {
		return nil, operatorErr("principal %s is revoked; issue it again", name)
	}
	if grace > 0 {
		_, err = tx.ExecContext(ctx, `
			UPDATE gate_intake_principals
			SET previous_token_hash = token_hash, previous_valid_until = now() + make_interval(secs => $4),
			    token_hash = $2, token_prefix = $3, issued_at = now()
			WHERE id = $1`, p.ID, tokenHash, tokenPrefix, grace.Seconds())
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE gate_intake_principals
			SET previous_token_hash = NULL, previous_valid_until = NULL,
			    token_hash = $2, token_prefix = $3, issued_at = now()
			WHERE id = $1`, p.ID, tokenHash, tokenPrefix)
	}
	if err != nil {
		return nil, err
	}
	if aerr := r.audit(ctx, tx, p.ID, operator, "rotate", "", tokenPrefix, map[string]any{
		"grace_seconds": int64(grace.Seconds()), "previous_token_prefix": p.TokenPrefix,
	}); aerr != nil {
		return nil, aerr
	}
	out, err := getGateIntakePrincipal(ctx, tx, `id = $1`, p.ID)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Revoke ends every token of the principal at once. Receipts stay; a later
// Issue of the same name re-arms the same principal.
func (r *GateIntakeRepo) Revoke(ctx context.Context, operator uuid.UUID, name, reason string) (*domain.GateIntakePrincipal, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := getGateIntakePrincipal(ctx, tx, `name = $1 FOR UPDATE`, name)
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
		UPDATE gate_intake_principals
		SET token_hash = NULL, previous_token_hash = NULL, previous_valid_until = NULL,
		    revoked_at = now(), revoke_reason = $2
		WHERE id = $1`, p.ID, reason); err != nil {
		return nil, err
	}
	if aerr := r.audit(ctx, tx, p.ID, operator, "revoke", reason, "", nil); aerr != nil {
		return nil, aerr
	}
	out, err := getGateIntakePrincipal(ctx, tx, `id = $1`, p.ID)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// GateIntakeAuditEntry is one row of the operator audit.
type GateIntakeAuditEntry struct {
	Action      string          `db:"action" json:"action"`
	OperatorID  uuid.UUID       `db:"operator_id" json:"operator_id"`
	Reason      *string         `db:"reason" json:"reason,omitempty"`
	TokenPrefix *string         `db:"token_prefix" json:"token_prefix,omitempty"`
	Details     json.RawMessage `db:"details" json:"details"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
}

// Audit returns the principal's operator audit, oldest first.
func (r *GateIntakeRepo) Audit(ctx context.Context, principalID uuid.UUID) ([]GateIntakeAuditEntry, error) {
	var out []GateIntakeAuditEntry
	err := r.db.SelectContext(ctx, &out, `
		SELECT action, operator_id, reason, token_prefix, details, created_at
		FROM gate_intake_audit WHERE principal_id = $1 ORDER BY id`, principalID)
	return out, err
}

// ReceiptCount returns how many receipts the principal holds.
func (r *GateIntakeRepo) ReceiptCount(ctx context.Context, principalID uuid.UUID) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT count(*) FROM gate_intake_receipts WHERE principal_id = $1`, principalID)
	return n, err
}
