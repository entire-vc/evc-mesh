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
	IconURL           *string         `db:"icon_url"`
	CreatedAt         time.Time       `db:"created_at"`
	UpdatedAt         time.Time       `db:"updated_at"`
	DeletedAt         *time.Time      `db:"deleted_at"`
}

// workspaceSelectCols is every column workspaceRow scans, listed explicitly
// — see agentSelectCols in agent_repo.go for why `SELECT *` is unsafe: sqlx
// refuses to scan a column with no matching struct field, so an additive
// migration to this table breaks every read until redeployed.
const workspaceSelectCols = `id, name, slug, owner_id, settings, billing_plan_id, billing_customer_id, icon_url, created_at, updated_at, deleted_at`

// workspaceSelectColsQualified is workspaceSelectCols prefixed with the `w`
// JOIN alias, for queries that select from workspaces alongside other
// tables.
const workspaceSelectColsQualified = `w.id, w.name, w.slug, w.owner_id, w.settings, w.billing_plan_id, w.billing_customer_id, w.icon_url, w.created_at, w.updated_at, w.deleted_at`

func (r *workspaceRow) toDomain() *domain.Workspace {
	ws := &domain.Workspace{
		ID:             r.ID,
		Name:           r.Name,
		Slug:           r.Slug,
		OwnerID:        r.OwnerID,
		Settings:       r.Settings,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		IconStorageKey: r.IconURL,
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
	const q = `SELECT ` + workspaceSelectCols + ` FROM workspaces WHERE id = $1 AND deleted_at IS NULL`
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
	const q = `SELECT ` + workspaceSelectCols + ` FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`
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
		SET name = $2, slug = $3, settings = $4, billing_plan_id = $5, billing_customer_id = $6,
		    icon_url = $7, updated_at = $8
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
		billingPlanID, billingCustomerID, workspace.IconStorageKey, workspace.UpdatedAt,
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

// Delete performs a soft delete of the workspace and, in the same
// transaction, cascades to every project and task inside it.
//
// Without the cascade a deleted workspace disappears from every query that
// lists WORKSPACES (GET /workspaces, membership checks, ...) while its
// projects and tasks stay exactly as visible as before to every query that
// only ever checked their OWN deleted_at — cross-cutting reads like
// /me/comments, /me/mentions, and a member's active-task list do exactly
// that, and have no other way to learn the workspace is gone. Cascading
// closes that by construction: those queries already filter tasks.deleted_at/
// projects.deleted_at (or now do, see the fixes alongside this one), so once
// this cascade runs there is nothing left for them to find.
func (r *WorkspaceRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	res, err := tx.ExecContext(ctx,
		`UPDATE workspaces SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Workspace")
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET deleted_at = NOW()
		 WHERE deleted_at IS NULL
		   AND project_id IN (SELECT id FROM projects WHERE workspace_id = $1)`,
		id,
	); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET deleted_at = NOW() WHERE workspace_id = $1 AND deleted_at IS NULL`,
		id,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// ListByOwner returns the workspaces owned by the given user. It ignores
// membership — use ListForUser for the user-facing workspace list.
func (r *WorkspaceRepo) ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
	const q = `SELECT ` + workspaceSelectCols + ` FROM workspaces WHERE owner_id = $1 AND deleted_at IS NULL ORDER BY created_at ASC`
	var rows []workspaceRow
	if err := r.db.SelectContext(ctx, &rows, q, ownerID); err != nil {
		return nil, err
	}
	return toDomainWorkspaces(rows), nil
}

// ListForUser returns every workspace the user can see: the ones they are a
// member of, plus the ones they own.
//
// Ownership is checked explicitly instead of relying on the workspace_members
// row alone. The owner is normally auto-inserted as a member on workspace
// creation, but that insert is best-effort (see workspaceService.Create and
// auth.Service.Register) and legacy rows may be missing it — such an owner must
// still see their own workspace. EXISTS (rather than a JOIN) keeps the result
// de-duplicated when the user is both owner and member.
func (r *WorkspaceRepo) ListForUser(ctx context.Context, userID uuid.UUID) ([]domain.Workspace, error) {
	const q = `
		SELECT ` + workspaceSelectColsQualified + ` FROM workspaces w
		WHERE w.deleted_at IS NULL
		  AND (
		      w.owner_id = $1
		      OR EXISTS (
		          SELECT 1 FROM workspace_members m
		          WHERE m.workspace_id = w.id AND m.user_id = $1
		      )
		  )
		ORDER BY w.created_at ASC, w.id ASC`
	var rows []workspaceRow
	if err := r.db.SelectContext(ctx, &rows, q, userID); err != nil {
		return nil, err
	}
	return toDomainWorkspaces(rows), nil
}

func toDomainWorkspaces(rows []workspaceRow) []domain.Workspace {
	result := make([]domain.Workspace, len(rows))
	for i := range rows {
		result[i] = *rows[i].toDomain()
	}
	return result
}
