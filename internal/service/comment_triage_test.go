package service

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// blockingMarkerRegex / hasBlockingMarker
// ---------------------------------------------------------------------------

func TestHasBlockingMarker(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"full marker", "❓ **Blocking @pavel**: need decision X", true},
		{"no emoji", "**Blocking @pavel**: x", true},
		{"no bold no emoji", "Blocking @pavel needs you", true},
		{"emoji no bold", "❓ Blocking @pavel", true},
		{"lowercase keyword", "blocking @pavel", true},
		{"leading indent", "   ❓ **Blocking @pavel**", true},
		{"marker on second line", "context here\n❓ **Blocking @pavel**: ask", true},
		{"hyphenated slug", "❓ **Blocking @mary-jane**", true},
		{"FYI is not blocking", "ℹ️ **FYI @pavel**: deployed Y", false},
		{"quoted line not matched", "> ❓ **Blocking @pavel**", false},
		{"keyword mid-line not matched", "I am not blocking anyone @pavel", false},
		{"single-char slug too short", "❓ **Blocking @a**", false},
		{"plain text", "just a normal comment @pavel", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasBlockingMarker(tt.body))
		})
	}
}

// ---------------------------------------------------------------------------
// enforceBlockingTriage (via Create / Update)
// ---------------------------------------------------------------------------

type triageTestEnv struct {
	svc          *commentService
	commentRepo  *MockCommentRepository
	taskRepo     *MockTaskRepository
	statusRepo   *MockTaskStatusRepository
	userRepo     *MockUserRepository
	taskMover    *fakeTaskMover
	projID       uuid.UUID
	wsID         uuid.UUID
	inProgressID uuid.UUID
	triageID     uuid.UUID
	activityRepo *MockActivityLogRepository
}

// setupTriageEnv wires a commentService with the deps the enforcement path needs.
// When withTriageColumn is false the project has no triage status (graceful no-op case).
func setupTriageEnv(t *testing.T, withTriageColumn bool) triageTestEnv {
	t.Helper()
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectRepo := NewMockProjectRepository()
	userRepo := NewMockUserRepository()
	taskMover := &fakeTaskMover{}

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	inProgressID := uuid.New()
	statusRepo.items[inProgressID] = &domain.TaskStatus{
		ID: inProgressID, ProjectID: projID, Category: domain.StatusCategoryInProgress, Name: "In Progress",
	}
	triageID := uuid.New()
	if withTriageColumn {
		statusRepo.items[triageID] = &domain.TaskStatus{
			ID: triageID, ProjectID: projID, Category: domain.StatusCategoryTriage, Name: "Triage",
		}
	}

	// Pavel is a human user; agents (e.g. @linus) are intentionally NOT registered here.
	userRepo.AddUser(wsID, &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"})

	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentProjectRepo(projectRepo),
		WithCommentStatusRepo(statusRepo),
		WithCommentUserRepo(userRepo),
		WithCommentTaskService(taskMover),
	).(*commentService)

	return triageTestEnv{
		svc, commentRepo, taskRepo, statusRepo, userRepo, taskMover,
		projID, wsID, inProgressID, triageID, activityRepo,
	}
}

// systemComments returns all system-authored comments persisted in the repo.
func (env triageTestEnv) systemComments() []domain.Comment {
	var out []domain.Comment
	for _, c := range env.commentRepo.items {
		if c.AuthorType == domain.ActorTypeSystem {
			out = append(out, *c)
		}
	}
	return out
}

// seedTask inserts a task with the given status and returns its ID.
func (env triageTestEnv) seedTask(statusID uuid.UUID) uuid.UUID {
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: statusID, Title: "T",
	}
	return taskID
}

func TestEnforceBlockingTriage_BlockingHuman_MovesToTriage(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: need decision X",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	moves := env.taskMover.calls()
	require.Len(t, moves, 1)
	assert.Equal(t, taskID, moves[0].taskID)
	require.NotNil(t, moves[0].input.StatusID)
	assert.Equal(t, env.triageID, *moves[0].input.StatusID)

	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Equal(t, domain.ActorTypeSystem, sys[0].AuthorType)
	assert.Contains(t, sys[0].Body, "переведена в triage")
	assert.Contains(t, sys[0].Body, "@pavel")
}

func TestEnforceBlockingTriage_FYIMarker_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "ℹ️ **FYI @pavel**: deployed Y",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls())
	assert.Empty(t, env.systemComments())
}

func TestEnforceBlockingTriage_AgentMentionOnly_NoTriage(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	// @linus is an agent (never registered as a user) → human-gate blocks the move.
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @linus**: help debug",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls())
	assert.Empty(t, env.systemComments())
}

// TestEnforceBlockingTriage_TypoSlug_NoOp documents behavior when the "Blocking" marker
// names a slug that does not resolve to any user — typo, unregistered account, or an
// agent slug all land here. This is a safe no-op: no triage move, no human_gate, no
// system comment. (It now also emits a [comment-triage] WARNING log for observability —
// task #2b600b3f — but the no-op behavior itself is unchanged and intentional.)
func TestEnforceBlockingTriage_TypoSlug_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	// "pavvel" is a typo of the registered "pavel" user — never resolves.
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavvel**: need decision X",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls(), "typo'd slug must not trigger triage move")
	assert.Empty(t, env.taskMover.humanGateCalls(), "typo'd slug must not arm human_gate")
	assert.Empty(t, env.systemComments())
}

// TestEnforceBlockingTriage_UnrelatedEarlierMention_DoesNotMisattribute is the regression
// test for #2b600b3f: before the fix, the human-gate check scanned ALL @-mentions
// anywhere in the comment body (in order) and used the first one that resolved to a
// user — NOT specifically the slug the "Blocking" marker names. A comment that cc's a
// real, registered user earlier in the text while the actual Blocking target is an
// unregistered/typo'd slug would incorrectly fire triage attributed to the cc'd user.
func TestEnforceBlockingTriage_UnrelatedEarlierMention_DoesNotMisattribute(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	// @pavel is a registered human mentioned BEFORE the marker, purely as a cc — the
	// actual Blocking target "unregistered-slug" never resolves.
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "cc @pavel for visibility\n\n❓ **Blocking @unregistered-slug**: need input",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls(), "must not triage on an unrelated earlier mention")
	assert.Empty(t, env.taskMover.humanGateCalls())
	assert.Empty(t, env.systemComments())
}

func TestEnforceBlockingTriage_MultiMentionWithHuman_MovesToTriage(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel @linus**: need both",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	moves := env.taskMover.calls()
	require.Len(t, moves, 1)
	assert.Equal(t, env.triageID, *moves[0].input.StatusID)

	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "@pavel") // the human slug is named in the system comment
}

func TestEnforceBlockingTriage_AlreadyTriage_Idempotent(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.triageID) // task already sits in triage

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: still blocked",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls(), "must not re-move a task already in triage")
	assert.Empty(t, env.systemComments())
}

func TestEnforceBlockingTriage_EditAddsMarker_FiresOnce(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	authorID := uuid.New()
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   authorID,
		AuthorType: domain.ActorTypeUser,
		Body:       "just discussing this", // no marker yet
	}

	ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeUser)
	updated := &domain.Comment{ID: cid, Body: "❓ **Blocking @pavel**: now blocked"}
	require.NoError(t, env.svc.Update(ctx, updated))

	moves := env.taskMover.calls()
	require.Len(t, moves, 1)
	assert.Equal(t, env.triageID, *moves[0].input.StatusID)
}

func TestEnforceBlockingTriage_EditMarkerAlreadyPresent_NoReFire(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	authorID := uuid.New()
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   authorID,
		AuthorType: domain.ActorTypeUser,
		Body:       "❓ **Blocking @pavel**: blocked", // marker already there
	}

	ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeUser)
	// Editing the tail while the marker persists must not re-trigger a move.
	updated := &domain.Comment{ID: cid, Body: "❓ **Blocking @pavel**: blocked (typo fix)"}
	require.NoError(t, env.svc.Update(ctx, updated))

	assert.Empty(t, env.taskMover.calls())
}

func TestEnforceBlockingTriage_NoTriageColumn_GracefulNoOp(t *testing.T) {
	env := setupTriageEnv(t, false) // project has no triage status
	taskID := env.seedTask(env.inProgressID)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: need decision",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls())
	assert.Empty(t, env.systemComments())
}

func TestEnforceBlockingTriage_QuotedMarker_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "> ❓ **Blocking @pavel**\n\nquoting an old question, not a new blocker",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls())
	assert.Empty(t, env.systemComments())
}

func TestEnforceBlockingTriage_AutoMode_SkipsTriage(t *testing.T) {
	// auto delegation_level: agents self-manage, so blocking markers must NOT trigger
	// triage escalation even when a human is mentioned.
	env := setupTriageEnv(t, true)
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.inProgressID,
		Title: "Auto task", DelegationLevel: domain.DelegationLevelAuto,
	}

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: need decision",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls(), "auto-mode must not trigger triage move")
	assert.Empty(t, env.systemComments(), "auto-mode must not create system comment")
}

// Auto-mode tasks skip triage escalation but must still have human_gate set
// so the MoveTask freeze gate protects them (audit P0 #3).
func TestEnforceBlockingTriage_AutoMode_SetsHumanGateFlag(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.inProgressID,
		Title: "Auto task", DelegationLevel: domain.DelegationLevelAuto,
	}

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: need sign-off",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	// The triage move must NOT happen for auto tasks.
	assert.Empty(t, env.taskMover.calls(), "auto-mode must not trigger triage move")
	// But SetHumanGate must be called so the MoveTask freeze gate protects the task.
	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "SetHumanGate must be called exactly once")
	assert.Equal(t, taskID, gateCalls[0].taskID)
	assert.True(t, gateCalls[0].value, "SetHumanGate must arm the flag (value=true)")
}

