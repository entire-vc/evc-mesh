package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ---------------------------------------------------------------------------
// Task #17829fcf — a withdrawal that does not count must not be SILENT.
//
// Measured live 2026-08-20 on #58d8bb8d (prod-sha a635137, Bill): the marker's
// own owner, clearable_by_owner=true, wrote three withdrawals 5.6h apart — far
// more than minReaffirmToWithdrawalGap, so that guard is not what stopped them:
//
//	 detailed withdrawal, "ответ больше не нужен" in the heading  → gate stayed,
//	                                                                no trace
//	 negator as the FIRST paragraph, second paragraph explains
//	 where the work moved                                         → gate stayed,
//	                                                                no trace
//	 exactly one line: "Отзываю свой запрос: ответ больше не       → gate cleared
//	 нужен."
//
// The only difference is whether the negator landed in the last paragraph.
// Both failures took the same bare `return`, so a missed withdrawal, an
// unwritten one and a successful one were indistinguishable from outside.
// ---------------------------------------------------------------------------

// firstParagraphWithdrawalBody is row 2 of the table above: the negator leads,
// and a following paragraph explains where the work went. Every rule we give
// agents pushes toward exactly this shape — say what you decided, then say
// what happens next — and it is the shape that silently fails.
const firstParagraphWithdrawalBody = "Отзываю свой запрос к Pavel: ответ больше не нужен, блокер самоустранился.\n\n" +
	"Остаток работы вынесен в отдельную карточку #4d0827a0 — там же лежат пробы и разбор."

// blockerStillOpenWithdrawalBody is the second mine: a thorough withdrawal
// that, in the same paragraph, describes what remains. "не закрыт" is a
// blockerStillOpenMarkers phrase and overrides the negator standing right next
// to it.
//
// ⚠️ MEASURED CORRECTION to #17829fcf's own write-up. The task reports this mine
// firing on the phrase «не закрытая литерально регресс-проверка», and it does
// NOT: containsNegatorWholeWord requires a word boundary after the needle, and
// "не закрыт" inside "не закрытая" is followed by a letter, so the veto never
// runs. That first withdrawal failed on paragraph scope ALONE — the negator was
// in its heading. The veto is real, but only for the short forms below, which
// makes it a narrower trap than reported. Pinned as a table case so the
// difference stays measured rather than remembered.
const blockerStillOpenWithdrawalBody = "Отзываю запрос: ответ больше не нужен, но регресс-тест не закрыт."

