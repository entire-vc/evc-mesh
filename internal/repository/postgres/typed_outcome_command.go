package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The typed-outcome commands (docs/api/typed-outcomes.md §Commands): each one
// checks the card and the target, writes the wait, the outcome, the comment and
// the checkout change in one transaction. A refusal anywhere rolls all of it
// back — the caller still holds the checkout and nothing new is visible.

// NextCheckoutExtension is how far continue_next pushes the checkout deadline.
const NextCheckoutExtension = 15 * time.Minute

// TurnCommandRepo writes typed turn outcomes.
type TurnCommandRepo struct {
	db *sqlx.DB
	// failAt, when set (tests only), is called after each write step; an error
	// aborts the transaction there.
	failAt func(step string) error
}

// NewTurnCommandRepo creates a TurnCommandRepo.
func NewTurnCommandRepo(db *sqlx.DB) *TurnCommandRepo {
	return &TurnCommandRepo{db: db}
}

func (r *TurnCommandRepo) step(name string) error {
	if r.failAt == nil {
		return nil
	}
	return r.failAt(name)
}

type turnTaskRow struct {
	ProjectID       uuid.UUID  `db:"project_id"`
	WorkspaceID     uuid.UUID  `db:"workspace_id"`
	CheckedOutBy    *uuid.UUID `db:"checked_out_by"`
	CheckoutExpires *time.Time `db:"checkout_expires"`
	AssignedBy      string     `db:"assigned_by"`
}