// ---------------------------------------------------------------------------
// releaseHumanGate (via Create)
// ---------------------------------------------------------------------------

// seedGatedTask inserts a task with human_gate=true and returns its ID.
func (env triageTestEnv) seedGatedTask(statusID uuid.UUID) uuid.UUID {
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: statusID, Title: "Gated", HumanGate: true,
	}
	return taskID
}

// seedBlockingComment pre-inserts a "❓ Blocking @pavel" comment on taskID so that
// releaseHumanGate finds a prior blocking signal when a user comment arrives.
func (env triageTestEnv) seedBlockingComment(taskID uuid.UUID) {
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: need your approval",
	}
}

func TestReleaseHumanGate_UserCommentAfterBlock_ReleasesFlag(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	env.seedBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Ок, делайте деплой.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	// SetHumanGate(false) must have been called.
	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1)
	assert.Equal(t, taskID, gateCalls[0].taskID)
	assert.False(t, gateCalls[0].value, "gate must be cleared (value=false)")

	// A system comment must record the release.
	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "human_gate снят")

	// No triage move must have been triggered (user comment has no blocking marker).
	assert.Empty(t, env.taskMover.calls())
}

func TestReleaseHumanGate_NoPriorBlockingComment_NoOp(t *testing.T) {
	// task.human_gate=true but no backing blocking comment (manual PATCH scenario)
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	// no seedBlockingComment

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Checking in on this.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(), "must not release when no prior blocking comment")
	assert.Empty(t, env.systemComments())
}

func TestReleaseHumanGate_AgentComment_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	env.seedBlockingComment(taskID)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "Still waiting on Pavel.",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.humanGateCalls(), "agent comment must not release the gate")
	assert.Empty(t, env.systemComments())
}

func TestReleaseHumanGate_TaskNotGated_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID) // gate not set — default task
	env.seedBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Looks good to me.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(), "gate already false — no call needed")
	assert.Empty(t, env.systemComments())
}

// ---------------------------------------------------------------------------
// Bug fix: completion reports from the assignee must NOT trigger triage
// ---------------------------------------------------------------------------

// TestEnforceBlockingTriage_AssigneeCompletionReport_NoTriage verifies that when
// the task's own assignee writes a completion summary that incidentally contains a
// "❓ Blocking @pavel" marker (e.g. "Done. ❓ Blocking @pavel: please close"), the
// task is NOT moved to triage and the human_gate flag is NOT armed.
// Regression: task #0a46e636 was re-triaged when Garfield wrote the final summary.
func TestEnforceBlockingTriage_AssigneeCompletionReport_NoTriage(t *testing.T) {
	env := setupTriageEnv(t, true)
	assigneeID := uuid.New()
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    env.projID,
		StatusID:     env.inProgressID,
		Title:        "T",
		AssigneeID:   &assigneeID,
		AssigneeType: domain.AssigneeTypeAgent,
	}

	// Completion report that contains a blocking marker as handoff context.
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   assigneeID,
		AuthorType: domain.ActorTypeAgent,
		Body: `## Все 4 фикса выполнены — работа завершена

Фикс 1: ✅ Фикс 2: ✅ Фикс 3: ✅ Фикс 4: ✅

❓ **Blocking @pavel**: задача на supervised — закрой вручную.`,
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls(), "completion report from assignee must not trigger triage move")
	assert.Empty(t, env.taskMover.humanGateCalls(), "completion report from assignee must not arm human_gate")
	assert.Empty(t, env.systemComments(), "no system comment expected")
}

// TestEnforceBlockingTriage_ThirdPartyBlockingMarker_TriagesNormally confirms that a
// blocking marker from a NON-assignee agent still triggers triage as before.
func TestEnforceBlockingTriage_ThirdPartyBlockingMarker_TriagesNormally(t *testing.T) {
	env := setupTriageEnv(t, true)
	assigneeID := uuid.New()
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    env.projID,
		StatusID:     env.inProgressID,
		Title:        "T",
		AssigneeID:   &assigneeID,
		AssigneeType: domain.AssigneeTypeAgent,
	}

	// A DIFFERENT agent (not the assignee) reports a blocker.
	otherAgentID := uuid.New()
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   otherAgentID, // not the assignee
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: unblocking decision needed from the lead",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	moves := env.taskMover.calls()
	require.Len(t, moves, 1, "third-party blocking marker must still trigger triage")
	assert.Equal(t, env.triageID, *moves[0].input.StatusID)
}

// TestEnforceBlockingTriage_AssigneeBlockerNoCompletion_Triages confirms that an
// assignee's comment that contains a blocking marker WITHOUT completion keywords
// still triggers triage (the assignee IS genuinely blocked).
func TestEnforceBlockingTriage_AssigneeBlockerNoCompletion_Triages(t *testing.T) {
	env := setupTriageEnv(t, true)
	assigneeID := uuid.New()
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID:           taskID,
		ProjectID:    env.projID,
		StatusID:     env.inProgressID,
		Title:        "T",
		AssigneeID:   &assigneeID,
		AssigneeType: domain.AssigneeTypeAgent,
	}

	// Assignee is genuinely blocked — no completion keywords.
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   assigneeID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: need credentials for staging DB before I can continue.",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	moves := env.taskMover.calls()
	require.Len(t, moves, 1, "assignee with no completion keywords must still trigger triage")
	assert.Equal(t, env.triageID, *moves[0].input.StatusID)
}

// TestEnforceBlockingTriage_UnrelatedCompletionWordFarFromMarker_ArmsGate is the
// regression for task #69fbb698: the server-side human_gate flag never got armed
// on task #e8b2c765 because the completion-report heuristic scanned the ENTIRE
// comment body for keywords like "завершен", and this assignee's long analytical
// comment happened to mention an unrelated "Helsinki-миграция ... завершена"
// (infra migration complete) thousands of characters before its genuinely live
// "❓ Blocking @pavel" question. That false match suppressed enforceBlockingTriage
// before it ever reached SetHumanGate, leaving the cheap human_gate.is_human_gated
// path fleet-wide blind to this task. Fix: only scan a bounded window immediately
// preceding the marker (completionKeywordSearchWindow).
func TestEnforceBlockingTriage_UnrelatedCompletionWordFarFromMarker_ArmsGate(t *testing.T) {
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

	filler := strings.Repeat("padding текста без блокирующих слов. ", 40)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   assigneeID,
		AuthorType: domain.ActorTypeAgent,
		Body: "Helsinki-миграция инфраструктурно завершена — все продукты за hel01 на " +
			"приватном мосту. " + filler +
			"\n\n❓ **Blocking @pavel**: какой из двух живых инстансов останавливать?",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	// auto-mode: no triage move, but the sticky gate must still be armed.
	assert.Empty(t, env.taskMover.calls(), "auto-mode must not trigger triage move")
	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "an unrelated completion word far from the marker must not suppress SetHumanGate")
	assert.Equal(t, taskID, gateCalls[0].taskID)
	assert.True(t, gateCalls[0].value, "SetHumanGate must arm the flag (value=true)")
}

// ---------------------------------------------------------------------------
// Bug fix: Pavel's response must move the task from triage → in_progress
// ---------------------------------------------------------------------------

// TestReleaseHumanGate_TriageTask_MovesToInProgress verifies that when Pavel
// comments on a human-gated task currently in triage, the server:
//  1. Clears human_gate;
//  2. Moves the task from triage → in_progress (enforceTriageRelease);
//  3. Writes a system comment mentioning both the gate release and the status change.
func TestReleaseHumanGate_TriageTask_MovesToInProgress(t *testing.T) {
	env := setupTriageEnv(t, true)

	// Task starts in triage with human_gate=true.
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.triageID, Title: "Gated in triage",
		HumanGate: true,
	}
	env.seedBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Ок, делайте деплой.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	// 1. human_gate must be cleared.
	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1)
	assert.False(t, gateCalls[0].value, "human_gate must be cleared (value=false)")

	// 2. Task must be moved from triage to in_progress.
	moves := env.taskMover.calls()
	require.Len(t, moves, 1, "task must be moved from triage to in_progress")
	require.NotNil(t, moves[0].input.StatusID)
	assert.Equal(t, env.inProgressID, *moves[0].input.StatusID, "destination must be in_progress")

	// 3. System comment must mention both gate release and status change.
	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "human_gate снят")
	assert.Contains(t, sys[0].Body, "in_progress")
}

// TestReleaseHumanGate_InProgressTask_NoStatusMove verifies that when a human-gated
// task is already in in_progress (not in triage), Pavel's response clears the gate
// flag but does NOT trigger any MoveTask call.
func TestReleaseHumanGate_InProgressTask_NoStatusMove(t *testing.T) {
	env := setupTriageEnv(t, true)

	// Task is in in_progress (not triage) with human_gate=true.
	taskID := env.seedGatedTask(env.inProgressID)
	env.seedBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Go ahead.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	// Gate must be cleared.
	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1)
	assert.False(t, gateCalls[0].value)

	// No MoveTask call since task is already in in_progress.
	assert.Empty(t, env.taskMover.calls(), "no move expected when task not in triage")

	// System comment must mention gate release but NOT in_progress.
	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "human_gate снят")
	assert.NotContains(t, sys[0].Body, "in_progress")
}

