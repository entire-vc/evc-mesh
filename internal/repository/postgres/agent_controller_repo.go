package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
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

// agentChatSnapshotRow is a snapshot row plus its age on the database clock.
type agentChatSnapshotRow struct {
	repository.ChatRunnerSnapshot
	SessionsJSON []byte  `db:"sessions"`
	AgeSeconds   float64 `db:"age_seconds"`
}

const agentChatSnapshotCols = `s.agent_id, s.runner_id, s.sessions, s.slots_max, s.slots_used, s.reported_at, s.received_at,
	EXTRACT(EPOCH FROM now() - s.received_at)::float8 AS age_seconds`

func (row agentChatSnapshotRow) snapshot() (repository.ChatRunnerSnapshot, error) {
	out := row.ChatRunnerSnapshot
	out.ReportedAt, out.ReceivedAt = out.ReportedAt.UTC(), out.ReceivedAt.UTC()
	out.Age = time.Duration(row.AgeSeconds * float64(time.Second))
	out.Sessions = []repository.ChatSessionEntry{}
	if len(row.SessionsJSON) > 0 {
		if err := json.Unmarshal(row.SessionsJSON, &out.Sessions); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (r *AgentControllerRepo) SaveChatSnapshot(ctx context.Context, s repository.ChatRunnerSnapshot) (bool, error) {
	sessions := s.Sessions
	if sessions == nil {
		sessions = []repository.ChatSessionEntry{}
	}
	raw, err := json.Marshal(sessions)
	if err != nil {
		return false, err
	}
	// The lease check and the write are one statement: a runner that lost the
	// lease between its claim and this write does not overwrite anything.
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_controller_chat_snapshots (agent_id, runner_id, sessions, slots_max, slots_used, reported_at, received_at)
		SELECT $1, $2, $3::jsonb, $4, $5, $6, now()
		WHERE EXISTS (SELECT 1 FROM agent_controllers c
			WHERE c.agent_id = $1 AND c.runner_id = $2 AND c.lease_expires_at > now())
		ON CONFLICT (agent_id, runner_id) DO UPDATE SET
			sessions = EXCLUDED.sessions,
			slots_max = EXCLUDED.slots_max,
			slots_used = EXCLUDED.slots_used,
			reported_at = EXCLUDED.reported_at,
			received_at = EXCLUDED.received_at`,
		s.AgentID, s.RunnerID, string(raw), s.SlotsMax, s.SlotsUsed, s.ReportedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *AgentControllerRepo) LatestChatSnapshot(ctx context.Context, agentID uuid.UUID) (*repository.ChatRunnerSnapshot, error) {
	var row agentChatSnapshotRow
	err := r.db.GetContext(ctx, &row, `SELECT `+agentChatSnapshotCols+`
		FROM agent_controller_chat_snapshots s WHERE s.agent_id = $1
		ORDER BY s.received_at DESC LIMIT 1`, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := row.snapshot()
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *AgentControllerRepo) RunnerChatSnapshots(ctx context.Context, workspaceID uuid.UUID, runnerID string) ([]repository.ChatRunnerSnapshot, error) {
	var rows []agentChatSnapshotRow
	if err := r.db.SelectContext(ctx, &rows, `SELECT `+agentChatSnapshotCols+`
		FROM agent_controller_chat_snapshots s
		JOIN agents a ON a.id = s.agent_id
		WHERE s.runner_id = $1 AND a.workspace_id = $2
		ORDER BY s.agent_id`, runnerID, workspaceID); err != nil {
		return nil, err
	}
	out := make([]repository.ChatRunnerSnapshot, 0, len(rows))
	for _, row := range rows {
		s, err := row.snapshot()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
