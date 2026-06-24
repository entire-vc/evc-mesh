package service

// Tests for the configurable Definition-of-Done gate (task #51fd215a).
//
// Rules under test:
//  1. Required gate not passed → MoveTask to done blocked with DodGateBlockedError.
//  2. Required gate passed → move to done allowed.
//  3. No dod_gates configured → move to done allowed as before.
//  4. Optional gate not passed → does NOT block done.
//  5. System actor is exempt from DoD gate (auto-close of recurring instances).
//  6. SetDodCheck rejects unknown gate names when project has gates configured.
//  7. SetDodCheck accepts valid gate names.
//  8. dodBlockingGates pure-function corner cases.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// buildDodEnv creates a taskService wired with a MockProjectRepo that returns
// the provided ProjectSettings for every GetByID call.
func buildDodEnv(t *testing.T, settings domain.ProjectSettings) (svc *taskService, taskID uuid.UUID, statusByCategory map[domain.StatusCategory]uuid.UUID) {
	t.Helper()
	ts, taskRepo, statusRepo := setupTaskService()

	projID := uuid.New()
	taskID = uuid.New()

	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	proj := &domain.Project{
		ID:       projID,
		Settings: json.RawMessage(settingsJSON),
	}
	projRepo := &mockProjectRepository{project: proj}
	ts.projectRepo = projRepo

	statusByCategory = make(map[domain.StatusCategory]uuid.UUID)
	for _, cat := range []domain.StatusCategory{
		domain.StatusCategoryBacklog,
		domain.StatusCategoryTodo,
		domain.StatusCategoryInProgress,
		domain.StatusCategoryReview,
		domain.StatusCategoryDone,
		domain.StatusCategoryCancelled,
	} {
		sid := uuid.New()
		statusRepo.items[sid] = &domain.TaskStatus{ID: sid, ProjectID: projID, Category: cat}
		statusByCategory[cat] = sid
	}

	taskRepo.items[taskID] = &domain.Task{
		ID:        taskID,
		ProjectID: projID,
		StatusID:  statusByCategory[domain.StatusCategoryInProgress],
		Title:     "dod test task",
		DodChecks: domain.DodChecks{},
	}

	return ts, taskID, statusByCategory
}

// mockProjectRepository satisfies repository.ProjectRepository for tests.
type mockProjectRepository struct {
	project *domain.Project
}

func (m *mockProjectRepository) GetByID(_ context.Context, _ uuid.UUID) (*domain.Project, error) {
	return m.project, nil
}
func (m *mockProjectRepository) GetBySlug(_ context.Context, _ uuid.UUID, _ string) (*domain.Project, error) {
	return m.project, nil
}
func (m *mockProjectRepository) Create(_ context.Context, _ *domain.Project) error { return nil }
func (m *mockProjectRepository) Update(_ context.Context, _ *domain.Project) error { return nil }
func (m *mockProjectRepository) Delete(_ context.Context, _ uuid.UUID) error       { return nil }
func (m *mockProjectRepository) List(_ context.Context, _ uuid.UUID, _ repository.ProjectFilter, _ pagination.Params) (*pagination.Page[domain.Project], error) {
	return nil, nil
}

var _ repository.ProjectRepository = (*mockProjectRepository)(nil)

// ---------------------------------------------------------------------------
// 1. Required gate not passed → blocked
// ---------------------------------------------------------------------------

func TestMoveTask_DodGate_RequiredGateNotPassed_Blocked(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "pr-merged", Required: true},
		},
	}
	ts, taskID, cats := buildDodEnv(t, settings)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	doneID := cats[domain.StatusCategoryDone]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID})
	var dodErr *DodGateBlockedError
	require.ErrorAs(t, err, &dodErr)
	assert.Contains(t, dodErr.BlockingGates, "pr-merged")
}

// ---------------------------------------------------------------------------
// 2. Required gate passed → allowed
// ---------------------------------------------------------------------------

