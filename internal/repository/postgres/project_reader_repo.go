package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

var _ repository.ProjectReaderRepository = (*AgentRepo)(nil)

// CreateProjectReader inserts a project reader in one transaction: the agent
// row with reader_project_id, its home agent_workspace_grants row with role
// viewer and a project_members row with role viewer for that one project. A
// crash between the writes would leave either a reader without its key row
// (harmless) or — far worse — a key that is not yet a reader, so all three
// commit together or not at all.
//
// The project is checked inside the transaction (live, in the agent's
// workspace) and locked FOR SHARE until commit, so it cannot be deleted or
// moved under the insert.
func (r *AgentRepo) CreateProjectReader(ctx context.Context, agent *domain.Agent) error {
	if agent.ReaderProjectID == nil || *agent.ReaderProjectID == uuid.Nil {
		return apierror.BadRequest("project reader needs a project")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	var projectWS uuid.UUID
	err = tx.GetContext(ctx, &projectWS,
		`SELECT workspace_id FROM projects WHERE id = $1 AND deleted_at IS NULL FOR SHARE`,
		*agent.ReaderProjectID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && projectWS != agent.WorkspaceID) {
		return apierror.NotFound("Project")
	}
	if err != nil {
		return err
	}

	if err := createAgentTx(ctx, tx, agent); err != nil {
		return err
	}

	const grantQ = `
		INSERT INTO agent_workspace_grants
			(id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, api_key_sha256, invited_by, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NULLIF($7, ''), NULL, $6)
	`
	if _, err := tx.ExecContext(ctx, grantQ,
		agent.ID, agent.WorkspaceID, domain.RoleViewer, agent.APIKeyPrefix, agent.APIKeyHash, agent.CreatedAt, agent.APIKeySHA256,
	); err != nil {
		return err
	}

	const memberQ = `
		INSERT INTO project_members (id, project_id, workspace_id, agent_id, role, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $5)
	`
	if _, err := tx.ExecContext(ctx, memberQ,
		*agent.ReaderProjectID, agent.WorkspaceID, agent.ID, domain.ProjectRoleViewer, agent.CreatedAt,
	); err != nil {
		return err
	}

	return tx.Commit()
}