// Without a TaskService wired, the enforcement path is skipped entirely (no panic).
func TestEnforceBlockingTriage_NoTaskService_SkipsSafely(t *testing.T) {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	projectRepo := NewMockProjectRepository()
	statusRepo := NewMockTaskStatusRepository()
	userRepo := NewMockUserRepository()

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}
	statusID := uuid.New()
	statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projID, Category: domain.StatusCategoryInProgress}
	userRepo.AddUser(wsID, &domain.User{ID: uuid.New(), Username: "pavel"})
	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentProjectRepo(projectRepo),
		WithCommentStatusRepo(statusRepo),
		WithCommentUserRepo(userRepo),
		// No WithCommentTaskService.
	)

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projID, StatusID: statusID}

	comment := &domain.Comment{TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent, Body: "❓ **Blocking @pavel**: x"}
	require.NoError(t, svc.Create(context.Background(), comment))
}

// ---------------------------------------------------------------------------
// enforceTriageExit — general triage EXIT rule
// ---------------------------------------------------------------------------

// seedTriageTask inserts a task in triage without human_gate and returns its ID.
func (env triageTestEnv) seedTriageTask() uuid.UUID {
	return env.seedTask(env.triageID)
}

// seedRealBlockingComment pre-inserts a genuine "❓ Blocking @user" comment from an
// agent (non-auto, no negators) so that enforceTriageExit finds a real block.
func (env triageTestEnv) seedRealBlockingComment(taskID uuid.UUID) {
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: need your decision on option B",
	}
}

// TestEnforceTriageExit_HumanCommentAfterRealBlock_MovesToInProgress verifies the core
// happy-path: a task in triage (no human_gate) with a prior real block exits to in_progress
// when a human user comments.
func TestEnforceTriageExit_HumanCommentAfterRealBlock_MovesToInProgress(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTriageTask()
	env.seedRealBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Выбирайте option B, не ждите.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	moves := env.taskMover.calls()
	require.Len(t, moves, 1, "task must be moved from triage to in_progress")
	require.NotNil(t, moves[0].input.StatusID)
	assert.Equal(t, env.inProgressID, *moves[0].input.StatusID, "destination must be in_progress")

	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "triage → in_progress")
}

// TestEnforceTriageExit_AgentComment_NoOp ensures an agent comment does not trigger the EXIT rule.
func TestEnforceTriageExit_AgentComment_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTriageTask()
	env.seedRealBlockingComment(taskID)

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "Still waiting on Pavel to answer.",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	assert.Empty(t, env.taskMover.calls(), "agent comment must not trigger triage exit")
	assert.Empty(t, env.systemComments())
}

// TestEnforceTriageExit_HumanGateSet_NoOp confirms that when human_gate=true the EXIT
// rule is skipped (releaseHumanGate owns that path and avoids a double MoveTask).
func TestEnforceTriageExit_HumanGateSet_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	// Task in triage with human_gate=true: releaseHumanGate handles the move.
	taskID := env.seedGatedTask(env.triageID)
	env.seedRealBlockingComment(taskID)
	env.seedBlockingComment(taskID) // ensure releaseHumanGate also has its blocker

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Ок, деплойте.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	// releaseHumanGate fires and produces exactly ONE move; enforceTriageExit must not add a second.
	moves := env.taskMover.calls()
	require.Len(t, moves, 1, "only one MoveTask call expected (from releaseHumanGate, not double)")
}

// TestEnforceTriageExit_NoPriorRealBlock_NoOp: no prior blocking comment → no exit.
func TestEnforceTriageExit_NoPriorRealBlock_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTriageTask()
	// No seedRealBlockingComment.

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Checking in on this.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.calls(), "no real block found — must not exit triage")
	assert.Empty(t, env.systemComments())
}

// TestEnforceTriageExit_AutoMarkerBlock_NoOp: a prior auto-generated comment that
// happens to contain a blocking marker must NOT count as a real block.
func TestEnforceTriageExit_AutoMarkerBlock_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTriageTask()
	// Seed an auto-generated comment that looks like a blocker but is from the server.
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "🤖 auto: задача переведена в triage — ❓ Blocking @pavel зафиксирован",
	}

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Ок",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.calls(), "auto-marker block must not satisfy the real-block check")
	assert.Empty(t, env.systemComments())
}

// TestEnforceTriageExit_NegatedBlock_NoOp: a prior blocking comment with a negator
// keyword must not count as a live gate.
func TestEnforceTriageExit_NegatedBlock_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTriageTask()
	// Seed a blocking comment that also contains a negator — the block was cancelled.
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: нет, уже resolved — продолжаем сами",
	}

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Ок, видел.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.calls(), "negated block must not satisfy the real-block check")
	assert.Empty(t, env.systemComments())
}

// TestEnforceTriageExit_SupervisedTask_NoOp: supervised tasks must not auto-exit triage.
func TestEnforceTriageExit_SupervisedTask_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.triageID,
		Title: "Supervised", DelegationLevel: domain.DelegationLevelSupervised,
	}
	env.seedRealBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Начинайте.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.calls(), "supervised task must not auto-exit triage")
	assert.Empty(t, env.systemComments())
}

// TestEnforceTriageExit_TaskAssignedToUser_NoOp: if the task is assigned to a human
// user, auto-exit must be skipped (user owns the triage slot).
func TestEnforceTriageExit_TaskAssignedToUser_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	pavelUID := uuid.New()
	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.triageID,
		Title: "Pavel's own task", AssigneeID: &pavelUID, AssigneeType: domain.AssigneeTypeUser,
	}
	env.seedRealBlockingComment(taskID)

	ctx := actorctx.WithActor(context.Background(), pavelUID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelUID,
		AuthorType: domain.ActorTypeUser,
		Body:       "I'll look at this tomorrow.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.calls(), "user-assigned task must not auto-exit triage")
	assert.Empty(t, env.systemComments())
}

// TestEnforceTriageExit_TaskNotInTriage_NoOp: if the task is NOT in triage, the exit rule
// must be a no-op.
func TestEnforceTriageExit_TaskNotInTriage_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID) // in_progress, not triage
	env.seedRealBlockingComment(taskID)

	pavelID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), pavelID, domain.ActorTypeUser)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   pavelID,
		AuthorType: domain.ActorTypeUser,
		Body:       "Looks good.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.calls(), "task not in triage — exit rule must be no-op")
	assert.Empty(t, env.systemComments())
}

// ---------------------------------------------------------------------------
// releaseHumanGateOnWithdrawal (via Create) — task #c375905c
// ---------------------------------------------------------------------------

// seedAgentBlockingComment inserts a "❓ Blocking @pavel" comment authored by
// authorID, so ownership of the live ask can be pinned to a specific agent.
func (env triageTestEnv) seedAgentBlockingComment(taskID, authorID uuid.UUID) {
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   authorID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: нужен выбор варианта A/Б",
		// Explicit, in the past relative to frozenTime (what the withdrawal
		// comment created via svc.Create receives): releaseHumanGateOnWithdrawal
		// scans in real chronological order, so tests with more than one
		// marker comment need genuine, distinct timestamps, not insertion order.
		CreatedAt: frozenTime.Add(-2 * time.Hour),
	}
}

// TestReleaseHumanGateOnWithdrawal_SameAgentNegates_ReleasesFlag is AC1's happy
// path: the same agent who raised the still-live ask posts a withdrawal, and the
// gate clears so move_task(done) is no longer blocked.
func TestReleaseHumanGateOnWithdrawal_SameAgentNegates_ReleasesFlag(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Blocker самоустранился, ask не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "SetHumanGate must be called exactly once")
	assert.Equal(t, taskID, gateCalls[0].taskID)
	assert.False(t, gateCalls[0].value, "gate must be cleared (value=false)")

	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "human_gate снят")
	assert.Contains(t, sys[0].Body, "отозвал его сам")
}

// TestReleaseHumanGateOnWithdrawal_DifferentAgent_NoOp is the core negative
// control from the task's own AC2: an agent OTHER than the one who raised the
// ask cannot silence it, even with a negator phrase — otherwise the fleet
// learns to bypass its own Pavel gate, which is worse than the original bug.
func TestReleaseHumanGateOnWithdrawal_DifferentAgent_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	bystanderID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), bystanderID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   bystanderID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Ask не нужен, снимаю за коллегу.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(), "a different agent must not release someone else's ask")
	assert.Empty(t, env.systemComments())
}

// TestReleaseHumanGateOnWithdrawal_NoNegator_NoOp is AC2's other half: a live,
// unwithdrawn ask must not clear just because the SAME asker comments again
// without an actual negator — ordinary progress updates must not accidentally
// release the gate.
func TestReleaseHumanGateOnWithdrawal_NoNegator_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Всё ещё жду ответа.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(), "no negator present — the live ask must stay gated")
	assert.Empty(t, env.systemComments())
}

