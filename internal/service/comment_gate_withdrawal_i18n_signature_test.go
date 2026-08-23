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

// ---------------------------------------------------------------------------
// Task #62560d6d — two independent, real, live-reproduced failures of gate
// withdrawal, both on the SAME card (#68df3b62, 2026-08-23): an owner with
// clearable_by_owner=true tried to withdraw twice and no-opped silently both
// times, for two different reasons. The bodies below are byte-for-byte the
// real comments (task #68df3b62, matching this file's convention for
// billLongStatusReportBody a few sections up), not paraphrases — a
// paraphrase can accidentally fix itself by not reproducing the exact shape
// that broke.
// ---------------------------------------------------------------------------

// english68df3b62RetractionBody is comment 46a4dbc3, 2026-08-23T17:27:37Z:
// a genuine, explicit retraction, written in English, that matched zero
// entries of the (Russian-only apart from "resolved"/"async")
// triageExitNegators vocabulary and left the gate up with no signal.
const english68df3b62RetractionBody = "Retracting the gate marker from my prior comment on this card — " +
	"that was meant purely as cross-reference explaining why I'm not duplicating the ask, " +
	"not as a second independent ask. The one live question for Pavel about VM-113 access " +
	"lives on #bdd682a4 only. This subtask is parked (todo) waiting on that resolution, " +
	"nothing further needed here directly."

// signedOff68df3b62WithdrawalBody is comment fb3ff606, 2026-08-23T20:09:31Z:
// a real, well-formed Russian withdrawal ("...не требуется.") that sat in the
// SECOND-to-last paragraph of a comment closing with a trailing "— Robert"
// sign-off on its own line. Before the #62560d6d fix, lastParagraph returned
// the signature (zero negators) and this exact body produced the
// negator-outside-searched-scope system notice seen live on the card.
const signedOff68df3b62WithdrawalBody = "Блокер по доступу снимаю: **ключи флоту на VM 113 раздавать не нужно, эта работа моя.**\n\n" +
	"VM 113 — не продуктовая машина, у неё нет лида и девов: там GitLab и зеркало, то есть " +
	"инфраструктура флота, и владелец у неё я. Диагностика лейнов была верной — на хосте " +
	"авторизован ровно один ключ, личный ключ Павла, поэтому не проходит ни один лейн и сам " +
	"`hel01` тоже. Порт при этом обычный, 22; «SSH на 2222» — это правило DNAT **на hel01** " +
	"(`2222 → 10.10.10.130:22`), а не порт на самой VM. Хост внесён в `bob/docs/servers.md`.\n\n" +
	"То, ради чего лейны туда стучались, теперь приходит к ним само: с 23.08 VM 113 под " +
	"наблюдением (цель `gitlab-vm`, четыре правила `severity: critical` → Telegram и карточка " +
	"в Mesh), а отказ зеркала сам заводит карточку с полным текстом причины. Шелл для этого " +
	"больше не нужен.\n\n" +
	"## Очерёдность у этой карточки изменилась — и она ниже, чем казалось\n\n" +
	"Реестр контейнеров нужен ради одного потребителя: переноса сборки образов Spark с " +
	"`ghcr.io`. Но этот перенос упирается **не в отсутствие реестра**, а в то, что " +
	"`FRONTEND_ENV_PROD` пишется в build context и секреты запекаются в слой образа " +
	"(записано в `infra/gitlab-deploy/gitlab-rollback-and-lessons`, Часть 3, ещё до этой карточки).\n\n" +
	"Включив реестр сейчас, мы получим место, куда складывать образы с запечёнными секретами " +
	"— уже на нашей машине, рядом с исходниками и CI-токенами. Это не приближает поставку, " +
	"а ухудшает исходную проблему.\n\n" +
	"Поэтому карточку **не делаю сегодня** и не считаю заблокированной доступом: она ждёт " +
	"рантайм-конфигурации фронтенда Spark. Порядок из `#c7110523`: сначала npm-половина " +
	"(её инфраструктура уже готова — пакетный реестр включён и отвечает), потом рантайм-конфиг " +
	"Spark, реестр контейнеров последним.\n\n" +
	"Забираю на себя. Гейт на Павла снят — вопрос про доступ отвечен, нового решения от него не требуется.\n\n" +
	"— Robert"

