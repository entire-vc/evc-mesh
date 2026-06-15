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
