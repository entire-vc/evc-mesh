package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// InitiativeRepo implements repository.InitiativeRepository with PostgreSQL.
type InitiativeRepo struct {
	db *sqlx.DB
}

// NewInitiativeRepo creates a new InitiativeRepo.
func NewInitiativeRepo(db *sqlx.DB) *InitiativeRepo {
	return &InitiativeRepo{db: db}
}

func (r *InitiativeRepo) Create(ctx context.Context, ini *domain.Initiative) error {
	const q = `
		INSERT INTO initiatives
			(id, workspace_id, name, description, status, target_date, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.ExecContext(ctx, q,
		ini.ID, ini.WorkspaceID, ini.Name, ini.Description,
		ini.Status, ini.TargetDate, ini.CreatedBy,
		ini.CreatedAt, ini.UpdatedAt,
	)
	return err
}

func (r *InitiativeRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Initiative, error) {
	const q = `SELECT * FROM initiatives WHERE id = $1`
	var ini domain.Initiative
	if err := r.db.GetContext(ctx, &ini, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &ini, nil
}

func (r *InitiativeRepo) Update(ctx context.Context, ini *domain.Initiative) error {
	const q = `
		UPDATE initiatives
		SET name = $2, description = $3, status = $4, target_date = $5, updated_at = $6
		WHERE id = $1
	`
	res, err := r.db.ExecContext(ctx, q,
		ini.ID, ini.Name, ini.Description, ini.Status, ini.TargetDate, ini.UpdatedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Initiative")
	}
	return nil
}

func (r *InitiativeRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM initiatives WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Initiative")
	}
	return nil
}

func (r *InitiativeRepo) List(ctx context.Context, workspaceID uuid.UUID) ([]domain.Initiative, error) {
	const q = `SELECT * FROM initiatives WHERE workspace_id = $1 ORDER BY created_at DESC`
	var items []domain.Initiative
	if err := r.db.SelectContext(ctx, &items, q, workspaceID); err != nil {
		return nil, err
	}
	if items == nil {
		items = []domain.Initiative{}
	}
	return items, nil
}

func (r *InitiativeRepo) LinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error {
	const q = `
		INSERT INTO initiative_projects (initiative_id, project_id)
		VALUES ($1, $2)
		ON CONFLICT (initiative_id, project_id) DO NOTHING
	`
	_, err := r.db.ExecContext(ctx, q, initiativeID, projectID)
	return err
}

func (r *InitiativeRepo) UnlinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error {
	const q = `DELETE FROM initiative_projects WHERE initiative_id = $1 AND project_id = $2`
	res, err := r.db.ExecContext(ctx, q, initiativeID, projectID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("InitiativeProject")
	}
	return nil
}

func (r *InitiativeRepo) GetByProjectID(ctx context.Context, projectID uuid.UUID) ([]domain.Initiative, error) {
	const q = `
		SELECT i.* FROM initiatives i
		INNER JOIN initiative_projects ip ON ip.initiative_id = i.id
		WHERE ip.project_id = $1
		ORDER BY i.name ASC
	`
	var items []domain.Initiative
	if err := r.db.SelectContext(ctx, &items, q, projectID); err != nil {
		return nil, err
	}
	if items == nil {
		items = []domain.Initiative{}
	}
	return items, nil
}

func (r *InitiativeRepo) ListLinkedProjects(ctx context.Context, initiativeID uuid.UUID) ([]domain.Project, error) {
	const q = `
		SELECT p.* FROM projects p
		INNER JOIN initiative_projects ip ON ip.project_id = p.id
		WHERE ip.initiative_id = $1 AND p.deleted_at IS NULL
		ORDER BY p.name ASC
	`
	var rows []projectRow
	if err := r.db.SelectContext(ctx, &rows, q, initiativeID); err != nil {
		return nil, fmt.Errorf("listing linked projects: %w", err)
	}

	projects := make([]domain.Project, len(rows))
	for i := range rows {
		projects[i] = rows[i].toDomain()
	}

	// Ensure JSON fields are valid for empty options
	for i := range projects {
		if projects[i].Settings == nil {
			projects[i].Settings = json.RawMessage(`{}`)
		}
	}

	return projects, nil
}
