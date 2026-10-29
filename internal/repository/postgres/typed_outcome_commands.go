package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Typed-outcome commands ask_agent, answer_decision and finish_task
// (docs/api/typed-outcomes.md §Commands 3–5). Every write of a command shares
// one transaction: a refusal or a failure half-way leaves nothing behind.

// insertCommandComment writes the server-generated comment of a command. The
// comment service's side effects (mention delivery, wakes) are deliberately not
// run: the command's own state is what tells the runner what happened.
func insertCommandComment(ctx context.Context, tx *sqlx.Tx, c *domain.Comment) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	if len(c.Metadata) == 0 {
		c.Metadata = json.RawMessage(`{}`)
	}
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	_, err := tx.ExecContext(ctx, `
		INSERT INTO comments (id, task_id, parent_comment_id, author_id, author_type, body, metadata, is_internal, created_at, updated_at)
		VALUES ($1, $2, NULL, $3, $4, $5, $6, false, $7, $7)`,
		c.ID, c.TaskID, c.AuthorID, c.AuthorType, c.Body, c.Metadata, now)
	return err
}

func appendOutcomeTx(ctx context.Context, tx *sqlx.Tx, o *domain.TurnOutcome) error {
	return tx.QueryRowxContext(ctx, `
		INSERT INTO turn_outcomes (task_id, agent_id, session_id, turn_id, kind, wait_id, result_kind, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		o.TaskID, o.AgentID, o.SessionID, o.TurnID, o.Kind, o.WaitID, o.ResultKind, o.Note,
	).Scan(&o.ID, &o.CreatedAt)
}

// writeTaskFinish is finish_task's part of the move transaction, run after the
// guarded UPDATE of the task row (which already set status, labels, result_kind
// and released the checkout): the closing comment, the caller's active wait
// cancelled, and the outcome.
func writeTaskFinish(ctx context.Context, tx *sqlx.Tx, taskID uuid.UUID, f *domain.TaskFinish) error {
	f.Comment.TaskID = taskID
	if err := insertCommandComment(ctx, tx, f.Comment); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_waits SET resolved_at = now(), resolution = 'cancelled'
		 WHERE task_id = $1 AND agent_id = $2 AND resolved_at IS NULL`, taskID, f.Caller); err != nil {
		return err
	}
	f.Outcome.TaskID = taskID
	return appendOutcomeTx(ctx, tx, f.Outcome)
}