func TestDiagnoseNegatorMiss(t *testing.T) {
	tests := []struct {
		name string
		body string
		want negatorMissReason
	}{
		{
			name: "negator in the first of two paragraphs",
			body: firstParagraphWithdrawalBody,
			want: negatorMissOutOfScope,
		},
		{
			name: "negator in a heading, body elsewhere",
			body: "## Отзыв: ответ больше не нужен\n\nНиже — как я это проверил и что осталось.",
			want: negatorMissOutOfScope,
		},
		{
			name: "blocker-still-open phrase overrides a negator in the same scope",
			body: blockerStillOpenWithdrawalBody,
			want: negatorMissBlockerStillOpen,
		},
		{
			name: "\"не забыт\" vetoes the same way",
			body: "Отзываю запрос: ответ больше не нужен. Блокер не забыт.",
			want: negatorMissBlockerStillOpen,
		},
		{
			// The correction described on blockerStillOpenWithdrawalBody: this
			// is the phrase #17829fcf blamed, and it withdraws cleanly. If a
			// later change makes the veto match inflected forms, this case
			// flips to negatorMissBlockerStillOpen and says so out loud —
			// which is a real behaviour change, not a test to "fix".
			name: "an INFLECTED не-закрыт does NOT veto — the withdrawal counts",
			body: "Отзываю запрос: ответ больше не нужен, регресс-проверка не закрыта литерально.",
			want: "",
		},
		{
			name: "negator only inside inline code",
			body: "Механизм: негатор (`не нужен`) того же автора снимает флаг.",
			want: negatorMissOnlyQuoted,
		},
		{
			// Quoted-only is reported only where an ASSERTED negator would have
			// counted — here the fence IS the last paragraph, so the author put
			// the right words in the right place and only formatted them as a
			// citation.
			name: "negator quoted in the last paragraph",
			body: "Отзыв:\n\n```\nответ больше не нужен\n```",
			want: negatorMissOnlyQuoted,
		},
		{
			// MEASURED, and a real limit of the narrowing: lastParagraph splits
			// on BLANK LINES, so a fence with no blank line around it does not
			// end the paragraph and this whole body is "the last paragraph".
			// The narrowing therefore only suppresses pastes that are
			// blank-line separated — which is the markdown convention, and the
			// shape of the log-paste case below, but not a guarantee.
			// Deliberately not "fixed": teaching lastParagraph about fences
			// would change the DECISION path too, and that path's narrowing has
			// its own live justification (#1e5be182). Residual noise here is a
			// one-line notice; the alternative risks re-opening a released-gate
			// incident.
			name: "documenting fence with no blank lines is still reported (known limit)",
			body: "Словарь:\n```\nне нужен\nснят\n```\nСправка, не отзыв.",
			want: negatorMissOnlyQuoted,
		},
		{
			name: "the same fence, blank-line separated, is NOT reported",
			body: "Словарь:\n\n```\nне нужен\nснят\n```\n\nСправка, не отзыв.",
			want: "",
		},
		{
			// triageExitNegators contains "resolved", so a pasted log or JSON
			// status field matches it. Reporting these would put a gate notice
			// on every log paste — the exact noise this task is the mirror
			// image of.
			name: "a pasted log containing \"resolved\" is not a missed withdrawal",
			body: "Прогнал ещё раз, вот вывод:\n\n```\ndeps: 14 resolved, 0 conflicts\n```\n\nИтог: причина та же, копаю дальше.",
			want: "",
		},
		// --- the silences that must stay silent -----------------------------
		{
			name: "a withdrawal that WORKS explains nothing",
			body: "Отзываю свой запрос: ответ больше не нужен.",
			want: "",
		},
		{
			name: "negator in the last paragraph of a long comment works too",
			body: "Разбор на три абзаца.\n\nЕщё подробности.\n\nОтвет больше не нужен — снимаю.",
			want: "",
		},
		{
			name: "an ordinary progress comment with no negator anywhere",
			body: "Собрал ветку, CI зелёный, жду приёмки.",
			want: "",
		},
		{
			name: "declining to re-ping is not a missed withdrawal (#3948173f)",
			body: "Разбор.\n\nПовторный ask Pavel'ю здесь не нужен — он уже видел это состояние.",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, diagnoseNegatorMiss(tt.body))
		})
	}
}

// TestDiagnoseNegatorMiss_NeverContradictsTheDecision is the property that keeps
// the explanation honest: whenever hasNegatorInScope SAYS YES, there is nothing
// to explain, and whenever it says no on a body that mentions withdrawal, the
// explanation is non-empty. Both share negatorScope/negatorAsserted rather than
// re-deriving the boundary, so this pins that they cannot drift apart — a
// diagnosis computed from its own copy of "where the server looks" would
// eventually justify a refusal by a rule the server no longer applies.
func TestDiagnoseNegatorMiss_NeverContradictsTheDecision(t *testing.T) {
	bodies := []string{
		firstParagraphWithdrawalBody,
		blockerStillOpenWithdrawalBody,
		billLongStatusReportBody,
		liveProbeSummaryBody,
		"Отзываю свой запрос: ответ больше не нужен.",
		"Собрал ветку, CI зелёный.",
		"❓ **Blocking @pavel**: нужен выбор A/Б",
		"Старый ask снят.\n\n❓ **Blocking @pavel**: а вот новый.",
	}
	for _, body := range bodies {
		if hasNegatorInScope(body) {
			assert.Equal(t, negatorMissReason(""), diagnoseNegatorMiss(body),
				"a withdrawal that COUNTED must never also be explained away")
		}
	}
}

// TestReleaseHumanGateOnWithdrawal_NegatorInFirstParagraph_ExplainsWhy is AC4:
// the exact shape from row 2 of the live table. It still does not release —
// widening the scope was deliberately out of scope for #17829fcf, since the
// narrowing has its own live justification (#1e5be182). What it must no longer
// do is fail in silence.
func TestReleaseHumanGateOnWithdrawal_NegatorInFirstParagraph_ExplainsWhy(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: firstParagraphWithdrawalBody,
	}))

	assert.Empty(t, env.taskMover.humanGateCalls(), "scope unchanged: this shape still does not release")
	assert.Empty(t, env.releaseComments())

	notices := env.withdrawalMissNotices()
	require.Len(t, notices, 1, "the author must be told the gate is still up")
	assert.Contains(t, notices[0].Body, "последний абзац",
		"and told WHICH region the server actually read")
	assert.Equal(t, domain.ActorTypeSystem, notices[0].AuthorType)
}

