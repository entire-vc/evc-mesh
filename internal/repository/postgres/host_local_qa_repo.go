package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/hostlocalqa"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// HostLocalQARepo stores capability-scoped receiver admissions and deliveries of
// host_local_qa_assignment_v1. It shares nothing with ExistingSessionRepo except
// the request-id namespace (see lockRequest): a sender's request id is immutable
// across both capabilities.
type HostLocalQARepo struct {
	db     *sqlx.DB
	policy *hostlocalqa.Policy
	now    func() time.Time
}

func NewHostLocalQARepo(db *sqlx.DB, policy *hostlocalqa.Policy) *HostLocalQARepo {
	return &HostLocalQARepo{db: db, policy: policy, now: time.Now}
}

// WithClock replaces the server clock (tests only).
func (r *HostLocalQARepo) WithClock(now func() time.Time) *HostLocalQARepo {
	r.now = now
	return r
}

func errHostLocalQADenied() error {
	return apierror.ForbiddenWithDetails("host-local QA target is not available to this caller", "host_local_qa_target_denied")
}

func errHostLocalQACheckDenied() error {
	return apierror.ForbiddenWithDetails("host-local QA assignment is not active for this request", "host_local_qa_check_denied")
}

func errHostLocalQAConflict(details string) error {
	e := apierror.Conflict("host-local QA request conflicts with the stored one")
	e.Details = details
	return e
}

// HostLocalQAReceiver is an admission as its owner sees it.
type HostLocalQAReceiver struct {
	ID                uuid.UUID            `json:"receiver_id"`
	AdmissionRevision int64                `json:"admission_revision"`
	WorkspaceID       uuid.UUID            `json:"workspace_id"`
	AgentID           uuid.UUID            `json:"agent_id"`
	RunnerID          uuid.UUID            `json:"runner_id"`
	SchemaVersion     int                  `json:"schema_version"`
	Capability        string               `json:"capability"`
	PolicySHA256      string               `json:"policy_sha256"`
	Target            hostlocalqa.Target   `json:"target"`
	Producer          hostlocalqa.Producer `json:"producer"`
	Expires           int64                `json:"expires"`
	Senders           []uuid.UUID          `json:"allowed_sender_agent_ids"`
}

