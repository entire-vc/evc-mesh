package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// A command that fails half-way leaves nothing behind: the dependency and the
// wait written before the failure are rolled back with it, and the caller
// still holds the checkout.
func TestTurnCommandRepo_FailureMidwayRollsBackEverything_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	_, err := f.db.Exec(`UPDATE tasks SET checked_out_by = $2, checkout_token = gen_random_uuid(),
		checkout_expires = now() + interval '1 hour', checkout_acquired_at = now() WHERE id = $1`, f.task, f.agent)
	require.NoError(t, err)

	count := func() (n int) {
		require.NoError(t, f.db.Get(&n, `SELECT
			(SELECT count(*) FROM task_waits WHERE task_id = $1) +
			(SELECT count(*) FROM turn_outcomes WHERE task_id = $1) +
			(SELECT count(*) FROM comments WHERE task_id = $1) +
			(SELECT count(*) FROM task_dependencies WHERE task_id = $1)`, f.task))
		return n
	}
	before := count()

	for _, step := range []string{"dependency", "wait", "outcome", "comment"} {
		t.Run(step, func(t *testing.T) {
			repo := NewTurnCommandRepo(f.db)
			boom := errors.New("injected failure after " + step)
			repo.failAt = func(s string) error {
				if s == step {
					return boom
				}
				return nil
			}
			target := f.other
			_, err = repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent,
				Kind: domain.TurnOutcomeWait, CommentBody: "Waiting on a card.",
				Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target}})
			require.ErrorIs(t, err, boom)
			assert.Equal(t, before, count(), "rows survived a failed command")
			var holder *uuid.UUID
			require.NoError(t, f.db.Get(&holder, `SELECT checked_out_by FROM tasks WHERE id = $1`, f.task))
			require.NotNil(t, holder, "the checkout was released by a failed command")
			assert.Equal(t, f.agent, *holder)
		})
	}

	// Positive control: the same command without the injected failure writes
	// a dependency, a wait, an outcome and a comment, and releases.
	res, err := NewTurnCommandRepo(f.db).CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent,
		Kind: domain.TurnOutcomeWait, CommentBody: "Waiting on a card.",
		Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &f.other}})
	require.NoError(t, err)
	assert.True(t, res.Released)
	assert.Equal(t, before+4, count())
}

func (f *typedOutcomeFixture) holdCheckout(t *testing.T, task, agent uuid.UUID) {
	t.Helper()
	_, err := f.db.Exec(`UPDATE tasks SET checked_out_by = $2, checkout_token = gen_random_uuid(),
		checkout_expires = now() + interval '1 hour', checkout_acquired_at = now() WHERE id = $1`, task, agent)
	require.NoError(t, err)
}

func outcomeCodeOf(t *testing.T, err error) string {
	t.Helper()
	var oe *domain.OutcomeError
	require.ErrorAs(t, err, &oe, "want an OutcomeError, got %v", err)
	return oe.Code
}

