package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AgentController is the runner currently leading an agent.
type AgentController struct {
	AgentID        uuid.UUID `db:"agent_id" json:"agent_id"`
	RunnerID       string    `db:"runner_id" json:"runner_id"`
	RunnerName     string    `db:"runner_name" json:"runner_name"`
	Host           string    `db:"host" json:"host"`
	Version        string    `db:"version" json:"version"`
	AnnouncedAt    time.Time `db:"announced_at" json:"announced_at"`
	LeaseExpiresAt time.Time `db:"lease_expires_at" json:"lease_expires_at"`
}

// AgentControllerRepository stores the one-controller-per-agent lease.
type AgentControllerRepository interface {
	// Claim takes or renews the lease for runnerID. It succeeds when no one
	// holds it, the holder is the same runnerID, or the holder's lease has
	// lapsed. Otherwise it returns held=false and the current holder.
	Claim(ctx context.Context, c AgentController, ttl time.Duration) (cur *AgentController, held bool, err error)
	// Renew extends the lease on a heartbeat. A non-empty runnerID extends
	// only that runner's lease; empty extends whatever lease the agent has.
	Renew(ctx context.Context, agentID uuid.UUID, runnerID string, ttl time.Duration) error
}