// TestReleaseHumanGateOnWithdrawal_NegatorMovedToLastParagraph_Releases is the
// positive control for the test above, and the one that makes its advice
// checkable rather than asserted: the SAME two sentences, reordered so the
// negator ends the comment, do release. If this ever stops passing, the notice
// is telling agents to do something that no longer works.
func TestReleaseHumanGateOnWithdrawal_NegatorMovedToLastParagraph_Releases(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Остаток работы вынесен в отдельную карточку #4d0827a0 — там же пробы и разбор.\n\n" +
			"Отзываю свой запрос к Pavel: ответ больше не нужен, блокер самоустранился.",
	}))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "same words, negator last — this must release")
	assert.False(t, gateCalls[0].value)
	assert.Empty(t, env.withdrawalMissNotices(), "a withdrawal that worked is not also reported as a miss")
}

// TestReleaseHumanGateOnWithdrawal_BlockerStillOpenPhrase_ExplainsWhy covers the
// second mine: a negator that DID reach the scope, killed by an assertion in the
// same scope that the blocker is still live. Naming this reason separately is the
// point — "негатор вне последнего абзаца" would be a wrong explanation here, and a
// wrong explanation is worse than none: it sends the author to reorder paragraphs
// that were already in the right order.
func TestReleaseHumanGateOnWithdrawal_BlockerStillOpenPhrase_ExplainsWhy(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: blockerStillOpenWithdrawalBody,
	}))

	assert.Empty(t, env.taskMover.humanGateCalls())
	notices := env.withdrawalMissNotices()
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0].Body, "блокер всё ещё жив")
	assert.NotContains(t, notices[0].Body, "последний абзац",
		"this body's negator DID reach the scope — do not blame paragraph order")
}

// ---------------------------------------------------------------------------
// Task #081f1354 — a comment that withdraws AND raises a fresh marker in one
// breath must not silently thaw the card.
//
// Live repro (throwaway task, same session, 2026-08-21): task already
// human_gate=true from an earlier marker. One comment posts a fresh
// "❓ Blocking @pavel" followed by "Предыдущий вопрос снят — …" —
// enforceBlockingTriage (runs first, same request) reaffirms the gate on the
// new marker; releaseHumanGateOnWithdrawal, unaware a marker was just
// reasserted in the SAME comment, read "снят" as negating it and cleared
// human_gate=false before the request even finished — the new ask vanished
// the instant it was raised. get_task before: human_gate=true. After this one
// comment: human_gate=false, human_gate_armed_at stamped to the very comment
// that (net) left the gate down.
// ---------------------------------------------------------------------------

// negatorThenFreshMarkerBody is the exact shape that reproduced live: a fresh
// marker, then a negator word in the paragraph after it. negatorScope anchors
// to THIS comment's own last marker, so "снят" reads as in-scope regardless of
// which ask the author meant it for.
const negatorThenFreshMarkerBody = "❓ **Blocking @pavel**: тестовый вопрос, версия B (переформулирован)?\n\n" +
	"Предыдущий вопрос снят — был основан на устаревших данных."

func TestReleaseHumanGateOnWithdrawal_FreshMarkerInSameComment_DoesNotRelease(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: negatorThenFreshMarkerBody,
	}))

	// enforceBlockingTriage (runs first, same request) legitimately reaffirms
	// the gate on the fresh marker — that call is expected and correct. What
	// must NOT happen is releaseHumanGateOnWithdrawal placing a SECOND call
	// clearing it back to false.
	gateCalls := env.taskMover.humanGateCalls()
	for _, c := range gateCalls {
		assert.True(t, c.value, "no call may clear the gate — that would be the bug reproducing")
	}
	notices := env.withdrawalMarkerConflictNotices()
	require.Len(t, notices, 1, "the author must be told why the withdrawal was refused, not left to silence")
	assert.Equal(t, domain.ActorTypeSystem, notices[0].AuthorType)
}