func apiStatusOf(err error) int {
	var ae *apierror.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

func TestTurnCommandRepo_CheckoutAndTargets_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	repo := NewTurnCommandRepo(f.db)
	next := func(task, agent uuid.UUID) error {
		_, err := repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: task, AgentID: agent, Kind: domain.TurnOutcomeNext, CommentBody: "Continuing."})
		return err
	}

	assert.Equal(t, 404, apiStatusOf(next(uuid.New(), f.agent)), "a missing card")
	assert.Equal(t, 409, apiStatusOf(next(f.task, f.agent)), "nobody holds the checkout")
	f.holdCheckout(t, f.task, f.agent2)
	assert.Equal(t, 409, apiStatusOf(next(f.task, f.agent)), "another agent holds it")
	_, err := f.db.Exec(`UPDATE tasks SET checkout_expires = now() - interval '1 minute' WHERE id = $1`, f.task)
	require.NoError(t, err)
	assert.Equal(t, 409, apiStatusOf(next(f.task, f.agent2)), "an expired checkout is not held")

	f.holdCheckout(t, f.task, f.agent)
	wait := func(target uuid.UUID, cond *string) error {
		_, werr := repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent, Kind: domain.TurnOutcomeWait,
			CommentBody: "Waiting.", Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target, TargetCondition: cond}})
		return werr
	}
	assert.Equal(t, domain.CodeInvalidTarget, outcomeCodeOf(t, wait(uuid.New(), nil)), "a missing target card")

	// A card of another workspace is not reachable.
	foreign := newTypedOutcomeFixture(t)
	assert.Equal(t, domain.CodeInvalidTarget, outcomeCodeOf(t, wait(foreign.task, nil)))

	// A closed target already meets task_done.
	var projectID uuid.UUID
	require.NoError(t, f.db.Get(&projectID, `SELECT project_id FROM tasks WHERE id = $1`, f.other))
	doneStatus := uuid.New()
	_, err = f.db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position)
		VALUES ($1, $2, 'Done', 'done', 'done', 9)`, doneStatus, projectID)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE tasks SET status_id = $2, completed_at = now() WHERE id = $1`, f.other, doneStatus)
	require.NoError(t, err)
	assert.Equal(t, domain.CodeAlreadyMet, outcomeCodeOf(t, wait(f.other, nil)))

	// A target that already waits on this card would close a cycle.
	var statusID uuid.UUID
	require.NoError(t, f.db.Get(&statusID, `SELECT status_id FROM tasks WHERE id = $1`, f.task))
	third := &domain.Task{ID: uuid.New(), ProjectID: projectID, StatusID: statusID, Title: "third",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
	require.NoError(t, NewTaskRepo(f.db).Create(ctx, third, nil))
	_, err = f.db.Exec(`INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type, created_at)
		VALUES (gen_random_uuid(), $1, $2, 'blocks', now())`, third.ID, f.task)
	require.NoError(t, err)
	assert.Equal(t, domain.CodeInvalidTarget, outcomeCodeOf(t, wait(third.ID, nil)))

	var rows int
	require.NoError(t, f.db.Get(&rows, `SELECT (SELECT count(*) FROM task_waits WHERE task_id = $1) +
		(SELECT count(*) FROM turn_outcomes WHERE task_id = $1) + (SELECT count(*) FROM comments WHERE task_id = $1)`, f.task))
	assert.Zero(t, rows, "refused commands wrote rows")
}

