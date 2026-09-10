package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

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

// GetByAgentAndWorkspace returns the connection row for (agentID,
// workspaceID) via the uq_agent_ws_grant unique index — active OR revoked,
// same "never collapse revoked into absent" contract as
// GetByWorkspaceAndPrefix.
func (r *AgentWorkspaceGrantRepo) GetByAgentAndWorkspace(ctx context.Context, agentID, workspaceID uuid.UUID) (*domain.AgentWorkspaceGrant, error) {
	const q = `
		SELECT id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, invited_by, created_at, revoked_at
		FROM agent_workspace_grants
		WHERE agent_id = $1 AND workspace_id = $2
	`
	var g domain.AgentWorkspaceGrant
	if err := r.db.GetContext(ctx, &g, q, agentID, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

// Create inserts a brand-new connection row.
func (r *AgentWorkspaceGrantRepo) Create(ctx context.Context, g *domain.AgentWorkspaceGrant) error {
	const q = `
		INSERT INTO agent_workspace_grants (id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, invited_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := r.db.ExecContext(ctx, q,
		g.ID, g.AgentID, g.WorkspaceID, g.Role, g.APIKeyPrefix, g.APIKeyHash, g.InvitedBy, g.CreatedAt,
	)
	return err
}

// Reactivate overwrites a revoked row's role and key material in place and
// clears revoked_at — the same row id, per uq_agent_ws_grant's "one row per
// (agent, workspace) ever" contract.
func (r *AgentWorkspaceGrantRepo) Reactivate(ctx context.Context, id uuid.UUID, role, apiKeyPrefix, apiKeyHash string, invitedBy *uuid.UUID) error {
	const q = `
		UPDATE agent_workspace_grants
		SET role = $2, api_key_prefix = $3, api_key_hash = $4, invited_by = $5, revoked_at = NULL
		WHERE id = $1
	`
	_, err := r.db.ExecContext(ctx, q, id, role, apiKeyPrefix, apiKeyHash, invitedBy)
	return err
}

// Revoke sets revoked_at = NOW() on (id, workspaceID) unless it is already
// set, and reports whether a row matching both exists at all — see the
// interface doc for why found alone (not "did this call change anything") is
// what the caller needs.
func (r *AgentWorkspaceGrantRepo) Revoke(ctx context.Context, id, workspaceID uuid.UUID) (bool, error) {
	const q = `
		UPDATE agent_workspace_grants
		SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE id = $1 AND workspace_id = $2
		RETURNING id
	`
	var returnedID uuid.UUID
	if err := r.db.GetContext(ctx, &returnedID, q, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// agentWorkspaceGrantAgentRow is a flat DB scan struct for
// ListActiveByWorkspace's JOIN — same convention as workspaceMemberRow.
type agentWorkspaceGrantAgentRow struct {
	ID           uuid.UUID  `db:"id"`
	AgentID      uuid.UUID  `db:"agent_id"`
	WorkspaceID  uuid.UUID  `db:"workspace_id"`
	Role         string     `db:"role"`
	APIKeyPrefix string     `db:"api_key_prefix"`
	APIKeyHash   string     `db:"api_key_hash"`
	InvitedBy    *uuid.UUID `db:"invited_by"`
	CreatedAt    time.Time  `db:"created_at"`
	RevokedAt    *time.Time `db:"revoked_at"`
	AgentName    string     `db:"a_name"`
	AgentSlug    string     `db:"a_slug"`
}

// ListActiveByWorkspace returns every non-revoked connection into
// workspaceID, joined with the connected agent's brief info.
func (r *AgentWorkspaceGrantRepo) ListActiveByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.AgentWorkspaceGrantWithAgent, error) {
	const q = `
		SELECT
			g.id, g.agent_id, g.workspace_id, g.role, g.api_key_prefix, g.api_key_hash, g.invited_by, g.created_at, g.revoked_at,
			a.name AS a_name, a.slug AS a_slug
		FROM agent_workspace_grants g
		JOIN agents a ON a.id = g.agent_id
		WHERE g.workspace_id = $1 AND g.revoked_at IS NULL
		ORDER BY g.created_at
	`
	var rows []agentWorkspaceGrantAgentRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID); err != nil {
		return nil, err
	}

	result := make([]domain.AgentWorkspaceGrantWithAgent, len(rows))
	for i, row := range rows {
		result[i] = domain.AgentWorkspaceGrantWithAgent{
			AgentWorkspaceGrant: domain.AgentWorkspaceGrant{
				ID:           row.ID,
				AgentID:      row.AgentID,
				WorkspaceID:  row.WorkspaceID,
				Role:         row.Role,
				APIKeyPrefix: row.APIKeyPrefix,
				APIKeyHash:   row.APIKeyHash,
				InvitedBy:    row.InvitedBy,
				CreatedAt:    row.CreatedAt,
				RevokedAt:    row.RevokedAt,
			},
			Agent: domain.AgentBrief{
				ID:   row.AgentID,
				Name: row.AgentName,
				Slug: row.AgentSlug,
			},
		}
	}
	return result, nil
}

// agentWorkspaceGrantWorkspaceRow is a flat DB scan struct for
// ListActiveByAgent's JOIN.
type agentWorkspaceGrantWorkspaceRow struct {
	ID            uuid.UUID  `db:"id"`
	AgentID       uuid.UUID  `db:"agent_id"`
	WorkspaceID   uuid.UUID  `db:"workspace_id"`
	Role          string     `db:"role"`
	APIKeyPrefix  string     `db:"api_key_prefix"`
	APIKeyHash    string     `db:"api_key_hash"`
	InvitedBy     *uuid.UUID `db:"invited_by"`
	CreatedAt     time.Time  `db:"created_at"`
	RevokedAt     *time.Time `db:"revoked_at"`
	WorkspaceName string     `db:"w_name"`
	WorkspaceSlug string     `db:"w_slug"`
}

// ListActiveByAgent returns every non-revoked connection agentID holds,
// joined with the granting workspace's brief info.
func (r *AgentWorkspaceGrantRepo) ListActiveByAgent(ctx context.Context, agentID uuid.UUID) ([]domain.AgentWorkspaceGrantWithWorkspace, error) {
	const q = `
		SELECT
			g.id, g.agent_id, g.workspace_id, g.role, g.api_key_prefix, g.api_key_hash, g.invited_by, g.created_at, g.revoked_at,
			w.name AS w_name, w.slug AS w_slug
		FROM agent_workspace_grants g
		JOIN workspaces w ON w.id = g.workspace_id
		WHERE g.agent_id = $1 AND g.revoked_at IS NULL
		ORDER BY g.created_at
	`
	var rows []agentWorkspaceGrantWorkspaceRow
	if err := r.db.SelectContext(ctx, &rows, q, agentID); err != nil {
		return nil, err
	}

	result := make([]domain.AgentWorkspaceGrantWithWorkspace, len(rows))
	for i, row := range rows {
		result[i] = domain.AgentWorkspaceGrantWithWorkspace{
			AgentWorkspaceGrant: domain.AgentWorkspaceGrant{
				ID:           row.ID,
				AgentID:      row.AgentID,
				WorkspaceID:  row.WorkspaceID,
				Role:         row.Role,
				APIKeyPrefix: row.APIKeyPrefix,
				APIKeyHash:   row.APIKeyHash,
				InvitedBy:    row.InvitedBy,
				CreatedAt:    row.CreatedAt,
				RevokedAt:    row.RevokedAt,
			},
			Workspace: domain.WorkspaceBrief{
				ID:   row.WorkspaceID,
				Name: row.WorkspaceName,
				Slug: row.WorkspaceSlug,
			},
		}
	}
	return result, nil
}
