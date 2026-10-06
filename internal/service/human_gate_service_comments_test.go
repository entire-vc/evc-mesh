package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Task #a36fa51d (analysis of #6443ff98): the gate flickered because fiddler
// comments posted under the author's key counted as a withdrawal, and a marker
// after Pavel's "ок" re-armed the gate silently.

func TestIsServiceComment(t *testing.T) {
	for _, b := range []string{
		"[fiddler] Восстановление сессии…",
		"  [fiddler] … брошенной",
		"🤖 Auto: задача переведена",
		"**no-stall** проверка",
		"🔀 **fleet-balancer сменил исполнителя**",
		"[intake-sweep] вернул карточку",
	} {
		assert.True(t, isServiceComment(b), b)
	}
	for _, b := range []string{"Отзываю запрос: ответ больше не нужен.", "", "fiddler упомянут в тексте"} {
		assert.False(t, isServiceComment(b), b)
	}
}

// Path 1: a service comment under the author's key does not release the gate.
func TestReleaseHumanGateOnWithdrawal_ServiceCommentUnderAuthorKey_KeepsGate(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	for _, body := range []string{
		"[fiddler] Восстановление сессии: карточка не нужна в брошенной очереди, вопрос снят с таймера.",
		"🤖 auto: ответ больше не нужен, блокер самоустранился.",
	} {
		require.NoError(t, env.svc.Create(ctx, &domain.Comment{
			TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent, Body: body,
		}))
	}

	assert.Empty(t, env.taskMover.humanGateCalls(), "service comments must never flip the gate")
	assert.Empty(t, env.releaseComments())
	assert.Empty(t, env.withdrawalMissNotices(), "and must not produce miss notices either")
}

// Positive control: the same words from the author themself do release.
func TestReleaseHumanGateOnWithdrawal_SameWordsFromAuthor_StillReleases(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Отзываю свой запрос: ответ больше не нужен, блокер самоустранился.",
	}))
	calls := env.taskMover.humanGateCalls()
	require.Len(t, calls, 1)
	assert.False(t, calls[0].value)
}

func (env triageTestEnv) seedUserReply(taskID uuid.UUID, body string, at time.Time) {
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID: cid, TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: body, CreatedAt: at,
	}
}

func (env triageTestEnv) repeatAskNotices() []domain.Comment {
	var out []domain.Comment
	for _, c := range env.systemComments() {
		if strings.Contains(c.Body, "на прошлый вопрос уже есть ответ") {
			out = append(out, c)
		}
	}
	return out
}

// Path 2: a marker after Pavel's answer is not armed and quotes the answer.
func TestEnforceBlockingTriage_MarkerAfterHumanAnswer_NotArmed(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID) // frozenTime-2h
	env.seedUserReply(taskID, "да - ок", frozenTime.Add(-time.Hour))

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Текст абзаца готов.\n\n❓ **Blocking @pavel**: утвердить текст?",
	}))

	assert.Empty(t, env.taskMover.humanGateCalls(), "no arm after an answer")
	n := env.repeatAskNotices()
	require.Len(t, n, 1)
	assert.Contains(t, n[0].Body, "да - ок")
	assert.Contains(t, n[0].Body, "supersedes")
}

// Escape hatch and controls: new subject with `supersedes:` arms; no answer arms.
func TestEnforceBlockingTriage_MarkerAfterAnswer_WithSupersedes_Arms(t *testing.T) {
	for name, mk := range map[string]func() *domain.Comment{
		"body line": func() *domain.Comment {
			return &domain.Comment{Body: "❓ **Blocking @pavel**: новый предмет — цена.\nsupersedes: ответ «да - ок» не покрывает цену"}
		},
		"metadata": func() *domain.Comment {
			m, _ := json.Marshal(map[string]string{"supersedes": "цена не входила в ответ"})
			return &domain.Comment{Body: "❓ **Blocking @pavel**: новый предмет — цена.", Metadata: m}
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := setupTriageEnv(t, true)
			taskID := env.seedTask(env.inProgressID)
			askerID := uuid.New()
			env.seedAgentBlockingComment(taskID, askerID)
			env.seedUserReply(taskID, "да - ок", frozenTime.Add(-time.Hour))

			c := mk()
			c.TaskID, c.AuthorID, c.AuthorType = taskID, askerID, domain.ActorTypeAgent
			ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
			require.NoError(t, env.svc.Create(ctx, c))
			assert.Empty(t, env.repeatAskNotices())
			assert.NotEmpty(t, env.taskMover.humanGateArmCalls(), "explicit supersedes must arm")
		})
	}
}