// TestReleaseHumanGateOnWithdrawal_ReaffirmedByOtherAgent_NoOp exercises the
// "chronologically LAST" ownership rule: agent A raises the ask, agent B later
// reaffirms it (a fresh, non-negated marker) — ownership of the LIVE ask moves
// to B. A's later negator must not release a gate B is now the owner of.
func TestReleaseHumanGateOnWithdrawal_ReaffirmedByOtherAgent_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	agentA := uuid.New()
	agentB := uuid.New()
	env.seedAgentBlockingComment(taskID, agentA) // CreatedAt: frozenTime - 2h
	// B reaffirms — a second, later (frozenTime - 1h), non-negated marker.
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   agentB,
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: подтверждаю, вопрос всё ещё живой",
		CreatedAt:  frozenTime.Add(-1 * time.Hour),
	}

	ctx := actorctx.WithActor(context.Background(), agentA, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   agentA,
		AuthorType: domain.ActorTypeAgent,
		Body:       "С моей стороны ask не нужен, снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(), "B now owns the live ask — A's withdrawal must not release it")
}

// TestReleaseHumanGateOnWithdrawal_MarkerWithUnrelatedNegatorProse_NotSelfNegated
// reproduces the live incident found by Riker on #7f646f08 (2026-07-30): a
// comment that raises a FRESH marker as its final line, but ALSO discusses a
// DIFFERENT, already-resolved ask earlier in the same body using a negator
// word ("снят"), must still be recognised as a live, un-negated marker — the
// unrelated prose must not make the comment self-negate its own new ask.
// Before the fix, hasNegatorInScope's whole-body predecessor found "снят"
// anywhere in agent B's comment and skipped it as "already negated", so
// ownership of the live ask silently stayed with agent A — exactly what
// stranded #7f646f08 after Riker's real withdrawal comment.
func TestReleaseHumanGateOnWithdrawal_MarkerWithUnrelatedNegatorProse_NotSelfNegated(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	agentA := uuid.New()
	agentB := uuid.New()
	env.seedAgentBlockingComment(taskID, agentA) // CreatedAt: frozenTime - 2h

	// B's comment: analysis discussing an OLD, unrelated ask being "снят" (an
	// SSH-access ask that resolved itself), THEN a brand-new marker as the
	// operative final line. This is the actual shape of Riker's 00:12 comment.
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   agentB,
		AuthorType: domain.ActorTypeAgent,
		Body: "Старый аск на SSH-доступ снят — доступ приехал сам с CI-фиксом.\n\n" +
			"❓ **Blocking @pavel**: закрой эту карточку кнопкой.",
		CreatedAt: frozenTime.Add(-1 * time.Hour),
	}

	// B's own later withdrawal — no marker, plain negator.
	ctx := actorctx.WithActor(context.Background(), agentB, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   agentB,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Блокер самоустранился, ask больше не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1,
		"B's marker must be recognised as live (not self-negated by unrelated earlier prose), so B's own withdrawal must release the gate")
	assert.False(t, gateCalls[0].value)
}

// TestReleaseHumanGateOnWithdrawal_NoPriorMarker_NoOp: task.HumanGate=true with
// no backing marker comment at all (e.g. a manual PATCH) must fail closed —
// nothing to withdraw, so nothing is released.
func TestReleaseHumanGateOnWithdrawal_NoPriorMarker_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Не нужен, снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls())
}

// TestReleaseHumanGateOnWithdrawal_TaskNotGated_NoOp: gate already false — no
// SetHumanGate call needed regardless of body content.
func TestReleaseHumanGateOnWithdrawal_TaskNotGated_NoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID) // human_gate=false by default
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Не нужен, снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls())
}

// TestReleaseHumanGateOnWithdrawal_TriageTask_MovesToInProgress mirrors
// TestReleaseHumanGate_TriageTask_MovesToInProgress for the agent-withdrawal path.
func TestReleaseHumanGateOnWithdrawal_TriageTask_MovesToInProgress(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := uuid.New()
	askerID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, StatusID: env.triageID, Title: "Gated in triage",
		HumanGate: true,
	}
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Blocker снят.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1)
	assert.False(t, gateCalls[0].value)

	moves := env.taskMover.calls()
	require.Len(t, moves, 1, "task must be moved from triage to in_progress")
	require.NotNil(t, moves[0].input.StatusID)
	assert.Equal(t, env.inProgressID, *moves[0].input.StatusID)

	sys := env.systemComments()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0].Body, "human_gate снят")
	assert.Contains(t, sys[0].Body, "in_progress")
}

// TestReleaseHumanGateOnWithdrawal_NotifiesAssigneeAgent verifies that once the
// gate is released, the task's assignee agent is woken via AgentNotifyService
// so a fiddler/dispatcher session re-feeds the task on its next cycle — the
// same wake-up guarantee releaseHumanGate already gives the human-release path.
func TestReleaseHumanGateOnWithdrawal_NotifiesAssigneeAgent(t *testing.T) {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectRepo := NewMockProjectRepository()
	taskMover := &fakeTaskMover{}
	agentNotify := NewMockAgentNotifyService()

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}
	inProgressID := uuid.New()
	statusRepo.items[inProgressID] = &domain.TaskStatus{
		ID: inProgressID, ProjectID: projID, Category: domain.StatusCategoryInProgress, Name: "In Progress",
	}
	timeNow = func() time.Time { return frozenTime }

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentProjectRepo(projectRepo),
		WithCommentStatusRepo(statusRepo),
		WithCommentTaskService(taskMover),
		WithCommentAgentNotify(agentNotify),
	).(*commentService)

	assigneeID := uuid.New()
	askerID := uuid.New()
	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projID, StatusID: inProgressID, Title: "Gated",
		HumanGate: true, AssigneeType: domain.AssigneeTypeAgent, AssigneeID: &assigneeID,
	}
	cid := uuid.New()
	commentRepo.items[cid] = &domain.Comment{
		ID: cid, TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: нужен выбор варианта A/Б",
	}

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Ask не нужен, blocker снят.",
	}
	require.NoError(t, svc.Create(ctx, comment))

	require.Len(t, taskMover.humanGateCalls(), 1)
	assert.False(t, taskMover.humanGateCalls()[0].value)

	// Create also fires a generic "task.commented" notification independent of
	// this path — filter to the release-specific event so this test targets
	// exactly what releaseHumanGateOnWithdrawal itself is responsible for.
	var released []AgentNotification
	for _, n := range agentNotify.Calls() {
		if n.EventType == "task.human_gate_released" {
			released = append(released, n)
		}
	}
	require.Len(t, released, 1, "assignee agent must be notified so it re-feeds the task")
	assert.Equal(t, assigneeID, released[0].AgentID)
}

// TestIsAutoGeneratedComment tests the auto-marker detector.
func TestIsAutoGeneratedComment(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"🤖 auto: задача переведена", true},
		{"[fiddler] session checkpoint", true},
		{"переведена в triage по запросу", true},
		{"moving to triage — reason: stale", true},
		{"❓ **Blocking @pavel**: need decision", false},
		{"just a regular agent comment", false},
		{"", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, isAutoGeneratedComment(c.body), "body: %q", c.body)
	}
}

// ---------------------------------------------------------------------------
// stripQuotedSpans / hasNegatorInScope quoting — task #5c69b4e5
//
// A comment that QUOTES the negator vocabulary while explaining the mechanism
// used to perform a withdrawal: live probe #a073a896 released a real human_gate
// 11 ms after a summary comment whose only "не нужен" sat in backticks inside a
// blockquote, and the genuine withdrawal 1.75 s later became a no-op.
// ---------------------------------------------------------------------------

func TestStripQuotedSpans(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"plain text untouched", "ask не нужен", "ask не нужен"},
		{"inline code removed", "цитата (`не нужен`) тут", "цитата () тут"},
		{"double-backtick span with inner backtick", "x ``a ` b`` y", "x  y"},
		{"blockquote line blanked", "before\n> не нужен\nafter", "before\n\nafter"},
		{"indented blockquote blanked", "  >  не нужен", ""},
		{"fenced block blanked, fence survives as blank", "a\n```\nне нужен\n```\nb", "a\n\n\n\nb"},
		{"tilde fence blanked", "a\n~~~\nне нужен\n~~~\nb", "a\n\n\n\nb"},
		{"longer closing fence closes", "```\nx\n````\nне нужен", "\n\n\nне нужен"},
		{"wrong fence char does not close", "```\nне нужен\n~~~\nне нужен", "\n\n\n"},
		{"unterminated fence swallows rest (fail closed)", "a\n```\nне нужен\nи ещё", "a\n\n\n"},
		{"unterminated inline code swallows rest of line only", "a `не нужен\nсразу", "a \nсразу"},
		{"line count always preserved", "a\nb\nc", "a\nb\nc"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripQuotedSpans(tt.body))
		})
	}
}

// liveProbeSummaryBody is the shape of the comment that actually disarmed a live
// gate in probe #a073a896: a blockquoted, backticked citation of the vocabulary
// inside a post-mortem that intended to withdraw nothing.
const liveProbeSummaryBody = "Сводка механизма:\n\n" +
	"> «негатор того же автора 02:58:05.226Z (`не нужен`) → системный коммент…»\n\n" +
	"Разбор выше описывает предыдущий отзыв, а не новый."

func TestHasNegatorInScope_QuotedVocabulary(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"plain assertion", "Blocker самоустранился, ask не нужен — снимаю.", true},
		{"mid-sentence prose still counts", "Блокер снят, вопрос закрыт.", true},
		{"english negator", "Question resolved, closing.", true},
		{"uppercase", "RESOLVED", true},
		{"inline code only", "словарь: `не нужен`, `снят`", false},
		{"fenced block only", "```\nне нужен\nснят\n```", false},
		{"blockquote only", "> ask не нужен", false},
		{"the live probe body that disarmed a real gate", liveProbeSummaryBody, false},
		{"quote plus a real assertion still withdraws", "цитата (`не нужен`)\n\nи да, ask действительно снят", true},
		{"no negator at all", "Всё ещё жду ответа.", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasNegatorInScope(tt.body))
		})
	}
}

