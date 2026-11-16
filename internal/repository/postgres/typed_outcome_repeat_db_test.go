package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The 12.11 cases of an internal ticket at the repository level: an answered question
// asked again, a wait for a named result of another card, and an already_met
// refusal that says who closed the target and when.

func (f *typedOutcomeFixture) answer(t *testing.T, req, by uuid.UUID) *domain.AnswerDecisionResult {
	t.Helper()
	res, err := NewTaskRepo(f.db).AnswerDecision(context.Background(), domain.AnswerDecisionWrite{RequestID: req,
		ActorID: by, ActorType: domain.ActorTypeAgent,
		CommentBody: func(*domain.DecisionRequest) string { return "v2 wins" }})
	require.NoError(t, err)
	return res
}

func TestAskAgent_AnsweredRepeatRefused_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	r := NewTaskRepo(f.db)
	f.checkOut(t, f.agent)
	asked, err := r.AskAgent(ctx, f.ask(f.agent2.String()))
	require.NoError(t, err)
	answered := f.answer(t, asked.Request.ID, f.agent2)

	f.checkOut(t, f.agent)
	before := f.commandRows(t)
	var openBefore int
	require.NoError(t, f.db.Get(&openBefore, `SELECT count(*) FROM decision_requests WHERE task_id = $1 AND state = 'open'`, f.task))

	// Same card, same addressee, the same question in other case and spacing.
	again := f.ask(f.agent2.String())
	again.Question = "  which   schema\tVERSION wins? "
	_, err = r.AskAgent(ctx, again)
	var oe *domain.OutcomeError
	require.ErrorAs(t, err, &oe, "an answered repeat must be refused, got %v", err)
	assert.Equal(t, "already_answered", oe.Code)
	assert.Contains(t, oe.Message, asked.Request.ID.String(), "names the answered request")
	assert.Contains(t, oe.Message, answered.CommentID.String(), "names the answer comment")
	assert.Contains(t, oe.Message, answered.Request.AnsweredAt.UTC().Format(time.RFC3339), "names when it was answered")
	assert.Contains(t, oe.Message, "a new question must say what changed")

	assert.Equal(t, before, f.commandRows(t), "a refused repeat writes no request, wait, outcome or comment")
	var openAfter int
	require.NoError(t, f.db.Get(&openAfter, `SELECT count(*) FROM decision_requests WHERE task_id = $1 AND state = 'open'`, f.task))
	assert.Equal(t, openBefore, openAfter, "the answered request is not reopened")
	var holder *uuid.UUID
	require.NoError(t, f.db.Get(&holder, `SELECT checked_out_by FROM tasks WHERE id = $1`, f.task))
	require.NotNil(t, holder, "the caller keeps its checkout")
	assert.Equal(t, f.agent, *holder)

	// Positive control: a different question to the same addressee is filed.
	other := f.ask(f.agent2.String())
	other.Question = "Which schema version wins now that v3 is merged?"
	res, err := r.AskAgent(ctx, other)
	require.NoError(t, err)
	assert.False(t, res.Reused)
	assert.Equal(t, before.Requests+1, f.commandRows(t).Requests)
}

// An answered question with an identical request still open (filed before the
// refusal existed) is refused too: the open request must not switch the
// refusal off, and a spacing variant must not file yet another request.
func TestAskAgent_AnsweredRepeatRefusedWithOpenIdentical_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	r := NewTaskRepo(f.db)
	f.checkOut(t, f.agent)
	asked, err := r.AskAgent(ctx, f.ask(f.agent2.String()))
	require.NoError(t, err)
	answered := f.answer(t, asked.Request.ID, f.agent2)
	var leftover uuid.UUID
	require.NoError(t, f.db.Get(&leftover, `INSERT INTO decision_requests (task_id, from_agent_id, to_agent_id, question)
		VALUES ($1, $2, $3, 'Which schema version wins?') RETURNING id`, f.task, f.agent, f.agent2))

	f.checkOut(t, f.agent)
	before := f.commandRows(t)
	again := f.ask(f.agent2.String())
	again.Question = "  which   schema\tVERSION wins? "
	_, err = r.AskAgent(ctx, again)
	var oe *domain.OutcomeError
	require.ErrorAs(t, err, &oe, "answered + open identical must still be refused, got %v", err)
	assert.Equal(t, "already_answered", oe.Code)
	assert.Contains(t, oe.Message, answered.CommentID.String())
	assert.Equal(t, before, f.commandRows(t), "a refused repeat writes no request, wait, outcome or comment")
}

