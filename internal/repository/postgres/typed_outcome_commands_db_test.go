package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The command transactions at the repository level: what ask_agent,
// answer_decision and finish_task write, and that a refusal writes nothing.

type commandRowCounts struct {
	Requests, Waits, Outcomes, Comments int
}

func (f *typedOutcomeFixture) commandRows(t *testing.T) commandRowCounts {
	t.Helper()
	var c commandRowCounts
	require.NoError(t, f.db.QueryRowx(`SELECT
		(SELECT count(*) FROM decision_requests WHERE task_id = $1),
		(SELECT count(*) FROM task_waits WHERE task_id = $1),
		(SELECT count(*) FROM turn_outcomes WHERE task_id = $1),
		(SELECT count(*) FROM comments WHERE task_id = $1)`, f.task).
		Scan(&c.Requests, &c.Waits, &c.Outcomes, &c.Comments))
	return c
}

func (f *typedOutcomeFixture) checkOut(t *testing.T, agent uuid.UUID) {
	t.Helper()
	_, err := NewTaskRepo(f.db).AcquireCheckout(context.Background(), f.task, agent, uuid.New(),
		time.Now().Add(time.Hour), generationScope())
	require.NoError(t, err)
}

func (f *typedOutcomeFixture) agentName(t *testing.T, id uuid.UUID) (name, slug string) {
	t.Helper()
	require.NoError(t, f.db.QueryRowx(`SELECT name, slug FROM agents WHERE id = $1`, id).Scan(&name, &slug))
	return name, slug
}

func (f *typedOutcomeFixture) ask(to string) domain.AskAgentWrite {
	return domain.AskAgentWrite{TaskID: f.task, Caller: f.agent, To: to, Question: "Which schema version wins?",
		CommentBody: func(req *domain.DecisionRequest, to *domain.AgentRef, reused bool) string {
			return "asked " + to.Name
		}}
}

func TestAskAgent_RefusalsWriteNothing_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	r := NewTaskRepo(f.db)
	before := f.commandRows(t)

	// A second agent with the same display name makes the name ambiguous.
	name, _ := f.agentName(t, f.agent2)
	var wsID uuid.UUID
	require.NoError(t, f.db.Get(&wsID, `SELECT workspace_id FROM agents WHERE id = $1`, f.agent2))
	s := uuid.New().String()[:8]
	require.NoError(t, NewAgentRepo(f.db).Create(ctx, &domain.Agent{
		ID: uuid.New(), WorkspaceID: wsID, Name: name, Slug: "twin-" + s,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + s, APIKeyPrefix: s, APIKeySHA256: "typed-" + uuid.New().String(),
	}))

	for _, to := range []string{"", "@", "alex", name, f.agent.String()} {
		_, err := r.AskAgent(ctx, f.ask(to))
		var oe *domain.OutcomeError
		require.ErrorAs(t, err, &oe, "to=%q", to)
		assert.Equal(t, domain.CodeNotAnAgent, oe.Code, "to=%q", to)
	}

	// Another agent's live checkout refuses the command before anything is resolved.
	f.checkOut(t, f.agent2)
	_, err := r.AskAgent(ctx, f.ask(f.agent2.String()))
	var api *apierror.Error
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 409, api.Code)

	_, err = r.AskAgent(ctx, domain.AskAgentWrite{TaskID: uuid.New(), Caller: f.agent, To: "x", Question: "q"})
	require.Error(t, err)

	assert.Equal(t, before, f.commandRows(t), "a refused ask writes nothing")
}

func TestAskAgent_WritesAndReleases_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	r := NewTaskRepo(f.db)
	f.checkOut(t, f.agent)
	_, slug := f.agentName(t, f.agent2)

	first, err := r.AskAgent(ctx, f.ask("@"+slug))
	require.NoError(t, err)
	assert.False(t, first.Reused)
	assert.True(t, first.Released)
	assert.Equal(t, f.agent2, first.To.ID)
	assert.Equal(t, domain.DecisionOpen, first.Request.State)
	assert.Equal(t, domain.TaskWaitDecision, first.Wait.Kind)
	assert.Equal(t, first.Wait.ID, *first.Outcome.WaitID)

	task, err := r.GetByID(ctx, f.task)
	require.NoError(t, err)
	assert.Nil(t, task.CheckedOutBy, "the asker's lease is released")
	var released int
	require.NoError(t, f.db.Get(&released, `SELECT count(*) FROM activity_log
		WHERE entity_id = $1 AND action = 'task.checkout_released'`, f.task))
	assert.Equal(t, 1, released)
	var meta string
	require.NoError(t, f.db.Get(&meta, `SELECT metadata::text FROM comments WHERE id = $1`, first.CommentID))
	assert.Contains(t, meta, `"wakes": false`)

	// Same question, different case and spacing: the open request is reused,
	// the earlier wait superseded, nothing released (no lease any more).
	again := f.ask(f.agent2.String())
	again.Question = "  which schema VERSION wins?"
	second, err := r.AskAgent(ctx, again)
	require.NoError(t, err)
	assert.True(t, second.Reused)
	assert.False(t, second.Released)
	assert.Equal(t, first.Request.ID, second.Request.ID)
	assert.Equal(t, commandRowCounts{Requests: 1, Waits: 2, Outcomes: 2, Comments: 2}, f.commandRows(t))
	active, err := f.waits.GetActive(ctx, f.task, &f.agent)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, second.Wait.ID, active.ID)
}

