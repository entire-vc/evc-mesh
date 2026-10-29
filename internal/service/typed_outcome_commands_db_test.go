package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ask_agent / answer_decision / finish_task over real Postgres: each command
// lands whole or not at all. Skips without a database like the other
// *_db_test.go here; CI runs them against its Postgres service.

type typedCommandFixture struct {
	f        *recurringActorTypeFixture
	repo     *postgres.TaskRepo
	svc      TaskService
	task     uuid.UUID
	owner    uuid.UUID
	asked    uuid.UUID
	askedTag string
	done     uuid.UUID
}

func newTypedCommandFixture(t *testing.T) *typedCommandFixture {
	t.Helper()
	f, repo, svc, task, owner := newCheckoutM1Fixture(t)
	ctx := context.Background()
	statuses := postgres.NewTaskStatusRepo(f.db)
	done := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: "Done", Slug: "done",
		Color: "#00AA00", Category: domain.StatusCategoryDone, Position: 9}
	require.NoError(t, statuses.Create(ctx, done))
	require.NoError(t, statuses.Create(ctx, &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: "Review",
		Slug: "review", Color: "#AAAA00", Category: domain.StatusCategoryReview, Position: 8}))
	asked := uuid.New()
	tag := "asked-" + asked.String()[:8]
	_, err := f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,$3,$3,'fixture-hash','test')`,
		asked, f.workspaceID, tag)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE tasks SET labels=$2 WHERE id=$1`, task, pq.Array([]string{"kind:research", "backend"}))
	require.NoError(t, err)
	return &typedCommandFixture{f: f, repo: repo, svc: svc, task: task, owner: owner, asked: asked, askedTag: tag, done: done.ID}
}

// cardState is everything a command may write, read straight from the tables.
type cardState struct {
	StatusID     uuid.UUID      `db:"status_id"`
	Labels       pq.StringArray `db:"labels"`
	ResultKind   *string        `db:"result_kind"`
	CheckedOutBy *uuid.UUID     `db:"checked_out_by"`
	Comments     int            `db:"comments"`
	Outcomes     int            `db:"outcomes"`
	OpenWaits    int            `db:"open_waits"`
	Requests     int            `db:"requests"`
}

func (x *typedCommandFixture) state(t *testing.T) cardState {
	t.Helper()
	var s cardState
	require.NoError(t, x.f.db.Get(&s, `SELECT status_id, labels, result_kind, checked_out_by,
		(SELECT count(*) FROM comments WHERE task_id=t.id) AS comments,
		(SELECT count(*) FROM turn_outcomes WHERE task_id=t.id) AS outcomes,
		(SELECT count(*) FROM task_waits WHERE task_id=t.id AND resolved_at IS NULL) AS open_waits,
		(SELECT count(*) FROM decision_requests WHERE task_id=t.id) AS requests
		FROM tasks t WHERE id=$1`, x.task))
	return s
}

func (x *typedCommandFixture) checkout(t *testing.T) context.Context {
	t.Helper()
	ctx := agentContext(x.owner)
	_, err := x.svc.CheckoutTask(ctx, x.task, 30, nil, checkoutScope())
	require.NoError(t, err)
	return ctx
}

func (x *typedCommandFixture) cmds() TypedOutcomeCommands { return x.svc.(TypedOutcomeCommands) }

func TestFinishTask_RefusalWritesNothing_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := x.checkout(t)
	before := x.state(t)

	for _, in := range []FinishTaskInput{
		{Evidence: "pipeline 7001 green", To: "done"},
		{ResultKind: "change", To: "done"},
	} {
		_, err := x.cmds().FinishTask(ctx, x.task, in)
		var oe *domain.OutcomeError
		require.ErrorAs(t, err, &oe)
		assert.Equal(t, before, x.state(t), "%s: status, labels, comments, outcomes and the lease are untouched", oe.Code)
	}
}