// lockCommandTask locks the card and refuses a command from an agent other
// than the holder of a live checkout.
func lockCommandTask(ctx context.Context, tx *sqlx.Tx, taskID, caller uuid.UUID) (*domain.Task, error) {
	task, err := lockParkedTask(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	if task.CheckedOutBy != nil && *task.CheckedOutBy != caller &&
		task.CheckoutExpires != nil && task.CheckoutExpires.After(time.Now()) {
		return nil, apierror.Conflict("the card is checked out by another agent")
	}
	return task, nil
}

// releaseCommandCheckout clears the caller's lease, keeping the session and
// request as the replay tombstone exactly as a terminal move does, and records
// the release in the same transaction.
func releaseCommandCheckout(ctx context.Context, tx *sqlx.Tx, task *domain.Task, caller uuid.UUID, reason string) (bool, error) {
	if task.CheckedOutBy == nil || *task.CheckedOutBy != caller {
		return false, nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE tasks SET checked_out_by = NULL, checkout_token = NULL, checkout_expires = NULL, checkout_acquired_at = NULL
		 WHERE id = $1 AND checked_out_by = $2`, task.ID, caller)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	changes, err := json.Marshal(map[string]any{"reason": reason, "previous_holder": caller,
		"generation": task.CheckoutGeneration, "session_id": task.CheckoutSessionID})
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO activity_log (id, workspace_id, entity_type, entity_id, action, actor_id, actor_type, changes, created_at)
		SELECT $1, workspace_id, 'task', $2, 'task.checkout_released', $3, $4, $5, now() FROM projects WHERE id = $6`,
		uuid.New(), task.ID, caller, domain.ActorTypeAgent, changes, task.ProjectID)
	return err == nil, err
}

// resolveDirectoryAgent finds the addressee among the agents of the card's
// workspace by id, slug or name (case-insensitive, an @ prefix ignored). A
// person, an unknown name, an ambiguous name or the caller is not_an_agent.
func resolveDirectoryAgent(ctx context.Context, tx *sqlx.Tx, projectID, caller uuid.UUID, to string) (*domain.AgentRef, error) {
	key := strings.TrimPrefix(strings.TrimSpace(to), "@")
	if key == "" {
		return nil, domain.NewOutcomeError(domain.CodeNotAnAgent, "to names nobody: give an agent name or id from the team directory")
	}
	var found []domain.AgentRef
	if err := tx.SelectContext(ctx, &found, `
		SELECT a.id, a.name, a.slug FROM agents a
		  JOIN projects p ON p.workspace_id = a.workspace_id AND p.id = $1
		 WHERE a.deleted_at IS NULL
		   AND (a.id::text = lower($2) OR lower(a.slug) = lower($2) OR lower(a.name) = lower($2))
		 LIMIT 2`, projectID, key); err != nil {
		return nil, err
	}
	switch {
	case len(found) == 0:
		return nil, domain.NewOutcomeError(domain.CodeNotAnAgent,
			"no agent "+key+" in this workspace's directory; a person is asked with ask_human")
	case len(found) > 1:
		return nil, domain.NewOutcomeError(domain.CodeNotAnAgent, key+" matches more than one agent; use the agent id")
	case found[0].ID == caller:
		return nil, domain.NewOutcomeError(domain.CodeNotAnAgent, "asking yourself is not a question: decide and log it on the card")
	}
	return &found[0], nil
}

// AskAgent opens (or reuses) the decision request, declares the caller's
// decision wait on it, appends a wait outcome, writes the comment and releases
// the caller's checkout — one transaction.
func (r *TaskRepo) AskAgent(ctx context.Context, in domain.AskAgentWrite) (*domain.AskAgentResult, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	task, err := lockCommandTask(ctx, tx, in.TaskID, in.Caller)
	if err != nil {
		return nil, err
	}
	to, err := resolveDirectoryAgent(ctx, tx, task.ProjectID, in.Caller, in.To)
	if err != nil {
		return nil, err
	}

	var req domain.DecisionRequest
	reused := false
	err = tx.GetContext(ctx, &req, `
		INSERT INTO decision_requests (task_id, from_agent_id, to_agent_id, question)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (task_id, from_agent_id, to_agent_id, md5(lower(btrim(question))))
		   WHERE state = 'open' DO NOTHING
		RETURNING `+decisionRequestCols, in.TaskID, in.Caller, to.ID, in.Question)
	if errors.Is(err, sql.ErrNoRows) {
		reused = true
		err = tx.GetContext(ctx, &req, `SELECT `+decisionRequestCols+` FROM decision_requests
			 WHERE task_id = $1 AND from_agent_id = $2 AND to_agent_id = $3
			   AND md5(lower(btrim(question))) = md5(lower(btrim($4))) AND state = 'open'`,
			in.TaskID, in.Caller, to.ID, in.Question)
	}
	if err != nil {
		return nil, err
	}

	wait := &domain.TaskWait{TaskID: in.TaskID, AgentID: in.Caller, SessionID: in.SessionID,
		Kind: domain.TaskWaitDecision, DecisionRequestID: &req.ID, Note: "asked " + to.Name}
	if werr := declareWaitTx(ctx, tx, wait); werr != nil {
		return nil, werr
	}
	outcome := &domain.TurnOutcome{TaskID: in.TaskID, AgentID: in.Caller, SessionID: in.SessionID,
		TurnID: in.TurnID, Kind: domain.TurnOutcomeWait, WaitID: &wait.ID, Note: "asked " + to.Name}
	if oerr := appendOutcomeTx(ctx, tx, outcome); oerr != nil {
		return nil, oerr
	}
	meta, err := json.Marshal(map[string]any{"command": "ask_agent", "decision_request_id": req.ID, "wakes": false})
	if err != nil {
		return nil, err
	}
	comment := &domain.Comment{TaskID: in.TaskID, AuthorID: in.Caller, AuthorType: domain.ActorTypeAgent,
		Body: in.CommentBody(&req, to, reused), Metadata: meta}
	if cerr := insertCommandComment(ctx, tx, comment); cerr != nil {
		return nil, cerr
	}
	released, err := releaseCommandCheckout(ctx, tx, task, in.Caller, "ask_agent")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.AskAgentResult{Request: &req, Reused: reused, To: *to, Wait: wait, Outcome: outcome,
		CommentID: comment.ID, Released: released}, nil
}

// AnswerDecision answers an open request: the answer comment on the asking
// card, the request answered, the asker's decision waits on it met — one
// transaction. Only the addressee agent may answer.
func (r *TaskRepo) AnswerDecision(ctx context.Context, in domain.AnswerDecisionWrite) (*domain.AnswerDecisionResult, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var req domain.DecisionRequest
	err = tx.GetContext(ctx, &req, `SELECT `+decisionRequestCols+` FROM decision_requests WHERE id = $1 FOR UPDATE`, in.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("DecisionRequest")
	}
	if err != nil {
		return nil, err
	}
	if in.ActorType != domain.ActorTypeAgent || in.ActorID != req.ToAgentID {
		return nil, apierror.Forbidden("only the addressee of the question may answer it")
	}
	if req.State != domain.DecisionOpen {
		return nil, apierror.Conflict("the question is already " + string(req.State))
	}

	meta, err := json.Marshal(map[string]any{"command": "answer_decision", "answers": req.ID})
	if err != nil {
		return nil, err
	}
	comment := &domain.Comment{TaskID: req.TaskID, AuthorID: in.ActorID, AuthorType: in.ActorType,
		Body: in.CommentBody(&req), Metadata: meta}
	if err := insertCommandComment(ctx, tx, comment); err != nil {
		return nil, err
	}
	if err := tx.GetContext(ctx, &req, `
		UPDATE decision_requests SET state = 'answered', answered_at = now(), answer_comment_id = $2
		 WHERE id = $1 RETURNING `+decisionRequestCols, req.ID, comment.ID); err != nil {
		return nil, err
	}
	met := []uuid.UUID{}
	if err := tx.SelectContext(ctx, &met, `
		UPDATE task_waits SET resolved_at = now(), resolution = 'met'
		 WHERE decision_request_id = $1 AND kind = 'decision' AND resolved_at IS NULL
		RETURNING id`, req.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.AnswerDecisionResult{Request: &req, CommentID: comment.ID, MetWaits: met}, nil
}