func TestAnswerDecision_AddresseeAnswersAndMeetsWait_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	r := NewTaskRepo(f.db)
	asked, err := r.AskAgent(ctx, f.ask(f.agent2.String()))
	require.NoError(t, err)
	answer := func(actor uuid.UUID, kind domain.ActorType) domain.AnswerDecisionWrite {
		return domain.AnswerDecisionWrite{RequestID: asked.Request.ID, ActorID: actor, ActorType: kind,
			CommentBody: func(req *domain.DecisionRequest) string { return "v2 wins" }}
	}
	before := f.commandRows(t)

	var api *apierror.Error
	_, err = r.AnswerDecision(ctx, answer(f.agent, domain.ActorTypeAgent))
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 403, api.Code, "the asker cannot answer its own question")
	_, err = r.AnswerDecision(ctx, answer(f.agent2, domain.ActorType("system")))
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 403, api.Code)
	_, err = r.AnswerDecision(ctx, answer(uuid.New(), domain.ActorTypeUser))
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 403, api.Code, "a person is not the addressee of an agent's question")
	missing := answer(f.agent2, domain.ActorTypeAgent)
	missing.RequestID = uuid.New()
	_, err = r.AnswerDecision(ctx, missing)
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 404, api.Code)
	assert.Equal(t, before, f.commandRows(t), "a refused answer writes nothing")

	res, err := r.AnswerDecision(ctx, answer(f.agent2, domain.ActorTypeAgent))
	require.NoError(t, err)
	assert.Equal(t, domain.DecisionAnswered, res.Request.State)
	require.NotNil(t, res.Request.AnsweredAt)
	assert.Equal(t, res.CommentID, *res.Request.AnswerCommentID)
	assert.Equal(t, []uuid.UUID{asked.Wait.ID}, res.MetWaits)
	active, err := f.waits.GetActive(ctx, f.task, &f.agent)
	require.NoError(t, err)
	assert.Nil(t, active, "the asker's wait is met")

	_, err = r.AnswerDecision(ctx, answer(f.agent2, domain.ActorTypeAgent))
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 409, api.Code, "an answered question is not answered twice")
}

func TestUpdateTransition_FinishWritesInTheMoveTransaction_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	r := NewTaskRepo(f.db)
	task, err := r.GetByID(ctx, f.task)
	require.NoError(t, err)
	done := &domain.TaskStatus{ID: uuid.New(), ProjectID: task.ProjectID, Name: "Done", Slug: "done",
		Color: "#0000FF", Category: domain.StatusCategoryDone, Position: 5}
	require.NoError(t, NewTaskStatusRepo(f.db).Create(ctx, done))
	_, err = f.db.Exec(`UPDATE tasks SET labels = ARRAY['kind:research','backend'] WHERE id = $1`, f.task)
	require.NoError(t, err)
	require.NoError(t, f.waits.Declare(ctx, f.cardWait(f.agent)))
	f.checkOut(t, f.agent)

	finish := func(caller uuid.UUID) *domain.TaskFinish {
		k := domain.ResultKindChange
		return &domain.TaskFinish{ResultKind: k, Caller: caller, Labels: []string{"kind:change", "backend"},
			Comment: &domain.Comment{AuthorID: caller, AuthorType: domain.ActorTypeAgent, Body: "Result: done"},
			Outcome: &domain.TurnOutcome{AgentID: caller, Kind: domain.TurnOutcomeDone, ResultKind: &k, Note: "go test ok"}}
	}
	move := func(caller uuid.UUID) (*domain.Task, domain.TaskTransition) {
		cur, gerr := r.GetByID(ctx, f.task)
		require.NoError(t, gerr)
		write := *cur
		now := time.Now().UTC()
		write.StatusID, write.CompletedAt, write.StatusChangedAt, write.UpdatedAt = done.ID, &now, &now, now
		// No Audit: Finish alone must open the transaction its writes share.
		return &write, domain.TaskTransition{ExpectedVersion: cur.Version, ReleaseCheckout: true, Finish: finish(caller)}
	}
	before := f.commandRows(t)

	alarm := time.Now().Add(time.Hour)
	write, input := move(f.agent)
	input.AlarmDue = &alarm
	require.Error(t, r.UpdateTransition(ctx, write, input), "a finish never arms a park alarm")

	// Another agent cannot finish a card this agent holds.
	write, input = move(f.agent2)
	var conflict *domain.TaskConflict
	require.ErrorAs(t, r.UpdateTransition(ctx, write, input), &conflict)
	assert.Equal(t, before, f.commandRows(t), "a refused finish writes nothing")

	write, input = move(f.agent)
	require.NoError(t, r.UpdateTransition(ctx, write, input))
	assert.Equal(t, []string{"kind:change", "backend"}, []string(write.Labels))

	var row struct {
		Labels     string     `db:"labels"`
		ResultKind *string    `db:"result_kind"`
		StatusID   uuid.UUID  `db:"status_id"`
		Holder     *uuid.UUID `db:"checked_out_by"`
	}
	require.NoError(t, f.db.Get(&row, `SELECT array_to_string(labels, ',') AS labels, result_kind, status_id, checked_out_by
		FROM tasks WHERE id = $1`, f.task))
	assert.Equal(t, "kind:change,backend", row.Labels)
	require.NotNil(t, row.ResultKind)
	assert.Equal(t, "change", *row.ResultKind)
	assert.Equal(t, done.ID, row.StatusID)
	assert.Nil(t, row.Holder)
	assert.Equal(t, commandRowCounts{Waits: 1, Outcomes: 1, Comments: 1}, f.commandRows(t))
	active, err := f.waits.GetActive(ctx, f.task, &f.agent)
	require.NoError(t, err)
	assert.Nil(t, active, "the finisher's wait is cancelled")
}
