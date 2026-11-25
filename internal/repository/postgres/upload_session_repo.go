package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Upload session kinds: what a confirmed upload becomes.
const (
	UploadKindDocumentAttachment = "document_attachment"
	UploadKindArtifact           = "artifact"
)

// UploadSession is one upload_sessions row: a declared two-step upload.
type UploadSession struct {
	ID            uuid.UUID  `db:"id"`
	WorkspaceID   uuid.UUID  `db:"workspace_id"`
	Kind          string     `db:"kind"`
	TargetID      uuid.UUID  `db:"target_id"`
	Name          string     `db:"name"`
	MimeType      string     `db:"mime_type"`
	ArtifactType  *string    `db:"artifact_type"`
	SizeBytes     int64      `db:"size_bytes"`
	SHA256        string     `db:"sha256"`
	StagingKey    string     `db:"staging_key"`
	CreatedBy     uuid.UUID  `db:"created_by"`
	CreatedByType string     `db:"created_by_type"`
	CreatedAt     time.Time  `db:"created_at"`
	ExpiresAt     time.Time  `db:"expires_at"`
	ConfirmedAt   *time.Time `db:"confirmed_at"`
	ResultID      *uuid.UUID `db:"result_id"`
	ClaimedUntil  *time.Time `db:"claimed_until"`
	// PendingResultID is the id the result row gets; set by the first Claim.
	PendingResultID *uuid.UUID `db:"pending_result_id"`
}

const uploadSessionCols = `id, workspace_id, kind, target_id, name, mime_type, artifact_type, size_bytes,
	sha256, staging_key, created_by, created_by_type, created_at, expires_at, confirmed_at, result_id, claimed_until, pending_result_id`

// UploadSessionRepo stores two-step upload sessions.
type UploadSessionRepo struct{ db *sqlx.DB }

// NewUploadSessionRepo returns an UploadSessionRepo on db.
func NewUploadSessionRepo(db *sqlx.DB) *UploadSessionRepo { return &UploadSessionRepo{db: db} }

// Create inserts a new session.
func (r *UploadSessionRepo) Create(ctx context.Context, s *UploadSession) error {
	err := r.db.GetContext(ctx, &s.CreatedAt, `
		INSERT INTO upload_sessions (id, workspace_id, kind, target_id, name, mime_type, artifact_type,
			size_bytes, sha256, staging_key, created_by, created_by_type, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING created_at`,
		s.ID, s.WorkspaceID, s.Kind, s.TargetID, s.Name, s.MimeType, s.ArtifactType,
		s.SizeBytes, s.SHA256, s.StagingKey, s.CreatedBy, s.CreatedByType, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create upload session: %w", err)
	}
	return nil
}

// GetOwned returns the session only to the actor that created it, and nil
// for anyone else or an unknown id: the two answers must look the same.
func (r *UploadSessionRepo) GetOwned(ctx context.Context, id, createdBy uuid.UUID, createdByType string) (*UploadSession, error) {
	var s UploadSession
	err := r.db.GetContext(ctx, &s, `SELECT `+uploadSessionCols+` FROM upload_sessions
		WHERE id = $1 AND created_by = $2 AND created_by_type = $3`, id, createdBy, createdByType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get upload session: %w", err)
	}
	return &s, nil
}

// Claim takes the confirm lease on an open, unexpired session whose lease is
// free or lapsed, and returns the id the result row must get: the one an
// earlier confirm already fixed, or candidate on the first claim. ok is false
// when another confirm holds the lease, the session is already confirmed, or
// it has expired; the caller re-reads to tell which.
func (r *UploadSessionRepo) Claim(ctx context.Context, id uuid.UUID, lease time.Duration, candidate uuid.UUID) (resultID uuid.UUID, ok bool, err error) {
	err = r.db.GetContext(ctx, &resultID, `
		UPDATE upload_sessions SET claimed_until = now() + make_interval(secs => $2),
			pending_result_id = COALESCE(pending_result_id, $3)
		WHERE id = $1 AND confirmed_at IS NULL AND expires_at > now()
		  AND (claimed_until IS NULL OR claimed_until <= now())
		RETURNING pending_result_id`, id, lease.Seconds(), candidate)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("claim upload session: %w", err)
	}
	return resultID, true, nil
}

// Release drops the confirm lease of a confirm that did not finish, so the
// caller may fix the upload and confirm again.
func (r *UploadSessionRepo) Release(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE upload_sessions SET claimed_until = NULL WHERE id = $1 AND confirmed_at IS NULL`, id)
	return err
}

// MarkConfirmed records the row the upload became and drops the lease.
func (r *UploadSessionRepo) MarkConfirmed(ctx context.Context, id, resultID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE upload_sessions SET confirmed_at = now(), result_id = $2, claimed_until = NULL
		WHERE id = $1 AND confirmed_at IS NULL`, id, resultID)
	if err != nil {
		return fmt.Errorf("confirm upload session: %w", err)
	}
	return nil
}

// Sweep removes up to limit sessions that will never be confirmed: open ones
// expired more than grace ago whose lease is free, and confirmed ones older
// than retention (kept that long only so a repeated confirm still answers).
// For each, deleteObject removes the staged object first; a session whose
// object could not be removed stays, so the next sweep retries it. Rows
// another sweeper holds are skipped. It returns how many sessions it removed.
func (r *UploadSessionRepo) Sweep(ctx context.Context, grace, retention time.Duration, limit int,
	deleteObject func(ctx context.Context, key string) error,
) (int, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var rows []struct {
		ID         uuid.UUID `db:"id"`
		StagingKey string    `db:"staging_key"`
	}
	if err = tx.SelectContext(ctx, &rows, `
		SELECT id, staging_key FROM upload_sessions
		WHERE (confirmed_at IS NULL
		       AND expires_at < now() - make_interval(secs => $1)
		       AND (claimed_until IS NULL OR claimed_until <= now()))
		   OR confirmed_at < now() - make_interval(secs => $2)
		ORDER BY created_at
		LIMIT $3
		FOR UPDATE SKIP LOCKED`, grace.Seconds(), retention.Seconds(), limit); err != nil {
		return 0, fmt.Errorf("select sweepable upload sessions: %w", err)
	}
	removed := 0
	for _, row := range rows {
		if delErr := deleteObject(ctx, row.StagingKey); delErr != nil {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM upload_sessions WHERE id = $1`, row.ID); err != nil {
			return 0, fmt.Errorf("delete upload session: %w", err)
		}
		removed++
	}
	return removed, tx.Commit()
}