// TestHasNegatorInScope_EnglishRetraction_RealBody68df3b62 pins the exact
// production failure at the unit level, before touching the release harness.
func TestHasNegatorInScope_EnglishRetraction_RealBody68df3b62(t *testing.T) {
	assert.True(t, hasNegatorInScope(english68df3b62RetractionBody),
		"an explicit English retraction (\"Retracting the gate marker...\") must count as a withdrawal")
}

func TestHasNegatorInScope_SignedOffWithdrawal_RealBody68df3b62(t *testing.T) {
	assert.True(t, hasNegatorInScope(signedOff68df3b62WithdrawalBody),
		"the real withdrawal sentence, one paragraph before a trailing \"— Robert\" signature, must count")
}

// TestReleaseHumanGateOnWithdrawal_EnglishRetraction_RealBody68df3b62 is the
// end-to-end reproduction of the first #68df3b62 failure: same shape as
// TestReleaseHumanGateOnWithdrawal_NegatorMovedToLastParagraph_Releases, but
// English, and the exact live body rather than a constructed one.
func TestReleaseHumanGateOnWithdrawal_EnglishRetraction_RealBody68df3b62(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: english68df3b62RetractionBody,
	}))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "the real English retraction must release the gate")
	assert.False(t, gateCalls[0].value)
	assert.Empty(t, env.withdrawalMissNotices(), "a withdrawal that worked is not also reported as a miss")
}

// TestReleaseHumanGateOnWithdrawal_SignedOffWithdrawal_RealBody68df3b62 is the
// end-to-end reproduction of the second #68df3b62 failure.
func TestReleaseHumanGateOnWithdrawal_SignedOffWithdrawal_RealBody68df3b62(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: signedOff68df3b62WithdrawalBody,
	}))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "the withdrawal one paragraph before the signature must release the gate")
	assert.False(t, gateCalls[0].value)
	assert.Empty(t, env.withdrawalMissNotices(), "a withdrawal that worked is not also reported as a miss")
}

// TestReleaseHumanGateOnWithdrawal_SignatureAloneAfterOpenBlocker_StillGated
// is the negative control task #62560d6d's own AC3 demands: a comment that
// ends in a sign-off but asserts NO withdrawal anywhere must not release —
// skipping a trailing signature must never turn an ordinary status update
// (or the blocker-still-open case) into a withdrawal it never was.
func TestReleaseHumanGateOnWithdrawal_SignatureAloneAfterOpenBlocker_StillGated(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Всё ещё жду доступа, вопрос не закрыт, продолжаю искать обход.\n\n— Robert",
	}))

	assert.Empty(t, env.taskMover.humanGateCalls(),
		"a genuinely open blocker must not release just because the comment happens to end in a signature")
}

// TestIsSignatureOnlyParagraph_Bilingual pins the helper directly against a
// spread of real and adjacent shapes, English included since sign-offs are
// not Russian-only either.
func TestIsSignatureOnlyParagraph_Bilingual(t *testing.T) {
	cases := []struct {
		name string
		p    string
		want bool
	}{
		{"em-dash Russian name", "— Robert", true},
		{"hyphen Russian name", "- Howard", true},
		{"em-dash English two-word", "— The Fleet", true},
		{"no dash at all", "Robert", false},
		{"dash bullet sentence", "- Fixed the bug and verified live", false},
		{"dash line with trailing period", "- Done.", false},
		{"dash line with comma", "- Not needed, see above.", false},
		{"empty string", "", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSignatureOnlyParagraph(tt.p))
		})
	}
}