// Without an answer, a spacing variant of an open question reuses that open
// request instead of filing a second one (positive control of the reuse key).
func TestAskAgent_OpenSpacingVariantReused_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	r := NewTaskRepo(f.db)
	f.checkOut(t, f.agent)
	asked, err := r.AskAgent(ctx, f.ask(f.agent2.String()))
	require.NoError(t, err)

	f.checkOut(t, f.agent)
	again := f.ask(f.agent2.String())
	again.Question = "  which   schema\tVERSION wins? "
	res, err := r.AskAgent(ctx, again)
	require.NoError(t, err)
	assert.True(t, res.Reused, "the open request is reused")
	assert.Equal(t, asked.Request.ID, res.Request.ID)
	var open int
	require.NoError(t, f.db.Get(&open, `SELECT count(*) FROM decision_requests WHERE task_id = $1 AND state = 'open'`, f.task))
	assert.Equal(t, 1, open, "no second open request")
}

// artifactWait declares the caller's artifact_published wait on f.other with
// criterion through the command and returns the stored edge.
func (f *typedOutcomeFixture) artifactWait(t *testing.T, criterion string) (*domain.TaskWait, *domain.TaskDependency) {
	t.Helper()
	ctx := context.Background()
	f.holdCheckout(t, f.task, f.agent)
	cond := string(domain.DependencyConditionArtifactPublished)
	target := f.other
	res, err := NewTurnCommandRepo(f.db).CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent,
		Kind: domain.TurnOutcomeWait, CommentBody: "Waiting on the schema export.",
		Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target, TargetCondition: &cond, Criterion: &criterion}})
	require.NoError(t, err)
	deps, err := NewTaskDependencyRepo(f.db).ListByTask(ctx, f.task)
	require.NoError(t, err)
	for i := range deps {
		if deps[i].DependsOnTaskID == f.other {
			return res.Wait, &deps[i]
		}
	}
	t.Fatalf("no edge to the target after the wait")
	return nil, nil
}

func (f *typedOutcomeFixture) edge(t *testing.T, id uuid.UUID) *domain.TaskDependency {
	t.Helper()
	dep, err := NewTaskDependencyRepo(f.db).GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, dep)
	return dep
}

func (f *typedOutcomeFixture) closeTarget(t *testing.T, by uuid.UUID, at time.Time) {
	t.Helper()
	var projectID, workspaceID uuid.UUID
	require.NoError(t, f.db.QueryRowx(`SELECT t.project_id, p.workspace_id FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, f.other).Scan(&projectID, &workspaceID))
	done := uuid.New()
	_, err := f.db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position)
		VALUES ($1, $2, 'Shipped and closed', 'shipped-closed', 'done', 9)`, done, projectID)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE tasks SET status_id = $2, completed_at = $3, status_changed_at = $3 WHERE id = $1`, f.other, done, at)
	require.NoError(t, err)
	changes, err := json.Marshal(map[string]any{"status": map[string]any{"old": "Open", "new": "Shipped and closed"}})
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO activity_log (id, workspace_id, entity_type, entity_id, action, actor_id, actor_type, changes, created_at)
		VALUES ($1, $2, 'task', $3, 'task.moved', $4, 'agent', $5, $6)`, uuid.New(), workspaceID, f.other, by, changes, at)
	require.NoError(t, err)
}

// resolveCardWait is what the runner does when it sees the edge released:
// the resolve route with resolution met.
func (f *typedOutcomeFixture) resolveCardWait(t *testing.T, w *domain.TaskWait) *domain.TaskWait {
	t.Helper()
	got, ok, err := f.waits.ResolveWithCause(context.Background(), f.task, w.ID, domain.WaitResolutionMet, "target_condition_met", f.agent)
	require.NoError(t, err)
	require.True(t, ok)
	return got
}

