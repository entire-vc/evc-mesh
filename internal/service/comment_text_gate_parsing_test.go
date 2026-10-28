package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// COMMENT_TEXT_GATE_PARSING (typed-outcomes W1b). With the switch off a comment is
// only a comment: a "❓ Blocking" marker arms nothing, and neither a human reply nor a
// negator word ("снят") clears human_gate. set_human_gate / clear_human_gate stay the
// one door. Each case runs the same comment through "on" first: the on run is the red
// control that shows the comment really would have moved the gate.

const textGateMarkerBody = "Отчёт готов.\n\n❓ **Blocking @alex**: мёржим сейчас или ждём?"

func TestTextGateParsing_Marker_ArmsOnlyWhenOn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		wantArms int
	}{
		{"on", true, 1},
		{"off", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTriageEnvWithOptions(t, true, WithCommentTextGateParsing(tc.enabled))
			taskID := env.seedTask(env.inProgressID)

			require.NoError(t, env.svc.Create(context.Background(), &domain.Comment{
				TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
				Body: textGateMarkerBody,
			}))

			assert.Len(t, env.taskMover.humanGateArmCalls(), tc.wantArms)
			if !tc.enabled {
				assert.Empty(t, env.taskMover.calls(), "off: no triage move either")
			}
		})
	}
}

func TestTextGateParsing_EditAddingMarker_ArmsOnlyWhenOn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		wantArms int
	}{
		{"on", true, 1},
		{"off", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTriageEnvWithOptions(t, true, WithCommentTextGateParsing(tc.enabled))
			taskID := env.seedTask(env.inProgressID)
			author := uuid.New()
			ctx := actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent)

			c := &domain.Comment{TaskID: taskID, AuthorID: author, AuthorType: domain.ActorTypeAgent, Body: "Отчёт готов."}
			require.NoError(t, env.svc.Create(ctx, c))
			require.Empty(t, env.taskMover.humanGateArmCalls())

			require.NoError(t, env.svc.Update(ctx, &domain.Comment{ID: c.ID, Body: textGateMarkerBody}))
			assert.Len(t, env.taskMover.humanGateArmCalls(), tc.wantArms)
		})
	}
}

func TestTextGateParsing_NegatorWithdrawal_ClearsOnlyWhenOn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		wantClear int
	}{
		{"on", true, 1},
		{"off", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTriageEnvWithOptions(t, true, WithCommentTextGateParsing(tc.enabled))
			taskID := env.seedGatedTask(env.inProgressID)
			askerID := uuid.New()
			env.seedAgentBlockingComment(taskID, askerID)

			ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
			require.NoError(t, env.svc.Create(ctx, &domain.Comment{
				TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
				Body: "Вопрос снят.",
			}))

			assert.Len(t, env.taskMover.humanGateCalls(), tc.wantClear)
		})
	}
}

func TestTextGateParsing_HumanReply_ClearsOnlyWhenOn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		wantClear int
	}{
		{"on", true, 1},
		{"off", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTriageEnvWithOptions(t, true, WithCommentTextGateParsing(tc.enabled))
			taskID := env.seedGatedTask(env.inProgressID)
			env.seedBlockingComment(taskID)

			pavelID := uuid.New()
			ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
			require.NoError(t, env.svc.Create(ctx, &domain.Comment{
				TaskID: taskID, AuthorID: pavelID, AuthorType: domain.ActorTypeUser,
				Body: "Ок, делайте деплой.",
			}))

			assert.Len(t, env.taskMover.humanGateCalls(), tc.wantClear)
		})
	}
}

// TestTextGateParsing_Off_EndpointDoorStillWorks wires the REAL task service under an
// "off" comment service: a gate armed through set_human_gate survives "снят" from its
// author and a human reply, and clear_human_gate / set_human_gate still move it.
func TestTextGateParsing_Off_EndpointDoorStillWorks(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskSvc := newTestTaskService(env.taskRepo, env.statusRepo,
		NewMockTaskDependencyRepository(), NewMockActivityLogRepository())
	svc := NewCommentService(env.commentRepo, env.taskRepo, NewMockActivityLogRepository(),
		WithCommentProjectRepo(newProjectRepoFor(env.projID, env.wsID)),
		WithCommentStatusRepo(env.statusRepo),
		WithCommentUserRepo(env.userRepo),
		WithCommentTaskService(taskSvc),
		WithCommentTextGateParsing(false),
	)

	taskID := env.seedTask(env.inProgressID)
	askerID := uuid.New()
	arm := func() {
		require.NoError(t, taskSvc.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
			TaskID: taskID, Author: askerID, AuthorType: domain.ActorTypeAgent,
			Reason: "r", RecommendedDefault: "d",
			Class: domain.HumanGateClassSoft, Source: domain.ArmHumanGateSourceAPI,
			Predicate: allowingPredicate(),
		}))
	}
	arm()
	require.True(t, env.taskRepo.items[taskID].HumanGate, "set_human_gate arms with parsing off")

	// The thread also carries a marker by the same agent, so with parsing on the
	// "снят" below would be a valid withdrawal.
	env.seedAgentBlockingComment(taskID, askerID)
	agentCtx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, svc.Create(agentCtx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent, Body: "Вопрос снят.",
	}))
	pavelID := uuid.New()
	userCtx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	require.NoError(t, svc.Create(userCtx, &domain.Comment{
		TaskID: taskID, AuthorID: pavelID, AuthorType: domain.ActorTypeUser, Body: "Ок, делайте.",
	}))
	assert.True(t, env.taskRepo.items[taskID].HumanGate, "no comment clears the gate with parsing off")

	require.NoError(t, taskSvc.ClearHumanGate(context.Background(), taskID))
	assert.False(t, env.taskRepo.items[taskID].HumanGate, "clear_human_gate clears with parsing off")

	require.NoError(t, svc.Create(agentCtx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent, Body: textGateMarkerBody,
	}))
	assert.False(t, env.taskRepo.items[taskID].HumanGate, "a marker does not re-arm with parsing off")

	arm()
	assert.True(t, env.taskRepo.items[taskID].HumanGate, "set_human_gate re-arms with parsing off")
}

func newProjectRepoFor(projID, wsID uuid.UUID) *MockProjectRepository {
	repo := NewMockProjectRepository()
	repo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}
	return repo
}
