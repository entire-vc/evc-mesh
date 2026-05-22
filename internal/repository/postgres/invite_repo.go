package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// InviteRepo implements repository.WorkspaceInviteRepository with PostgreSQL.
type InviteRepo struct {
	db *sqlx.DB
}

// NewInviteRepo creates a new InviteRepo.
func NewInviteRepo(db *sqlx.DB) *InviteRepo {
	return &InviteRepo{db: db}
}

func (r *InviteRepo) Create(ctx context.Context, invite *domain.WorkspaceInvite) error {
	const q = `
		INSERT INTO user_invites (id, workspace_id, email, role, token, invited_by, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := r.db.ExecContext(ctx, q,
		invite.ID, invite.WorkspaceID, invite.Email, invite.Role,
		invite.Token, invite.InvitedBy, invite.ExpiresAt, invite.CreatedAt,
	)
	return err
}

func (r *InviteRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.WorkspaceInvite, error) {
	const q = `SELECT id, workspace_id, email, role, token, invited_by, expires_at, accepted_at, created_at FROM user_invites WHERE id = $1`
	var inv domain.WorkspaceInvite
	if err := r.db.GetContext(ctx, &inv, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &inv, nil
}

func (r *InviteRepo) GetByToken(ctx context.Context, token string) (*domain.WorkspaceInvite, error) {
	const q = `SELECT id, workspace_id, email, role, token, invited_by, expires_at, accepted_at, created_at FROM user_invites WHERE token = $1`
	var inv domain.WorkspaceInvite
	if err := r.db.GetContext(ctx, &inv, q, token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &inv, nil
}

func (r *InviteRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceInvite, error) {
	const q = `
		SELECT id, workspace_id, email, role, token, invited_by, expires_at, accepted_at, created_at
		FROM user_invites
		WHERE workspace_id = $1 AND accepted_at IS NULL AND expires_at > NOW()
		ORDER BY created_at DESC
	`
	var invites []domain.WorkspaceInvite
	if err := r.db.SelectContext(ctx, &invites, q, workspaceID); err != nil {
		return nil, err
	}
	return invites, nil
}

func (r *InviteRepo) Accept(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE user_invites SET accepted_at = NOW() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

func (r *InviteRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM user_invites WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

func (r *InviteRepo) DeleteExpired(ctx context.Context) (int64, error) {
	const q = `DELETE FROM user_invites WHERE expires_at < NOW() AND accepted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
