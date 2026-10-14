package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// AgentAPIKeyRepo implements repository.AgentAPIKeyRepository.
type AgentAPIKeyRepo struct {
	db *sqlx.DB
}

// NewAgentAPIKeyRepo creates a new AgentAPIKeyRepo.
func NewAgentAPIKeyRepo(db *sqlx.DB) *AgentAPIKeyRepo {
	return &AgentAPIKeyRepo{db: db}
}

const agentAPIKeyCols = `id, agent_id, workspace_id, key_prefix, key_sha256, label, issued_by_type, issued_by_id,
	expires_at, created_at, revoked_at`

func (r *AgentAPIKeyRepo) Create(ctx context.Context, k *domain.AgentAPIKey) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_api_keys (id, agent_id, workspace_id, key_prefix, key_sha256, label, issued_by_type, issued_by_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		k.ID, k.AgentID, k.WorkspaceID, k.KeyPrefix, k.KeySHA256, k.Label, string(k.IssuedByType), k.IssuedByID, k.ExpiresAt)
	return err
}

func (r *AgentAPIKeyRepo) GetByPrefix(ctx context.Context, workspaceID uuid.UUID, prefix string) (*domain.AgentAPIKey, error) {
	var out domain.AgentAPIKey
	err := r.db.GetContext(ctx, &out, `SELECT `+agentAPIKeyCols+` FROM agent_api_keys WHERE workspace_id = $1 AND key_prefix = $2`, workspaceID, prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *AgentAPIKeyRepo) GetByID(ctx context.Context, agentID, keyID uuid.UUID) (*domain.AgentAPIKey, error) {
	var out domain.AgentAPIKey
	err := r.db.GetContext(ctx, &out, `SELECT `+agentAPIKeyCols+` FROM agent_api_keys WHERE id = $1 AND agent_id = $2`, keyID, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// IsRevoked treats a vanished row as revoked, the same fail-closed reading as
// AgentWorkspaceGrantRepo.IsRevoked.
func (r *AgentAPIKeyRepo) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	var revoked bool
	err := r.db.GetContext(ctx, &revoked, `SELECT revoked_at IS NOT NULL FROM agent_api_keys WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return revoked, err
}

func (r *AgentAPIKeyRepo) Revoke(ctx context.Context, agentID, keyID uuid.UUID) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_api_keys SET revoked_at = now() WHERE id = $1 AND agent_id = $2 AND revoked_at IS NULL`, keyID, agentID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *AgentAPIKeyRepo) ListByAgent(ctx context.Context, agentID uuid.UUID) ([]domain.AgentAPIKey, error) {
	var out []domain.AgentAPIKey
	err := r.db.SelectContext(ctx, &out, `SELECT `+agentAPIKeyCols+` FROM agent_api_keys WHERE agent_id = $1 ORDER BY created_at, id`, agentID)
	return out, err
}

func (r *AgentAPIKeyRepo) CountActiveByAgent(ctx context.Context, agentID uuid.UUID) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT count(*) FROM agent_api_keys WHERE agent_id = $1 AND revoked_at IS NULL`, agentID)
	return n, err
}
