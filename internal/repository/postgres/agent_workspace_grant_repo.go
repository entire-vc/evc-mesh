package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Ensure AgentWorkspaceGrantRepo implements repository.AgentWorkspaceGrantRepository at compile time.
var _ repository.AgentWorkspaceGrantRepository = (*AgentWorkspaceGrantRepo)(nil)

// AgentWorkspaceGrantRepo implements repository.AgentWorkspaceGrantRepository with PostgreSQL.
type AgentWorkspaceGrantRepo struct {
	db *sqlx.DB
}

// NewAgentWorkspaceGrantRepo creates a new AgentWorkspaceGrantRepo.
func NewAgentWorkspaceGrantRepo(db *sqlx.DB) *AgentWorkspaceGrantRepo {
	return &AgentWorkspaceGrantRepo{db: db}
}

// GetByWorkspaceAndPrefix intentionally does NOT filter on revoked_at.
//
// idx_grants_lookup (workspace_id, api_key_prefix) WHERE revoked_at IS NULL
// only covers the active-row case, so this query — which must also see a
// revoked row to tell "revoked" apart from "never connected" — runs unindexed
// against agent_workspace_grants. That is fine at fleet scale (one row per
// agent per workspace, low hundreds at most): correctness of the
// revoked-vs-absent distinction matters more here than the index hit.
func (r *AgentWorkspaceGrantRepo) GetByWorkspaceAndPrefix(ctx context.Context, workspaceID uuid.UUID, prefix string) (*domain.AgentWorkspaceGrant, error) {
	const q = `
		SELECT id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, invited_by, created_at, revoked_at
		FROM agent_workspace_grants
		WHERE workspace_id = $1 AND api_key_prefix = $2
	`
	var g domain.AgentWorkspaceGrant
	if err := r.db.GetContext(ctx, &g, q, workspaceID, prefix); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

// IsRevoked reads revoked_at for a single grant by primary key. A PK lookup,
// unlike GetByWorkspaceAndPrefix, so it is safe to run on every cache hit
// (see the interface doc) rather than only on a miss.
//
// A row that no longer exists reports revoked=true — see the interface doc
// for why "gone" and "revoked" get the same answer here.
func (r *AgentWorkspaceGrantRepo) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	const q = `SELECT revoked_at IS NOT NULL FROM agent_workspace_grants WHERE id = $1`
	var revoked bool
	if err := r.db.GetContext(ctx, &revoked, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		return false, err
	}
	return revoked, nil
}