// TestHasBlockingMarker_QuotedFormsDoNotArm covers the symmetric arm-side leak:
// documenting the marker template must not raise a one-way Pavel gate. The `> `
// case already worked by accident (the regex is line-anchored); the fenced and
// inline-code cases did not.
func TestHasBlockingMarker_QuotedFormsDoNotArm(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"fenced template", "Как поднять аск:\n```\n❓ **Blocking @pavel**: <вопрос>\n```\n", false},
		{"inline code template", "поставь `❓ **Blocking @pavel**` последним", false},
		{"blockquoted template", "> ❓ **Blocking @pavel**: пример", false},
		{"real marker still arms", "❓ **Blocking @pavel**: нужен выбор A/Б", true},
		{"real marker after a quoted one still arms", "```\n❓ Blocking @pavel\n```\n❓ **Blocking @pavel**: настоящий аск", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasBlockingMarker(tt.body))
		})
	}
}

// TestReleaseHumanGateOnWithdrawal_QuotedNegatorOnly_KeepsGate is this task's AC1:
// with a live human_gate raised by THIS agent, a comment whose only negator sits in
// inline code / a fenced block / a blockquote must leave the gate armed and post no
// system comment. Before the fix each of these released the gate.
func TestReleaseHumanGateOnWithdrawal_QuotedNegatorOnly_KeepsGate(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"inline code", "Механизм: негатор (`не нужен`) того же автора снимает флаг."},
		{"fenced block", "Словарь негаторов:\n```\nне нужен\nснят\nresolved\n```\nЭто справка."},
		{"blockquote", "Цитирую тред:\n> ask не нужен, снят\n\nПросто фиксирую."},
		{"the live probe #a073a896 body", liveProbeSummaryBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTriageEnv(t, true)
			taskID := env.seedGatedTask(env.inProgressID)
			askerID := uuid.New()
			env.seedAgentBlockingComment(taskID, askerID)

			ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
			comment := &domain.Comment{
				TaskID:     taskID,
				AuthorID:   askerID,
				AuthorType: domain.ActorTypeAgent,
				Body:       tc.body,
			}
			require.NoError(t, env.svc.Create(ctx, comment))

			assert.Empty(t, env.taskMover.humanGateCalls(),
				"a QUOTED negator is not a withdrawal — the gate must stay armed")
			assert.Empty(t, env.systemComments())
		})
	}
}

// TestReleaseHumanGateOnWithdrawal_SummaryThenRealWithdrawal reproduces the live
// incident end to end and carries AC2's positive control in the SAME run: the
// documenting comment must be a no-op, and the intentional withdrawal that follows
// it must still work. In probe #a073a896 these were inverted — the summary released
// the gate and the real withdrawal 1.75 s later did nothing.
func TestReleaseHumanGateOnWithdrawal_SummaryThenRealWithdrawal(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)

	summary := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: liveProbeSummaryBody,
	}
	require.NoError(t, env.svc.Create(ctx, summary))
	require.Empty(t, env.taskMover.humanGateCalls(), "the summary must not withdraw anything")

	withdrawal := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Ответ получен, ask больше не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, withdrawal))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "the real withdrawal must still release (PR #451 not broken)")
	assert.Equal(t, taskID, gateCalls[0].taskID)
	assert.False(t, gateCalls[0].value)
	require.Len(t, env.systemComments(), 1)
	assert.Contains(t, env.systemComments()[0].Body, "human_gate снят")
}

// TestReleaseHumanGateOnWithdrawal_MarkerQuotingNegators_KeepsOwnership covers the
// sibling trap of the same root cause (#7f646f08): the OWNERSHIP scan also matched
// negators over the whole body, so a marker comment that discussed earlier asks
// self-negated — ownership of the live ask silently fell back to an older marker by
// a different agent, and the real author's own withdrawal was refused by the
// author-match guard. That guard is untouched here; what is fixed is who it points at.
func TestReleaseHumanGateOnWithdrawal_MarkerQuotingNegators_KeepsOwnership(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	agentA := uuid.New() // older marker, different agent
	agentB := uuid.New() // newest marker — the real owner

	env.seedAgentBlockingComment(taskID, agentA) // frozenTime - 2h

	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID: cid, TaskID: taskID, AuthorID: agentB, AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: закрой карточку кнопкой\n\n" +
			"Контекст: старый аск (ssh) `снят`, и\n> «снятия нет ни в одном скрипте»",
		CreatedAt: frozenTime.Add(-1 * time.Hour),
	}

	ctx := actorctx.WithActor(context.Background(), agentB, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: agentB, AuthorType: domain.ActorTypeAgent,
		Body: "Pavel ответил, ask не нужен.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1,
		"B's marker only QUOTED negators — it is still the live ask, so B may withdraw it")
	assert.False(t, gateCalls[0].value)
}

// TestReleaseHumanGateOnWithdrawal_QuotedNegatorFromBystander_StillNoOp pins the
// governance guard the task explicitly asked not to weaken: stripping quotes must
// not become a side door for a third party.
func TestReleaseHumanGateOnWithdrawal_QuotedNegatorFromBystander_StillNoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	bystanderID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), bystanderID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body: "Ask не нужен, снимаю за коллегу.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls())
	assert.Empty(t, env.systemComments())
}

// ---------------------------------------------------------------------------
// hasNegatorInScope: no-marker whole-body scan over a long, multi-topic report
// — task #1e5be182, live incident on #f46d5589 (Bill, 2026-07-30 13:18Z).
//
// Bill's real comment carried NO blocking marker (it was a status report, not
// itself a fresh ask), so the pre-fix code scoped the ENTIRE ~90-line body.
// Three literal matches fired: "не нужно" inside the unrelated word "мне нужно"
// (a plain cross-word substring accident), "снят" describing the OBSIDIAN
// product line being dropped from scope (a real standalone word, wrong topic),
// and "не нужен" revising away one stale framing of the SAME still-live ask —
// while the comment's own LAST paragraph reaffirmed that ask as the sole
// remaining option. The whole-body scan read any one of these as a withdrawal
// and auto-cleared task #f46d5589's human_gate.
// ---------------------------------------------------------------------------

// billLongStatusReportBody is byte-for-byte the real comment (task #f46d5589,
// author Bill, 2026-07-30T13:18:11Z) that disarmed a live human_gate. Kept
// verbatim rather than trimmed down, because the defect was specifically about
// scanning a body this shape and this long — a shortened fixture would risk
// "fixing" a test that no longer reproduces the incident.
const billLongStatusReportBody = "## Развилка закрыта: **A невозможен** (моя рекомендация была неверной), **C** для Obsidian. Но карточка не закрывается — и ask к Pavel'ю стал обоснованнее, чем был.\n\n" +
	"@deadalus ответил на `#91070c00` за 11 минут и заодно **опроверг мой же вариант A замером**. Признаю прямо: я рекомендовал «DataForSEO как фолбэк, локация Russia» — и локацию не проверил.\n\n" +
	"### A мёртв, доказано контрольным тестом (его вывод)\n\n" +
	"```\n" +
	"POST /v3/keywords_data/google_ads/search_volume/live\n" +
	"  location_code=2840 (US) → Ok.  obsidian sync vol=6600\n" +
	"  location_code=2643 (RU) → Invalid Field: 'location_code'\n" +
	"google_trends/explore      RU → Invalid Field\n" +
	"dataforseo_labs/keyword_ideas  RU → Invalid Field   (US отвечает данными)\n\n" +
	"дамп /v3/keywords_data/google_ads/locations (324 КБ): вхождений \"Russia\" — 0\n" +
	"тарифы аккаунта: движки google/bing/naver, вхождений \"yandex\" — 0\n" +
	"```\n\n" +
	"Форма запроса заведомо валидна — на US все три отдают данные, отвергается именно Россия. Причина внешняя: Google свернул рекламные сервисы в РФ в 2022, а все три эндпоинта производные от Google Ads/Trends. **DataForSEO не заменяет Яндекс ни объёмами, ни трендами, ни SERP.** Хороший урок про мою же рекомендацию: «инструмент оплачен и живёт в проде» ≠ «он умеет то, что мне нужно».\n\n" +
	"### C принят для Obsidian — его линия снята\n\n" +
	"Daedalus замерил обе посадочные: `entire.vc/local-sync/` и `/team-relay/` — `lang=en`, кириллицы **0 символов**, hreflang нет; в Яндекс.Вебмастере заведён только `http://entire.vc/` (не тот хост, что отдаёт страницы), `Top 0 queries`; дистрибуция вся англоязычная — каталог Obsidian, GitHub, forum.obsidian.md, r/ObsidianMD. RU-запрос физически некуда посадить. Шаг RU keyword research из его недельного аудита убирается — решение его, окончательное, и шаблон аудита его же, я туда не лезу.\n\n" +
	"### Почему карточку НЕ закрываю — и почему ask стал сильнее\n\n" +
	"Daedalus прав, что просил не закрывать шире своей линейки. Проверил владельца RU-проперти сам (это моя зона как Content Marketing) — тем же методом:\n\n" +
	"| домен | lang | кириллица | латиница |\n" +
	"|---|---|---:|---:|\n" +
	"| **prototypes.ventures** | **ru** | **7439** | 50005 |\n" +
	"| venture-crew.com | en | 0 | 25407 |\n" +
	"| entire.vc | en | 0 | 103974 |\n\n" +
	"**`prototypes.ventures` — русский сайт.** Единственный русский из трёх. Для него данные о RU-спросе относятся к делу напрямую, а замены, как только что доказано, **не существует ни одной**.\n\n" +
	"Это меняет обоснование ask'а, а не только его судьбу. Пять недель он стоял как «нужно для недельного аудита Obsidian» — слабая формулировка, и по факту неверная: для англоязычных плагинов он был не нужен вообще. Настоящая причина другая и сильнее:\n\n" +
	"> ключ Yandex Cloud — **единственный** способ получить данные о поисковом спросе для нашего единственного русскоязычного сайта. Альтернатива проверена и отсутствует.\n\n" +
	"Не удивлюсь, если он и провисел столько именно потому, что просили не то и не за тем.\n\n" +
	"### Итог по карточке\n\n" +
	"- Область сужена: Obsidian снят, привязка — `prototypes.ventures`.\n" +
	"- Вариант **B** остаётся единственным. Клиент под Yandex Cloud Search API написан и доведён до 401 — ждёт ровно секрета, ничего не выброшено.\n" +
	"- Отдельного пинга Pavel'ю сегодня не делаю: он получил от меня два сообщения (14:23 и briefing 15:25), а карточка и так в его дайджесте. Новая формулировка ask'а теперь живёт здесь, в ней и смысл — следующий раз он увидит уже её, а не прежнюю слабую.\n\n" +
	"ℹ️ **FYI @deadalus** — твоя граница соблюдена: линейку снял, карточку не закрыл, владельца RU-проперти проверил (это оказался я). Замер DataForSEO забрал себе в память как канон, спасибо — он экономит сессию всякому, кто снова предложит «взять RU из DataForSEO»."

