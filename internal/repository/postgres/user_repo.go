package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// UserRepo implements repository.UserRepository with PostgreSQL.
type UserRepo struct {
	db *sqlx.DB
}

// NewUserRepo creates a new UserRepo.
func NewUserRepo(db *sqlx.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) Create(ctx context.Context, user *domain.User) error {
	const q = `
		INSERT INTO users (id, email, password_hash, display_name, avatar_url, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := r.db.ExecContext(ctx, q,
		user.ID, user.Email, user.PasswordHash, user.Name,
		user.AvatarURL, user.IsActive, user.CreatedAt, user.UpdatedAt,
	)
	return err
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	const q = `SELECT id, email, password_hash, display_name, avatar_url, is_active, created_at, updated_at FROM users WHERE id = $1`
	var user domain.User
	if err := r.db.GetContext(ctx, &user, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	const q = `SELECT id, email, password_hash, display_name, avatar_url, is_active, created_at, updated_at FROM users WHERE email = $1`
	var user domain.User
	if err := r.db.GetContext(ctx, &user, q, email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

func (r *UserRepo) Update(ctx context.Context, user *domain.User) error {
	const q = `
		UPDATE users SET display_name = $2, avatar_url = $3, is_active = $4, updated_at = $5
		WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, q,
		user.ID, user.Name, user.AvatarURL, user.IsActive, time.Now(),
	)
	return err
}