func TestCardWait_ArtifactPublishedReleasedByEvidence_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	w, dep := f.artifactWait(t, "schema v2 export at /exports/v2.json")
	assert.Equal(t, "schema v2 export at /exports/v2.json", dep.Criterion, "the edge stores the criterion")
	require.NotNil(t, w.Criterion)
	assert.Equal(t, "schema v2 export at /exports/v2.json", *w.Criterion, "the wait keeps the criterion")
	assert.False(t, dep.ConditionSatisfied)
	assert.Empty(t, dep.ReleasedBy)

	// The producer attests the named result through the evidence path.
	ev := domain.VerifyDependencyEvidence(*dep, domain.EvidenceClaim{URL: "https://example.test/exports/v2.json",
		Criterion: dep.Criterion, Attest: true}, nil, true)
	require.Equal(t, domain.EvidenceTrusted, ev.Status, ev.Reason)
	now := time.Now().UTC()
	ev.MetAt, ev.ActorID = &now, &f.agent2
	_, err := NewTaskDependencyRepo(f.db).UpdateCondition(ctx, dep.ID, domain.DependencyConditionUpdate{
		Condition: dep.Condition, Criterion: dep.Criterion, Evidence: &ev, ExpectedGeneration: &dep.Generation})
	require.NoError(t, err)

	got := f.edge(t, dep.ID)
	assert.True(t, got.ConditionSatisfied)
	assert.Equal(t, domain.ReleasedByEvidence, got.ReleasedBy)
	met := f.resolveCardWait(t, w)
	require.NotNil(t, met.ReleasedBy)
	assert.Equal(t, domain.ReleasedByEvidence, *met.ReleasedBy)
	require.NotNil(t, met.TargetStatus)
	assert.Equal(t, "Open", *met.TargetStatus, "released by evidence while the target is still open")
}

func TestCardWait_ArtifactPublishedReleasedByClosureWithoutEvidence_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	w, dep := f.artifactWait(t, "schema v2 export at /exports/v2.json")

	// The target is closed and nobody recorded the named result: the edge is
	// still released (nobody waits forever), and says so.
	closedAt := time.Now().UTC().Add(-time.Minute)
	f.closeTarget(t, f.agent2, closedAt)
	got := f.edge(t, dep.ID)
	assert.True(t, got.ConditionSatisfied)
	assert.Equal(t, domain.ReleasedByClosureWithoutEvidence, got.ReleasedBy)
	met := f.resolveCardWait(t, w)
	require.NotNil(t, met.ReleasedBy)
	assert.Equal(t, domain.ReleasedByClosureWithoutEvidence, *met.ReleasedBy)
	// The receipt carries the closure: what the target became, who and when.
	closer, _ := f.agentName(t, f.agent2)
	require.NotNil(t, met.TargetStatus)
	assert.Equal(t, "Shipped and closed", *met.TargetStatus)
	require.NotNil(t, met.TargetClosedBy)
	assert.Equal(t, closer, *met.TargetClosedBy)
	require.NotNil(t, met.TargetClosedAt)
	assert.WithinDuration(t, closedAt, *met.TargetClosedAt, time.Second)
	receipt, _, err := f.waits.GetForTask(context.Background(), f.task, w.ID)
	require.NoError(t, err)
	assert.Equal(t, met.ReleasedBy, receipt.ReleasedBy, "GET wait?wait_id returns the same receipt")
	assert.Equal(t, met.TargetClosedBy, receipt.TargetClosedBy)
	require.NotNil(t, receipt.Criterion)
	assert.Equal(t, dep.Criterion, *receipt.Criterion)

	// Control: a task_done edge released by the same closure is target_state.
	var plain domain.TaskDependency
	plain.DependencyType, plain.Condition = domain.DependencyTypeBlocks, domain.DependencyConditionTaskDone
	assert.Equal(t, domain.ReleasedByTargetState, plain.ReleasedByOf(domain.BlockerState{Category: domain.StatusCategoryDone}))
}

func TestCardWait_CriterionNeedsArtifactPublished_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	f.holdCheckout(t, f.task, f.agent)
	target, crit := f.other, "schema export"
	cond := string(domain.DependencyConditionTaskDone)
	w := &domain.TaskWait{TaskID: f.task, AgentID: f.agent, Kind: domain.TaskWaitCard, TargetTaskID: &target,
		TargetCondition: &cond, Criterion: &crit}
	err := w.Validate(time.Now())
	var oe *domain.OutcomeError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, domain.CodeInvalidTarget, oe.Code)
	assert.Contains(t, oe.Message, "artifact_published")
}

