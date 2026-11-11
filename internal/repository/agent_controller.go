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

// ChatSessionEntry is one chat session in a runner's snapshot. SlotHeld and
// ProcessAlive independently describe its slot and harness process; stopped
// sessions may remain in the snapshot for diagnosis.
type ChatSessionEntry struct {
	ThreadID     uuid.UUID  `json:"thread_id"`
	Agent        string     `json:"agent"`
	ProcessAlive bool       `json:"process_alive"`
	SlotHeld     bool       `json:"slot_held"`
	Busy         bool       `json:"busy"`
	LastEndAt    *time.Time `json:"last_end_at"`
}

// ChatRunnerSnapshot is the last chat-session snapshot one runner sent for one
// agent with its controller announce. ReceivedAt is the database clock at the
// write; Age is now() - ReceivedAt on the same clock at the read.
type ChatRunnerSnapshot struct {
	AgentID    uuid.UUID          `db:"agent_id"`
	RunnerID   string             `db:"runner_id"`
	Sessions   []ChatSessionEntry `db:"-"`
	SlotsMax   *int               `db:"slots_max"`
	SlotsUsed  *int               `db:"slots_used"`
	ReportedAt time.Time          `db:"reported_at"`
	ReceivedAt time.Time          `db:"received_at"`
	Age        time.Duration      `db:"-"`
}

// ChatRunnerSnapshotRepository keeps the latest snapshot per (agent, runner).
type ChatRunnerSnapshotRepository interface {
	// SaveChatSnapshot overwrites the (agent, runner) snapshot, but only while
	// that runner holds the agent's live controller lease; saved=false otherwise.
	SaveChatSnapshot(ctx context.Context, s ChatRunnerSnapshot) (saved bool, err error)
	// LatestChatSnapshot is the agent's most recently received snapshot, nil if none.
	LatestChatSnapshot(ctx context.Context, agentID uuid.UUID) (*ChatRunnerSnapshot, error)
	// RunnerChatSnapshots lists the runner's snapshots for agents of one workspace.
	RunnerChatSnapshots(ctx context.Context, workspaceID uuid.UUID, runnerID string) ([]ChatRunnerSnapshot, error)
}
