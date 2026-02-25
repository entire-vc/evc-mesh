package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// savedViewRow is the DB row representation for saved_views.
type savedViewRow struct {
	ID        uuid.UUID `db:"id"`
	ProjectID uuid.UUID `db:"project_id"`
	Name      string    `db:"name"`
	ViewType  string    `db:"view_type"`
	Filters   []byte    `db:"filters"`
	SortBy    *string   `db:"sort_by"`
	SortOrder *string   `db:"sort_order"`
	Columns   []byte    `db:"columns"`
	IsShared  bool      `db:"is_shared"`
	CreatedBy uuid.UUID `db:"created_by"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

func (r *savedViewRow) toDomain() (*domain.SavedView, error) {
	filters := make(map[string]interface{})
	if len(r.Filters) > 0 {
		if err := json.Unmarshal(r.Filters, &filters); err != nil {
			return nil, err
		}
	}

	var columns []string
	if len(r.Columns) > 0 {
		if err := json.Unmarshal(r.Columns, &columns); err != nil {
			return nil, err
		}
	}

	return &domain.SavedView{
		ID:        r.ID,
		ProjectID: r.ProjectID,
		Name:      r.Name,
		ViewType:  r.ViewType,
		Filters:   filters,
		SortBy:    r.SortBy,
		SortOrder: r.SortOrder,
		Columns:   columns,
		IsShared:  r.IsShared,
		CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}, nil
}

// SavedViewRepo implements repository.SavedViewRepository with PostgreSQL.
type SavedViewRepo struct {
	db *sqlx.DB
}

// NewSavedViewRepo creates a new SavedViewRepo.
func NewSavedViewRepo(db *sqlx.DB) *SavedViewRepo {
	return &SavedViewRepo{db: db}
}

// Create inserts a new saved view.
func (r *SavedViewRepo) Create(ctx context.Context, view *domain.SavedView) error {
	filtersJSON, err := json.Marshal(view.Filters)
	if err != nil {
		return err
	}

	columnsJSON, err := json.Marshal(view.Columns)
	if err != nil {
		return err
	}

	const q = `
		INSERT INTO saved_views (
			id, project_id, name, view_type, filters, sort_by, sort_order,
			columns, is_shared, created_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12
		)
	`
	_, err = r.db.ExecContext(ctx, q,
		view.ID, view.ProjectID, view.Name, view.ViewType, filtersJSON, view.SortBy, view.SortOrder,
		columnsJSON, view.IsShared, view.CreatedBy, view.CreatedAt, view.UpdatedAt,
	)
	return err
}

// GetByID retrieves a saved view by its ID.
func (r *SavedViewRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.SavedView, error) {
	const q = `SELECT * FROM saved_views WHERE id = $1`
	var row savedViewRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.toDomain()
}

// Update applies a partial update to a saved view.
func (r *SavedViewRepo) Update(ctx context.Context, id uuid.UUID, input domain.UpdateSavedViewInput) (*domain.SavedView, error) {
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, apierror.NotFound("SavedView")
	}

	if input.Name != nil {
		existing.Name = *input.Name
	}
	if input.ViewType != nil {
		existing.ViewType = *input.ViewType
	}
	if input.Filters != nil {
		existing.Filters = input.Filters
	}
	if input.SortBy != nil {
		existing.SortBy = input.SortBy
	}
	if input.SortOrder != nil {
		existing.SortOrder = input.SortOrder
	}
	if input.Columns != nil {
		existing.Columns = input.Columns
	}
	if input.IsShared != nil {
		existing.IsShared = *input.IsShared
	}
	existing.UpdatedAt = time.Now()

	filtersJSON, err := json.Marshal(existing.Filters)
	if err != nil {
		return nil, err
	}
	columnsJSON, err := json.Marshal(existing.Columns)
	if err != nil {
		return nil, err
	}

	const q = `
		UPDATE saved_views
		SET name = $2, view_type = $3, filters = $4, sort_by = $5, sort_order = $6,
		    columns = $7, is_shared = $8, updated_at = $9
		WHERE id = $1
	`
	_, err = r.db.ExecContext(ctx, q,
		id, existing.Name, existing.ViewType, filtersJSON, existing.SortBy, existing.SortOrder,
		columnsJSON, existing.IsShared, existing.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete removes a saved view by its ID.
func (r *SavedViewRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM saved_views WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("SavedView")
	}
	return nil
}

// ListByProject returns all saved views for a project visible to the given user:
// own views + shared views.
func (r *SavedViewRepo) ListByProject(ctx context.Context, projectID uuid.UUID, userID uuid.UUID) ([]domain.SavedView, error) {
	const q = `
		SELECT * FROM saved_views
		WHERE project_id = $1
		  AND (created_by = $2 OR is_shared = true)
		ORDER BY created_at ASC
	`
	var rows []savedViewRow
	if err := r.db.SelectContext(ctx, &rows, q, projectID, userID); err != nil {
		return nil, err
	}
	result := make([]domain.SavedView, 0, len(rows))
	for i := range rows {
		v, err := rows[i].toDomain()
		if err != nil {
			return nil, err
		}
		result = append(result, *v)
	}
	return result, nil
}

// Ensure SavedViewRepo satisfies the repository.SavedViewRepository interface.
var _ repository.SavedViewRepository = (*SavedViewRepo)(nil)