func TestTurnCommandRepo_NextHandoffNote_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	repo := NewTurnCommandRepo(f.db)

	projectID, workspaceID, err := repo.TaskScope(ctx, f.task)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, projectID)
	_, _, err = repo.TaskScope(ctx, uuid.New())
	assert.Equal(t, 404, apiStatusOf(err))

	var slug string
	require.NoError(t, f.db.Get(&slug, `SELECT slug FROM agents WHERE id = $1`, f.agent2))
	for _, to := range []string{f.agent2.String(), slug, "TYPED-B"} {
		id, name, found, rerr := repo.ResolveAgent(ctx, workspaceID, to)
		require.NoError(t, rerr)
		require.True(t, found, to)
		assert.Equal(t, f.agent2, id)
		assert.Equal(t, "typed-b", name)
	}
	for _, to := range []string{"", "nobody"} {
		_, _, found, rerr := repo.ResolveAgent(ctx, workspaceID, to)
		require.NoError(t, rerr)
		assert.False(t, found, to)
	}
	_, err = f.db.Exec(`UPDATE agents SET name = 'typed-a' WHERE id = $1`, f.agent2)
	require.NoError(t, err)
	_, _, found, err := repo.ResolveAgent(ctx, workspaceID, "typed-a")
	require.NoError(t, err)
	assert.False(t, found, "an ambiguous name resolves to nobody")
	_, err = f.db.Exec(`UPDATE agents SET name = 'typed-b' WHERE id = $1`, f.agent2)
	require.NoError(t, err)

	// next extends the checkout and keeps it.
	f.holdCheckout(t, f.task, f.agent)
	_, err = f.db.Exec(`UPDATE tasks SET checkout_expires = now() + interval '1 minute' WHERE id = $1`, f.task)
	require.NoError(t, err)
	res, err := repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent, Kind: domain.TurnOutcomeNext,
		Note: "tests next", CommentBody: "Continuing: tests next"})
	require.NoError(t, err)
	assert.False(t, res.Released)
	require.NotNil(t, res.Outcome)
	var left float64
	require.NoError(t, f.db.Get(&left, `SELECT EXTRACT(EPOCH FROM checkout_expires - now()) FROM tasks WHERE id = $1`, f.task))
	assert.Greater(t, left, (NextCheckoutExtension - time.Minute).Seconds(), "next extends the checkout")

	// hand_off: refused targets write nothing.
	handoff := func(to uuid.UUID) (*domain.TurnCommandResult, error) {
		return repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent, Kind: domain.TurnOutcomeHandoff,
			HandoffTo: &to, Note: "frontend", CommentBody: "Handed over to typed-b.\n\nfrontend"})
	}
	_, err = handoff(uuid.New())
	assert.Equal(t, domain.CodeNotAnAgent, outcomeCodeOf(t, err))
	_, err = handoff(f.agent)
	assert.Equal(t, domain.CodeNotAnAgent, outcomeCodeOf(t, err), "not to oneself")
	_, err = f.db.Exec(`UPDATE tasks SET assigned_by = 'human' WHERE id = $1`, f.task)
	require.NoError(t, err)
	_, err = handoff(f.agent2)
	assert.Equal(t, 409, apiStatusOf(err), "a person pinned the assignee")
	_, err = f.db.Exec(`UPDATE tasks SET assigned_by = 'system' WHERE id = $1`, f.task)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE task_statuses SET category = 'in_progress' WHERE project_id = $1`, projectID)
	require.NoError(t, err)
	_, err = handoff(f.agent2)
	assert.Equal(t, 422, apiStatusOf(err), "no todo status to hand over into")
	_, err = f.db.Exec(`UPDATE task_statuses SET category = 'todo' WHERE project_id = $1`, projectID)
	require.NoError(t, err)

	// A wait of the caller is cancelled by the hand-off.
	target := f.other
	_, err = repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent, Kind: domain.TurnOutcomeWait,
		CommentBody: "Waiting.", Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target}})
	require.NoError(t, err)
	f.holdCheckout(t, f.task, f.agent)
	res, err = handoff(f.agent2)
	require.NoError(t, err)
	assert.True(t, res.Released)
	require.NotNil(t, res.Task.AssigneeID)
	assert.Equal(t, f.agent2, *res.Task.AssigneeID)
	assert.Equal(t, "open", res.Task.Status)
	var state struct {
		Holder     *uuid.UUID `db:"checked_out_by"`
		AssignedBy *string    `db:"assigned_by"`
		Active     int        `db:"active"`
	}
	require.NoError(t, f.db.Get(&state, `SELECT checked_out_by, assigned_by,
		(SELECT count(*) FROM task_waits WHERE task_id = t.id AND resolved_at IS NULL) AS active FROM tasks t WHERE id = $1`, f.task))
	assert.Nil(t, state.Holder)
	require.NotNil(t, state.AssignedBy)
	assert.Equal(t, "system", *state.AssignedBy)
	assert.Zero(t, state.Active, "hand_off cancels the caller's wait")

	// add_note: a quiet comment, no outcome.
	var outcomesBefore int
	require.NoError(t, f.db.Get(&outcomesBefore, `SELECT count(*) FROM turn_outcomes WHERE task_id = $1`, f.task))
	id, err := repo.AddNote(ctx, f.task, f.agent, domain.ActorTypeAgent, "a quiet note")
	require.NoError(t, err)
	var meta []byte
	require.NoError(t, f.db.Get(&meta, `SELECT metadata FROM comments WHERE id = $1`, id))
	assert.False(t, domain.CommentWakes(meta))
	var outcomesAfter int
	require.NoError(t, f.db.Get(&outcomesAfter, `SELECT count(*) FROM turn_outcomes WHERE task_id = $1`, f.task))
	assert.Equal(t, outcomesBefore, outcomesAfter)
	_, err = repo.AddNote(ctx, uuid.New(), f.agent, domain.ActorTypeAgent, "x")
	assert.Equal(t, 404, apiStatusOf(err))
}
