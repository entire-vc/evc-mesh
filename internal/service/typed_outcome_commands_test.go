package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// finish_task refusals are decided before anything is read for writing: the
// card is byte-for-byte what it was and the error names what to add.
func TestFinishTask_RefusalsChangeNothing(t *testing.T) {
	svc, taskRepo, _ := setupTaskService()
	agent := uuid.New()
	id := uuid.New()
	taskRepo.items[id] = &domain.Task{ID: id, ProjectID: uuid.New(), StatusID: uuid.New(), Title: "T",
		Labels: []string{"kind:research"}, Version: 7, CheckedOutBy: &agent}
	before := *taskRepo.items[id]
	ctx := agentContext(agent)

	cases := []struct {
		name string
		ctx  context.Context
		in   FinishTaskInput
		code string
	}{
		{"no result_kind", ctx, FinishTaskInput{Evidence: "go test ok", To: "done"}, domain.CodeMissingResultKind},
		{"unknown result_kind", ctx, FinishTaskInput{ResultKind: "feature", Evidence: "go test ok", To: "done"}, domain.CodeMissingResultKind},
		{"no evidence", ctx, FinishTaskInput{ResultKind: "change", To: "done"}, domain.CodeMissingEvidence},
		{"blank evidence", ctx, FinishTaskInput{ResultKind: "change", Evidence: " \n\t", To: "review"}, domain.CodeMissingEvidence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.FinishTask(tc.ctx, id, tc.in)
			var oe *domain.OutcomeError
			require.ErrorAs(t, err, &oe)
			assert.Equal(t, tc.code, oe.Code)
			assert.Equal(t, before, *taskRepo.items[id])
		})
	}

	_, err := svc.FinishTask(ctx, id, FinishTaskInput{ResultKind: "change", Evidence: "ok", To: "closed"})
	var api *apierror.Error
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 400, api.Code)

	_, err = svc.FinishTask(actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser), id,
		FinishTaskInput{ResultKind: "change", Evidence: "ok", To: "done"})
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 403, api.Code, "a person closes with move_task, not with an agent's turn command")
	assert.Equal(t, before, *taskRepo.items[id])
}

// metadata.wakes=false: the note is stored, but the @-handle in it gets no
// delivery verdict, no mention notification and no assignee wake. The same
// comment without the flag is delivered as before (positive control).
func TestCommentCreate_WakesFalseDeliversNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata json.RawMessage
		wakes    bool
	}{
		{"wakes unset", nil, true},
		{"wakes true", json.RawMessage(`{"wakes":true}`), true},
		{"wakes false", json.RawMessage(`{"wakes":false}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupCommentServiceWithMentions()
			deliveries := &fakeDeliveryRepo{}
			env.svc.deliveryRepo = deliveries
			leo := &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "leo", Name: "Leo"}
			env.agentSvc.AddAgent(env.wsID, leo)
			assignee := uuid.New()
			taskID := uuid.New()
			env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T",
				AssigneeType: domain.AssigneeTypeAgent, AssigneeID: &assignee}

			c := &domain.Comment{TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
				Body: "@leo FYI: the pipeline is green", Metadata: tc.metadata}
			require.NoError(t, env.svc.Create(context.Background(), c))
			stored, err := env.commentRepo.GetByID(context.Background(), c.ID)
			require.NoError(t, err)
			require.NotNil(t, stored, "the note itself is always written")

			env.notifySvc.mu.Lock()
			calls := len(env.notifySvc.calls)
			env.notifySvc.mu.Unlock()
			if tc.wakes {
				assert.NotEmpty(t, deliveries.snapshot(), "a waking mention is delivered as before")
				assert.NotZero(t, calls)
			} else {
				assert.Empty(t, deliveries.snapshot(), "wakes=false: no mention delivery")
				assert.Zero(t, calls, "wakes=false: nobody is notified or woken")
			}
		})
	}
}

// Input checks of the three commands answer before any write; a repository
// without the command transactions answers 503 rather than half-doing it.
func TestTypedOutcomeCommands_InputRefusals(t *testing.T) {
	svc, taskRepo, _ := setupTaskService()
	agent := uuid.New()
	id := uuid.New()
	taskRepo.items[id] = &domain.Task{ID: id, ProjectID: uuid.New(), StatusID: uuid.New(), Title: "T"}
	ctx := agentContext(agent)
	person := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	long := strings.Repeat("s", domain.MaxSessionIDLen+1)
	links := make([]string, domain.MaxEvidenceLinks+1)
	for i := range links {
		links[i] = "https://x.test/" + strconv.Itoa(i)
	}

	status := func(err error) int {
		var api *apierror.Error
		require.ErrorAs(t, err, &api)
		return api.Code
	}
	_, err := svc.AskAgent(person, id, AskAgentInput{To: "a", Question: "q"})
	assert.Equal(t, 403, status(err))
	_, err = svc.AskAgent(ctx, id, AskAgentInput{To: "a", Question: "  "})
	assert.Equal(t, 400, status(err))
	_, err = svc.AskAgent(ctx, id, AskAgentInput{To: "a", Question: strings.Repeat("q", domain.MaxQuestionLen+1)})
	assert.Equal(t, 400, status(err))
	_, err = svc.AskAgent(ctx, id, AskAgentInput{CommandSession: CommandSession{TurnID: &long}, To: "a", Question: "q"})
	assert.Equal(t, 400, status(err))
	_, err = svc.AskAgent(ctx, id, AskAgentInput{To: "a", Question: "q"})
	assert.Equal(t, 503, status(err))

	_, err = svc.AnswerDecision(context.Background(), uuid.New(), AnswerDecisionInput{Answer: "a"})
	assert.Equal(t, 403, status(err))
	_, err = svc.AnswerDecision(person, uuid.New(), AnswerDecisionInput{Answer: "a"})
	assert.Equal(t, 403, status(err), "only the addressee agent answers; a person is not one")
	_, err = svc.AnswerDecision(ctx, uuid.New(), AnswerDecisionInput{Answer: ""})
	assert.Equal(t, 400, status(err))
	_, err = svc.AnswerDecision(ctx, uuid.New(), AnswerDecisionInput{Answer: "a"})
	assert.Equal(t, 503, status(err))

	ok := FinishTaskInput{ResultKind: "change", Evidence: "go test ok", To: "done"}
	for name, in := range map[string]FinishTaskInput{
		"evidence over 4 KB": {ResultKind: "change", Evidence: strings.Repeat("e", domain.MaxEvidenceLen+1), To: "done"},
		"too many links":     {ResultKind: "change", Evidence: "ok", EvidenceLinks: links, To: "done"},
		"not a URL":          {ResultKind: "change", Evidence: "ok", EvidenceLinks: []string{"ftp://x.test/a"}, To: "done"},
		"session too long":   {CommandSession: CommandSession{SessionID: &long}, ResultKind: "change", Evidence: "ok", To: "done"},
	} {
		_, err = svc.FinishTask(ctx, id, in)
		assert.Equal(t, 400, status(err), name)
	}
	_, err = svc.FinishTask(ctx, uuid.New(), ok)
	assert.Equal(t, 404, status(err), "unknown card")
	_, err = svc.FinishTask(ctx, id, ok)
	assert.Equal(t, 400, status(err), "a project without a done status")
}