func TestHasNegatorInScope_LongMultiTopicReport(t *testing.T) {
	assert.False(t, hasNegatorInScope(billLongStatusReportBody),
		"a long multi-section report whose final paragraph reaffirms the ask must not read as a withdrawal")
}

// TestContainsNegatorWholeWord_CrossWordSubstring pins the "мне нужно" class
// found in the real body above: "не нужно" is a literal substring of "мне
// нужно" purely because "мне" ends in "не" — a plain strings.Contains hit
// spanning two unrelated words with no space between the match and its
// neighbour on one side.
func TestContainsNegatorWholeWord_CrossWordSubstring(t *testing.T) {
	assert.False(t, containsNegatorWholeWord("то, что мне нужно", "не нужно"),
		"\"не нужно\" must not match inside \"мне нужно\" — it's not a standalone word here")
	assert.True(t, containsNegatorWholeWord("ask больше не нужно", "не нужно"),
		"a real standalone occurrence, bounded by spaces, must still match")
}

// TestLastParagraph pins the paragraph-splitting helper directly.
func TestLastParagraph(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"single paragraph, no blank line", "Blocker снят.", "Blocker снят."},
		{"two paragraphs", "Первое.\n\nВторое.", "Второе."},
		{"multiple blank lines between paragraphs", "Первое.\n\n\n\nВторое.", "Второе."},
		{"trailing whitespace trimmed", "Первое.\n\nВторое.  \n\n", "Второе."},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, lastParagraph(tt.body))
		})
	}
}

// TestReleaseHumanGateOnWithdrawal_LongStatusReport_LeavesGateIntact is AC1 of
// task #1e5be182: reproduce the real incident end-to-end. The SAME agent who
// owns the live ask posts the real long report as a plain (marker-less)
// comment — it must NOT release the gate, because the report's own final
// paragraph reaffirms the ask rather than withdrawing it.
func TestReleaseHumanGateOnWithdrawal_LongStatusReport_LeavesGateIntact(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   askerID,
		AuthorType: domain.ActorTypeAgent,
		Body:       billLongStatusReportBody,
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(),
		"the report's mid-body negators are about a different topic / a superseded framing — the live ask must stay gated")
	assert.Empty(t, env.systemComments())
}

// TestReleaseHumanGateOnWithdrawal_LongStatusReportThenRealWithdrawal is AC2:
// the same author's real, focused withdrawal AFTER the long report must still
// release the gate — the fix must not overcorrect into never releasing again.
func TestReleaseHumanGateOnWithdrawal_LongStatusReportThenRealWithdrawal(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)
	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)

	report := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: billLongStatusReportBody,
	}
	require.NoError(t, env.svc.Create(ctx, report))
	require.Empty(t, env.taskMover.humanGateCalls(), "the long report itself must not withdraw")

	withdrawal := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Ключ Yandex Cloud получен от Pavel'я, ask больше не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, withdrawal))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "the real, focused withdrawal that follows must still release the gate")
	assert.False(t, gateCalls[0].value)
}

// TestReleaseHumanGateOnWithdrawal_LongStatusReportFromBystander_StillNoOp is
// AC3: a different agent posting the same long report must not withdraw
// someone else's ask either — the negative control holds regardless of body
// shape.
func TestReleaseHumanGateOnWithdrawal_LongStatusReportFromBystander_StillNoOp(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	bystanderID := uuid.New()
	ctx := actorctx.WithActor(context.Background(), bystanderID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body: billLongStatusReportBody,
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls())
	assert.Empty(t, env.systemComments())
}

// ---------------------------------------------------------------------------
// releaseHumanGateOnWithdrawal: two-comment ownership hijack — task #9959f201,
// live finding by Bill on prod-sha b2a8068 (2026-07-31), following #c375905c.
//
// The existing "last non-negated marker" ownership scan is, on its own, correct
// and required (TestReleaseHumanGateOnWithdrawal_MarkerWithUnrelatedNegatorProse_NotSelfNegated
// above depends on it: a genuinely orphaned ask must stay withdrawable once
// picked up by a different agent). The composition is the bug: nothing stopped
// that SAME agent from posting the reaffirming marker and the negator back to
// back, becoming owner and releasing owner in one uninterrupted turn, with zero
// permission check and — before this fix — zero activity_log trace.
// ---------------------------------------------------------------------------

// TestReleaseHumanGateOnWithdrawal_TwoCommentHijack_Blocked is the task's AC1:
// reproduces Bill's live repro exactly. agentB, a bystander to agentA's live
// ask, posts their OWN marker (legitimately becomes the live owner per the
// existing reaffirm rule) and IMMEDIATELY — same instant under the frozen test
// clock, matching a real same-turn two-comment sequence — posts their own
// negator. Before the fix this released the gate; after, it must not, because
// agentB is not this ask's sole marker-author and no real gap separates the
// two comments.
func TestReleaseHumanGateOnWithdrawal_TwoCommentHijack_Blocked(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	agentA := uuid.New()
	agentB := uuid.New()
	env.seedAgentBlockingComment(taskID, agentA) // frozenTime - 2h, still live/non-negated

	// Step 1: agentB posts their own fresh marker — legitimately becomes owner.
	hijackMarkerID := uuid.New()
	env.commentRepo.items[hijackMarkerID] = &domain.Comment{
		ID: hijackMarkerID, TaskID: taskID, AuthorID: agentB, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: подтверждаю, вопрос ещё актуален",
		CreatedAt: frozenTime, // "now" — posted this turn
	}

	// Step 2: agentB immediately negates their own brand-new marker.
	ctx := actorctx.WithActor(context.Background(), agentB, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: agentB, AuthorType: domain.ActorTypeAgent,
		Body: "Ask не нужен, снимаю.", // CreatedAt via svc.Create → timeNow() → also frozenTime, zero gap
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(),
		"a bystander must not be able to arm-and-release someone else's live ask in one uninterrupted turn")
	assert.Empty(t, env.systemComments())
}

// TestReleaseHumanGateOnWithdrawal_TwoCommentHijack_AllowedAfterRealGap proves
// the fix is a gap requirement, not a blanket ban on a reaffirming agent ever
// withdrawing — the exact scenario above still succeeds once real time (or at
// least minReaffirmToWithdrawalGap) separates the two comments, matching what
// a genuine hand-off (read the thread, verify the blocker, THEN withdraw)
// naturally looks like.
func TestReleaseHumanGateOnWithdrawal_TwoCommentHijack_AllowedAfterRealGap(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	agentA := uuid.New()
	agentB := uuid.New()
	env.seedAgentBlockingComment(taskID, agentA) // frozenTime - 2h

	reaffirmID := uuid.New()
	env.commentRepo.items[reaffirmID] = &domain.Comment{
		ID: reaffirmID, TaskID: taskID, AuthorID: agentB, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: подтверждаю, вопрос ещё актуален",
		CreatedAt: frozenTime.Add(-45 * time.Minute), // reaffirmed 45m before the withdrawal below
	}

	ctx := actorctx.WithActor(context.Background(), agentB, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: agentB, AuthorType: domain.ActorTypeAgent,
		Body: "Ask не нужен, снимаю.", // CreatedAt = frozenTime → 45m gap ≥ minReaffirmToWithdrawalGap
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "a real gap after a genuine reaffirm must still release the gate — this is not a blanket ban")
	assert.False(t, gateCalls[0].value)
}

