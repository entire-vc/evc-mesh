package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// AgentControllerRepo implements repository.AgentControllerRepository.
// Lease times use the database clock so racing API replicas agree.
type AgentControllerRepo struct {
	db *sqlx.DB
}

// NewAgentControllerRepo creates a new AgentControllerRepo.
func NewAgentControllerRepo(db *sqlx.DB) *AgentControllerRepo {
	return &AgentControllerRepo{db: db}
}

const agentControllerCols = `agent_id, runner_id, runner_name, host, version, announced_at, lease_expires_at`

func (r *AgentControllerRepo) Claim(ctx context.Context, c repository.AgentController, ttl time.Duration) (*repository.AgentController, bool, error) {
	var out repository.AgentController
	err := r.db.GetContext(ctx, &out, `
		INSERT INTO agent_controllers (agent_id, runner_id, runner_name, host, version, lease_expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))
		ON CONFLICT (agent_id) DO UPDATE SET
			announced_at = CASE WHEN agent_controllers.runner_id = EXCLUDED.runner_id
				THEN agent_controllers.announced_at ELSE now() END,
			runner_id = EXCLUDED.runner_id,
			runner_name = EXCLUDED.runner_name,
			host = EXCLUDED.host,
			version = EXCLUDED.version,
			lease_expires_at = EXCLUDED.lease_expires_at
		WHERE agent_controllers.runner_id = EXCLUDED.runner_id
		   OR agent_controllers.lease_expires_at <= now()
		RETURNING `+agentControllerCols,
		c.AgentID, c.RunnerID, c.RunnerName, c.Host, c.Version, ttl.Seconds())
	if err == nil {
		return &out, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	// The WHERE refused the update: someone else holds a live lease.
	var cur repository.AgentController
	if err := r.db.GetContext(ctx, &cur, `SELECT `+agentControllerCols+` FROM agent_controllers WHERE agent_id = $1`, c.AgentID); err != nil {
		return nil, false, err
	}
	return &cur, false, nil
}

func (r *AgentControllerRepo) Renew(ctx context.Context, agentID uuid.UUID, runnerID string, ttl time.Duration) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE agent_controllers
		SET lease_expires_at = now() + make_interval(secs => $3)
		WHERE agent_id = $1 AND ($2 = '' OR runner_id = $2)`,
		agentID, runnerID, ttl.Seconds())
	return err
}
