package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// an internal ticket: the fleet outcome line «⏳ WAIT decision:@<person>» arms the same
// human_gate as «❓ Blocking @<person>». Only as the last non-empty line, only for a
// user handle, never from a quote.

func postWaitDecision(t *testing.T, body string) triageTestEnv {
	t.Helper()
	env, taskID, assigneeID := tailMarkerEnv(t)
	require.NoError(t, env.svc.Create(context.Background(), &domain.Comment{
		TaskID: taskID, AuthorID: assigneeID, AuthorType: domain.ActorTypeAgent, Body: body,
	}))
	return env
}

func TestWaitDecision_HumanHandle_ArmsGate(t *testing.T) {
	for name, body := range map[string]string{
		"plain":           "Готово, жду выбора.\n\n⏳ WAIT decision:@pavel: вариант A или B?",
		"no question":     "Итог.\n⏳ WAIT decision:@pavel",
		"trailing blanks": "Итог.\n⏳ WAIT decision:@pavel: GO?\n\n",
		"upper-case slug": "Итог.\n⏳ WAIT decision:@Pavel: GO?",
	} {
		t.Run(name, func(t *testing.T) {
			env := postWaitDecision(t, body)
			calls := env.taskMover.humanGateCalls()
			require.Len(t, calls, 1, "a WAIT line naming a human must arm human_gate")
			assert.True(t, calls[0].value)
		})
	}
}

func TestWaitDecision_NegativeControls(t *testing.T) {
	for name, body := range map[string]string{
		"agent handle":    "Итог.\n⏳ WAIT decision:@linus: нужен вердикт",
		"unknown handle":  "Итог.\n⏳ WAIT decision:@nobody-here: ?",
		"backticks":       "Маркер пишется `⏳ WAIT decision:@pavel: вопрос` — это шаблон.",
		"blockquote":      "Шаблон:\n\n> ⏳ WAIT decision:@pavel: вопрос\n\nКонец.",
		"fenced":          "Шаблон:\n\n```\n⏳ WAIT decision:@pavel: вопрос\n```\n\nКонец.",
		"not the tail":    "⏳ WAIT decision:@pavel: вопрос\n\nА дальше обычный текст.",
		"other wait kind": "Итог.\n⏳ WAIT card:#00000000",
	} {
		t.Run(name, func(t *testing.T) {
			env := postWaitDecision(t, body)
			assert.Empty(t, env.taskMover.humanGateCalls(), "must not arm human_gate")
		})
	}
}

func TestWaitDecision_HumanReplyReleasesGate(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID: cid, TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body:      "Итог.\n⏳ WAIT decision:@pavel: GO?",
		CreatedAt: frozenTime.Add(-2 * time.Hour),
	}

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: pavelID, AuthorType: domain.ActorTypeUser, Body: "GO.",
	}))

	calls := env.taskMover.humanGateCalls()
	require.Len(t, calls, 1)
	assert.False(t, calls[0].value, "a human reply must clear the gate")
}

// an internal ticket: a slug shared by a workspace agent and a same-named human login
// (hana, rita) addresses the agent — the marker must not arm a human gate. The
// control (no agent registered under that slug) still arms, so the test is not vacuous.
func TestWaitDecision_AgentSlugSharedWithUser_DoesNotArmGate(t *testing.T) {
	for name, body := range map[string]string{
		"wait decision": "Итог.\n⏳ WAIT decision:@hugh: нужна приёмка",
		"blocking":      "❓ **Blocking @hugh**: нужна приёмка",
	} {
		for _, agentExists := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				agentSvc := NewMockAgentService()
				env := setupTriageEnvWithOptions(t, true, WithCommentAgentService(agentSvc))
				env.userRepo.AddUser(env.wsID, &domain.User{ID: uuid.New(), Username: "hugh", Name: "Hugh (QA)"})
				if agentExists {
					agentSvc.AddAgent(env.wsID, &domain.Agent{ID: uuid.New(), WorkspaceID: env.wsID, Slug: "hugh"})
				}
				assigneeID := uuid.New()
				taskID := uuid.New()
				env.taskRepo.items[taskID] = &domain.Task{
					ID: taskID, ProjectID: env.projID, StatusID: env.inProgressID, Title: "T",
					AssigneeID: &assigneeID, AssigneeType: domain.AssigneeTypeAgent,
					DelegationLevel: domain.DelegationLevelAuto,
				}
				require.NoError(t, env.svc.Create(context.Background(), &domain.Comment{
					TaskID: taskID, AuthorID: assigneeID, AuthorType: domain.ActorTypeAgent, Body: body,
				}))
				if agentExists {
					assert.Empty(t, env.taskMover.humanGateCalls(), "agent slug must not arm human_gate")
				} else {
					assert.NotEmpty(t, env.taskMover.humanGateCalls(), "control: user-only slug arms")
				}
			})
		}
	}
}
