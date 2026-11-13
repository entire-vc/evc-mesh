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

	var waitScopes map[uuid.UUID]bool
	if cmd.Kind == domain.TurnOutcomeWait && cmd.Wait != nil && cmd.Wait.Kind == domain.TaskWaitCard {
		if cmd.Wait.TargetTaskID == nil {
			return nil, domain.NewOutcomeError(domain.CodeInvalidTarget, "a card wait needs target_task_id")
		}
		// Revocation takes these gates exclusively before graph/source locks.
		// Do not lock grant rows: the revoke statement already owns that row.
		var scopes []uuid.UUID
		if scopeErr := tx.SelectContext(ctx, &scopes, `SELECT DISTINCT p.workspace_id
			FROM tasks t JOIN projects p ON p.id=t.project_id
			WHERE t.id IN ($1,$2) ORDER BY p.workspace_id`, cmd.TaskID, *cmd.Wait.TargetTaskID); scopeErr != nil {
			return nil, scopeErr
		}
		waitScopes = make(map[uuid.UUID]bool, len(scopes))
		for _, ws := range scopes {
			if _, err = tx.ExecContext(ctx, `SELECT card_wait_membership_lock($1,$2,false)`, cmd.AgentID, ws); err != nil {
				return nil, err
			}
			waitScopes[ws] = true
		}
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
			err = linkWaitTarget(ctx, tx, task.WorkspaceID, waitScopes, cmd.DenySameWorkspaceTarget, w)
			if err != nil {
				return nil, err
			}
			err = r.step("dependency")
			if err != nil {
				return nil, err
			}
		}
		if w.Kind == domain.TaskWaitTime {
			err = checkEvidenceReceipt(ctx, tx, task.WorkspaceID, w)
			if err != nil {
				return nil, err
			}
		}
		err = declareWaitTx(ctx, tx, w)
		if err != nil {
			return nil, err
		}
		if w.ServerObserved {
			// Evidence may already release an existing edge. Do not create a
			// permanently open wait when no future target mutation is needed.
			if _, err = tx.ExecContext(ctx, `SELECT resolve_server_card_waits($1)`, *w.TargetTaskID); err != nil {
				return nil, err
			}
			if receiptErr := tx.GetContext(ctx, w, `SELECT `+taskWaitCols+` FROM task_waits WHERE id=$1`, w.ID); receiptErr != nil {
				return nil, receiptErr
			}
		}
		err = r.step("wait")
		if err != nil {
			return nil, err
		}
		outcome.WaitID = &w.ID
		outcome.WaitServerObserved = w.ServerObserved
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
		err = tx.GetContext(ctx, &ok, `SELECT EXISTS(SELECT 1 FROM agents a
			WHERE a.id = $1 AND a.deleted_at IS NULL AND `+workspaceAgentSQL+`)`, *cmd.HandoffTo, task.WorkspaceID)
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
		// The caller is working the card again: a wait it still has here is
		// over, whether or not the runner resolved it on waking it.
		_, err = tx.ExecContext(ctx, `UPDATE task_waits SET resolved_at = now(), resolution = 'superseded',
			resolve_cause = 'continue_next' WHERE task_id = $1 AND agent_id = $2 AND resolved_at IS NULL`, cmd.TaskID, cmd.AgentID)
		if err != nil {
			return nil, err
		}
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

	if cmd.CommentID != nil {
		res.CommentID = *cmd.CommentID
	} else {
		meta := map[string]any{"typed_outcome": map[string]any{"kind": outcome.Kind, "outcome_id": outcome.ID}}
		if outcome.WaitID != nil {
			meta["typed_outcome"].(map[string]any)["wait_id"] = *outcome.WaitID
		}
		if res.CommentID, err = insertComment(ctx, tx, cmd.TaskID, cmd.AgentID, domain.ActorTypeAgent, cmd.CommentBody, meta); err != nil {
			return nil, err
		}
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

// linkWaitTarget creates a blocks edge within a workspace, or across the
// boundary only for a typed caller with current membership on both sides.
func linkWaitTarget(ctx context.Context, tx *sqlx.Tx, workspaceID uuid.UUID, scopes map[uuid.UUID]bool, denySameWorkspace bool, w *domain.TaskWait) error {
	cond := string(domain.DependencyConditionTaskDone)
	if w.TargetCondition != nil {
		cond = *w.TargetCondition
	}
	w.TargetCondition = &cond
	var target struct {
		WorkspaceID uuid.UUID `db:"workspace_id"`
		Released    bool      `db:"released"`
	}
	// Lock the target row alone first. Under READ COMMITTED a locking join
	// that waits on a concurrent UPDATE re-checks the join against the old
	// status/project rows; a status or project change then drops the row and
	// the card reads as missing (invalid_target instead of already_met).
	// With the row already held, the verdict query below sees its latest
	// committed version and nothing can move it under us.
	var locked uuid.UUID
	err := tx.GetContext(ctx, &locked, `SELECT id FROM tasks WHERE id = $1 AND deleted_at IS NULL FOR SHARE`, *w.TargetTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewOutcomeError(domain.CodeInvalidTarget, "target_task_id: target card not found")
	}
	if err != nil {
		return err
	}
	// The verdict is the same expression dependency reads use, over the edge
	// this wait is about to create.
	err = tx.GetContext(ctx, &target, `SELECT bp.workspace_id, `+domain.DependencyReleasedSQL("d", "bt", "bts.category")+` AS released
		FROM (SELECT 'blocks'::dependency_type AS dependency_type, $2::text AS condition,
		             NULL::text AS evidence_url, NULL::timestamptz AS condition_met_at, 'none'::text AS evidence_status) d,
		     tasks bt
		JOIN projects bp ON bp.id = bt.project_id
		JOIN task_statuses bts ON bts.id = bt.status_id
		WHERE bt.id = $1 AND bt.deleted_at IS NULL FOR SHARE OF bt,bts`, *w.TargetTaskID, cond)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewOutcomeError(domain.CodeInvalidTarget, "target_task_id: target card not found")
	}
	if err != nil {
		return err
	}
	if !scopes[workspaceID] || !scopes[target.WorkspaceID] {
		// A project move raced the prelock read. Retry from the new scope,
		// never acquire membership gates after holding the graph lock.
		return domain.NewOutcomeError(domain.CodeInvalidTarget, "target_task_id: target card not found")
	}
	w.ServerObserved = target.WorkspaceID != workspaceID
	if !w.ServerObserved && denySameWorkspace {
		return domain.NewOutcomeError(domain.CodeInvalidTarget, "target_task_id: target card not found")
	}
	var edgeAgent *uuid.UUID
	if w.ServerObserved {
		for _, ws := range []uuid.UUID{workspaceID, target.WorkspaceID} {
			var member bool
			if memberErr := tx.GetContext(ctx, &member, `SELECT EXISTS (SELECT 1 FROM agents a
				WHERE a.id=$1 AND a.deleted_at IS NULL AND `+workspaceAgentSQL+`)`, w.AgentID, ws); memberErr != nil {
				return memberErr
			}
			if !member {
				return domain.NewOutcomeError(domain.CodeInvalidTarget, "target_task_id: target card not found")
			}
		}
		edgeAgent = &w.AgentID
	}
	if target.Released {
		return domain.NewOutcomeError(domain.CodeAlreadyMet, "target_task_id: the target card already meets "+cond)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type, created_at, condition, wait_agent_id)
		VALUES ($1, $2, $3, 'blocks', now(), $4, $5)
		ON CONFLICT (task_id, depends_on_task_id) DO UPDATE SET dependency_type = 'blocks',
		condition = EXCLUDED.condition, wait_agent_id = EXCLUDED.wait_agent_id,
		evidence_status = CASE WHEN task_dependencies.condition IS DISTINCT FROM EXCLUDED.condition
		    OR task_dependencies.dependency_type <> 'blocks' THEN 'revoked' ELSE task_dependencies.evidence_status END,
		generation = task_dependencies.generation + CASE WHEN task_dependencies.condition IS DISTINCT FROM EXCLUDED.condition
		    OR task_dependencies.dependency_type <> 'blocks' THEN 1 ELSE 0 END`,
		uuid.New(), w.TaskID, *w.TargetTaskID, cond, edgeAgent)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Constraint == "completion_cycle" {
		return domain.NewOutcomeError(domain.CodeInvalidTarget, "target_task_id: waiting on this card would close a dependency cycle")
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
			 until_state, until_at, decision_request_id, human_gate_ref, note,
			 event_ref, evidence_source, evidence_why, until_at_offset, evidence_status,
			 after_sha, after_pipeline_id, after_status, registration, registration_ref,
			 addressee, question, server_observed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30)
		RETURNING id, created_at`,
		w.TaskID, w.AgentID, w.SessionID, w.Kind, w.TargetTaskID, w.TargetCondition,
		w.VCSLinkID, w.Provider, w.ProjectPath, w.MRIID, w.PipelineID, w.RequiredJobs,
		w.Until, w.UntilAt, w.DecisionRequestID, w.HumanGateRef, w.Note,
		w.EventRef, w.Source, w.Why, w.UntilAtOffset, w.EvidenceStatus,
		w.AfterSHA, w.AfterPipelineID, w.AfterStatus, w.Registration, w.RegistrationRef,
		w.Addressee, w.Question, w.ServerObserved,
	).Scan(&w.ID, &w.CreatedAt)
}

