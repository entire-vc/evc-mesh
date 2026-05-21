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
		INSERT INTO users (id, email, password_hash, display_name, username, avatar_url, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.ExecContext(ctx, q,
		user.ID, user.Email, user.PasswordHash, user.Name, user.Username,
		user.AvatarURL, user.IsActive, user.CreatedAt, user.UpdatedAt,
	)
	return err
}

// UsernameExists reports whether any user already holds the given username
// (case-insensitive, matching the global ix_users_username unique index).
func (r *UserRepo) UsernameExists(ctx context.Context, username string) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username) = lower($1))`
	var exists bool
	if err := r.db.GetContext(ctx, &exists, q, username); err != nil {
		return false, err
	}
	return exists, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	const q = `SELECT id, email, password_hash, display_name, COALESCE(avatar_url, '') AS avatar_url, is_active, created_at, updated_at FROM users WHERE id = $1`
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
	const q = `SELECT id, email, password_hash, display_name, COALESCE(avatar_url, '') AS avatar_url, is_active, created_at, updated_at FROM users WHERE email = $1`
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
		UPDATE users SET display_name = $2, avatar_url = $3, is_active = $4, username = NULLIF($5, ''), updated_at = $6
		WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, q,
		user.ID, user.Name, user.AvatarURL, user.IsActive, user.Username, time.Now(),
	)
	return err
}

// GetByUsernameGlobal returns the user with the given username across all workspaces, or (nil, nil) if not found.
// Uses the global unique index ix_users_username (case-insensitive via lower()).
func (r *UserRepo) GetByUsernameGlobal(ctx context.Context, username string) (*domain.User, error) {
	const q = `
		SELECT id, email, password_hash, display_name, COALESCE(username, '') AS username,
		       avatar_url, is_active, created_at, updated_at
		FROM users WHERE lower(username) = lower($1) LIMIT 1
	`
	var user domain.User
	if err := r.db.GetContext(ctx, &user, q, username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// GetByUsername returns the user with the given username in the workspace, or (nil, nil) if not found.
func (r *UserRepo) GetByUsername(ctx context.Context, workspaceID uuid.UUID, username string) (*domain.User, error) {
	const q = `
		SELECT u.id, u.email, u.password_hash, u.display_name, COALESCE(u.username, '') AS username,
		       COALESCE(u.avatar_url, '') AS avatar_url, u.is_active, u.created_at, u.updated_at
		FROM users u
		JOIN workspace_members wm ON wm.user_id = u.id AND wm.workspace_id = $1
		WHERE u.username = $2
		LIMIT 1
	`
	var user domain.User
	if err := r.db.GetContext(ctx, &user, q, workspaceID, username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// SearchUsers returns up to limit users whose email or display_name match the query (ILIKE).
func (r *UserRepo) SearchUsers(ctx context.Context, query string, limit int) ([]domain.User, error) {
	const q = `
		SELECT id, email, password_hash, display_name, COALESCE(avatar_url, '') AS avatar_url, is_active, created_at, updated_at
		FROM users
		WHERE email ILIKE $1 OR display_name ILIKE $1
		ORDER BY display_name
		LIMIT $2
	`
	pattern := "%" + query + "%"
	var users []domain.User
	if err := r.db.SelectContext(ctx, &users, q, pattern, limit); err != nil {
		return nil, err
	}
	return users, nil
}

// SearchInWorkspace returns users who are workspace members and whose display_name, username,
// or email match the query (ILIKE), sorted by exact-prefix match first then display_name, up to limit results.
func (r *UserRepo) SearchInWorkspace(ctx context.Context, workspaceID uuid.UUID, query string, limit int) ([]domain.User, error) {
	const q = `
		SELECT u.id, u.email, u.password_hash, u.display_name,
		       COALESCE(u.username, '') AS username, COALESCE(u.avatar_url, '') AS avatar_url, u.is_active, u.created_at, u.updated_at
		FROM users u
		JOIN workspace_members wm ON wm.user_id = u.id AND wm.workspace_id = $1
		WHERE u.display_name ILIKE $2 OR COALESCE(u.username, '') ILIKE $2 OR u.email ILIKE $2
		ORDER BY
		  CASE WHEN COALESCE(u.username, '') ILIKE $3 OR u.display_name ILIKE $3 THEN 0 ELSE 1 END,
		  u.display_name
		LIMIT $4
	`
	pattern := "%" + query + "%"
	prefixPattern := query + "%"
	var users []domain.User
	if err := r.db.SelectContext(ctx, &users, q, workspaceID, pattern, prefixPattern, limit); err != nil {
		return nil, err
	}
	return users, nil
}
