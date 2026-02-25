package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// WorkspaceMemberRepo implements repository.WorkspaceMemberRepository with PostgreSQL.
type WorkspaceMemberRepo struct {
	db *sqlx.DB
}

// NewWorkspaceMemberRepo creates a new WorkspaceMemberRepo.
func NewWorkspaceMemberRepo(db *sqlx.DB) *WorkspaceMemberRepo {
	return &WorkspaceMemberRepo{db: db}
}

func (r *WorkspaceMemberRepo) Create(ctx context.Context, member *domain.WorkspaceMember) error {
	const q = `
		INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := r.db.ExecContext(ctx, q,
		member.ID, member.WorkspaceID, member.UserID,
		member.Role, member.CreatedAt, member.UpdatedAt,
	)
	return err
}

func (r *WorkspaceMemberRepo) GetByWorkspaceAndUser(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	const q = `SELECT id, workspace_id, user_id, role, created_at, updated_at FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`
	var member domain.WorkspaceMember
	if err := r.db.GetContext(ctx, &member, q, workspaceID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &member, nil
}

// GetRole returns the workspace_role for a specific workspace + user combination.
// Returns an error (sql.ErrNoRows wrapped) if the membership does not exist.
func (r *WorkspaceMemberRepo) GetRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error) {
	const q = `SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`
	var role string
	if err := r.db.GetContext(ctx, &role, q, workspaceID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("user is not a member of this workspace: %w", sql.ErrNoRows)
		}
		return "", fmt.Errorf("GetRole: %w", err)
	}
	return role, nil
}