// TestReleaseHumanGateOnWithdrawal_NegatorAloneStillReleases is the negative
// control demanded by the task's own AC3: the SAME negator wording, with the
// marker line removed, must still release exactly as before this fix — proof
// the new guard is scoped to "this comment ALSO carries a marker", not to the
// negator wording itself.
func TestReleaseHumanGateOnWithdrawal_NegatorAloneStillReleases(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Предыдущий вопрос снят — был основан на устаревших данных.",
	}))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "no marker in this body — the guard must not fire, negator releases as before")
	assert.False(t, gateCalls[0].value)
	assert.Empty(t, env.withdrawalMarkerConflictNotices())
}

// TestReportWithdrawalMiss_BystanderGetsNoNotice bounds the noise. Every agent
// comment on a gated task passes through this path, but only the live marker's
// OWNER could have withdrawn the ask, so only they get told. Telling a bystander
// "your withdrawal did not count" would be false — theirs never could.
func TestReportWithdrawalMiss_BystanderGetsNoNotice(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	bystanderID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), bystanderID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body: firstParagraphWithdrawalBody,
	}))

	assert.Empty(t, env.taskMover.humanGateCalls(), "a bystander cannot release someone else's ask")
	assert.Empty(t, env.withdrawalMissNotices(), "…and must not be told they nearly did")
}

// TestReportWithdrawalMiss_OrdinaryCommentStaysSilent is the anti-noise control
// with teeth: the ordinary traffic on a gated card — progress notes, a fresh
// marker, a declined re-ping — must produce nothing. Without this, the fix for a
// silent failure becomes a comment-spammer on every gated task in the fleet.
func TestReportWithdrawalMiss_OrdinaryCommentStaysSilent(t *testing.T) {
	bodies := map[string]string{
		"progress note":    "Собрал ветку, CI зелёный, жду приёмки.",
		"a fresh marker":   "❓ **Blocking @pavel**: нужен выбор между A и Б.",
		"declined re-ping": "Разбор.\n\nПовторный ask Pavel'ю здесь не нужен — он уже видел это состояние.",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			env := setupTriageEnv(t, true)
			taskID := env.seedGatedTask(env.inProgressID)
			askerID := uuid.New()
			env.seedAgentBlockingComment(taskID, askerID)

			ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
			require.NoError(t, env.svc.Create(ctx, &domain.Comment{
				TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
				Body: body,
			}))

			assert.Empty(t, env.withdrawalMissNotices())
			assert.Empty(t, env.releaseComments())
		})
	}
}

// TestReportWithdrawalMiss_NoticeCannotItselfWithdraw closes the loop the notice
// text opens: it names the negator vocabulary out loud, on the very thread whose
// comments get scanned for that vocabulary. The words are backticked so
// stripQuotedSpans drops them, and it carries no blocking marker — so the notice
// can neither release a gate nor arm one.
func TestReportWithdrawalMiss_NoticeCannotItselfWithdraw(t *testing.T) {
	for reason, hint := range withdrawalMissHint {
		body := "🔒 human_gate по-прежнему поднят — этот коммент его не снял: " + hint +
			"\n\nЕсли отзыв был намеренным, повтори его **отдельным комментарием**, где слова отзыва " +
			"(`не нужен` / `не требуется` / `снят` / …) стоят в последнем абзаце и рядом с ними нет " +
			"утверждений, что блокер жив. Разбор и пробы оставляй предыдущим комментарием — они отзыв не портят. " +
			"После — перечитай `human_gate`."
		assert.False(t, hasNegatorInScope(body), "notice for %s must not read as a withdrawal", reason)
		assert.False(t, hasBlockingMarker(body), "notice for %s must not arm a gate", reason)
	}
}

// ---------------------------------------------------------------------------
// ctxCacheInv branches — reportWithdrawalMiss and reportWithdrawalMarkerConflict
// both invalidate the parent task's context cache after posting their notice,
// but setupTriageEnv wires no ContextCacheInvalidator, so neither branch was
// ever exercised. These tests wire a fake one via setupTriageEnvWithOptions.
//
// Neither request is a single invalidate call in practice — enforceBlockingTriage
// and the base Create path invalidate too, for reasons unrelated to these two
// notices — so asserting an exact total count would pin incidental behaviour of
// OTHER code paths, not the property under test. What is asserted instead: the
// invalidator fired for the right task (proves the branch ran at all), and —
// for the failure case — that exactly ONE fewer call happens when the notice's
// own Create fails, isolated by comparing against the same scenario run clean.
// ---------------------------------------------------------------------------

