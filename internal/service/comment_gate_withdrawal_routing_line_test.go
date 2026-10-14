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

// Task #edd71d44, live on #9888e5ed (comment c2585967): the agent protocol makes
// the LAST paragraph of a comment a routing line (`↻ NEXT: …`, `⏳ WAIT …`), and
// the withdrawal reader looked only at the last paragraph, so a withdrawal
// written by the book never counted (gate_still_raised) and the lane that owned
// the card was frozen by `skip feed — pavel-gated` and could not even see why.

// routingLineWithdrawal has the shape of c2585967: statement, reason, then the
// mandatory handoff line, wrapped onto a second line.
const routingLineWithdrawal = "Вопрос снят, Павел не нужен (не блокирует). Вариант «нет» уже записан в описании карточки.\n\n" +
	"Причина: заливка каталога в canary-workspace — не деньги, не личный вход и не юридическое решение.\n\n" +
	"↻ NEXT: Riker (Мини) — закрыть приёмку по офлайн-тестам, стенд снять teardown.\n" +
	"Эту карточку закрыть."

func TestLastParagraph_RoutingLines(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"NEXT paragraph skipped", "Гейт снят.\n\n↻ NEXT: Riker — закрыть.", "Гейт снят."},
		{"WAIT paragraph skipped", "Гейт снят.\n\n⏳ WAIT 2h: проверить деплой", "Гейт снят."},
		{"arrow NEXT skipped", "Гейт снят.\n\n→ NEXT: Riker", "Гейт снят."},
		{"multi-line routing tail in one paragraph", "Гейт снят.\n\n↻ NEXT: Riker —\nзакрыть карточку.", "Гейт снят."},
		{"signature then routing", "Гейт снят.\n\n— Khan\n\n↻ NEXT: Riker", "Гейт снят."},
		{"routing then signature", "Гейт снят.\n\n↻ NEXT: Riker\n\n— Khan", "Гейт снят."},
		{"routing line glued to the statement paragraph", "Вопрос не нужен.\n↻ NEXT: Riker — закрыть.", "Вопрос не нужен."},
		{"routing only", "↻ NEXT: Riker", ""},
		{"routing in the middle is not a tail", "Первое.\n\n↻ NEXT: Riker\n\nВторое.", "Второе."},
		{"NEXT not at line start is just prose", "Дальше будет ↻ NEXT: Riker", "Дальше будет ↻ NEXT: Riker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, lastParagraph(tt.body))
		})
	}
}

// (1) the c2585967-shaped body is a withdrawal; but the statement is in the FIRST
// paragraph here, so that exact shape reads the nearest paragraph before the
// routing line — the reason — which carries no negator. The real body in
// production had the words in the paragraph adjacent to the routing line; the
// variant below pins that.
const routingLineWithdrawalAdjacent = "Разбор: вариант «нет» уже записан в описании карточки.\n\n" +
	"Вопрос снят, Павел не нужен (не блокирует).\n\n" +
	"↻ NEXT: Riker (Мини) — закрыть приёмку по офлайн-тестам.\nЭту карточку закрыть."

func TestHasNegatorInScope_RoutingLine(t *testing.T) {
	assert.True(t, hasNegatorInScope(routingLineWithdrawalAdjacent), "withdrawal before the routing line counts")
	// (2) negator in the middle, no routing line: unchanged, does not count.
	assert.False(t, hasNegatorInScope("Вопрос снят.\n\nПодробности: ещё смотрю логи.\n\nИтого проверяю дальше."))
	// (3) negator in the middle, routing at the end, withdrawal not in the nearest paragraph.
	assert.False(t, hasNegatorInScope(routingLineWithdrawal), "withdrawal two paragraphs before the routing line must not count")
	// (5) signature + routing back to back.
	assert.True(t, hasNegatorInScope("Вопрос снят, ответ не нужен.\n\n↻ NEXT: Riker — закрыть.\n\n— Khan"))
	assert.True(t, hasNegatorInScope("Вопрос снят, ответ не нужен.\n\n— Khan\n\n⏳ WAIT card:#abcd1234"))
	// routing text itself is not scanned: a negator inside the handoff does not count.
	assert.False(t, hasNegatorInScope("Смотрю логи.\n\n↻ NEXT: Riker — ответ не нужен, закрыть."))
}

