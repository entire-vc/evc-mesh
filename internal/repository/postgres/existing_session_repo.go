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

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Existing-session metadata delivery (contract runner-existing-session-metadata-v1).
// A receiver registers the identity of an already running native session; a
// sender that receiver listed queues one bounded text for exactly that identity;
// only the exact registered receiver can list and acknowledge it. Nothing here
// starts, adopts or restarts a session, and a delivery that may already have
// been injected is never handed out again.
const (
	ExistingSessionCapability = "readonly_metadata"
	ExistingSessionMaxText    = 16 * 1024
	ExistingSessionMaxSenders = 32

	ExistingSessionQueued       = "queued"
	ExistingSessionReceived     = "received"
	ExistingSessionAcknowledged = "acknowledged"
	ExistingSessionFailed       = "failed"
)

// ExistingSessionRepo stores receiver registrations and their deliveries.
type ExistingSessionRepo struct {
	db  *sqlx.DB
	now func() time.Time
}

func NewExistingSessionRepo(db *sqlx.DB) *ExistingSessionRepo {
	return &ExistingSessionRepo{db: db, now: time.Now}
}

// ExistingSessionTarget is the complete, immutable identity of a receiver.
type ExistingSessionTarget struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	AgentID     uuid.UUID `json:"agent_id"`
	RunnerID    uuid.UUID `json:"runner_id"`
	HostID      string    `json:"host_id"`
	Hostname    string    `json:"hostname"`
	SessionID   string    `json:"session_id"`
}

// ExistingSessionReceiver is a registered receiver as its owner sees it.
type ExistingSessionReceiver struct {
	ID                uuid.UUID      `db:"id" json:"receiver_id"`
	WorkspaceID       uuid.UUID      `db:"workspace_id" json:"workspace_id"`
	AgentID           uuid.UUID      `db:"agent_id" json:"agent_id"`
	RunnerID          uuid.UUID      `db:"runner_id" json:"runner_id"`
	HostID            string         `db:"host_id" json:"host_id"`
	Hostname          string         `db:"hostname" json:"hostname"`
	SessionID         string         `db:"session_id" json:"session_id"`
	Capability        string         `db:"capability" json:"capability"`
	AdmissionRevision int64          `db:"admission_revision" json:"admission_revision"`
	AllowedSenders    pq.StringArray `db:"allowed_senders" json:"-"`
	AllowedSenderIDs  []uuid.UUID    `db:"-" json:"allowed_sender_agent_ids"`
}

func (r *ExistingSessionReceiver) fill() {
	r.AllowedSenderIDs = make([]uuid.UUID, 0, len(r.AllowedSenders))
	for _, s := range r.AllowedSenders {
		if id, err := uuid.Parse(s); err == nil {
			r.AllowedSenderIDs = append(r.AllowedSenderIDs, id)
		}
	}
}

// ExistingSessionReceiverIdentity is the receiver part of a receipt.
type ExistingSessionReceiverIdentity struct {
	ReceiverID        uuid.UUID `json:"receiver_id"`
	RunnerID          uuid.UUID `json:"runner_id"`
	HostID            string    `json:"host_id"`
	Hostname          string    `json:"hostname"`
	SessionID         string    `json:"session_id"`
	AdmissionRevision int64     `json:"admission_revision"`
}

// ExistingSessionReceipt is immutable metadata about a delivery. It never
// carries the text or any transcript.
type ExistingSessionReceipt struct {
	DeliveryID       uuid.UUID                       `json:"delivery_id"`
	RequestID        uuid.UUID                       `json:"request_id"`
	Target           ExistingSessionTarget           `json:"target"`
	ExpectedRevision int64                           `json:"expected_admission_revision"`
	Capability       string                          `json:"capability"`
	TextSHA256       string                          `json:"text_sha256"`
	State            string                          `json:"state"`
	ReceiverIdentity ExistingSessionReceiverIdentity `json:"receiver_identity"`
	ReceivedAt       *time.Time                      `json:"received_at"`
	AcknowledgedAt   *time.Time                      `json:"acknowledged_at"`
	ResultCommentURL *string                         `json:"result_comment_url"`
	ErrorCode        *string                         `json:"error_code"`
}

