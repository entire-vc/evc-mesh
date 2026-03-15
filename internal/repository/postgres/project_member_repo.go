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

// ProjectMemberRepo implements repository.ProjectMemberRepository with PostgreSQL.
type ProjectMemberRepo struct {
	db *sqlx.DB
}

// NewProjectMemberRepo creates a new ProjectMemberRepo.
func NewProjectMemberRepo(db *sqlx.DB) *ProjectMemberRepo {
	return &ProjectMemberRepo{db: db}
}

func (r *ProjectMemberRepo) Create(ctx context.Context, member *domain.ProjectMember) error {
	const q = `
		INSERT INTO project_members (id, project_id, user_id, agent_id, role, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := r.db.ExecContext(ctx, q,
		member.ID, member.ProjectID, member.UserID, member.AgentID,
		member.Role, member.CreatedAt, member.UpdatedAt,
	)
	return err
}

func (r *ProjectMemberRepo) GetByProjectAndUser(ctx context.Context, projectID, userID uuid.UUID) (*domain.ProjectMember, error) {
	const q = `SELECT id, project_id, user_id, agent_id, role, created_at, updated_at FROM project_members WHERE project_id = $1 AND user_id = $2`
	var member domain.ProjectMember
	if err := r.db.GetContext(ctx, &member, q, projectID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &member, nil
}

func (r *ProjectMemberRepo) GetByProjectAndAgent(ctx context.Context, projectID, agentID uuid.UUID) (*domain.ProjectMember, error) {
	const q = `SELECT id, project_id, user_id, agent_id, role, created_at, updated_at FROM project_members WHERE project_id = $1 AND agent_id = $2`
	var member domain.ProjectMember
	if err := r.db.GetContext(ctx, &member, q, projectID, agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &member, nil
}

// projectMemberRow is a flat DB scan struct for the JOIN query.
type projectMemberRow struct {
	ID         uuid.UUID  `db:"id"`
	ProjectID  uuid.UUID  `db:"project_id"`
	UserID     *uuid.UUID `db:"user_id"`
	AgentID    *uuid.UUID `db:"agent_id"`
	Role       string     `db:"role"`
	CreatedAt  time.Time  `db:"created_at"`
	UpdatedAt  time.Time  `db:"updated_at"`
	UserIDJoin *uuid.UUID `db:"u_id"`
	UserEmail  *string    `db:"u_email"`
	UserName   *string    `db:"u_display_name"`
	UserAvatar *string    `db:"u_avatar_url"`
	AgentName  *string    `db:"a_name"`
}

// List returns all project members with their user/agent details.
func (r *ProjectMemberRepo) List(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectMemberWithUser, error) {
	const q = `
		SELECT
			pm.id, pm.project_id, pm.user_id, pm.agent_id, pm.role, pm.created_at, pm.updated_at,
			u.id AS u_id, u.email AS u_email, u.display_name AS u_display_name, u.avatar_url AS u_avatar_url,
			a.name AS a_name
		FROM project_members pm
		LEFT JOIN users u ON u.id = pm.user_id
		LEFT JOIN agents a ON a.id = pm.agent_id
		WHERE pm.project_id = $1
		ORDER BY pm.created_at
	`
	var rows []projectMemberRow
	if err := r.db.SelectContext(ctx, &rows, q, projectID); err != nil {
		return nil, err
	}

	result := make([]domain.ProjectMemberWithUser, len(rows))
	for i, row := range rows {
		m := domain.ProjectMemberWithUser{
			ProjectMember: domain.ProjectMember{
				ID:        row.ID,
				ProjectID: row.ProjectID,
				UserID:    row.UserID,
				AgentID:   row.AgentID,
				Role:      row.Role,
				CreatedAt: row.CreatedAt,
				UpdatedAt: row.UpdatedAt,
			},
		}
		if row.UserIDJoin != nil {
			m.User = &domain.UserBrief{
				ID:        *row.UserIDJoin,
				Email:     derefStr(row.UserEmail),
				Name:      derefStr(row.UserName),
				AvatarURL: derefStr(row.UserAvatar),
			}
		}
		if row.AgentName != nil {
			m.AgentName = *row.AgentName
		}
		result[i] = m
	}
	return result, nil
}

// UpdateRole changes the role for a given project + user.
func (r *ProjectMemberRepo) UpdateRole(ctx context.Context, projectID, userID uuid.UUID, role string) error {
	const q = `UPDATE project_members SET role = $3, updated_at = $4 WHERE project_id = $1 AND user_id = $2`
	_, err := r.db.ExecContext(ctx, q, projectID, userID, role, time.Now())
	return err
}

// Delete removes the project membership for the given user.
func (r *ProjectMemberRepo) Delete(ctx context.Context, projectID, userID uuid.UUID) error {
	const q = `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`
	_, err := r.db.ExecContext(ctx, q, projectID, userID)
	return err
}

// DeleteAgent removes the project membership for the given agent.
func (r *ProjectMemberRepo) DeleteAgent(ctx context.Context, projectID, agentID uuid.UUID) error {
	const q = `DELETE FROM project_members WHERE project_id = $1 AND agent_id = $2`
	_, err := r.db.ExecContext(ctx, q, projectID, agentID)
	return err
}

// DeleteByWorkspaceAndUser removes all project memberships for a user across a workspace.
func (r *ProjectMemberRepo) DeleteByWorkspaceAndUser(ctx context.Context, workspaceID, userID uuid.UUID) error {
	const q = `
		DELETE FROM project_members pm
		USING projects p
		WHERE pm.project_id = p.id
		  AND p.workspace_id = $1
		  AND pm.user_id = $2
	`
	_, err := r.db.ExecContext(ctx, q, workspaceID, userID)
	return err
}

// ExistsMember returns true if the given user or agent is a member of the project.
func (r *ProjectMemberRepo) ExistsMember(ctx context.Context, projectID uuid.UUID, userID, agentID *uuid.UUID) (bool, error) {
	var exists bool
	if userID != nil {
		err := r.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)",
			projectID, *userID,
		).Scan(&exists)
		return exists, err
	}
	if agentID != nil {
		err := r.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id = $1 AND agent_id = $2)",
			projectID, *agentID,
		).Scan(&exists)
		return exists, err
	}
	return false, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