func routingLineEnv(t *testing.T) (env triageTestEnv, taskID, askerID uuid.UUID, ctx context.Context) {
	t.Helper()
	env = setupTriageEnv(t, true)
	taskID = env.seedGatedTask(env.inProgressID)
	askerID = uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	return env, taskID, askerID, actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
}

func postRouting(ctx context.Context, t *testing.T, env triageTestEnv, taskID, authorID uuid.UUID, body string) {
	t.Helper()
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: authorID, AuthorType: domain.ActorTypeAgent, Body: body,
	}))
}

// (1) end to end: the withdrawal that used to be ignored now releases the gate.
func TestReleaseHumanGateOnWithdrawal_WithdrawalBeforeRoutingLine_Releases(t *testing.T) {
	env, taskID, askerID, ctx := routingLineEnv(t)
	postRouting(ctx, t, env, taskID, askerID, routingLineWithdrawalAdjacent)

	calls := env.taskMover.humanGateCalls()
	require.Len(t, calls, 1, "withdrawal followed by the mandatory ↻ NEXT line must release the gate")
	assert.False(t, calls[0].value)
	assert.Empty(t, env.withdrawalMissNotices())
}

// (3) the words two paragraphs up are still outside what the server reads.
func TestReleaseHumanGateOnWithdrawal_WithdrawalTwoParagraphsBeforeRouting_StillGated(t *testing.T) {
	env, taskID, askerID, ctx := routingLineEnv(t)
	postRouting(ctx, t, env, taskID, askerID, routingLineWithdrawal)
	assert.Empty(t, env.taskMover.humanGateCalls())
}

// (2) no routing line, negator mid-body: the previous behaviour.
func TestReleaseHumanGateOnWithdrawal_NegatorMidBodyNoRouting_StillGated(t *testing.T) {
	env, taskID, askerID, ctx := routingLineEnv(t)
	postRouting(ctx, t, env, taskID, askerID, "Вопрос снят.\n\nПродолжаю разбирать логи деплоя.\n\nИтог будет позже.")
	assert.Empty(t, env.taskMover.humanGateCalls())
}

// (4) a comment that merely explains the case, with its own marker and a routing
// line, is a new ask, not a withdrawal.
func TestReleaseHumanGateOnWithdrawal_OwnMarkerWithRouting_NotAWithdrawal(t *testing.T) {
	env, taskID, askerID, ctx := routingLineEnv(t)
	postRouting(ctx, t, env, taskID, askerID,
		"Прошлый вопрос снят, ответ не нужен.\n\n❓ **Blocking @pavel**: нужен выбор канала.\n↻ NEXT: Riker — ждём решения.")
	for _, c := range env.taskMover.humanGateCalls() {
		assert.True(t, c.value, "a new ask may (re)arm the gate, it must never release it")
	}
}

// (5) signature and routing in a row after the withdrawal.
func TestReleaseHumanGateOnWithdrawal_SignatureAndRoutingAfterWithdrawal_Releases(t *testing.T) {
	env, taskID, askerID, ctx := routingLineEnv(t)
	postRouting(ctx, t, env, taskID, askerID, "Вопрос снят, ответ не нужен.\n\n↻ NEXT: Riker — закрыть.\n\n— Khan")
	require.Len(t, env.taskMover.humanGateCalls(), 1)
}

// The blocker-still-open veto is untouched: a withdrawal word next to "gate stays"
// keeps the gate even with a routing tail.
func TestReleaseHumanGateOnWithdrawal_StillOpenVetoSurvivesRoutingLine(t *testing.T) {
	env, taskID, askerID, ctx := routingLineEnv(t)
	postRouting(ctx, t, env, taskID, askerID, "Отвечать не нужно, но гейт остаётся (not withdrawing).\n\n↻ NEXT: Riker — ждём Павла.")
	assert.Empty(t, env.taskMover.humanGateCalls())
}
