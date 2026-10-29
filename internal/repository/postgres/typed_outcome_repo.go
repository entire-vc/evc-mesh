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

// Typed turn outcomes (migration 20261028001, docs/api/typed-outcomes.md).

var (
	_ repository.TaskWaitRepository        = (*TaskWaitRepo)(nil)
	_ repository.TurnOutcomeRepository     = (*TurnOutcomeRepo)(nil)
	_ repository.DecisionRequestRepository = (*DecisionRequestRepo)(nil)
)

const taskWaitCols = `id, task_id, agent_id, session_id, kind, target_task_id, target_condition,
	vcs_link_id, provider, project_path, mr_iid, pipeline_id, required_jobs, until_state,
	until_at, decision_request_id, human_gate_ref, note, event_ref, evidence_source, evidence_why,
	until_at_offset, evidence_status, created_at, resolved_at, resolution`

// TaskWaitRepo implements repository.TaskWaitRepository.
type TaskWaitRepo struct {
	db *sqlx.DB
}

// NewTaskWaitRepo creates a TaskWaitRepo.
func NewTaskWaitRepo(db *sqlx.DB) *TaskWaitRepo {
	return &TaskWaitRepo{db: db}
}

// Declare supersedes the pair's active wait and inserts w in one transaction.
// The partial unique index task_waits_one_active is what keeps two concurrent
// declarations from both staying active: the loser's INSERT fails.
func (r *TaskWaitRepo) Declare(ctx context.Context, w *domain.TaskWait) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := declareWaitTx(ctx, tx, w); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *TaskWaitRepo) GetActive(ctx context.Context, taskID uuid.UUID, agentID *uuid.UUID) (*domain.TaskWait, error) {
	var w domain.TaskWait
	err := r.db.GetContext(ctx, &w, `SELECT `+taskWaitCols+` FROM task_waits
		 WHERE task_id = $1 AND ($2::uuid IS NULL OR agent_id = $2) AND resolved_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, taskID, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (r *TaskWaitRepo) Resolve(ctx context.Context, id uuid.UUID, resolution domain.WaitResolution) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE task_waits SET resolved_at = now(), resolution = $2
		 WHERE id = $1 AND resolved_at IS NULL`, id, resolution)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

const turnOutcomeCols = `id, task_id, agent_id, session_id, turn_id, kind, wait_id, result_kind, note, created_at`

// TurnOutcomeRepo implements repository.TurnOutcomeRepository.
type TurnOutcomeRepo struct {
	db *sqlx.DB
}

// NewTurnOutcomeRepo creates a TurnOutcomeRepo.
func NewTurnOutcomeRepo(db *sqlx.DB) *TurnOutcomeRepo {
	return &TurnOutcomeRepo{db: db}
}

func (r *TurnOutcomeRepo) Append(ctx context.Context, o *domain.TurnOutcome) error {
	return r.db.QueryRowxContext(ctx, `
		INSERT INTO turn_outcomes (task_id, agent_id, session_id, turn_id, kind, wait_id, result_kind, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		o.TaskID, o.AgentID, o.SessionID, o.TurnID, o.Kind, o.WaitID, o.ResultKind, o.Note,
	).Scan(&o.ID, &o.CreatedAt)
}

func (r *TurnOutcomeRepo) ListByTask(ctx context.Context, taskID uuid.UUID, sessionID *string, limit int) ([]domain.TurnOutcome, error) {
	out := []domain.TurnOutcome{}
	err := r.db.SelectContext(ctx, &out, `SELECT `+turnOutcomeCols+` FROM turn_outcomes
		 WHERE task_id = $1 AND ($2::text IS NULL OR session_id = $2)
		 ORDER BY created_at DESC, id DESC LIMIT $3`, taskID, sessionID, limit)
	return out, err
}

const decisionRequestCols = `id, task_id, from_agent_id, to_agent_id, question, state,
	answer_comment_id, answered_at, created_at`

// DecisionRequestRepo implements repository.DecisionRequestRepository.
type DecisionRequestRepo struct {
	db *sqlx.DB
}

// NewDecisionRequestRepo creates a DecisionRequestRepo.
func NewDecisionRequestRepo(db *sqlx.DB) *DecisionRequestRepo {
	return &DecisionRequestRepo{db: db}
}

// Open inserts the request unless the same one is already open; the conflict
// target is the expression of the partial index decision_requests_one_open.
func (r *DecisionRequestRepo) Open(ctx context.Context, in *domain.DecisionRequest) (*domain.DecisionRequest, bool, error) {
	var d domain.DecisionRequest
	err := r.db.GetContext(ctx, &d, `
		INSERT INTO decision_requests (task_id, from_agent_id, to_agent_id, question)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (task_id, from_agent_id, to_agent_id, md5(lower(btrim(question))))
		   WHERE state = 'open' DO NOTHING
		RETURNING `+decisionRequestCols,
		in.TaskID, in.FromAgentID, in.ToAgentID, in.Question)
	if err == nil {
		return &d, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	err = r.db.GetContext(ctx, &d, `SELECT `+decisionRequestCols+` FROM decision_requests
		 WHERE task_id = $1 AND from_agent_id = $2 AND to_agent_id = $3
		   AND md5(lower(btrim(question))) = md5(lower(btrim($4))) AND state = 'open'`,
		in.TaskID, in.FromAgentID, in.ToAgentID, in.Question)
	if err != nil {
		return nil, false, err
	}
	return &d, true, nil
}

func (r *DecisionRequestRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.DecisionRequest, error) {
	var d domain.DecisionRequest
	err := r.db.GetContext(ctx, &d, `SELECT `+decisionRequestCols+` FROM decision_requests WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DecisionRequestRepo) Answer(ctx context.Context, id uuid.UUID, answerCommentID *uuid.UUID) (bool, error) {
	return r.closeOpen(ctx, `UPDATE decision_requests
		   SET state = 'answered', answered_at = now(), answer_comment_id = $2
		 WHERE id = $1 AND state = 'open'`, id, answerCommentID)
}

func (r *DecisionRequestRepo) Withdraw(ctx context.Context, id uuid.UUID) (bool, error) {
	return r.closeOpen(ctx, `UPDATE decision_requests SET state = 'withdrawn'
		 WHERE id = $1 AND state = 'open'`, id)
}

func (r *DecisionRequestRepo) closeOpen(ctx context.Context, q string, args ...any) (bool, error) {
	res, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
