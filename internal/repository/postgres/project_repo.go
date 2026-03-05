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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// projectRow is the DB row representation (includes deleted_at).
type projectRow struct {
	ID                  uuid.UUID                `db:"id"`
	WorkspaceID         uuid.UUID                `db:"workspace_id"`
	Name                string                   `db:"name"`
	Description         string                   `db:"description"`
	Slug                string                   `db:"slug"`
	Icon                string                   `db:"icon"`
	Settings            json.RawMessage          `db:"settings"`
	DefaultAssigneeType domain.DefaultAssigneeType `db:"default_assignee_type"`
	IsArchived          bool                     `db:"is_archived"`
	CreatedAt           time.Time                `db:"created_at"`
	UpdatedAt           time.Time                `db:"updated_at"`
	DeletedAt           *time.Time               `db:"deleted_at"`
}

func (r *projectRow) toDomain() domain.Project {
	return domain.Project{
		ID:                  r.ID,
		WorkspaceID:         r.WorkspaceID,
		Name:                r.Name,
		Description:         r.Description,
		Slug:                r.Slug,
		Icon:                r.Icon,
		Settings:            r.Settings,
		DefaultAssigneeType: r.DefaultAssigneeType,
		IsArchived:          r.IsArchived,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
}

// ProjectRepo implements repository.ProjectRepository with PostgreSQL.
type ProjectRepo struct {
	db *sqlx.DB
}

// NewProjectRepo creates a new ProjectRepo.
func NewProjectRepo(db *sqlx.DB) *ProjectRepo {
	return &ProjectRepo{db: db}
}

func (r *ProjectRepo) Create(ctx context.Context, project *domain.Project) error {
	const q = `
		INSERT INTO projects (id, workspace_id, name, description, slug, icon, settings, default_assignee_type, is_archived, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	settings := project.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		project.ID, project.WorkspaceID, project.Name, project.Description,
		project.Slug, project.Icon, settings, project.DefaultAssigneeType,
		project.IsArchived, project.CreatedAt, project.UpdatedAt,
	)
	return err
}

func (r *ProjectRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	const q = `SELECT * FROM projects WHERE id = $1 AND deleted_at IS NULL`
	var row projectRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p := row.toDomain()
	return &p, nil
}

func (r *ProjectRepo) GetBySlug(ctx context.Context, workspaceID uuid.UUID, slug string) (*domain.Project, error) {
	const q = `SELECT * FROM projects WHERE workspace_id = $1 AND slug = $2 AND deleted_at IS NULL`
	var row projectRow
	if err := r.db.GetContext(ctx, &row, q, workspaceID, slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p := row.toDomain()
	return &p, nil
}

func (r *ProjectRepo) Update(ctx context.Context, project *domain.Project) error {
	const q = `
		UPDATE projects
		SET name = $2, description = $3, slug = $4, icon = $5, settings = $6,
		    default_assignee_type = $7, is_archived = $8, updated_at = $9
		WHERE id = $1 AND deleted_at IS NULL
	`
	settings := project.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}
	res, err := r.db.ExecContext(ctx, q,
		project.ID, project.Name, project.Description, project.Slug,
		project.Icon, settings, project.DefaultAssigneeType,
		project.IsArchived, project.UpdatedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Project")
	}
	return nil
}

// Delete performs a soft delete by setting deleted_at.
func (r *ProjectRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE projects SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Project")
	}
	return nil
}

func (r *ProjectRepo) List(ctx context.Context, workspaceID uuid.UUID, filter repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error) {
	pg.Normalize()

	var conditions []string
	var args []interface{}
	args = append(args, workspaceID) // $1
	argIdx := 2

	conditions = append(conditions, "p.workspace_id = $1")
	conditions = append(conditions, "p.deleted_at IS NULL")

	if filter.IsArchived != nil {
		conditions = append(conditions, fmt.Sprintf("p.is_archived = $%d", argIdx))
		args = append(args, *filter.IsArchived)
		argIdx++
	}
	if filter.Search != "" {
		pattern := "%" + filter.Search + "%"
		conditions = append(conditions, fmt.Sprintf("(p.name ILIKE $%d OR p.description ILIKE $%d)", argIdx, argIdx))
		args = append(args, pattern)
		argIdx++
	}

	// Membership filter: JOIN project_members to restrict to user's/agent's projects.
	memberJoin := ""
	if filter.MemberUserID != nil {
		memberJoin = fmt.Sprintf(" JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $%d", argIdx)
		args = append(args, *filter.MemberUserID)
		argIdx++
	} else if filter.MemberAgentID != nil {
		memberJoin = fmt.Sprintf(" JOIN project_members pm ON pm.project_id = p.id AND pm.agent_id = $%d", argIdx)
		args = append(args, *filter.MemberAgentID)
		argIdx++
	}

	where := "WHERE " + joinAnd(conditions)
	order := orderClause(pg, allowedSortColumns{
		"name":       "p.name",
		"created_at": "p.created_at",
		"updated_at": "p.updated_at",
	}, "p.created_at")

	fromClause := fmt.Sprintf("projects p%s", memberJoin)

	// Count query
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM %s %s`, fromClause, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	// Data query
	dataQ := fmt.Sprintf(`SELECT p.* FROM %s %s %s %s`, fromClause, where, order, paginationClause(pg))
	var rows []projectRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	items := make([]domain.Project, len(rows))
	for i := range rows {
		items[i] = rows[i].toDomain()
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

func joinAnd(conditions []string) string {
	return fmt.Sprintf("%s", join(conditions, " AND "))
}

func join(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}
