package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ProjectUpdateRepo implements repository.ProjectUpdateRepository with PostgreSQL.
type ProjectUpdateRepo struct {
	db *sqlx.DB
}

// NewProjectUpdateRepo creates a new ProjectUpdateRepo.
func NewProjectUpdateRepo(db *sqlx.DB) *ProjectUpdateRepo {
	return &ProjectUpdateRepo{db: db}
}

func (r *ProjectUpdateRepo) Create(ctx context.Context, u *domain.ProjectUpdate) error {
	const q = `
		INSERT INTO project_updates
			(id, project_id, title, status, summary, highlights, blockers, next_steps, metrics, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	highlights := u.Highlights
	if highlights == nil {
		highlights = json.RawMessage(`[]`)
	}
	blockers := u.Blockers
	if blockers == nil {
		blockers = json.RawMessage(`[]`)
	}
	nextSteps := u.NextSteps
	if nextSteps == nil {
		nextSteps = json.RawMessage(`[]`)
	}
	metrics := u.Metrics
	if metrics == nil {
		metrics = json.RawMessage(`{}`)
	}
	_, err := r.db.ExecContext(ctx, q,
		u.ID, u.ProjectID, u.Title, u.Status, u.Summary,
		highlights, blockers, nextSteps, metrics,
		u.CreatedBy, u.CreatedAt,
	)
	return err
}

func (r *ProjectUpdateRepo) List(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ProjectUpdate], error) {
	pg.Normalize()

	const countQ = `SELECT COUNT(*) FROM project_updates WHERE project_id = $1`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, projectID); err != nil {
		return nil, err
	}

	const dataQ = `
		SELECT * FROM project_updates
		WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	var items []domain.ProjectUpdate
	if err := r.db.SelectContext(ctx, &items, dataQ, projectID, pg.Limit(), pg.Offset()); err != nil {
		return nil, err
	}
	if items == nil {
		items = []domain.ProjectUpdate{}
	}

	return pagination.NewPage(items, totalCount, pg), nil
}

func (r *ProjectUpdateRepo) GetLatest(ctx context.Context, projectID uuid.UUID) (*domain.ProjectUpdate, error) {
	const q = `
		SELECT * FROM project_updates
		WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`
	var u domain.ProjectUpdate
	if err := r.db.GetContext(ctx, &u, q, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}