// ExistingSessionQueued is one delivery as the receiver lists it.
type ExistingSessionQueuedDelivery struct {
	DeliveryID       uuid.UUID             `json:"delivery_id"`
	RequestID        uuid.UUID             `json:"request_id"`
	Text             string                `json:"text"`
	TextSHA256       string                `json:"text_sha256"`
	Capability       string                `json:"capability"`
	Target           ExistingSessionTarget `json:"target"`
	ExpectedRevision int64                 `json:"expected_admission_revision"`
	CreatedAt        time.Time             `json:"created_at"`
}

// ExistingSessionRequest is a validated sender request.
type ExistingSessionRequest struct {
	RequestID  uuid.UUID
	Target     ExistingSessionTarget
	Expected   int64
	Text       string
	TextSHA256 string
}

func errExistingSessionDenied() error {
	return apierror.ForbiddenWithDetails("existing-session target is not available to this caller", "existing_session_target_denied")
}

func errExistingSessionConflict(details string) error {
	e := apierror.Conflict("existing-session request conflicts with the stored one")
	e.Details = details
	return e
}

// Register adds or re-announces a receiver identity. Every call bumps the
// admission revision, and queued deliveries addressed to an older revision fail
// with admission_stale: a restarted or re-admitted receiver never inherits work
// that was admitted for a previous registration.
func (r *ExistingSessionRepo) Register(ctx context.Context, ws, agent, runner uuid.UUID, hostID, hostname, sessionID string, allowed []uuid.UUID) (*ExistingSessionReceiver, error) {
	ids := make([]string, 0, len(allowed))
	seen := map[uuid.UUID]bool{}
	for _, a := range allowed {
		if !seen[a] {
			seen[a] = true
			ids = append(ids, a.String())
		}
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if cerr := tx.GetContext(ctx, &n, `SELECT count(*) FROM agents WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, pq.Array(ids)); cerr != nil {
		return nil, cerr
	}
	if n != len(ids) {
		return nil, apierror.BadRequest("allowed_sender_agent_ids must be agents of this workspace")
	}
	var out ExistingSessionReceiver
	err = tx.GetContext(ctx, &out, `INSERT INTO existing_session_receivers
		(workspace_id, agent_id, runner_id, host_id, hostname, session_id, capability, admission_revision, allowed_senders)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8::uuid[])
		ON CONFLICT (workspace_id, agent_id, runner_id, host_id, session_id) DO UPDATE
		SET admission_revision = existing_session_receivers.admission_revision + 1,
		    hostname = EXCLUDED.hostname, allowed_senders = EXCLUDED.allowed_senders,
		    revoked_at = NULL, registered_at = now()
		RETURNING id, workspace_id, agent_id, runner_id, host_id, hostname, session_id, capability, admission_revision, allowed_senders`,
		ws, agent, runner, hostID, hostname, sessionID, ExistingSessionCapability, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	if err := r.failStale(ctx, tx, out.ID, out.AdmissionRevision); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out.fill()
	return &out, nil
}

// failStale fails queued deliveries of a receiver that address another revision.
func (r *ExistingSessionRepo) failStale(ctx context.Context, q sqlx.ExecerContext, receiver uuid.UUID, revision int64) error {
	_, err := q.ExecContext(ctx, `UPDATE existing_session_deliveries
		SET state = 'failed', error_code = 'admission_stale', body = NULL, updated_at = $3
		WHERE receiver_id = $1 AND state = 'queued' AND expected_admission_revision <> $2`, receiver, revision, r.now().UTC())
	return err
}

// Revoke ends a registration of the calling runner. Queued work fails as
// receiver_revoked; a received delivery that was never acknowledged fails as
// delivery_uncertain (it may have been injected), and its text is dropped.
func (r *ExistingSessionRepo) Revoke(ctx context.Context, ws, agent, runner, receiver uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE existing_session_receivers SET revoked_at = $5
		WHERE id = $1 AND workspace_id = $2 AND agent_id = $3 AND runner_id = $4 AND revoked_at IS NULL`,
		receiver, ws, agent, runner, r.now().UTC())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("receiver")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE existing_session_deliveries
		SET state = 'failed', body = NULL, updated_at = $2,
		    error_code = CASE WHEN state = 'received' THEN 'delivery_uncertain' ELSE 'receiver_revoked' END
		WHERE receiver_id = $1 AND state IN ('queued', 'received')`, receiver, r.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

const existingSessionReceiptSelect = `SELECT d.id, d.request_id, d.target, d.expected_admission_revision, d.capability, d.text_sha256,
	d.state, d.received_at, d.acknowledged_at, d.result_comment_url, d.error_code,
	rc.id AS receiver_id, rc.runner_id, rc.host_id, rc.hostname, rc.session_id
	FROM existing_session_deliveries d JOIN existing_session_receivers rc ON rc.id = d.receiver_id `

type existingSessionReceiptRow struct {
	ID               uuid.UUID  `db:"id"`
	RequestID        uuid.UUID  `db:"request_id"`
	Target           []byte     `db:"target"`
	Expected         int64      `db:"expected_admission_revision"`
	Capability       string     `db:"capability"`
	TextSHA256       string     `db:"text_sha256"`
	State            string     `db:"state"`
	ReceivedAt       *time.Time `db:"received_at"`
	AcknowledgedAt   *time.Time `db:"acknowledged_at"`
	ResultCommentURL *string    `db:"result_comment_url"`
	ErrorCode        *string    `db:"error_code"`
	ReceiverID       uuid.UUID  `db:"receiver_id"`
	RunnerID         uuid.UUID  `db:"runner_id"`
	HostID           string     `db:"host_id"`
	Hostname         string     `db:"hostname"`
	SessionID        string     `db:"session_id"`
}

func (row *existingSessionReceiptRow) receipt() (*ExistingSessionReceipt, error) {
	var target ExistingSessionTarget
	if err := json.Unmarshal(row.Target, &target); err != nil {
		return nil, err
	}
	return &ExistingSessionReceipt{
		DeliveryID: row.ID, RequestID: row.RequestID, Target: target, ExpectedRevision: row.Expected,
		Capability: row.Capability, TextSHA256: row.TextSHA256, State: row.State,
		ReceiverIdentity: ExistingSessionReceiverIdentity{ReceiverID: row.ReceiverID, RunnerID: row.RunnerID,
			HostID: row.HostID, Hostname: row.Hostname, SessionID: row.SessionID, AdmissionRevision: row.Expected},
		ReceivedAt: row.ReceivedAt, AcknowledgedAt: row.AcknowledgedAt,
		ResultCommentURL: row.ResultCommentURL, ErrorCode: row.ErrorCode,
	}, nil
}

func (r *ExistingSessionRepo) loadReceipt(ctx context.Context, q sqlx.QueryerContext, where string, args ...any) (*ExistingSessionReceipt, error) {
	var row existingSessionReceiptRow
	if err := sqlx.GetContext(ctx, q, &row, existingSessionReceiptSelect+where, args...); err != nil {
		return nil, err
	}
	return row.receipt()
}

// Enqueue queues a delivery for an exactly registered target. created reports
// whether a new row was written (false = idempotent replay). Order of checks:
// replay of the same request first, then target identity, then the receiver's
// own sender allow-list (an unlisted sender gets the same refusal as an unknown
// target), then the admission revision.
func (r *ExistingSessionRepo) Enqueue(ctx context.Context, ws, sender uuid.UUID, req ExistingSessionRequest) (*ExistingSessionReceipt, bool, error) {
	if req.Target.WorkspaceID != ws {
		return nil, false, errExistingSessionDenied()
	}
	if rec, err := r.replay(ctx, ws, sender, req); rec != nil || err != nil {
		return rec, false, err
	}
	// The receiver row is locked FOR SHARE until the insert commits: a concurrent
	// re-registration or revocation (both UPDATE the row) waits, so a delivery is
	// never queued under an admission that was already replaced.
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// A sender's request id is immutable across capabilities: one already used
	// for a host-local QA assignment is a conflict here, never a second delivery.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"request:"+ws.String()+":"+sender.String()+":"+req.RequestID.String()); err != nil {
		return nil, false, err
	}
	var qaUsed bool
	err = tx.GetContext(ctx, &qaUsed, `SELECT EXISTS (SELECT 1 FROM host_local_qa_deliveries
		WHERE workspace_id = $1 AND sender_agent_id = $2 AND request_id = $3)`, ws, sender, req.RequestID)
	if err != nil {
		return nil, false, err
	}
	if qaUsed {
		return nil, false, errExistingSessionConflict("request_conflict")
	}
	var rc struct {
		ID       uuid.UUID      `db:"id"`
		Hostname string         `db:"hostname"`
		Revision int64          `db:"admission_revision"`
		Allowed  pq.StringArray `db:"allowed_senders"`
	}
	err = tx.GetContext(ctx, &rc, `SELECT id, hostname, admission_revision, allowed_senders FROM existing_session_receivers
		WHERE workspace_id = $1 AND agent_id = $2 AND runner_id = $3 AND host_id = $4 AND session_id = $5
		  AND revoked_at IS NULL AND capability = $6
		  AND EXISTS (SELECT 1 FROM ext_agent_chat_runners cr WHERE cr.id = runner_id AND cr.revoked_at IS NULL)
		FOR SHARE`,
		ws, req.Target.AgentID, req.Target.RunnerID, req.Target.HostID, req.Target.SessionID, ExistingSessionCapability)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, errExistingSessionDenied()
	}
	if err != nil {
		return nil, false, err
	}
	allowed := false
	for _, a := range rc.Allowed {
		if a == sender.String() {
			allowed = true
		}
	}
	if !allowed || rc.Hostname != req.Target.Hostname {
		return nil, false, errExistingSessionDenied()
	}
	if rc.Revision != req.Expected {
		e := apierror.Conflict("admission revision is stale")
		e.Details = "admission_stale"
		return nil, false, e
	}
	target, _ := json.Marshal(req.Target)
	var id uuid.UUID
	err = tx.GetContext(ctx, &id, `INSERT INTO existing_session_deliveries
		(workspace_id, request_id, sender_agent_id, receiver_id, target, expected_admission_revision, capability, text_sha256, body)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9)
		ON CONFLICT (workspace_id, sender_agent_id, request_id) DO NOTHING RETURNING id`,
		ws, req.RequestID, sender, rc.ID, target, req.Expected, ExistingSessionCapability, req.TextSHA256, req.Text)
	if errors.Is(err, sql.ErrNoRows) { // lost a race with the same request id
		_ = tx.Rollback()
		rec, rerr := r.replay(ctx, ws, sender, req)
		return rec, false, rerr
	}
	if err != nil {
		return nil, false, err
	}
	rec, err := r.loadReceipt(ctx, tx, `WHERE d.id = $1`, id)
	if err != nil {
		return nil, false, err
	}
	return rec, true, tx.Commit()
}

// replay returns the stored receipt of the same sender+request_id, or a
// conflict when its target, revision or hash differ; nil,nil when none exists.
func (r *ExistingSessionRepo) replay(ctx context.Context, ws, sender uuid.UUID, req ExistingSessionRequest) (*ExistingSessionReceipt, error) {
	rec, err := r.loadReceipt(ctx, r.db, `WHERE d.workspace_id = $1 AND d.sender_agent_id = $2 AND d.request_id = $3`, ws, sender, req.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.Target != req.Target || rec.TextSHA256 != req.TextSHA256 || rec.ExpectedRevision != req.Expected {
		return nil, errExistingSessionConflict("request_conflict")
	}
	return rec, nil
}

// Get returns the receipt of a delivery the caller sent; another sender's and
// an unknown id are the same 404.
func (r *ExistingSessionRepo) Get(ctx context.Context, ws, sender, delivery uuid.UUID) (*ExistingSessionReceipt, error) {
	rec, err := r.loadReceipt(ctx, r.db, `WHERE d.id = $1 AND d.workspace_id = $2 AND d.sender_agent_id = $3`, delivery, ws, sender)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("delivery")
	}
	return rec, err
}

// Pending lists the queued deliveries of the exact current registration. A
// delivery that is no longer queued (possibly injected) is never listed again.
func (r *ExistingSessionRepo) Pending(ctx context.Context, ws, agent, runner uuid.UUID, hostID, sessionID string) ([]ExistingSessionQueuedDelivery, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// FOR SHARE keeps a concurrent re-registration (which updates the row) from
	// changing the revision or allow-list between the read and the listing.
	var rc struct {
		ID       uuid.UUID `db:"id"`
		Revision int64     `db:"admission_revision"`
	}
	err = tx.GetContext(ctx, &rc, `SELECT id, admission_revision FROM existing_session_receivers
		WHERE workspace_id = $1 AND agent_id = $2 AND runner_id = $3 AND host_id = $4 AND session_id = $5 AND revoked_at IS NULL
		FOR SHARE`,
		ws, agent, runner, hostID, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errExistingSessionDenied()
	}
	if err != nil {
		return nil, err
	}
	if err := r.failStale(ctx, tx, rc.ID, rc.Revision); err != nil {
		return nil, err
	}
	var rows []struct {
		ID         uuid.UUID `db:"id"`
		RequestID  uuid.UUID `db:"request_id"`
		Body       string    `db:"body"`
		TextSHA256 string    `db:"text_sha256"`
		Target     []byte    `db:"target"`
		Expected   int64     `db:"expected_admission_revision"`
		CreatedAt  time.Time `db:"created_at"`
	}
	if err := tx.SelectContext(ctx, &rows, `SELECT id, request_id, body, text_sha256, target, expected_admission_revision, created_at
		FROM existing_session_deliveries WHERE receiver_id = $1 AND state = 'queued' AND body IS NOT NULL
		  AND expected_admission_revision = $2
		ORDER BY created_at, id LIMIT 50`, rc.ID, rc.Revision); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]ExistingSessionQueuedDelivery, 0, len(rows))
	for _, row := range rows {
		var target ExistingSessionTarget
		if err := json.Unmarshal(row.Target, &target); err != nil {
			return nil, err
		}
		out = append(out, ExistingSessionQueuedDelivery{DeliveryID: row.ID, RequestID: row.RequestID, Text: row.Body,
			TextSHA256: row.TextSHA256, Capability: ExistingSessionCapability, Target: target,
			ExpectedRevision: row.Expected, CreatedAt: row.CreatedAt})
	}
	return out, nil
}

// ExistingSessionAck is a receiver's report on one delivery.
type ExistingSessionAck struct {
	HostID, SessionID string
	Revision          int64
	State             string
	ResultCommentURL  string
	ErrorCode         string
}

// Ack moves a delivery forward: queued→received|failed, received→acknowledged|failed.
// Repeating the current state is a no-op; anything else is a 409. Only the exact
// registered receiver (runner, host, session, revision) may report; every other
// caller gets the same 403 as for an unknown delivery.
func (r *ExistingSessionRepo) Ack(ctx context.Context, ws, agent, runner, delivery uuid.UUID, a ExistingSessionAck) (*ExistingSessionReceipt, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var cur struct {
		State    string `db:"state"`
		Expected int64  `db:"expected_admission_revision"`
		HostID   string `db:"host_id"`
		Session  string `db:"session_id"`
	}
	err = tx.GetContext(ctx, &cur, `SELECT d.state, d.expected_admission_revision, rc.host_id, rc.session_id
		FROM existing_session_deliveries d JOIN existing_session_receivers rc ON rc.id = d.receiver_id
		WHERE d.id = $1 AND d.workspace_id = $2 AND rc.agent_id = $3 AND rc.runner_id = $4 AND rc.revoked_at IS NULL
		FOR UPDATE OF d`, delivery, ws, agent, runner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errExistingSessionDenied()
	}
	if err != nil {
		return nil, err
	}
	if cur.HostID != a.HostID || cur.Session != a.SessionID || cur.Expected != a.Revision {
		return nil, errExistingSessionDenied()
	}
	if cur.State != a.State {
		ok := (cur.State == ExistingSessionQueued && (a.State == ExistingSessionReceived || a.State == ExistingSessionFailed)) ||
			(cur.State == ExistingSessionReceived && (a.State == ExistingSessionAcknowledged || a.State == ExistingSessionFailed))
		if !ok {
			return nil, errExistingSessionConflict("delivery_state_conflict")
		}
		now := r.now().UTC()
		var url, code any
		if a.ResultCommentURL != "" {
			url = a.ResultCommentURL
		}
		if a.ErrorCode != "" {
			code = a.ErrorCode
		}
		_, err = tx.ExecContext(ctx, `UPDATE existing_session_deliveries SET state = $2, updated_at = $3,
			received_at = CASE WHEN $2 = 'received' OR (received_at IS NULL AND $2 = 'acknowledged') THEN $3 ELSE received_at END,
			acknowledged_at = CASE WHEN $2 = 'acknowledged' THEN $3 ELSE acknowledged_at END,
			result_comment_url = COALESCE($4, result_comment_url), error_code = COALESCE($5, error_code),
			body = CASE WHEN $2 IN ('acknowledged', 'failed') THEN NULL ELSE body END
			WHERE id = $1`, delivery, a.State, now, url, code)
		if err != nil {
			return nil, err
		}
	}
	rec, err := r.loadReceipt(ctx, tx, `WHERE d.id = $1`, delivery)
	if err != nil {
		return nil, err
	}
	return rec, tx.Commit()
}