func TestFinishTask_WritesEverythingInOneTransaction_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := x.checkout(t)
	note := "waiting for the merged contract"
	ref := "gate-1"
	require.NoError(t, postgres.NewTaskWaitRepo(x.f.db).Declare(context.Background(), &domain.TaskWait{
		TaskID: x.task, AgentID: x.owner, Kind: domain.TaskWaitHuman, HumanGateRef: &ref, Note: note}))
	session := "sess-1"

	res, err := x.cmds().FinishTask(ctx, x.task, FinishTaskInput{
		CommandSession: CommandSession{SessionID: &session},
		ResultKind:     "change", Evidence: "go test ./internal/service -run Finish: ok",
		EvidenceLinks: []string{"https://git.example/mr/1"}, To: "done",
	})
	require.NoError(t, err)

	after := x.state(t)
	assert.Equal(t, x.done, after.StatusID)
	assert.Equal(t, pq.StringArray{"kind:change", "backend"}, after.Labels, "kind:research is replaced, not added to")
	require.NotNil(t, after.ResultKind)
	assert.Equal(t, "change", *after.ResultKind)
	assert.Nil(t, after.CheckedOutBy, "the lease is released by the same write")
	assert.Equal(t, 1, after.Comments)
	assert.Equal(t, 1, after.Outcomes)
	assert.Zero(t, after.OpenWaits, "the caller's wait is cancelled")
	assert.True(t, res.Released)
	require.NotNil(t, res.Task)
	assert.Equal(t, "done", res.Task.Status)

	var body string
	require.NoError(t, x.f.db.Get(&body, `SELECT body FROM comments WHERE id=$1`, res.CommentID))
	assert.Contains(t, body, "go test ./internal/service -run Finish: ok")
	assert.Contains(t, body, "https://git.example/mr/1")
	var o domain.TurnOutcome
	require.NoError(t, x.f.db.Get(&o, `SELECT id, task_id, agent_id, session_id, turn_id, kind, wait_id, result_kind, note, created_at
		FROM turn_outcomes WHERE task_id=$1`, x.task))
	assert.Equal(t, domain.TurnOutcomeDone, o.Kind)
	require.NotNil(t, o.ResultKind)
	assert.Equal(t, domain.ResultKindChange, *o.ResultKind)
	assert.Equal(t, session, *o.SessionID)
	var resolution string
	require.NoError(t, x.f.db.Get(&resolution, `SELECT resolution FROM task_waits WHERE task_id=$1`, x.task))
	assert.Equal(t, "cancelled", resolution)
}

// A failure after the task row, its comment and its labels were already
// written inside the transaction — the outcome insert violates its CHECK —
// rolls all of them back: no half-closed card.
func TestFinishTask_FailureMidwayLeavesNoPartialWrite_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	x.checkout(t)
	before := x.state(t)
	task, err := x.repo.GetByID(context.Background(), x.task)
	require.NoError(t, err)

	tooLong := strings.Repeat("s", domain.MaxSessionIDLen+1)
	kind := domain.ResultKindChange
	finish := &domain.TaskFinish{
		ResultKind: kind, Caller: x.owner, Labels: domain.MirrorResultKindLabel(task.Labels, kind),
		Comment: &domain.Comment{AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "closing"},
		Outcome: &domain.TurnOutcome{AgentID: x.owner, SessionID: &tooLong, Kind: domain.TurnOutcomeDone, ResultKind: &kind},
	}
	moved := *task
	moved.StatusID = x.done
	err = x.repo.UpdateTransition(actorctx.WithActor(context.Background(), x.owner, domain.ActorTypeAgent), &moved,
		domain.TaskTransition{ExpectedVersion: task.Version, ReleaseCheckout: true, Finish: finish})
	require.Error(t, err, "the outcome row breaks its CHECK after everything else was written")
	assert.Equal(t, before, x.state(t), "status, labels, result_kind, lease and comment all rolled back")

	// Positive control: the same write with a valid outcome commits.
	finish.Outcome.SessionID = nil
	finish.Comment.ID = uuid.Nil
	require.NoError(t, x.repo.UpdateTransition(actorctx.WithActor(context.Background(), x.owner, domain.ActorTypeAgent), &moved,
		domain.TaskTransition{ExpectedVersion: task.Version, ReleaseCheckout: true, Finish: finish}))
	after := x.state(t)
	assert.Equal(t, x.done, after.StatusID)
	assert.Equal(t, 1, after.Comments)
}