func TestEnforceBlockingTriage_MarkerWithNoAnswerYet_Arms(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	// only a fiddler comment after the marker — not an answer
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID: cid, TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "[fiddler] session restored", CreatedAt: frozenTime.Add(-time.Hour),
	}
	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: ещё раз, ответа не было",
	}))
	assert.Empty(t, env.repeatAskNotices())
	assert.NotEmpty(t, env.taskMover.humanGateArmCalls())
}

// Path 2b: a recorded decision (human-gate-decisions) counts as an answer too.
func TestEnforceBlockingTriage_MarkerAfterRecordedDecision_NotArmed(t *testing.T) {
	env, hgdRepo := setupHGDTriageEnv(t)
	taskID := env.seedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	q := "Павел в TG: да, ок"
	require.NoError(t, hgdRepo.Create(context.Background(), &domain.HumanGateDecision{
		TaskID: taskID, CanonicalKey: ptr("privacy-policy"), DecidedBy: uuid.New(), Quote: &q,
		Provenance: ptr(domain.HumanGateProvenanceDirect), Channel: ptr(domain.HumanGateChannelMesh),
		CreatedAt: frozenTime.Add(-time.Hour),
	}))
	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: утвердить текст?",
	}))
	assert.Empty(t, env.taskMover.humanGateArmCalls())
	require.Len(t, env.repeatAskNotices(), 1)
}

// Path 3: hard-class default notice never presents the default as an auto-decision.
func TestPostMarkerDefaultAppliedComment_HardClass_NoAutoDecisionWording(t *testing.T) {
	svc, repo, commentRepo, taskID := newArmingTestServiceWithComments(t)
	repo.items[taskID].HumanGateClass = domain.HumanGateClassHard
	svc.(*taskService).postMarkerDefaultAppliedComment(context.Background(), domain.ArmHumanGateInput{TaskID: taskID})
	c := firstCommentOn(commentRepo, taskID)
	require.NotNil(t, c)
	assert.Contains(t, c.Body, "авто-применения не будет")
	assert.NotContains(t, c.Body, domain.DefaultMarkerRecommendedDefault)
	assert.NotContains(t, c.Body, "применится автоматически")
}

func TestPostMarkerDefaultAppliedComment_SoftClass_KeepsDefault(t *testing.T) {
	svc, repo, commentRepo, taskID := newArmingTestServiceWithComments(t)
	repo.items[taskID].HumanGateClass = domain.HumanGateClassSoft
	svc.(*taskService).postMarkerDefaultAppliedComment(context.Background(), domain.ArmHumanGateInput{TaskID: taskID})
	c := firstCommentOn(commentRepo, taskID)
	require.NotNil(t, c)
	assert.Contains(t, c.Body, domain.DefaultMarkerRecommendedDefault)
}

// Review finding: an unrelated human remark while the ask is still live is not
// an answer — the new marker must still arm (reaffirm).
func TestEnforceBlockingTriage_UnrelatedHumanCommentWhileGateUp_StillArms(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	env.seedUserReply(taskID, "кстати, посмотри ещё логи за вчера", frozenTime.Add(-time.Hour))

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: всё ещё нужен выбор A/Б",
	}))
	assert.Empty(t, env.repeatAskNotices())
	assert.NotEmpty(t, env.taskMover.humanGateArmCalls())
}

// A direct reply to the marker is an answer even if the gate flag is still up.
func TestEnforceBlockingTriage_DirectReplyWhileGateUp_NotArmed(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	var markerID uuid.UUID
	for id, c := range env.commentRepo.items {
		if hasBlockingMarker(c.Body) {
			markerID = id
		}
	}
	rid := uuid.New()
	env.commentRepo.items[rid] = &domain.Comment{
		ID: rid, TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		Body: "вариант A", ParentCommentID: &markerID, CreatedAt: frozenTime.Add(-time.Hour),
	}
	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: подтвердить A?",
	}))
	assert.Empty(t, env.taskMover.humanGateArmCalls())
	require.Len(t, env.repeatAskNotices(), 1)
}
