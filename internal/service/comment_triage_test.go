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
		projID, wsID, inProgressID, triageID,
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
