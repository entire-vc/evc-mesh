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

// WorkspaceExportRepo reads the workspace hierarchy for the extension export
// (W3.5) and records agents.sponsor_user_id. It is its own repo, not a method of
// the agent or project repo, because the export is one snapshot over five tables
// and should not grow any of their interfaces.
type WorkspaceExportRepo struct {
	db *sqlx.DB
}

// NewWorkspaceExportRepo creates a new WorkspaceExportRepo.
func NewWorkspaceExportRepo(db *sqlx.DB) *WorkspaceExportRepo {
	return &WorkspaceExportRepo{db: db}
}

// SetAgentSponsor records the user who stands behind the agent. It overwrites:
// the sponsor is whoever issued the agent's latest key.
func (r *WorkspaceExportRepo) SetAgentSponsor(ctx context.Context, agentID, userID uuid.UUID) error {
	// The operator defaults to whoever issued the key, unless one is already set.
	_, err := r.db.ExecContext(ctx, `UPDATE agents SET sponsor_user_id = $2, operator_user_id = COALESCE(operator_user_id, $2) WHERE id = $1`, agentID, userID)
	return err
}

// SetAgentOperator records agents.operator_user_id, the user who runs the agent.
func (r *WorkspaceExportRepo) SetAgentOperator(ctx context.Context, agentID, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE agents SET operator_user_id = $2 WHERE id = $1`, agentID, userID)
	return err
}

// UserCanOperate reports whether the user may be named operator of an agent of
// the workspace: an active owner or member of it.
func (r *WorkspaceExportRepo) UserCanOperate(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.GetContext(ctx, &ok, `SELECT EXISTS (
		SELECT 1 FROM users u WHERE u.id=$2 AND u.is_active
		AND (EXISTS (SELECT 1 FROM workspaces w WHERE w.id=$1 AND w.deleted_at IS NULL AND w.owner_id=u.id)
		  OR EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id=$1 AND m.user_id=u.id)))`, workspaceID, userID)
	return ok, err
}

// Hierarchy reads the whole snapshot. ok is false when the workspace does not
// exist. Every list is ordered by id so two exports of the same state are
// byte-identical (the ETag depends on it).
func (r *WorkspaceExportRepo) Hierarchy(ctx context.Context, workspaceID uuid.UUID) (*domain.HierarchyExport, bool, error) {
	out := &domain.HierarchyExport{Projects: []domain.HierarchyProject{}, Members: []domain.HierarchyMember{}, Agents: []domain.HierarchyAgent{}}
	err := r.db.GetContext(ctx, &out.Workspace, `SELECT id, slug, name FROM workspaces WHERE id = $1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := r.db.SelectContext(ctx, &out.Projects, `
		SELECT id, slug, name, is_archived, created_at, updated_at
		  FROM projects WHERE workspace_id = $1 AND deleted_at IS NULL ORDER BY id`, workspaceID); err != nil {
		return nil, false, err
	}
	if err := r.db.SelectContext(ctx, &out.Members, `
		SELECT wm.user_id, u.email, u.display_name AS name, wm.role::text AS role
		  FROM workspace_members wm JOIN users u ON u.id = wm.user_id
		 WHERE wm.workspace_id = $1 ORDER BY wm.user_id`, workspaceID); err != nil {
		return nil, false, err
	}
	if err := r.db.SelectContext(ctx, &out.Agents, `
		SELECT id, slug, name, agent_type::text AS agent_type, role, parent_agent_id, sponsor_user_id, created_at
		  FROM agents WHERE workspace_id = $1 AND deleted_at IS NULL ORDER BY id`, workspaceID); err != nil {
		return nil, false, err
	}
	for i := range out.Agents {
		out.Agents[i].ActiveKeys = []domain.HierarchyAgentKey{}
	}
	idx := make(map[uuid.UUID]int, len(out.Agents))
	for i := range out.Agents {
		idx[out.Agents[i].ID] = i
	}

	// The original key lives on the agent row; it is active until it expires.
	var primary []struct {
		ID        uuid.UUID  `db:"id"`
		Prefix    string     `db:"api_key_prefix"`
		CreatedAt time.Time  `db:"created_at"`
		ExpiresAt *time.Time `db:"expires_at"`
	}
	if err := r.db.SelectContext(ctx, &primary, `
		SELECT id, api_key_prefix, COALESCE(last_rotated_at, created_at) AS created_at, expires_at
		  FROM agents
		 WHERE workspace_id = $1 AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, workspaceID); err != nil {
		return nil, false, err
	}
	for _, p := range primary {
		if i, ok := idx[p.ID]; ok {
			out.Agents[i].ActiveKeys = append(out.Agents[i].ActiveKeys, domain.HierarchyAgentKey{
				KeyID: domain.AgentPrimaryKeyID, KeyPrefix: p.Prefix, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
			})
		}
	}

	var extra []struct {
		ID           uuid.UUID  `db:"id"`
		AgentID      uuid.UUID  `db:"agent_id"`
		Prefix       string     `db:"key_prefix"`
		Label        string     `db:"label"`
		IssuedByType string     `db:"issued_by_type"`
		IssuedByID   uuid.UUID  `db:"issued_by_id"`
		CreatedAt    time.Time  `db:"created_at"`
		ExpiresAt    *time.Time `db:"expires_at"`
	}
	if err := r.db.SelectContext(ctx, &extra, `
		SELECT id, agent_id, key_prefix, label, issued_by_type::text AS issued_by_type, issued_by_id, created_at, expires_at
		  FROM agent_api_keys
		 WHERE workspace_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		 ORDER BY id`, workspaceID); err != nil {
		return nil, false, err
	}
	for _, k := range extra {
		if i, ok := idx[k.AgentID]; ok {
			by := k.IssuedByID
			out.Agents[i].ActiveKeys = append(out.Agents[i].ActiveKeys, domain.HierarchyAgentKey{
				KeyID: k.ID.String(), KeyPrefix: k.Prefix, Label: k.Label,
				IssuedByType: k.IssuedByType, IssuedByID: &by, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt,
			})
		}
	}
	return out, true, nil
}
