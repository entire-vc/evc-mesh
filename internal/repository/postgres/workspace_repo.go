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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// workspaceRow is the DB row representation (includes deleted_at).
type workspaceRow struct {
	ID                uuid.UUID       `db:"id"`
	Name              string          `db:"name"`
	Slug              string          `db:"slug"`
	OwnerID           uuid.UUID       `db:"owner_id"`
	Settings          json.RawMessage `db:"settings"`
	BillingPlanID     *string         `db:"billing_plan_id"`
	BillingCustomerID *string         `db:"billing_customer_id"`
	CreatedAt         time.Time       `db:"created_at"`
	UpdatedAt         time.Time       `db:"updated_at"`
	DeletedAt         *time.Time      `db:"deleted_at"`
}

func (r *workspaceRow) toDomain() *domain.Workspace {
	ws := &domain.Workspace{
		ID:        r.ID,
		Name:      r.Name,
		Slug:      r.Slug,
		OwnerID:   r.OwnerID,
		Settings:  r.Settings,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if r.BillingPlanID != nil {
		ws.BillingPlanID = *r.BillingPlanID
	}
	if r.BillingCustomerID != nil {
		ws.BillingCustomerID = *r.BillingCustomerID
	}
	return ws
}

// WorkspaceRepo implements repository.WorkspaceRepository with PostgreSQL.
type WorkspaceRepo struct {
	db *sqlx.DB
}

// NewWorkspaceRepo creates a new WorkspaceRepo.
func NewWorkspaceRepo(db *sqlx.DB) *WorkspaceRepo {
	return &WorkspaceRepo{db: db}
}

func (r *WorkspaceRepo) Create(ctx context.Context, workspace *domain.Workspace) error {
	const q = `
		INSERT INTO workspaces (id, name, slug, owner_id, settings, billing_plan_id, billing_customer_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	settings := workspace.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}
	var billingPlanID, billingCustomerID *string
	if workspace.BillingPlanID != "" {
		billingPlanID = &workspace.BillingPlanID
	}
	if workspace.BillingCustomerID != "" {
		billingCustomerID = &workspace.BillingCustomerID
	}
	_, err := r.db.ExecContext(ctx, q,
		workspace.ID, workspace.Name, workspace.Slug, workspace.OwnerID,
		settings, billingPlanID, billingCustomerID,
		workspace.CreatedAt, workspace.UpdatedAt,
	)
	return err
}

func (r *WorkspaceRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
	const q = `SELECT * FROM workspaces WHERE id = $1 AND deleted_at IS NULL`
	var row workspaceRow
	if err := r.db.GetContext(ctx, &row, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *WorkspaceRepo) GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error) {
	const q = `SELECT * FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`
	var row workspaceRow
	if err := r.db.GetContext(ctx, &row, q, slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *WorkspaceRepo) Update(ctx context.Context, workspace *domain.Workspace) error {
	const q = `
		UPDATE workspaces
		SET name = $2, slug = $3, settings = $4, billing_plan_id = $5, billing_customer_id = $6, updated_at = $7
		WHERE id = $1 AND deleted_at IS NULL
	`
	settings := workspace.Settings
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}
	var billingPlanID, billingCustomerID *string
	if workspace.BillingPlanID != "" {
		billingPlanID = &workspace.BillingPlanID
	}
	if workspace.BillingCustomerID != "" {
		billingCustomerID = &workspace.BillingCustomerID
	}
	res, err := r.db.ExecContext(ctx, q,
		workspace.ID, workspace.Name, workspace.Slug, settings,
		billingPlanID, billingCustomerID, workspace.UpdatedAt,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Workspace")
	}
	return nil
}

// Delete performs a soft delete by setting deleted_at.
func (r *WorkspaceRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE workspaces SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Workspace")
	}
	return nil
}

func (r *WorkspaceRepo) ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
	const q = `SELECT * FROM workspaces WHERE owner_id = $1 AND deleted_at IS NULL ORDER BY created_at ASC`
	var rows []workspaceRow
	if err := r.db.SelectContext(ctx, &rows, q, ownerID); err != nil {
		return nil, err
	}
	result := make([]domain.Workspace, len(rows))
	for i := range rows {
		result[i] = *rows[i].toDomain()
	}
	return result, nil
}