func TestMoveTask_DodGate_RequiredGatePassed_Allowed(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "pr-merged", Required: true},
		},
	}
	ts, taskID, cats := buildDodEnv(t, settings)

	// Pre-set the gate to pass directly on the task in the repo.
	taskRepo := ts.taskRepo.(*MockTaskRepository)
	taskRepo.mu.Lock()
	t2 := taskRepo.items[taskID]
	t2.DodChecks = domain.DodChecks{
		"pr-merged": {Status: domain.DodCheckPass},
	}
	taskRepo.items[taskID] = t2
	taskRepo.mu.Unlock()

	doneID2 := cats[domain.StatusCategoryDone]
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID2})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// 3. No dod_gates configured → no block
// ---------------------------------------------------------------------------

func TestMoveTask_DodGate_NoGatesConfigured_Allowed(t *testing.T) {
	ts, taskID, cats := buildDodEnv(t, domain.ProjectSettings{})
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	doneID := cats[domain.StatusCategoryDone]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// 4. Optional gate not passed → does NOT block
// ---------------------------------------------------------------------------

func TestMoveTask_DodGate_OptionalGateNotPassed_Allowed(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "e2e-authed", Required: false},
		},
	}
	ts, taskID, cats := buildDodEnv(t, settings)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	doneID := cats[domain.StatusCategoryDone]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// 5. System actor exempt from DoD gate
// ---------------------------------------------------------------------------

func TestMoveTask_DodGate_SystemActorExempt(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "pr-merged", Required: true},
		},
	}
	ts, taskID, cats := buildDodEnv(t, settings)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeSystem)

	doneID := cats[domain.StatusCategoryDone]
	err := ts.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &doneID})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// 6. SetDodCheck rejects unknown gate names
// ---------------------------------------------------------------------------

func TestSetDodCheck_UnknownGateName_Error(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "pr-merged", Required: true},
		},
	}
	ts, taskID, _ := buildDodEnv(t, settings)
	ctx := context.Background()

	err := ts.SetDodCheck(ctx, taskID, "unknown-gate", "pass", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-gate")
}

// ---------------------------------------------------------------------------
// 7. SetDodCheck accepts a valid gate name
// ---------------------------------------------------------------------------

func TestSetDodCheck_ValidGateName_OK(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "pr-merged", Required: true},
		},
	}
	ts, taskID, _ := buildDodEnv(t, settings)
	ctx := context.Background()

	err := ts.SetDodCheck(ctx, taskID, "pr-merged", "pass", "verify-driver")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// 8. dodBlockingGates pure-function tests
// ---------------------------------------------------------------------------

func TestDodBlockingGates_AllPass(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "a", Required: true},
			{Name: "b", Required: true},
		},
	}
	checks := domain.DodChecks{
		"a": {Status: domain.DodCheckPass},
		"b": {Status: domain.DodCheckPass},
	}
	assert.Empty(t, dodBlockingGates(settings, checks))
}

func TestDodBlockingGates_OneFail(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "a", Required: true},
			{Name: "b", Required: true},
		},
	}
	checks := domain.DodChecks{
		"a": {Status: domain.DodCheckPass},
		"b": {Status: domain.DodCheckFail},
	}
	blocking := dodBlockingGates(settings, checks)
	assert.Equal(t, []string{"b"}, blocking)
}

func TestDodBlockingGates_Missing(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "a", Required: true},
		},
	}
	blocking := dodBlockingGates(settings, domain.DodChecks{})
	assert.Equal(t, []string{"a"}, blocking)
}

func TestDodBlockingGates_OptionalNotBlocking(t *testing.T) {
	settings := domain.ProjectSettings{
		DodGates: []domain.DodGateConfig{
			{Name: "optional", Required: false},
			{Name: "required", Required: true},
		},
	}
	checks := domain.DodChecks{
		"required": {Status: domain.DodCheckPass},
	}
	assert.Empty(t, dodBlockingGates(settings, checks))
}