func TestAskAgent_PersonIsNotAnAgentAndWritesNothing_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := x.checkout(t)
	before := x.state(t)
	for _, to := range []string{"alex", "@alex", uuid.NewString(), x.owner.String()} {
		_, err := x.cmds().AskAgent(ctx, x.task, AskAgentInput{To: to, Question: "merge now or after the release?"})
		var oe *domain.OutcomeError
		require.ErrorAs(t, err, &oe, to)
		assert.Equal(t, domain.CodeNotAnAgent, oe.Code, to)
		assert.Equal(t, before, x.state(t), "%s: no request, wait, outcome, comment; lease kept", to)
	}
}

func TestAskAgent_RepeatReusesRequestAndAnswerMeetsWait_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := x.checkout(t)

	first, err := x.cmds().AskAgent(ctx, x.task, AskAgentInput{To: "@" + strings.ToUpper(x.askedTag), Question: "Merge now or after the release?"})
	require.NoError(t, err)
	assert.False(t, *first.Reused)
	assert.True(t, first.Released)
	require.NotNil(t, first.Wait)
	assert.Equal(t, domain.TaskWaitDecision, first.Wait.Kind)
	assert.Equal(t, first.DecisionRequest.ID, *first.Wait.DecisionRequestID)
	assert.Equal(t, domain.TurnOutcomeWait, first.Outcome.Kind)
	assert.Nil(t, x.state(t).CheckedOutBy)

	again, err := x.cmds().AskAgent(ctx, x.task, AskAgentInput{To: x.asked.String(), Question: "  merge now or after the release?  "})
	require.NoError(t, err)
	assert.True(t, *again.Reused)
	assert.Equal(t, first.DecisionRequest.ID, again.DecisionRequest.ID, "the same question to the same agent is one request")
	s := x.state(t)
	assert.Equal(t, 1, s.Requests)
	assert.Equal(t, 1, s.OpenWaits, "the repeat supersedes, never stacks, the wait")

	// Only the addressee answers.
	_, err = x.cmds().AnswerDecision(agentContext(x.owner), first.DecisionRequest.ID, AnswerDecisionInput{Answer: "now"})
	var api *apierror.Error
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 403, api.Code)

	ans, err := x.cmds().AnswerDecision(agentContext(x.asked), first.DecisionRequest.ID, AnswerDecisionInput{Answer: "After the release."})
	require.NoError(t, err)
	assert.Equal(t, domain.DecisionAnswered, ans.Request.State)
	require.NotNil(t, ans.Request.AnsweredAt)
	assert.Equal(t, ans.CommentID, *ans.Request.AnswerCommentID)
	assert.Zero(t, x.state(t).OpenWaits, "the asker's wait is met")
	var resolution string
	require.NoError(t, x.f.db.Get(&resolution, `SELECT resolution FROM task_waits WHERE id=$1`, again.Wait.ID))
	assert.Equal(t, "met", resolution)
	var answers string
	require.NoError(t, x.f.db.Get(&answers, `SELECT metadata->>'answers' FROM comments WHERE id=$1`, ans.CommentID))
	assert.Equal(t, first.DecisionRequest.ID.String(), answers)

	_, err = x.cmds().AnswerDecision(agentContext(x.asked), first.DecisionRequest.ID, AnswerDecisionInput{Answer: "again"})
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 409, api.Code, "an answered request is not answered twice")
}
