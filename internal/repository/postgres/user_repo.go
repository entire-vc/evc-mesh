package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
		INSERT INTO users (id, email, password_hash, display_name, username, avatar_url, is_active, display_name_self_set, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := r.db.ExecContext(ctx, q,
		user.ID, user.Email, user.PasswordHash, user.Name, user.Username,
		user.AvatarURL, user.IsActive, user.DisplayNameSelfSet, user.CreatedAt, user.UpdatedAt,
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

// Count returns the total number of users on the instance.
func (r *UserRepo) Count(ctx context.Context) (int, error) {
	const q = `SELECT COUNT(*) FROM users`
	var n int
	if err := r.db.GetContext(ctx, &n, q); err != nil {
		return 0, err
	}
	return n, nil
}

// GetByID returns the user with the given id, or (nil, nil) if there is none.
//
// username is in the projection on purpose. It used to be missing, and since
// Update writes `username = NULLIF($5, ”)`, every read-modify-write through
// this function — PATCH /api/v1/auth/me is the only one users can reach —
// carried an empty Username back into a NOT NULL column and died on the
// constraint. Editing your own display name returned 500 for that reason alone.
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	const q = `SELECT id, email, password_hash, display_name, COALESCE(username, '') AS username,
	                  COALESCE(avatar_url, '') AS avatar_url, is_active, display_name_self_set, created_at, updated_at
	           FROM users WHERE id = $1`
	var user domain.User
	if err := r.db.GetContext(ctx, &user, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// GetByEmail returns the user with the given email address, or (nil, nil) if
// there is none. The match is case-insensitive and whitespace-insensitive,
// backed by the unique index ix_users_email_lower on lower(email) — callers
// should still pass an address through auth.NormalizeEmail, but a stray
// mixed-case address must never silently resolve to "no such user".
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	const q = `SELECT id, email, password_hash, display_name, COALESCE(username, '') AS username,
	                  COALESCE(avatar_url, '') AS avatar_url, is_active, display_name_self_set, created_at, updated_at
	           FROM users WHERE lower(email) = lower($1)`
	var user domain.User
	if err := r.db.GetContext(ctx, &user, q, strings.TrimSpace(email)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}

// Update writes the mutable profile columns.
//
// username keeps its current value when the struct carries an empty one:
// the column is NOT NULL, so a blank must mean "leave it alone" rather than
// "clear it". Writing NULL here is not a recoverable mistake — it aborts the
// whole statement, so a caller that merely forgot to load the username would
// lose an unrelated edit.
func (r *UserRepo) Update(ctx context.Context, user *domain.User) error {
	const q = `
		UPDATE users
		   SET display_name = $2,
		       avatar_url = $3,
		       is_active = $4,
		       username = COALESCE(NULLIF($5, ''), username),
		       display_name_self_set = $6,
		       updated_at = $7
		 WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, q,
		user.ID, user.Name, user.AvatarURL, user.IsActive, user.Username,
		user.DisplayNameSelfSet, time.Now(),
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

// SearchAddableUsers returns the users callerID is allowed to see when looking
// for somebody to add to a workspace, under two disjoint rules.
//
//  1. Exact address. lower(email) = the normalized query. Knowing somebody's
//     address is the credential for inviting them, and it is the only way to
//     reach a person you share nothing with. It discloses one bit about one
//     address the caller already typed, which POST /members discloses anyway by
//     answering 404 or 201.
//
//  2. People the caller already shares a workspace with, matched loosely on
//     name, username or address. This adds no reach: those users are on the
//     /members list of a workspace the caller belongs to, emails included. It is
//     what makes "add Maombi's designer to Prototypes" work by typing a name.
//
// What is deliberately absent is the third rule this function used to be —
// `email ILIKE '%q%' OR display_name ILIKE '%q%'` over the whole users table.
// Scoped to the caller's manage-members permission, that still meant anyone who
// created a workspace of their own (which is open to every authenticated user)
// became an owner and could page the entire instance's directory, addresses
// included, one letter at a time: ?q=a. Restricting the route to admins moved
// the bar to "make your own workspace", not to "be trusted with this".
//
// callerID may be uuid.Nil — an agent key authenticates a workspace, not a
// person, so there is no "shares a workspace with me" to compute. Rule 2 is
// then skipped and only the exact address resolves.
func (r *UserRepo) SearchAddableUsers(ctx context.Context, callerID uuid.UUID, query string, limit int) ([]domain.User, error) {
	const q = `
		SELECT u.id, u.email, u.password_hash, u.display_name, COALESCE(u.username, '') AS username,
		       COALESCE(u.avatar_url, '') AS avatar_url, u.is_active, u.display_name_self_set,
		       u.created_at, u.updated_at
		FROM users u
		WHERE u.is_active
		  AND (
		        lower(u.email) = $1
		     OR (
		          $2::uuid IS NOT NULL
		          AND EXISTS (
		            SELECT 1
		            FROM workspace_members mine
		            JOIN workspace_members theirs ON theirs.workspace_id = mine.workspace_id
		            WHERE mine.user_id = $2::uuid AND theirs.user_id = u.id
		          )
		          AND (u.display_name ILIKE $3 OR COALESCE(u.username, '') ILIKE $3 OR u.email ILIKE $3)
		        )
		      )
		ORDER BY (lower(u.email) = $1) DESC, u.display_name
		LIMIT $4
	`
	var caller any
	if callerID != uuid.Nil {
		caller = callerID
	}
	pattern := "%" + escapeLike(query) + "%"
	var users []domain.User
	if err := r.db.SelectContext(ctx, &users, q,
		strings.ToLower(strings.TrimSpace(query)), caller, pattern, limit,
	); err != nil {
		return nil, err
	}
	return users, nil
}

// escapeLike neutralizes the ILIKE metacharacters so that a query of "%" means
// the literal character and not "every user I am allowed to see".
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
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
	escaped := escapeLike(query)
	pattern := "%" + escaped + "%"
	prefixPattern := escaped + "%"
	var users []domain.User
	if err := r.db.SelectContext(ctx, &users, q, workspaceID, pattern, prefixPattern, limit); err != nil {
		return nil, err
	}
	return users, nil
}