// CommitTurn applies a next, wait or handoff command.
func (r *TurnCommandRepo) CommitTurn(ctx context.Context, cmd *domain.TurnCommand) (*domain.TurnCommandResult, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if cmd.Kind == domain.TurnOutcomeWait && cmd.Wait != nil && cmd.Wait.Kind == domain.TaskWaitCard {
		// Dependency writers take the workspace graph lock before any row lock.
		if _, err = tx.ExecContext(ctx, `SELECT completion_graph_lock(p.workspace_id)
			FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, cmd.TaskID); err != nil {
			return nil, err
		}
	}
	// NO KEY UPDATE: a concurrent dependency insert only needs KEY SHARE on
	// this row and must not deadlock against us.
	var task turnTaskRow
	err = tx.GetContext(ctx, &task, `SELECT t.project_id, p.workspace_id, t.checked_out_by, t.checkout_expires, t.assigned_by
		FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1 AND t.deleted_at IS NULL FOR NO KEY UPDATE OF t`, cmd.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Task")
	}
	if err != nil {
		return nil, err
	}
	if task.CheckedOutBy == nil || *task.CheckedOutBy != cmd.AgentID ||
		task.CheckoutExpires == nil || !task.CheckoutExpires.After(time.Now()) {
		return nil, apierror.Conflict("the caller does not hold this card's checkout")
	}

	res := &domain.TurnCommandResult{}
	outcome := &domain.TurnOutcome{TaskID: cmd.TaskID, AgentID: cmd.AgentID, SessionID: cmd.SessionID,
		TurnID: cmd.TurnID, Kind: cmd.Kind, Note: cmd.Note}
	var runtimeState any // nil leaves runtime_state alone in the next/handoff branches below

	switch cmd.Kind {
	case domain.TurnOutcomeWait:
		w := cmd.Wait
		if w == nil {
			return nil, fmt.Errorf("wait command without a wait")
		}
		w.TaskID, w.AgentID, w.SessionID = cmd.TaskID, cmd.AgentID, cmd.SessionID
		if w.Kind == domain.TaskWaitCard {
			err = linkWaitTarget(ctx, tx, task.WorkspaceID, w)
			if err != nil {
				return nil, err
			}
			err = r.step("dependency")
			if err != nil {
				return nil, err
			}
		}
		err = declareWaitTx(ctx, tx, w)
		if err != nil {
			return nil, err
		}
		err = r.step("wait")
		if err != nil {
			return nil, err
		}
		outcome.WaitID = &w.ID
		res.Wait = w
		var raw string
		raw, err = waitRuntimeState(ctx, tx, w, cmd)
		if err != nil {
			return nil, err
		}
		runtimeState = raw
	case domain.TurnOutcomeHandoff:
		if cmd.HandoffTo == nil {
			return nil, fmt.Errorf("handoff command without an addressee")
		}
		if task.AssignedBy == string(domain.AssignmentSourceHuman) {
			return nil, apierror.Conflict("a person pinned this card's assignee; only a person can reassign it")
		}
		var ok bool
		err = tx.GetContext(ctx, &ok, `SELECT EXISTS(SELECT 1 FROM agents
			WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL)`, *cmd.HandoffTo, task.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if !ok || *cmd.HandoffTo == cmd.AgentID {
			return nil, domain.NewOutcomeError(domain.CodeNotAnAgent, "hand_off needs another agent of this workspace")
		}
		_, err = tx.ExecContext(ctx, `UPDATE task_waits SET resolved_at = now(), resolution = 'cancelled'
			WHERE task_id = $1 AND agent_id = $2 AND resolved_at IS NULL`, cmd.TaskID, cmd.AgentID)
		if err != nil {
			return nil, err
		}
	case domain.TurnOutcomeNext:
	default:
		return nil, fmt.Errorf("unsupported turn command %q", cmd.Kind)
	}

	err = tx.QueryRowxContext(ctx, `
		INSERT INTO turn_outcomes (task_id, agent_id, session_id, turn_id, kind, wait_id, result_kind, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		outcome.TaskID, outcome.AgentID, outcome.SessionID, outcome.TurnID, outcome.Kind, outcome.WaitID,
		outcome.ResultKind, outcome.Note).Scan(&outcome.ID, &outcome.CreatedAt)
	if err != nil {
		return nil, err
	}
	res.Outcome = outcome
	err = r.step("outcome")
	if err != nil {
		return nil, err
	}

	meta := map[string]any{"typed_outcome": map[string]any{"kind": outcome.Kind, "outcome_id": outcome.ID}}
	if outcome.WaitID != nil {
		meta["typed_outcome"].(map[string]any)["wait_id"] = *outcome.WaitID
	}
	if res.CommentID, err = insertComment(ctx, tx, cmd.TaskID, cmd.AgentID, domain.ActorTypeAgent, cmd.CommentBody, meta); err != nil {
		return nil, err
	}
	err = r.step("comment")
	if err != nil {
		return nil, err
	}

	switch cmd.Kind {
	case domain.TurnOutcomeWait:
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET checked_out_by = NULL, checkout_token = NULL,
			checkout_expires = NULL, checkout_acquired_at = NULL, runtime_state = $2::jsonb, updated_at = now()
			WHERE id = $1`, cmd.TaskID, runtimeState)
		res.Released = true
	case domain.TurnOutcomeNext:
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET checkout_expires = GREATEST(checkout_expires, now() + $2::interval),
			updated_at = now() WHERE id = $1`, cmd.TaskID, fmt.Sprintf("%d seconds", int(NextCheckoutExtension.Seconds())))
	case domain.TurnOutcomeHandoff:
		var todo uuid.UUID
		err = tx.GetContext(ctx, &todo, `SELECT id FROM task_statuses WHERE project_id = $1 AND category = 'todo'
			ORDER BY position, is_default DESC, id LIMIT 1`, task.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.Unprocessable("the project has no todo status", "add a todo status to the project")
		} else if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET assignee_id = $2, assignee_type = 'agent', assigned_by = 'system',
			status_changed_at = CASE WHEN status_id = $3 THEN status_changed_at ELSE now() END,
			status_id = $3, completed_at = NULL,
			checked_out_by = NULL, checkout_token = NULL, checkout_expires = NULL, checkout_acquired_at = NULL,
			runtime_state = NULL, updated_at = now()
			WHERE id = $1`, cmd.TaskID, *cmd.HandoffTo, todo)
		res.Released = true
	}
	if err != nil {
		return nil, err
	}
	err = tx.GetContext(ctx, &res.Task, `SELECT t.id, s.slug AS status, t.assignee_id
		FROM tasks t JOIN task_statuses s ON s.id = t.status_id WHERE t.id = $1`, cmd.TaskID)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return res, nil
}

// AddNote writes a comment that wakes nobody (metadata.wakes=false). No
// outcome, no checkout change.
func (r *TurnCommandRepo) AddNote(ctx context.Context, taskID, authorID uuid.UUID, authorType domain.ActorType, body string) (uuid.UUID, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	err = tx.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1 AND deleted_at IS NULL)`, taskID)
	if err != nil {
		return uuid.Nil, err
	}
	if !exists {
		return uuid.Nil, apierror.NotFound("Task")
	}
	id, err := insertComment(ctx, tx, taskID, authorID, authorType, body, map[string]any{"wakes": false})
	if err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit()
}

// linkWaitTarget checks a card target (exists, same workspace, not already
// released for the condition) and creates or reuses the blocks edge.
func linkWaitTarget(ctx context.Context, tx *sqlx.Tx, workspaceID uuid.UUID, w *domain.TaskWait) error {
	cond := string(domain.DependencyConditionTaskDone)
	if w.TargetCondition != nil {
		cond = *w.TargetCondition
	}
	w.TargetCondition = &cond
	var target struct {
		WorkspaceID uuid.UUID `db:"workspace_id"`
		Released    bool      `db:"released"`
	}
	// The verdict is the same expression dependency reads use, over the edge
	// this wait is about to create.
	err := tx.GetContext(ctx, &target, `SELECT bp.workspace_id, `+domain.DependencyReleasedSQL("d", "bt", "bts.category")+` AS released
		FROM (SELECT 'blocks'::dependency_type AS dependency_type, $2::text AS condition,
		             NULL::text AS evidence_url, NULL::timestamptz AS condition_met_at) d,
		     tasks bt
		JOIN projects bp ON bp.id = bt.project_id
		JOIN task_statuses bts ON bts.id = bt.status_id
		WHERE bt.id = $1 AND bt.deleted_at IS NULL`, *w.TargetTaskID, cond)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && target.WorkspaceID != workspaceID) {
		return domain.NewOutcomeError(domain.CodeInvalidTarget, "target card not found")
	}
	if err != nil {
		return err
	}
	if target.Released {
		return domain.NewOutcomeError(domain.CodeAlreadyMet, "the target card already meets "+cond)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type, created_at, condition)
		VALUES ($1, $2, $3, 'blocks', now(), $4)
		ON CONFLICT (task_id, depends_on_task_id) DO UPDATE SET dependency_type = 'blocks', condition = EXCLUDED.condition`,
		uuid.New(), w.TaskID, *w.TargetTaskID, cond)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Constraint == "completion_cycle" {
		return domain.NewOutcomeError(domain.CodeInvalidTarget, "waiting on this card would close a dependency cycle")
	}
	return err
}

// declareWaitTx is TaskWaitRepo.Declare inside the command's transaction.
func declareWaitTx(ctx context.Context, tx *sqlx.Tx, w *domain.TaskWait) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_waits SET resolved_at = now(), resolution = 'superseded'
		 WHERE task_id = $1 AND agent_id = $2 AND resolved_at IS NULL`, w.TaskID, w.AgentID); err != nil {
		return err
	}
	if w.RequiredJobs == nil {
		w.RequiredJobs = []string{}
	}
	return tx.QueryRowxContext(ctx, `
		INSERT INTO task_waits
			(task_id, agent_id, session_id, kind, target_task_id, target_condition,
			 vcs_link_id, provider, project_path, mr_iid, pipeline_id, required_jobs,
			 until_state, until_at, decision_request_id, human_gate_ref, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id, created_at`,
		w.TaskID, w.AgentID, w.SessionID, w.Kind, w.TargetTaskID, w.TargetCondition,
		w.VCSLinkID, w.Provider, w.ProjectPath, w.MRIID, w.PipelineID, w.RequiredJobs,
		w.Until, w.UntilAt, w.DecisionRequestID, w.HumanGateRef, w.Note,
	).Scan(&w.ID, &w.CreatedAt)
}