func TestCardWait_AlreadyMetNamesTheClosure_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	closedAt := time.Date(2026, 11, 12, 21, 4, 0, 0, time.UTC)
	f.closeTarget(t, f.agent2, closedAt)
	closer, _ := f.agentName(t, f.agent2)
	f.holdCheckout(t, f.task, f.agent)

	wait := func(cond string) *domain.OutcomeError {
		target := f.other
		_, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), &domain.TurnCommand{TaskID: f.task, AgentID: f.agent,
			Kind: domain.TurnOutcomeWait, CommentBody: "Waiting.",
			Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target, TargetCondition: &cond}})
		var oe *domain.OutcomeError
		require.ErrorAs(t, err, &oe, "want already_met, got %v", err)
		return oe
	}

	oe := wait(string(domain.DependencyConditionTaskDone))
	assert.Equal(t, domain.CodeAlreadyMet, oe.Code)
	assert.Contains(t, oe.Message, "Shipped and closed", "names the final status")
	assert.Contains(t, oe.Message, "(done)", "names its category")
	assert.Contains(t, oe.Message, closer, "names who closed it")
	assert.Contains(t, oe.Message, "2026-11-12T21:04:00Z", "names when")
	assert.Contains(t, oe.Message, "Read its closing comment")

	oe = wait(string(domain.DependencyConditionArtifactPublished))
	assert.Equal(t, domain.CodeAlreadyMet, oe.Code)
	assert.Contains(t, oe.Message, "without evidence", "says the named result was never recorded")
	assert.Contains(t, oe.Message, "ask_agent")
}

// The positive branch of the already_met note: the named result was attested
// on this card's edge before the target closed, so the refusal says it is
// recorded, not that the target closed without it.
//
// The evidence on the edge answers only the result it was recorded for: the
// same condition and the same criterion, or no criterion (the edge keeps the
// one it names). A different criterion or condition is a result nobody has
// posted, so already_met must say the card closed without it.
func TestCardWait_AlreadyMetNamesRecordedEvidence_DB(t *testing.T) {
	const recorded = "schema v2 export at /exports/v2.json"
	cases := []struct {
		name      string
		cond      domain.DependencyCondition
		criterion *string
		evidence  bool
	}{
		{"same criterion", domain.DependencyConditionArtifactPublished, strPtr(recorded), true},
		{"no criterion keeps the recorded one", domain.DependencyConditionArtifactPublished, nil, true},
		{"other criterion", domain.DependencyConditionArtifactPublished, strPtr("schema v3 export at /exports/v3.json"), false},
		{"other condition", domain.DependencyConditionContractReady, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTypedOutcomeFixture(t)
			ctx := context.Background()
			_, dep := f.artifactWait(t, recorded)
			ev := domain.VerifyDependencyEvidence(*dep, domain.EvidenceClaim{URL: "https://example.test/exports/v2.json",
				Criterion: dep.Criterion, Attest: true}, nil, true)
			require.Equal(t, domain.EvidenceTrusted, ev.Status, ev.Reason)
			now := time.Now().UTC()
			ev.MetAt, ev.ActorID = &now, &f.agent2
			_, err := NewTaskDependencyRepo(f.db).UpdateCondition(ctx, dep.ID, domain.DependencyConditionUpdate{
				Condition: dep.Condition, Criterion: dep.Criterion, Evidence: &ev, ExpectedGeneration: &dep.Generation})
			require.NoError(t, err)
			closedAt := time.Date(2026, 11, 12, 21, 4, 0, 0, time.UTC)
			f.closeTarget(t, f.agent2, closedAt)
			closer, _ := f.agentName(t, f.agent2)

			f.holdCheckout(t, f.task, f.agent)
			target, cond := f.other, string(tc.cond)
			_, err = NewTurnCommandRepo(f.db).CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent,
				Kind: domain.TurnOutcomeWait, CommentBody: "Waiting on the schema export.",
				Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target, TargetCondition: &cond, Criterion: tc.criterion}})
			var oe *domain.OutcomeError
			require.ErrorAs(t, err, &oe, "want already_met, got %v", err)
			assert.Equal(t, domain.CodeAlreadyMet, oe.Code)
			if tc.evidence {
				assert.Contains(t, oe.Message, "the named result is recorded as evidence on this card's edge")
				assert.NotContains(t, oe.Message, "without evidence")
			} else {
				assert.Contains(t, oe.Message, "it closed without evidence of the named result")
				assert.NotContains(t, oe.Message, "recorded as evidence")
			}
			assert.Contains(t, oe.Message, "Shipped and closed")
			assert.Contains(t, oe.Message, closer)
			assert.Contains(t, oe.Message, "2026-11-12T21:04:00Z")
			assert.Equal(t, domain.ReleasedByEvidence, f.edge(t, dep.ID).ReleasedBy,
				"the refused wait leaves the edge as it was: released by its own evidence")
		})
	}
}
