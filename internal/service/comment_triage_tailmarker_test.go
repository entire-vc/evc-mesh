package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Task #a84b443c — a Blocking marker at the TAIL of a long report must arm the gate
// ---------------------------------------------------------------------------
//
// CLAUDE-communication.md §5a prescribes exactly one ask shape: the analysis, then
// a "---" rule, then "❓ **Blocking @pavel**: …" at the tail. That shape did not arm
// human_gate when the author was the task's own assignee, because
// isAssigneeCompletionReport scans the 500 bytes before the marker for a completion
// keyword — and a work report reliably contains one. The card stayed feedable and the
// ask was never queued, while the comment published normally: a silent failure in the
// worst direction. Fix: the heuristic now suppresses only the triage MOVE, never the
// arming. See isAssigneeCompletionReport's own docblock for the two live losses.

// tailMarkerEnv builds a task assigned to the comment's author, which is the only
// configuration in which isAssigneeCompletionReport can fire at all.
func tailMarkerEnv(t *testing.T) (triageTestEnv, uuid.UUID, uuid.UUID) {
	t.Helper()
	env := setupTriageEnv(t, true)
	assigneeID := uuid.New()
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID:              taskID,
		ProjectID:       env.projID,
		StatusID:        env.inProgressID,
		Title:           "T",
		AssigneeID:      &assigneeID,
		AssigneeType:    domain.AssigneeTypeAgent,
		DelegationLevel: domain.DelegationLevelAuto,
	}
	return env, taskID, assigneeID
}

const tailMarker = "❓ **Blocking @pavel**: нужно твоё решение — вариант A или вариант B?"

// TestEnforceBlockingTriage_TailMarkerAfterReport_ArmsGate is AC #1: four bodies that
// all carry the same live marker at the tail of an assignee-authored report. All four
// must arm human_gate.
func TestEnforceBlockingTriage_TailMarkerAfterReport_ArmsGate(t *testing.T) {
	// Two of these bodies are the live losses, quoted from prod, not paraphrased.
	//
	// DISCRIMINATING POWER — measured by reverting the fix and re-running (AC #2).
	// 3 of the 5 fail without the fix: #29a0a879, the table body, and the >4000-byte
	// body. The other two are deliberately NOT discriminators and must not be read as
	// such:
	//
	//   - "marker first line" is the POSITIVE CONTROL — it armed before the fix too.
	//     It is here to prove the fix did not break the shape that already worked.
	//   - "#8286e487" (Bill, 2026-08-10T05:44:30Z) is the FIRST live loss, but it no
	//     longer fails against current main: its keyword hit is "закрыт" INSIDE the
	//     word "незакрытые", and #548's negation check reads the word's own "не"
	//     prefix as a negation token, so the guard misses it by accident. Measured
	//     against a7459b1^ (the code in force when the comment was posted, 14 minutes
	//     before #548 landed) it does fail. Retained as the documented first loss and
	//     as a guard against a future change to the negation logic reviving it.
	//
	// #29a0a879 (Deadalus, 2026-08-13T23:52:52Z) is the one that still bit after #548 —
	// "сделан" ⊂ "**Уже сделано**", unnegated, plainly inside the window. A real
	// MIT-violation escalation from an upstream copyright holder, lost for 8 hours.
	cases := []struct {
		name string
		body string
	}{
		{
			name: "marker first line (control — armed before the fix too)",
			body: tailMarker + "\n\nПодробный замер с контролями — в комментарии выше.",
		},
		{
			name: "live loss #8286e487 — tail marker after --- rule",
			body: "## Проверил перед тем как нести Pavel'ю\n\n" +
				"### Немедленное следствие\n\n" +
				"`#c085b1fe` (WW33 Reddit — 2 коммента в незакрытые треды) и цикл warmup-сессий " +
				"сейчас пишут в невидимый канал. Написал туда отдельно с воспроизводимой пробой, " +
				"чтобы слот не сгорел до решения.\n\n---\n\n" + tailMarker,
		},
		{
			name: "live loss #29a0a879 — tail marker after a completion line",
			body: "## Расследование — issue легитимен, это не недоразумение\n\n" +
				"**Уже сделано** (авто-intake, до меня): ACK на issue от daedalus-mb — только " +
				"«посмотрели, вернёмся с решением», никаких обещаний.\n" +
				"**Не сделано** и не буду делать без ответа: не трогал LICENSE/код.\n\n---\n\n" + tailMarker,
		},
		{
			name: "tail marker after a markdown table",
			body: "## Замер\n\n| проверка | результат |\n|---|---|\n| миграция | завершена |\n" +
				"| прогон | готово |\n\nВсе подзадачи закрыты.\n\n" + tailMarker,
		},
		{
			name: "tail marker in a body over 4000 bytes",
			body: "## Отчёт\n\n" +
				strings.Repeat("Подробности замера с контролями в обе стороны. ", 120) +
				"\n\nРабота завершена, все фиксы выполнены.\n\n---\n\n" + tailMarker,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, taskID, assigneeID := tailMarkerEnv(t)
			require.NoError(t, env.svc.Create(context.Background(), &domain.Comment{
				TaskID:     taskID,
				AuthorID:   assigneeID,
				AuthorType: domain.ActorTypeAgent,
				Body:       tc.body,
			}))

			gateCalls := env.taskMover.humanGateCalls()
			require.Len(t, gateCalls, 1, "a live marker naming a real user must arm human_gate")
			assert.Equal(t, taskID, gateCalls[0].taskID)
			assert.True(t, gateCalls[0].value, "SetHumanGate must arm the flag (value=true)")
		})
	}
}

// TestEnforceBlockingTriage_TailMarkerNegativeControls is AC #3: shapes that must NOT
// arm the gate. These worked before the fix and must keep working — without them the
// suite above would also pass on a detector that simply armed on everything.
func TestEnforceBlockingTriage_TailMarkerNegativeControls(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "marker inside a blockquote (documenting the mechanism)",
			body: "Разбор формы маркера — вот как это пишется:\n\n> ❓ **Blocking @pavel**: пример\n\n" +
				"Это цитата шаблона, а не аск.",
		},
		{
			name: "marker inside inline code",
			body: "Пиши `❓ **Blocking @pavel**: вопрос` первой строкой — это шаблон, не аск.",
		},
		{
			name: "marker inside a fenced block",
			body: "Шаблон:\n\n```\n❓ **Blocking @pavel**: вопрос\n```\n\nКонец.",
		},
		{
			name: "Blocking @<agent-slug> — resolves to no user",
			body: "Разбор готов.\n\n---\n\n❓ **Blocking @linus**: нужен твой вердикт по миграции.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, taskID, assigneeID := tailMarkerEnv(t)
			require.NoError(t, env.svc.Create(context.Background(), &domain.Comment{
				TaskID:     taskID,
				AuthorID:   assigneeID,
				AuthorType: domain.ActorTypeAgent,
				Body:       tc.body,
			}))

			assert.Empty(t, env.taskMover.humanGateCalls(), "must not arm human_gate")
			assert.Empty(t, env.taskMover.calls(), "must not move the task")
		})
	}
}