// TestReleaseHumanGateOnWithdrawal_SoleOwner_NoGapRequired confirms AC2's (a)
// half is unaffected: the gate's ORIGINAL and ONLY-ever marker author still
// self-clears immediately, zero gap, exactly AC1 of #c375905c. This is the
// same fixture as TestReleaseHumanGateOnWithdrawal_SameAgentNegates_ReleasesFlag
// but named to make explicit that it is what THIS task's soleMarkerAuthor
// carve-out protects.
func TestReleaseHumanGateOnWithdrawal_SoleOwner_NoGapRequired(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID) // frozenTime - 2h, but irrelevant here — see below

	// Overwrite with a marker at frozenTime itself, so the gap to the withdrawal
	// (also frozenTime, via svc.Create) is exactly ZERO — the strictest possible
	// case for "no gap required when sole owner".
	for id, c := range env.commentRepo.items {
		if c.TaskID == taskID {
			c.CreatedAt = frozenTime
			env.commentRepo.items[id] = c
		}
	}

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Blocker самоустранился, ask не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "the sole/original owner must release immediately, zero gap required")
	assert.False(t, gateCalls[0].value)
}

// seedRawArmMarker inserts task_handler.go's raw PATCH/UI arm system comment
// directly, simulating a human_gate that went false→true with no "❓ Blocking
// @user" comment involved at all — see hasRawArmMarker.
//
// Fixed 2026-07-31 (task #15694816): this used to construct the marker with
// AuthorType: domain.ActorTypeAgent — matching what task_handler.go's Update
// handler ACTUALLY posted before that same task's fix, and thereby directly
// demonstrating the forgery hole rather than a real system comment. Corrected
// to AuthorType: system / AuthorID: uuid.Nil, the only shape no external
// caller can produce through the public comment-creation API.
func (env triageTestEnv) seedRawArmMarker(taskID uuid.UUID, at time.Time) {
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		Body:       "🔒 Auto: human_gate взведён напрямую (PATCH/UI), без маркерного коммента — actor: 4e2f... (agent)",
		IsInternal: true,
		CreatedAt:  at,
	}
}

// seedForgedRawArmMarker inserts an ORDINARY agent-authored comment carrying
// the exact same substring hasRawArmMarker looks for — what any real agent
// COULD post through the public comment API before this fix. Distinct from
// seedRawArmMarker precisely in AuthorType, which is what task #15694816
// closes the gap on.
func (env triageTestEnv) seedForgedRawArmMarker(taskID, authorID uuid.UUID, at time.Time) {
	cid := uuid.New()
	env.commentRepo.items[cid] = &domain.Comment{
		ID:         cid,
		TaskID:     taskID,
		AuthorID:   authorID,
		AuthorType: domain.ActorTypeAgent,
		Body:       "🔒 Auto: human_gate взведён напрямую (PATCH/UI), без маркерного коммента — actor: 4e2f... (agent)",
		CreatedAt:  at,
	}
}

// TestReleaseHumanGateOnWithdrawal_GateArmedWithoutMarker_BystanderBlocked is
// task #a2e2ac72's own repro (Bill's live proof on #ed8b4af6, prod-sha
// 871fb04): the gate was armed via a raw PATCH/UI call with NO marker comment
// in the thread at all. A bystander agent then posts their own marker and
// immediately negates it. Before this fix, soleMarkerAuthor was trivially
// true (there is still only ONE marker author ever — the bystander's own
// fabricated one), so #9959f201's fast path released the gate anyway —
// exactly as easily as the original two-comment hijack it was meant to close,
// just needing one identity instead of two. This must stay gated: the raw-arm
// marker predates the bystander's own marker, so rawArmPrecedesMarker forces
// the same friction as "not sole author".
func TestReleaseHumanGateOnWithdrawal_GateArmedWithoutMarker_BystanderBlocked(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	env.seedRawArmMarker(taskID, frozenTime.Add(-1*time.Hour)) // PATCH arm, 1h before the marker below

	bystanderID := uuid.New()
	hijackMarkerID := uuid.New()
	env.commentRepo.items[hijackMarkerID] = &domain.Comment{
		ID: hijackMarkerID, TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: подтверждаю, вопрос ещё актуален",
		CreatedAt: frozenTime, // "now" — posted this turn
	}

	ctx := actorctx.WithActor(context.Background(), bystanderID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body: "Ask не нужен, снимаю.", // CreatedAt via svc.Create → timeNow() → also frozenTime, zero gap
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	assert.Empty(t, env.taskMover.humanGateCalls(),
		"a marker fabricated onto an already (raw-armed) gate must not grant the sole-owner fast path")
	assert.Len(t, env.systemComments(), 1, "only the pre-seeded raw-arm marker should exist — no new release comment")
}

// TestReleaseHumanGateOnWithdrawal_GateArmedWithoutMarker_AllowedAfterRealGap
// proves the fix above is the same friction-not-ban shape as
// TwoCommentHijack_AllowedAfterRealGap: once real time separates the
// bystander's marker from their own negator, the release still succeeds.
func TestReleaseHumanGateOnWithdrawal_GateArmedWithoutMarker_AllowedAfterRealGap(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	env.seedRawArmMarker(taskID, frozenTime.Add(-2*time.Hour)) // PATCH arm, well before the marker below

	bystanderID := uuid.New()
	markerID := uuid.New()
	env.commentRepo.items[markerID] = &domain.Comment{
		ID: markerID, TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: подтверждаю, вопрос ещё актуален",
		CreatedAt: frozenTime.Add(-45 * time.Minute), // 45m before the withdrawal below
	}

	ctx := actorctx.WithActor(context.Background(), bystanderID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body: "Ask не нужен, снимаю.", // CreatedAt = frozenTime → 45m gap ≥ minReaffirmToWithdrawalGap
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "a real gap after the raw arm must still release the gate — this is not a blanket ban")
	assert.False(t, gateCalls[0].value)
}

// TestReleaseHumanGateOnWithdrawal_RawArmAfterMarker_SoleOwnerStillNoGapRequired
// guards the comparison direction: a raw PATCH/UI arm comment that lands AFTER
// a genuine marker (e.g. a redundant re-arm following a real ask) must NOT
// retroactively strip the sole owner's zero-gap self-clear — only a raw arm
// that PRECEDES the live marker is suspect.
func TestReleaseHumanGateOnWithdrawal_RawArmAfterMarker_SoleOwnerStillNoGapRequired(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID) // frozenTime - 2h — the genuine, original ask
	env.seedRawArmMarker(taskID, frozenTime.Add(-1*time.Hour))

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Blocker самоустранился, ask не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "a raw arm AFTER the genuine marker must not retroactively block the sole owner")
	assert.False(t, gateCalls[0].value)
}

// TestReleaseHumanGateOnWithdrawal_ForgedRawArmMarker_DoesNotBlockSoleOwner is
// task #15694816 (found in cross-verification of #486): hasRawArmMarker used
// to match on body substring alone, with no author check. Any agent could
// post an ORDINARY comment carrying task_handler.go's exact raw-arm text —
// nothing stopped it, since AuthorType is derived from the poster's own
// identity (always agent/user, never system) for anything posted through the
// public API. Before this fix, that forged comment would pin lastRawArmAt in
// the past and strip the sole owner's zero-gap self-clear FOREVER (every
// future withdrawal attempt on this task would see rawArmPrecedesMarker=true
// and require the 30-minute gap). Direction was always safe — it can only add
// friction, never release a live gate early — but it is real griefing and
// worth closing: a bystander agent's ordinary comment must never carry the
// same weight as task_handler.go's own system comment.
func TestReleaseHumanGateOnWithdrawal_ForgedRawArmMarker_DoesNotBlockSoleOwner(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	bystanderID := uuid.New()
	// A bystander posts an ORDINARY agent comment carrying the raw-arm
	// substring well before the genuine ask ever appears — the forgery.
	env.seedForgedRawArmMarker(taskID, bystanderID, frozenTime.Add(-3*time.Hour))
	env.seedAgentBlockingComment(taskID, askerID) // frozenTime - 2h, the genuine sole ask

	// Overwrite to zero gap — the strictest case for "sole owner still clears".
	for id, c := range env.commentRepo.items {
		if c.TaskID == taskID && hasBlockingMarker(c.Body) {
			c.CreatedAt = frozenTime
			env.commentRepo.items[id] = c
		}
	}

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Blocker самоустранился, ask не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "a forged agent-authored raw-arm comment must not strip the sole owner's fast path")
	assert.False(t, gateCalls[0].value)
}

// TestHasRawArmMarker_RequiresSystemAuthorType is the direct unit-level probe
// for task #15694816: identical body, different AuthorType — only the system
// one counts.
func TestHasRawArmMarker_RequiresSystemAuthorType(t *testing.T) {
	body := "🔒 Auto: human_gate взведён напрямую (PATCH/UI), без маркерного коммента — actor: x"
	assert.True(t, hasRawArmMarker(body, domain.ActorTypeSystem))
	assert.False(t, hasRawArmMarker(body, domain.ActorTypeAgent), "an agent-authored comment must never count as a raw-arm marker")
	assert.False(t, hasRawArmMarker(body, domain.ActorTypeUser), "a user-authored comment must never count as a raw-arm marker")
}

// ---------------------------------------------------------------------------
// GetHumanGateOwner (task #040cddcf) — read-only exposure of ownership
// ---------------------------------------------------------------------------

