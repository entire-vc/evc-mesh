package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// extensionProjectAllowed is the single SQL form of "connection ic may reach
// project p". Every reader of the extension project scope goes through it, so
// the API guard, event delivery and the listings cannot drift apart. It expects
// the aliases ic (integration_configs) and p (projects).
const extensionProjectAllowed = `
	p.workspace_id = ic.workspace_id
	AND p.deleted_at IS NULL
	AND ic.is_active
	AND (ic.project_scope = 'workspace'
	     OR EXISTS (SELECT 1 FROM project_integrations pi
	                 WHERE pi.project_id = p.id
	                   AND pi.extension_id = ic.provider
	                   AND pi.enabled))`

// ExtensionProjectScopeRepo implements repository.ExtensionProjectScopeRepository.
type ExtensionProjectScopeRepo struct {
	db *sqlx.DB
}

// NewExtensionProjectScopeRepo creates an ExtensionProjectScopeRepo.
func NewExtensionProjectScopeRepo(db *sqlx.DB) *ExtensionProjectScopeRepo {
	return &ExtensionProjectScopeRepo{db: db}
}

func (r *ExtensionProjectScopeRepo) AllowsProject(ctx context.Context, connectionID, projectID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.GetContext(ctx, &ok, `
		SELECT EXISTS (
			SELECT 1 FROM integration_configs ic, projects p
			 WHERE ic.id = $1 AND p.id = $2 AND`+extensionProjectAllowed+`)`,
		connectionID, projectID)
	return ok, err
}

func (r *ExtensionProjectScopeRepo) AllowsProjectFor(ctx context.Context, workspaceID uuid.UUID, extensionID string, projectID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.GetContext(ctx, &ok, `
		SELECT CASE
		  WHEN EXISTS (SELECT 1 FROM integration_configs WHERE workspace_id = $1 AND provider = $2)
		  THEN EXISTS (
			SELECT 1 FROM integration_configs ic, projects p
			 WHERE ic.workspace_id = $1 AND ic.provider = $2 AND p.id = $3 AND`+extensionProjectAllowed+`)
		  ELSE EXISTS (SELECT 1 FROM projects WHERE id = $3 AND workspace_id = $1 AND deleted_at IS NULL)
		END`,
		workspaceID, extensionID, projectID)
	return ok, err
}

func (r *ExtensionProjectScopeRepo) BoundProjects(ctx context.Context, connectionID uuid.UUID) (bool, []uuid.UUID, error) {
	var scope string
	err := r.db.GetContext(ctx, &scope, `SELECT project_scope FROM integration_configs WHERE id = $1`, connectionID)
	if errors.Is(err, sql.ErrNoRows) {
		// No connection reaches nothing.
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if scope != domain.ExtensionProjectScopeBound {
		return true, nil, nil
	}
	var ids []uuid.UUID
	err = r.db.SelectContext(ctx, &ids, `
		SELECT p.id FROM integration_configs ic, projects p
		 WHERE ic.id = $1 AND`+extensionProjectAllowed+`
		 ORDER BY p.id`, connectionID)
	return false, ids, err
}

func (r *ExtensionProjectScopeRepo) SetProjectScope(ctx context.Context, workspaceID uuid.UUID, extensionID, scope string) error {
	if scope != domain.ExtensionProjectScopeWorkspace && scope != domain.ExtensionProjectScopeBound {
		return apierror.ValidationError(map[string]string{"project_scope": "must be workspace or bound"})
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE integration_configs SET project_scope = $3, updated_at = NOW()
		 WHERE workspace_id = $1 AND provider = $2`, workspaceID, extensionID, scope)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("extension connection")
	}
	return nil
}
