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

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
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
		INSERT INTO workspace_members (id, workspace_id, user_id, role, invited_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := r.db.ExecContext(ctx, q,
		member.ID, member.WorkspaceID, member.UserID,
		member.Role, member.InvitedBy, member.CreatedAt, member.UpdatedAt,
	)
	return err
}

func (r *WorkspaceMemberRepo) GetByWorkspaceAndUser(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	const q = `SELECT id, workspace_id, user_id, role, invited_by, created_at, updated_at FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`
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

// workspaceMemberRow is a flat DB scan struct for the JOIN query.
type workspaceMemberRow struct {
	ID          uuid.UUID  `db:"id"`
	WorkspaceID uuid.UUID  `db:"workspace_id"`
	UserID      uuid.UUID  `db:"user_id"`
	Role        string     `db:"role"`
	InvitedBy   *uuid.UUID `db:"invited_by"`
	CreatedAt   time.Time  `db:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"`
	UserIDJoin  uuid.UUID  `db:"u_id"`
	UserEmail   string     `db:"u_email"`
	UserName    string     `db:"u_display_name"`
	UserAvatar  string     `db:"u_avatar_url"`
}

// List returns all workspace members with their user details.
func (r *WorkspaceMemberRepo) List(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	const q = `
		SELECT
			wm.id, wm.workspace_id, wm.user_id, wm.role, wm.invited_by, wm.created_at, wm.updated_at,
			u.id AS u_id, u.email AS u_email, u.display_name AS u_display_name, u.avatar_url AS u_avatar_url
		FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = $1
		ORDER BY wm.created_at
	`
	var rows []workspaceMemberRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID); err != nil {
		return nil, err
	}

	result := make([]domain.WorkspaceMemberWithUser, len(rows))
	for i, row := range rows {
		result[i] = domain.WorkspaceMemberWithUser{
			WorkspaceMember: domain.WorkspaceMember{
				ID:          row.ID,
				WorkspaceID: row.WorkspaceID,
				UserID:      row.UserID,
				Role:        row.Role,
				InvitedBy:   row.InvitedBy,
				CreatedAt:   row.CreatedAt,
				UpdatedAt:   row.UpdatedAt,
			},
			User: domain.UserBrief{
				ID:        row.UserIDJoin,
				Email:     row.UserEmail,
				Name:      row.UserName,
				AvatarURL: row.UserAvatar,
			},
		}
	}
	return result, nil
}

// UpdateRole changes the role for a given workspace + user.
func (r *WorkspaceMemberRepo) UpdateRole(ctx context.Context, workspaceID, userID uuid.UUID, role string) error {
	const q = `UPDATE workspace_members SET role = $3, updated_at = $4 WHERE workspace_id = $1 AND user_id = $2`
	_, err := r.db.ExecContext(ctx, q, workspaceID, userID, role, time.Now())
	return err
}

// Delete removes the workspace membership for the given user.
func (r *WorkspaceMemberRepo) Delete(ctx context.Context, workspaceID, userID uuid.UUID) error {
	const q = `DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`
	_, err := r.db.ExecContext(ctx, q, workspaceID, userID)
	return err
}

// CountOwners returns the number of members with the "owner" role in the workspace.
func (r *WorkspaceMemberRepo) CountOwners(ctx context.Context, workspaceID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'owner'`
	var count int
	if err := r.db.GetContext(ctx, &count, q, workspaceID); err != nil {
		return 0, err
	}
	return count, nil
}

// workspaceMemberWithProjectsRow is the flat DB scan struct for ListWithProjects.
type workspaceMemberWithProjectsRow struct {
	workspaceMemberRow
	ProjectNames json.RawMessage `db:"project_names"`
}

// ListWithProjects returns all workspace members with their project affiliations.
func (r *WorkspaceMemberRepo) ListWithProjects(ctx context.Context, workspaceID uuid.UUID) ([]repository.HumanWithProjects, error) {
	const q = `
		SELECT
			wm.id, wm.workspace_id, wm.user_id, wm.role, wm.invited_by, wm.created_at, wm.updated_at,
			u.id AS u_id, u.email AS u_email, u.display_name AS u_display_name, u.avatar_url AS u_avatar_url,
			COALESCE(
			    json_agg(DISTINCT p.name) FILTER (WHERE p.id IS NOT NULL),
			    '[]'::json
			) AS project_names
		FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		LEFT JOIN project_members pm ON pm.user_id = wm.user_id
		LEFT JOIN projects p ON p.id = pm.project_id AND p.deleted_at IS NULL
		WHERE wm.workspace_id = $1
		GROUP BY wm.id, wm.workspace_id, wm.user_id, wm.role, wm.invited_by, wm.created_at, wm.updated_at,
		         u.id, u.email, u.display_name, u.avatar_url
		ORDER BY wm.created_at
	`
	var rows []workspaceMemberWithProjectsRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID); err != nil {
		return nil, fmt.Errorf("list workspace members with projects: %w", err)
	}

	result := make([]repository.HumanWithProjects, len(rows))
	for i, row := range rows {
		var projects []string
		if len(row.ProjectNames) > 0 {
			_ = json.Unmarshal(row.ProjectNames, &projects)
		}
		result[i] = repository.HumanWithProjects{
			WorkspaceMemberWithUser: domain.WorkspaceMemberWithUser{
				WorkspaceMember: domain.WorkspaceMember{
					ID:          row.ID,
					WorkspaceID: row.WorkspaceID,
					UserID:      row.UserID,
					Role:        row.Role,
					InvitedBy:   row.InvitedBy,
					CreatedAt:   row.CreatedAt,
					UpdatedAt:   row.UpdatedAt,
				},
				User: domain.UserBrief{
					ID:        row.UserIDJoin,
					Email:     row.UserEmail,
					Name:      row.UserName,
					AvatarURL: row.UserAvatar,
				},
			},
			Projects: projects,
		}
	}
	return result, nil
}