// Register creates or re-admits a receiver and bumps the admission revision.
// Queued deliveries addressed to an older revision fail as admission_stale: a
// re-admitted receiver never inherits work admitted for a previous registration.
// ws, agent and runner are the authenticated values; the caller has verified
// that the body's target matches them and the policy.
func (r *HostLocalQARepo) Register(ctx context.Context, ws, agent, runner uuid.UUID, reg *hostlocalqa.Registration) (*HostLocalQAReceiver, error) {
	if reason := hostlocalqa.ExpiryError(reg.Expires, r.now()); reason != "" {
		return nil, apierror.BadRequestWithDetails("expires is outside the allowed window", reason)
	}
	producer, _ := json.Marshal(reg.Producer)
	senders := make([]string, 0, len(reg.Senders))
	for _, s := range reg.Senders {
		senders = append(senders, s.String())
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	err = tx.GetContext(ctx, &n, `SELECT count(*) FROM agents WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, pq.Array(senders))
	if err != nil {
		return nil, err
	}
	if n != len(senders) {
		return nil, apierror.BadRequest("allowed_sender_agent_ids must be agents of this workspace")
	}
	var id uuid.UUID
	var rev int64
	err = tx.QueryRowxContext(ctx, `INSERT INTO host_local_qa_receivers
		(workspace_id, agent_id, runner_id, host_id, hostname, session_id, capability, policy_sha256, producer, expires_at, admission_revision, allowed_senders)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, 1, $11::uuid[])
		ON CONFLICT (workspace_id, agent_id, runner_id, host_id, session_id) DO UPDATE
		SET admission_revision = host_local_qa_receivers.admission_revision + 1,
		    hostname = EXCLUDED.hostname, policy_sha256 = EXCLUDED.policy_sha256, producer = EXCLUDED.producer,
		    expires_at = EXCLUDED.expires_at, allowed_senders = EXCLUDED.allowed_senders,
		    revoked_at = NULL, registered_at = now()
		RETURNING id, admission_revision`,
		ws, agent, runner, reg.Target.HostID, reg.Target.Hostname, reg.Target.SessionID, hostlocalqa.Capability,
		r.policy.SHA256, producer, time.Unix(reg.Expires, 0).UTC(), pq.Array(senders)).Scan(&id, &rev)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE host_local_qa_deliveries
		SET state = 'failed', error_code = 'admission_stale', updated_at = $3
		WHERE receiver_id = $1 AND state = 'queued' AND expected_admission_revision <> $2`, id, rev, r.now().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &HostLocalQAReceiver{ID: id, AdmissionRevision: rev, WorkspaceID: ws, AgentID: agent, RunnerID: runner,
		SchemaVersion: hostlocalqa.SchemaVersion, Capability: hostlocalqa.Capability, PolicySHA256: r.policy.SHA256,
		Target: reg.Target, Producer: reg.Producer, Expires: reg.Expires, Senders: reg.Senders}, nil
}

// Revoke ends the caller's own admission. Queued work fails as receiver_revoked;
// a received delivery that was never acknowledged becomes uncertain
// (delivery_uncertain): it may have been injected. Revocation touches nothing
// else: not the legacy admission, the producer, a ledger or any target file.
func (r *HostLocalQARepo) Revoke(ctx context.Context, ws, agent, runner, receiver uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := r.now().UTC()
	res, err := tx.ExecContext(ctx, `UPDATE host_local_qa_receivers SET revoked_at = $5
		WHERE id = $1 AND workspace_id = $2 AND agent_id = $3 AND runner_id = $4 AND revoked_at IS NULL`,
		receiver, ws, agent, runner, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("receiver")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE host_local_qa_deliveries
		SET updated_at = $2,
		    state = CASE WHEN state = 'received' THEN 'uncertain' ELSE 'failed' END,
		    error_code = CASE WHEN state = 'received' THEN 'delivery_uncertain' ELSE 'receiver_revoked' END
		WHERE receiver_id = $1 AND state IN ('queued', 'received')`, receiver, now); err != nil {
		return err
	}
	return tx.Commit()
}

// HostLocalQAReceipt is the transport receipt. It never carries the text, a
// transcript or a credential.
type HostLocalQAReceipt struct {
	DeliveryID        uuid.UUID            `json:"delivery_id"`
	RequestID         uuid.UUID            `json:"request_id"`
	SchemaVersion     int                  `json:"schema_version"`
	Capability        string               `json:"capability"`
	Target            hostlocalqa.Target   `json:"target"`
	Producer          hostlocalqa.Producer `json:"producer"`
	ReceiverID        uuid.UUID            `json:"receiver_id"`
	AdmissionRevision int64                `json:"admission_revision"`
	PolicySHA256      string               `json:"policy_sha256"`
	TextSHA256        string               `json:"text_sha256"`
	Expires           int64                `json:"expires"`
	State             string               `json:"state"`
	ReceivedAt        *time.Time           `json:"received_at"`
	AcknowledgedAt    *time.Time           `json:"acknowledged_at"`
	ResultCommentURL  *string              `json:"result_comment_url"`
	ErrorCode         *string              `json:"error_code"`
}

type hostLocalQARow struct {
	ID               uuid.UUID  `db:"id"`
	ReceiverID       uuid.UUID  `db:"receiver_id"`
	SenderAgentID    uuid.UUID  `db:"sender_agent_id"`
	Envelope         []byte     `db:"envelope"`
	State            string     `db:"state"`
	ReceivedAt       *time.Time `db:"received_at"`
	AcknowledgedAt   *time.Time `db:"acknowledged_at"`
	ResultCommentURL *string    `db:"result_comment_url"`
	ErrorCode        *string    `db:"error_code"`
	CreatedAt        time.Time  `db:"created_at"`
}

const hostLocalQARowSelect = `SELECT d.id, d.receiver_id, d.sender_agent_id, d.envelope, d.state, d.received_at,
	d.acknowledged_at, d.result_comment_url, d.error_code, d.created_at FROM host_local_qa_deliveries d `

func (row *hostLocalQARow) envelope() (hostlocalqa.Envelope, error) {
	var e hostlocalqa.Envelope
	err := json.Unmarshal(row.Envelope, &e)
	return e, err
}

func (row *hostLocalQARow) receipt() (*HostLocalQAReceipt, error) {
	e, err := row.envelope()
	if err != nil {
		return nil, err
	}
	return &HostLocalQAReceipt{DeliveryID: row.ID, RequestID: e.RequestID, SchemaVersion: e.SchemaVersion,
		Capability: e.Capability, Target: e.Target, Producer: e.Producer, ReceiverID: row.ReceiverID,
		AdmissionRevision: e.ExpectedRevision, PolicySHA256: e.PolicySHA256, TextSHA256: e.TextSHA256,
		Expires: e.Expires, State: row.State, ReceivedAt: row.ReceivedAt, AcknowledgedAt: row.AcknowledgedAt,
		ResultCommentURL: row.ResultCommentURL, ErrorCode: row.ErrorCode}, nil
}

func (r *HostLocalQARepo) loadRow(ctx context.Context, q sqlx.QueryerContext, where string, args ...any) (*hostLocalQARow, error) {
	var row hostLocalQARow
	if err := sqlx.GetContext(ctx, q, &row, hostLocalQARowSelect+where, args...); err != nil {
		return nil, err
	}
	return &row, nil
}

// expireDue fails queued deliveries whose absolute expiry has passed, by the
// server clock. Expiry bounds forwarding: an expired delivery is never listed.
// where is written against $2.. ($1 is the server clock reading).
func (r *HostLocalQARepo) expireDue(ctx context.Context, q sqlx.ExecerContext, where string, args ...any) error {
	_, err := q.ExecContext(ctx, `UPDATE host_local_qa_deliveries d SET state = 'failed', error_code = 'expired', updated_at = $1
		WHERE d.state = 'queued' AND d.expires_at <= $1 AND `+where, append([]any{r.now().UTC()}, args...)...)
	return err
}

// lockRequest serialises every writer of one (workspace, sender, request_id),
// across capabilities, for the rest of the transaction.
func lockRequest(ctx context.Context, tx *sqlx.Tx, ws, sender, request uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "request:"+ws.String()+":"+sender.String()+":"+request.String())
	return err
}

// Send queues one delivery. created=false is an identical replay. Order: replay
// of the same request (any envelope change is request_conflict), cross-capability
// conflict, server-clock expiry, then the exact active receiver.
func (r *HostLocalQARepo) Send(ctx context.Context, ws, sender uuid.UUID, env *hostlocalqa.Envelope) (*HostLocalQAReceipt, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	err = lockRequest(ctx, tx, ws, sender, env.RequestID)
	if err != nil {
		return nil, false, err
	}
	row, err := r.loadRow(ctx, tx, `WHERE d.workspace_id = $1 AND d.sender_agent_id = $2 AND d.request_id = $3`, ws, sender, env.RequestID)
	if err == nil {
		stored, derr := row.envelope()
		if derr != nil {
			return nil, false, derr
		}
		if stored != *env {
			return nil, false, errHostLocalQAConflict("request_conflict")
		}
		rec, rerr := row.receipt()
		return rec, false, rerr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var legacy bool
	err = tx.GetContext(ctx, &legacy, `SELECT EXISTS (SELECT 1 FROM existing_session_deliveries
		WHERE workspace_id = $1 AND sender_agent_id = $2 AND request_id = $3)`, ws, sender, env.RequestID)
	if err != nil {
		return nil, false, err
	}
	if legacy {
		return nil, false, errHostLocalQAConflict("request_conflict")
	}
	now := r.now()
	if reason := hostlocalqa.ExpiryError(env.Expires, now); reason != "" {
		return nil, false, apierror.BadRequestWithDetails("expires is outside the allowed window", reason)
	}
	// FOR SHARE: a concurrent re-admission or revoke (both UPDATE the row) waits
	// until this insert commits, so nothing is queued under a replaced admission.
	var rc struct {
		Revision int64          `db:"admission_revision"`
		Allowed  pq.StringArray `db:"allowed_senders"`
		Producer []byte         `db:"producer"`
	}
	err = tx.GetContext(ctx, &rc, `SELECT admission_revision, allowed_senders, producer FROM host_local_qa_receivers rc
		WHERE rc.id = $1 AND rc.workspace_id = $2 AND rc.agent_id = $3 AND rc.runner_id = $4 AND rc.host_id = $5
		  AND rc.session_id = $6 AND rc.hostname = $7 AND rc.revoked_at IS NULL AND rc.expires_at > $8
		  AND rc.capability = $9 AND rc.policy_sha256 = $10
		  AND EXISTS (SELECT 1 FROM ext_agent_chat_runners cr WHERE cr.id = rc.runner_id AND cr.revoked_at IS NULL)
		FOR SHARE OF rc`,
		env.ReceiverID, ws, env.Target.AgentID, env.Target.RunnerID, env.Target.HostID, env.Target.SessionID,
		env.Target.Hostname, now.UTC(), hostlocalqa.Capability, env.PolicySHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, errHostLocalQADenied()
	}
	if err != nil {
		return nil, false, err
	}
	var producer hostlocalqa.Producer
	if json.Unmarshal(rc.Producer, &producer) != nil || producer != env.Producer || !r.senderAllowed(rc.Allowed, sender) {
		return nil, false, errHostLocalQADenied()
	}
	if rc.Revision != env.ExpectedRevision {
		e := apierror.Conflict("admission revision is stale")
		e.Details = "admission_stale"
		return nil, false, e
	}
	stored, _ := json.Marshal(env)
	var id uuid.UUID
	err = tx.GetContext(ctx, &id, `INSERT INTO host_local_qa_deliveries
		(workspace_id, request_id, sender_agent_id, receiver_id, envelope, expected_admission_revision, expires_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7) RETURNING id`,
		ws, env.RequestID, sender, env.ReceiverID, stored, env.ExpectedRevision, time.Unix(env.Expires, 0).UTC())
	if err != nil {
		return nil, false, err
	}
	row, err = r.loadRow(ctx, tx, `WHERE d.id = $1`, id)
	if err != nil {
		return nil, false, err
	}
	rec, err := row.receipt()
	if err != nil {
		return nil, false, err
	}
	return rec, true, tx.Commit()
}

func (r *HostLocalQARepo) senderAllowed(list pq.StringArray, sender uuid.UUID) bool {
	if !r.policy.SenderAllowed(sender) {
		return false
	}
	for _, a := range list {
		if a == sender.String() {
			return true
		}
	}
	return false
}

// Get returns the receipt of a delivery the caller sent; another sender's and an
// unknown id are the same 404. Never carries text.
func (r *HostLocalQARepo) Get(ctx context.Context, ws, sender, delivery uuid.UUID) (*HostLocalQAReceipt, error) {
	if err := r.expireDue(ctx, r.db, `d.id = $2 AND d.workspace_id = $3 AND d.sender_agent_id = $4`, delivery, ws, sender); err != nil {
		return nil, err
	}
	row, err := r.loadRow(ctx, r.db, `WHERE d.id = $1 AND d.workspace_id = $2 AND d.sender_agent_id = $3`, delivery, ws, sender)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("delivery")
	}
	if err != nil {
		return nil, err
	}
	return row.receipt()
}

// HostLocalQAQueued is one delivery as the receiver polls it: the sender envelope
// (including the exact text) plus delivery_id and created_at, and nothing else.
type HostLocalQAQueued struct {
	hostlocalqa.Envelope
	Text       string    `json:"text"`
	DeliveryID uuid.UUID `json:"delivery_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// Pending lists queued deliveries of the exact current, unexpired, unrevoked
// admission of the calling runner. A delivery that is no longer queued
// (possibly injected) is never listed again.
func (r *HostLocalQARepo) Pending(ctx context.Context, ws, agent, runner uuid.UUID, hostID, sessionID string) ([]HostLocalQAQueued, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var rc struct {
		ID       uuid.UUID `db:"id"`
		Revision int64     `db:"admission_revision"`
	}
	err = tx.GetContext(ctx, &rc, `SELECT id, admission_revision FROM host_local_qa_receivers rc
		WHERE workspace_id = $1 AND agent_id = $2 AND runner_id = $3 AND host_id = $4 AND session_id = $5
		  AND revoked_at IS NULL AND expires_at > $6
		  AND EXISTS (SELECT 1 FROM ext_agent_chat_runners cr WHERE cr.id = rc.runner_id AND cr.revoked_at IS NULL)
		FOR SHARE OF rc`, ws, agent, runner, hostID, sessionID, r.now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errHostLocalQADenied()
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE host_local_qa_deliveries SET state = 'failed', error_code = 'admission_stale', updated_at = now()
		WHERE receiver_id = $1 AND state = 'queued' AND expected_admission_revision <> $2`, rc.ID, rc.Revision); err != nil {
		return nil, err
	}
	if err := r.expireDue(ctx, tx, `d.receiver_id = $2`, rc.ID); err != nil {
		return nil, err
	}
	var rows []hostLocalQARow
	if err := tx.SelectContext(ctx, &rows, hostLocalQARowSelect+`WHERE d.receiver_id = $1 AND d.state = 'queued'
		AND d.expected_admission_revision = $2 ORDER BY d.created_at, d.id LIMIT 50`, rc.ID, rc.Revision); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]HostLocalQAQueued, 0, len(rows))
	for i := range rows {
		e, err := rows[i].envelope()
		if err != nil {
			return nil, err
		}
		out = append(out, HostLocalQAQueued{Envelope: e, Text: r.policy.Text, DeliveryID: rows[i].ID, CreatedAt: rows[i].CreatedAt})
	}
	return out, nil
}

// Ack applies a receiver report. The body must repeat every binding id, hash and
// identity of the stored delivery; any mismatch, and any caller other than the
// exact registered runner, is the same 403. Transitions: queued -> received |
// failed | uncertain, received -> acknowledged | failed | uncertain; queued ->
// acknowledged is a 409. Repeating the current state is a no-op. Becoming
// received needs an unexpired delivery.
func (r *HostLocalQARepo) Ack(ctx context.Context, ws, agent, runner, delivery uuid.UUID, b *hostlocalqa.Binding) (*HostLocalQAReceipt, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var cur struct {
		hostLocalQARow
		Revision  int64     `db:"rc_revision"`
		ExpiresAt time.Time `db:"expires_at"`
	}
	err = tx.GetContext(ctx, &cur, `SELECT d.id, d.receiver_id, d.sender_agent_id, d.envelope, d.state, d.received_at,
		d.acknowledged_at, d.result_comment_url, d.error_code, d.created_at, d.expires_at, rc.admission_revision AS rc_revision
		FROM host_local_qa_deliveries d JOIN host_local_qa_receivers rc ON rc.id = d.receiver_id
		WHERE d.id = $1 AND d.workspace_id = $2 AND rc.agent_id = $3 AND rc.runner_id = $4 AND rc.revoked_at IS NULL
		FOR UPDATE OF d`, delivery, ws, agent, runner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errHostLocalQADenied()
	}
	if err != nil {
		return nil, err
	}
	env, err := cur.envelope()
	if err != nil {
		return nil, err
	}
	if !b.Matches(delivery, cur.ReceiverID, env) || cur.Revision != env.ExpectedRevision {
		return nil, errHostLocalQADenied()
	}
	if cur.State != b.State {
		ok := (cur.State == hostlocalqa.StateQueued && (b.State == hostlocalqa.StateReceived || b.State == hostlocalqa.StateFailed || b.State == hostlocalqa.StateUncertain)) ||
			(cur.State == hostlocalqa.StateReceived && (b.State == hostlocalqa.StateAcknowledged || b.State == hostlocalqa.StateFailed || b.State == hostlocalqa.StateUncertain))
		if !ok {
			return nil, errHostLocalQAConflict("delivery_state_conflict")
		}
		now := r.now().UTC()
		if b.State == hostlocalqa.StateReceived && !now.Before(cur.ExpiresAt) {
			return nil, errHostLocalQAConflict("delivery_expired")
		}
		var url, code any
		if b.ResultCommentURL != "" {
			url = b.ResultCommentURL
		}
		if b.ErrorCode != "" {
			code = b.ErrorCode
		}
		if _, err = tx.ExecContext(ctx, `UPDATE host_local_qa_deliveries SET state = $2, updated_at = $3, observed_at = $4,
			received_at = CASE WHEN $2 = 'received' THEN $3 ELSE received_at END,
			acknowledged_at = CASE WHEN $2 = 'acknowledged' THEN $3 ELSE acknowledged_at END,
			result_comment_url = COALESCE($5, result_comment_url), error_code = COALESCE($6, error_code)
			WHERE id = $1`, delivery, b.State, now, b.ObservedAt.UTC(), url, code); err != nil {
			return nil, err
		}
	}
	row, err := r.loadRow(ctx, tx, `WHERE d.id = $1`, delivery)
	if err != nil {
		return nil, err
	}
	rec, err := row.receipt()
	if err != nil {
		return nil, err
	}
	return rec, tx.Commit()
}

// HostLocalQACheck is the authoritative guard answer.
type HostLocalQACheck struct {
	Active            bool                 `json:"active"`
	ReceiverID        uuid.UUID            `json:"receiver_id"`
	AdmissionRevision int64                `json:"admission_revision"`
	DeliveryID        uuid.UUID            `json:"delivery_id"`
	RequestID         uuid.UUID            `json:"request_id"`
	Capability        string               `json:"capability"`
	PolicySHA256      string               `json:"policy_sha256"`
	TextSHA256        string               `json:"text_sha256"`
	Target            hostlocalqa.Target   `json:"target"`
	Producer          hostlocalqa.Producer `json:"producer"`
	Expires           int64                `json:"expires"`
	CheckedAt         string               `json:"checked_at"`
}

// Check answers whether the stored assignment is active right now, from current
// authoritative state: the receiver admission is unrevoked, unexpired, at the
// delivery's revision and owned by the calling runner; its runner credential is
// live; the sender is still allowed by both policy and admission; the delivery
// is neither failed nor uncertain nor expired; and every field of the body
// equals the stored envelope. Anything else is the same 403. It writes nothing:
// checking is replay-safe and never marks anything installed or loaded.
func (r *HostLocalQARepo) Check(ctx context.Context, ws, agent, runner, delivery uuid.UUID, b *hostlocalqa.Binding) (*HostLocalQACheck, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := r.now().UTC()
	var cur struct {
		hostLocalQARow
		Revision  int64          `db:"rc_revision"`
		Allowed   pq.StringArray `db:"allowed_senders"`
		ExpiresAt time.Time      `db:"expires_at"`
	}
	err = tx.GetContext(ctx, &cur, `SELECT d.id, d.receiver_id, d.sender_agent_id, d.envelope, d.state, d.received_at,
		d.acknowledged_at, d.result_comment_url, d.error_code, d.created_at, d.expires_at,
		rc.admission_revision AS rc_revision, rc.allowed_senders
		FROM host_local_qa_deliveries d JOIN host_local_qa_receivers rc ON rc.id = d.receiver_id
		WHERE d.id = $1 AND d.workspace_id = $2 AND rc.agent_id = $3 AND rc.runner_id = $4 AND rc.revoked_at IS NULL
		  AND rc.expires_at > $5 AND rc.capability = $6
		  AND EXISTS (SELECT 1 FROM ext_agent_chat_runners cr WHERE cr.id = rc.runner_id AND cr.revoked_at IS NULL)
		FOR SHARE OF d, rc`, delivery, ws, agent, runner, now, hostlocalqa.Capability)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errHostLocalQACheckDenied()
	}
	if err != nil {
		return nil, err
	}
	env, err := cur.envelope()
	if err != nil {
		return nil, err
	}
	live := cur.State == hostlocalqa.StateQueued || cur.State == hostlocalqa.StateReceived || cur.State == hostlocalqa.StateAcknowledged
	if !live || !now.Before(cur.ExpiresAt) || cur.Revision != env.ExpectedRevision ||
		!b.Matches(delivery, cur.ReceiverID, env) || !r.senderAllowed(cur.Allowed, cur.SenderAgentID) {
		return nil, errHostLocalQACheckDenied()
	}
	return &HostLocalQACheck{Active: true, ReceiverID: cur.ReceiverID, AdmissionRevision: env.ExpectedRevision,
		DeliveryID: delivery, RequestID: env.RequestID, Capability: env.Capability, PolicySHA256: env.PolicySHA256,
		TextSHA256: env.TextSHA256, Target: env.Target, Producer: env.Producer, Expires: env.Expires,
		CheckedAt: now.Format(time.RFC3339)}, nil
}
