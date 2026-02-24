package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// RefreshTokenRepo implements repository.RefreshTokenRepository with PostgreSQL.
type RefreshTokenRepo struct {
	db *sqlx.DB
}

// NewRefreshTokenRepo creates a new RefreshTokenRepo.
func NewRefreshTokenRepo(db *sqlx.DB) *RefreshTokenRepo {
	return &RefreshTokenRepo{db: db}
}

func (r *RefreshTokenRepo) Create(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	const q = `
		INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := r.db.ExecContext(ctx, q, uuid.New(), userID, tokenHash, expiresAt, time.Now())
	return err
}

func (r *RefreshTokenRepo) GetByHash(ctx context.Context, tokenHash string) (*repository.RefreshToken, error) {
	const q = `SELECT id, user_id, token_hash, expires_at, created_at, revoked_at FROM refresh_tokens WHERE token_hash = $1`
	var rt repository.RefreshToken
	if err := r.db.GetContext(ctx, &rt, q, tokenHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rt, nil
}

func (r *RefreshTokenRepo) RevokeByUserID(ctx context.Context, userID uuid.UUID) error {
	const q = `UPDATE refresh_tokens SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, time.Now(), userID)
	return err
}

func (r *RefreshTokenRepo) RevokeByHash(ctx context.Context, tokenHash string) error {
	const q = `UPDATE refresh_tokens SET revoked_at = $1 WHERE token_hash = $2 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, time.Now(), tokenHash)
	return err
}

func (r *RefreshTokenRepo) DeleteExpired(ctx context.Context) error {
	const q = `DELETE FROM refresh_tokens WHERE expires_at < $1`
	_, err := r.db.ExecContext(ctx, q, time.Now())
	return err
}