// checkEvidenceReceipt confirms a time wait's Mesh receipt (comment:<uuid> or
// artifact:<uuid>) exists in the card's workspace and was not written by the
// waiting agent itself: an agent cannot vouch for its own deadline. An https
// source is kept as given; the wait stays unverified either way.
func checkEvidenceReceipt(ctx context.Context, tx *sqlx.Tx, workspaceID uuid.UUID, w *domain.TaskWait) error {
	return checkEvidenceSource(ctx, tx, workspaceID, *w.Source, w.AgentID)
}

// checkEvidenceSource is checkEvidenceReceipt for any writer: start_after
// evidence is held to the same receipt rule as a time wait's.
func checkEvidenceSource(ctx context.Context, db sqlx.QueryerContext, workspaceID uuid.UUID, source string, writer uuid.UUID) error {
	kind, id, err := domain.ParseEvidenceSource(source)
	if err != nil || kind == "" {
		return err
	}
	q := `SELECT c.author_id FROM comments c JOIN tasks t ON t.id = c.task_id JOIN projects p ON p.id = t.project_id
		WHERE c.id = $1 AND p.workspace_id = $2`
	if kind == "artifact" {
		q = `SELECT a.uploaded_by FROM artifacts a JOIN tasks t ON t.id = a.task_id JOIN projects p ON p.id = t.project_id
		WHERE a.id = $1 AND p.workspace_id = $2`
	}
	var author uuid.UUID
	err = sqlx.GetContext(ctx, db, &author, q, id, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewOutcomeError(domain.CodeMissingEvidence, "source "+kind+" not found in this workspace")
	}
	if err != nil {
		return err
	}
	if author == writer {
		return domain.NewOutcomeError(domain.CodeMissingEvidence, "source "+kind+" was written by the agent itself")
	}
	return nil
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

// HoldsCheckout reports whether agentID holds a live checkout of the card.
// CommitTurn checks the same under the row lock; this is the early read
// ask_human makes before it posts the question.
func (r *TurnCommandRepo) HoldsCheckout(ctx context.Context, taskID, agentID uuid.UUID) (bool, error) {
	var held bool
	err := r.db.GetContext(ctx, &held, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1 AND deleted_at IS NULL
		AND checked_out_by = $2 AND checkout_expires > now())`, taskID, agentID)
	return held, err
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

// workspaceAgentSQL is true for an agent “a“ of the workspace bound as $2: its
// home workspace, or a workspace it holds an active grant in. A guest agent
// (a lead that serves several workspaces) is in the team directory of each and
// takes a hand-off or a question there like a home agent.
const workspaceAgentSQL = `(a.workspace_id = $2 OR EXISTS (SELECT 1 FROM agent_workspace_grants g
			WHERE g.agent_id = a.id AND g.workspace_id = $2 AND g.revoked_at IS NULL))`

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
	err = r.db.SelectContext(ctx, &rows, `SELECT a.id, a.name FROM agents a
		WHERE a.deleted_at IS NULL AND `+workspaceAgentSQL+`
		  AND (a.id::text = lower($1) OR lower(a.slug) = lower($1) OR lower(a.name) = lower($1))
		LIMIT 2`, to, workspaceID)
	if err != nil {
		return uuid.Nil, "", false, err
	}
	if len(rows) != 1 {
		return uuid.Nil, "", false, nil
	}
	return rows[0].ID, rows[0].Name, true, nil
}