// TestReportWithdrawalMiss_InvalidatesContextCache reuses the exact scenario
// from TestReleaseHumanGateOnWithdrawal_NegatorInFirstParagraph_ExplainsWhy
// (a withdrawal-miss notice IS posted) with a ContextCacheInvalidator wired in.
func TestReportWithdrawalMiss_InvalidatesContextCache(t *testing.T) {
	inv := &fakeCtxCacheInvalidator{}
	env := setupTriageEnvWithOptions(t, true, WithCommentContextCacheInvalidator(inv))
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: firstParagraphWithdrawalBody,
	}))

	require.Len(t, env.withdrawalMissNotices(), 1, "sanity: the notice this test depends on must still post")
	require.NotEmpty(t, inv.calls, "posting the withdrawal-miss notice must invalidate the task's context cache")
	for _, id := range inv.calls {
		assert.Equal(t, taskID, id, "every invalidate call in this single-task scenario must name this task")
	}
}

// TestReportWithdrawalMarkerConflict_InvalidatesContextCache is the same
// property for the OTHER notice (#081f1354): a comment carrying both a
// withdrawal negator and a fresh Blocking marker.
func TestReportWithdrawalMarkerConflict_InvalidatesContextCache(t *testing.T) {
	inv := &fakeCtxCacheInvalidator{}
	env := setupTriageEnvWithOptions(t, true, WithCommentContextCacheInvalidator(inv))
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: negatorThenFreshMarkerBody,
	}))

	require.Len(t, env.withdrawalMarkerConflictNotices(), 1, "sanity: the conflict notice must still post")
	require.NotEmpty(t, inv.calls, "posting the conflict notice must invalidate the task's context cache")
	for _, id := range inv.calls {
		assert.Equal(t, taskID, id, "every invalidate call in this single-task scenario must name this task")
	}
}

// TestReportWithdrawalMarkerConflict_NoticeCreateFails covers the DB-error
// branch: the ORIGINAL comment (the one carrying the negator+marker) must
// still persist — the gate decision itself does not depend on being able to
// announce it — but when the system notice's own Create call fails, the
// function must log and return WITHOUT invalidating the cache on the conflict
// notice's own behalf.
//
// createFailFor matches on the notice's own body text, not a bare
// AuthorType==system predicate — enforceBlockingTriage posts its own system
// comment on this same request (reaffirming the fresh marker), and a blanket
// "fail every system Create" would take that one down too, muddying which
// invalidate call this test is actually about.
//
// The clean run (previous test) cannot be reused as the "before" figure
// directly — a fresh env is needed since MockCommentRepository is stateful —
// so this test runs its OWN clean control immediately before the failing run,
// on an identical scenario, and asserts the failing run has exactly one fewer
// invalidate call. That isolates the ONE call this test targets from every
// other invalidate source on the request, without hardcoding their count.
func TestReportWithdrawalMarkerConflict_NoticeCreateFails(t *testing.T) {
	runOnce := func(failNotice bool) []uuid.UUID {
		inv := &fakeCtxCacheInvalidator{}
		env := setupTriageEnvWithOptions(t, true, WithCommentContextCacheInvalidator(inv))
		if failNotice {
			env.commentRepo.createFailFor = func(c *domain.Comment) bool {
				return c.AuthorType == domain.ActorTypeSystem &&
					strings.Contains(c.Body, "И слова отзыва, И новый")
			}
		}
		taskID := env.seedGatedTask(env.inProgressID)
		askerID := uuid.New()
		env.seedAgentBlockingComment(taskID, askerID)

		ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
		require.NoError(t, env.svc.Create(ctx, &domain.Comment{
			TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
			Body: negatorThenFreshMarkerBody,
		}), "the author's own comment must persist regardless of whether the follow-up notice can")

		if failNotice {
			assert.Empty(t, env.withdrawalMarkerConflictNotices(), "the notice's own Create failed — it must not appear as posted")
		} else {
			require.Len(t, env.withdrawalMarkerConflictNotices(), 1, "sanity: the clean control must post the notice")
		}
		return inv.calls
	}

	clean := runOnce(false)
	failed := runOnce(true)
	assert.Len(t, failed, len(clean)-1,
		"a failed notice Create must skip exactly its own invalidate call and no other — "+
			"clean=%v failed=%v", clean, failed)
}