// waitRuntimeState is the card's runtime_state while the wait is active.
func waitRuntimeState(ctx context.Context, tx *sqlx.Tx, w *domain.TaskWait, cmd *domain.TurnCommand) (string, error) {
	var lane string
	if err := tx.GetContext(ctx, &lane, `SELECT name FROM agents WHERE id = $1`, cmd.AgentID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	ref := domain.WaitRuntimeRefPrefix + w.ID.String()
	state := &domain.RuntimeState{Kind: domain.RuntimeKindWaiting, Since: w.CreatedAt, Until: w.UntilAt, Ref: &ref}
	if lane != "" {
		state.Lane = &lane
	}
	if cmd.RuntimeReason != "" {
		reason := cmd.RuntimeReason
		state.Reason = &reason
	}
	if err := state.Normalize(w.CreatedAt); err != nil {
		return "", err
	}
	raw, err := state.Marshal()
	return string(raw), err
}

func insertComment(ctx context.Context, tx *sqlx.Tx, taskID, authorID uuid.UUID, authorType domain.ActorType, body string, meta map[string]any) (uuid.UUID, error) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	_, err = tx.ExecContext(ctx, `INSERT INTO comments (id, task_id, author_id, author_type, body, metadata, is_internal, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, false, now(), now())`, id, taskID, authorID, authorType, body, raw)
	return id, err
}

// TaskScope returns the card's project and workspace.
func (r *TurnCommandRepo) TaskScope(ctx context.Context, taskID uuid.UUID) (projectID, workspaceID uuid.UUID, err error) {
	var row struct {
		ProjectID   uuid.UUID `db:"project_id"`
		WorkspaceID uuid.UUID `db:"workspace_id"`
	}
	err = r.db.GetContext(ctx, &row, `SELECT t.project_id, p.workspace_id FROM tasks t
		JOIN projects p ON p.id = t.project_id WHERE t.id = $1 AND t.deleted_at IS NULL`, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, uuid.Nil, apierror.NotFound("Task")
	}
	return row.ProjectID, row.WorkspaceID, err
}

// ResolveAgent finds an agent of the workspace directory by id, slug or name
// (case-insensitive). An ambiguous name is not found.
func (r *TurnCommandRepo) ResolveAgent(ctx context.Context, workspaceID uuid.UUID, to string) (id uuid.UUID, name string, found bool, err error) {
	if to == "" {
		return uuid.Nil, "", false, nil
	}
	var rows []struct {
		ID   uuid.UUID `db:"id"`
		Name string    `db:"name"`
	}
	err = r.db.SelectContext(ctx, &rows, `SELECT id, name FROM agents
		WHERE workspace_id = $1 AND deleted_at IS NULL
		  AND (id::text = lower($2) OR lower(slug) = lower($2) OR lower(name) = lower($2))
		LIMIT 2`, workspaceID, to)
	if err != nil {
		return uuid.Nil, "", false, err
	}
	if len(rows) != 1 {
		return uuid.Nil, "", false, nil
	}
	return rows[0].ID, rows[0].Name, true, nil
}