// TestGetHumanGateOwner_SoleOwner_ClearableNow is the common case: a single
// agent raised the still-live ask and nobody else ever touched it — exactly
// the population #040cddcf measured at 46/47 open gated tasks. Reported
// owner must match the marker author, and ClearableByOwner must be true with
// zero gap required — mirrors TestReleaseHumanGateOnWithdrawal_SoleOwner_NoGapRequired.
func TestGetHumanGateOwner_SoleOwner_ClearableNow(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.Gated)
	require.NotNil(t, info.OwnerAgentID)
	assert.Equal(t, askerID, *info.OwnerAgentID)
	assert.True(t, info.ClearableByOwner, "sole marker author must be reported as clearable right now")
	assert.Empty(t, info.ReasonIfNot)
	require.NotNil(t, info.MarkerCommentID)
	require.NotNil(t, info.MarkerCreatedAt)
}

// TestGetHumanGateOwner_NoLiveMarker_ReasonNoLiveMarker is #040cddcf's own
// negative control: a gate with no marker comment in the thread at all (the
// class the task's own live measurement found exactly ONE of 47 open gated
// tasks belongs to) must report no owner, not a fabricated one.
func TestGetHumanGateOwner_NoLiveMarker_ReasonNoLiveMarker(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	// No marker seeded at all — matches releaseHumanGateOnWithdrawal's own
	// TestReleaseHumanGateOnWithdrawal_NoPriorMarker_NoOp fixture shape.

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.Gated)
	assert.Nil(t, info.OwnerAgentID)
	assert.Empty(t, info.OwnerName)
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "no_live_marker", info.ReasonIfNot)
}

// TestGetHumanGateOwner_RawArmed_NotYetClearable is #a2e2ac72's raw-arm case
// (same fixture as TestReleaseHumanGateOnWithdrawal_GateArmedWithoutMarker_BystanderBlocked):
// there IS a reported owner (the bystander's marker), but ClearableByOwner
// must be false with ReasonIfNot="raw_armed" — the API must not tell that
// agent they can clear it right now when releaseHumanGateOnWithdrawal would
// refuse them.
func TestGetHumanGateOwner_RawArmed_NotYetClearable(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	env.seedRawArmMarker(taskID, frozenTime.Add(-1*time.Hour))

	bystanderID := uuid.New()
	env.commentRepo.items[uuid.New()] = &domain.Comment{
		ID: uuid.New(), TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: подтверждаю, вопрос ещё актуален",
		CreatedAt: frozenTime, // "now" — well within the 30-minute gap
	}

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.NotNil(t, info.OwnerAgentID)
	assert.Equal(t, bystanderID, *info.OwnerAgentID, "an owner IS reported — just not yet clearable")
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "raw_armed", info.ReasonIfNot)
}

// TestGetHumanGateOwner_RawArmed_ClearableAfterGap proves ClearableByOwner is
// a live, time-dependent answer, not a static verdict frozen at scan time:
// once minReaffirmToWithdrawalGap has genuinely elapsed since the marker, the
// SAME raw-armed thread reports clearable — matching
// TestReleaseHumanGateOnWithdrawal_GateArmedWithoutMarker_AllowedAfterRealGap.
func TestGetHumanGateOwner_RawArmed_ClearableAfterGap(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	env.seedRawArmMarker(taskID, frozenTime.Add(-2*time.Hour))

	bystanderID := uuid.New()
	env.commentRepo.items[uuid.New()] = &domain.Comment{
		ID: uuid.New(), TaskID: taskID, AuthorID: bystanderID, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: подтверждаю, вопрос ещё актуален",
		CreatedAt: frozenTime.Add(-45 * time.Minute), // 45m before "now" (frozenTime)
	}

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.ClearableByOwner, "45m gap exceeds minReaffirmToWithdrawalGap — must report clearable")
	assert.Empty(t, info.ReasonIfNot)
}

// TestGetHumanGateOwner_ReaffirmPending_NotYetClearable is the OTHER source
// of required friction besides raw-arm: ownership transferred to a second
// agent via reaffirm (soleMarkerAuthor=false), same shape as
// TestReleaseHumanGateOnWithdrawal_TwoCommentHijack_Blocked. The task's own
// JSON sketch names only "no_live_marker"/"raw_armed" as example reasons;
// this is the third, real case the shared predicate produces and it must not
// be silently reported as clearable.
func TestGetHumanGateOwner_ReaffirmPending_NotYetClearable(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	agentA := uuid.New()
	agentB := uuid.New()
	env.seedAgentBlockingComment(taskID, agentA) // frozenTime - 2h
	env.commentRepo.items[uuid.New()] = &domain.Comment{
		ID: uuid.New(), TaskID: taskID, AuthorID: agentB, AuthorType: domain.ActorTypeAgent,
		Body:      "❓ **Blocking @pavel**: подтверждаю, вопрос ещё актуален",
		CreatedAt: frozenTime, // reaffirmed just now — zero gap so far
	}

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.NotNil(t, info.OwnerAgentID)
	assert.Equal(t, agentB, *info.OwnerAgentID, "ownership follows the chronologically last non-negated marker")
	assert.False(t, info.ClearableByOwner)
	assert.Equal(t, "reaffirm_pending", info.ReasonIfNot)
}

// TestGetHumanGateOwner_PredictionMatchesActualWithdrawal is #040cddcf's own
// AC3 in spirit: the SAME shared scan must back both the read path and the
// clearing path, so what GetHumanGateOwner predicts and what
// releaseHumanGateOnWithdrawal actually does can never disagree. Proven here
// by DOING both in sequence on one fixture, not just asserting they call the
// same private method.
func TestGetHumanGateOwner_PredictionMatchesActualWithdrawal(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	info, err := env.svc.GetHumanGateOwner(context.Background(), taskID)
	require.NoError(t, err)
	require.True(t, info.ClearableByOwner, "prediction: this owner can clear it right now")

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Blocker самоустранился, ask не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))

	gateCalls := env.taskMover.humanGateCalls()
	require.Len(t, gateCalls, 1, "the prediction must be borne out: the actual withdrawal succeeds")
	assert.False(t, gateCalls[0].value)
}

// TestReleaseHumanGateOnWithdrawal_LogsReleasedByToActivityLog is the task's
// AC4: a successful release must now be visible in the task's activity log —
// before this fix, /activity carried no human_gate signal at all.
func TestReleaseHumanGateOnWithdrawal_LogsReleasedByToActivityLog(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	comment := &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: "Blocker самоустранился, ask не нужен — снимаю.",
	}
	require.NoError(t, env.svc.Create(ctx, comment))
	require.Len(t, env.taskMover.humanGateCalls(), 1, "sanity: the release itself must have happened")

	page, err := env.activityRepo.ListByTask(context.Background(), taskID, pagination.Params{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.NotNil(t, page)
	var found *domain.ActivityLog
	for i := range page.Items {
		if page.Items[i].Action == "human_gate.released_by" {
			found = &page.Items[i]
		}
	}
	require.NotNil(t, found, "a human_gate.released_by activity log entry must exist")
	assert.Equal(t, askerID, found.ActorID)
	assert.Equal(t, domain.ActorTypeAgent, found.ActorType)
	assert.Contains(t, string(found.Changes), askerID.String())
}

// TestBlockingMarkerSlugs_QuotedTemplateDoesNotSteerTarget covers the third raw
// call site found while verifying this change: regex matches come back in source
// order, so a body carrying BOTH a quoted template and a real marker used to
// resolve the QUOTED slug first — enforceBlockingTriage would then triage against
// whoever the documentation example named, not who the real ask names.
func TestBlockingMarkerSlugs_QuotedTemplateDoesNotSteerTarget(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			"fenced template before the real marker",
			"Шаблон:\n```\n❓ **Blocking @example-user**: <вопрос>\n```\n❓ **Blocking @pavel**: настоящий аск",
			[]string{"pavel"},
		},
		// The two forms below were already safe: the regex is line-anchored, so an
		// inline-code or blockquoted template never matched even before the strip.
		// Kept as documentation of that property — neither discriminates the fix.
		{
			"inline-code template before the real marker",
			"пиши `❓ Blocking @example-user`\n❓ **Blocking @pavel**: настоящий аск",
			[]string{"pavel"},
		},
		{
			"blockquoted template before the real marker",
			"> ❓ **Blocking @example-user**\n❓ **Blocking @pavel**: настоящий аск",
			[]string{"pavel"},
		},
		{"plain single marker unchanged", "❓ **Blocking @pavel**: ask", []string{"pavel"}},
		{"quoted only resolves to nothing", "```\n❓ Blocking @pavel\n```", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, blockingMarkerSlugs(tt.body))
		})
	}
}

// TestCompletionKeywordSearchWindow_AnchorsToRealMarker pins the fourth call site:
// the window must precede the first REAL marker. Anchoring it to a quoted template
// earlier in the body would hand isAssigneeCompletionReport the wrong text — and,
// because offsets from the raw body cannot index the stripped one, mixing the two
// strings would slice at a meaningless position.
//
// Only a FENCED template discriminates here. The regex is line-anchored, so an
// inline-code or blockquoted template never matched in the first place — those
// forms are safe for free, and a fixture built from them would pass against the
// unstripped code too, i.e. prove nothing.
func TestCompletionKeywordSearchWindow_AnchorsToRealMarker(t *testing.T) {
	body := "Шаблон:\n```\n❓ Blocking @example-user\n```\n" +
		"Готово, работа завершена.\n" +
		"❓ **Blocking @pavel**: закрой карточку"
	win := completionKeywordSearchWindow(body)

	assert.Contains(t, win, "работа завершена",
		"the window must cover the prose between the quoted template and the real marker")
	assert.NotContains(t, win, "закрой карточку",
		"the window must stop at the real marker")
	assert.True(t, utf8.ValidString(win), "window must never split a multi-byte rune")
}
