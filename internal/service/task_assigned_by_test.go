package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// assignedByEnv is a lightweight harness for pin-assignee tests.
type assignedByEnv struct {
	svc        TaskService
	taskRepo   *MockTaskRepository
	statusRepo *MockTaskStatusRepository
	projectID  uuid.UUID
	agentA     uuid.UUID
	agentB     uuid.UUID
}

func newAssignedByEnv(t *testing.T) *assignedByEnv {
	t.Helper()
	taskRepo := NewMockTaskRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectID := uuid.New()

	st := &domain.TaskStatus{ID: uuid.New(), ProjectID: projectID, Category: domain.StatusCategoryTodo, Name: "todo"}
	if err := statusRepo.Create(context.Background(), st); err != nil {
		t.Fatalf("create status: %v", err)
	}
	// Both agents must exist in the directory: the assignee tenancy guard refuses
	// a principal it cannot find, because an id in no directory cannot be shown to
	// belong to this workspace.
	agentRepo := NewMockAgentRepository()
	agentA, agentB := uuid.New(), uuid.New()
	agentRepo.items[agentA] = &domain.Agent{ID: agentA, Slug: "agent-a"}
	agentRepo.items[agentB] = &domain.Agent{ID: agentB, Slug: "agent-b"}

	svc := newTestTaskService(taskRepo, statusRepo, nil, nil, WithTaskAgentRepo(agentRepo))
	e := &assignedByEnv{
		svc: svc, taskRepo: taskRepo, statusRepo: statusRepo,
		projectID: projectID,
		agentA:    agentA,
		agentB:    agentB,
	}

	// Seed the status so task creation works.
	_ = st
	return e
}

func (e *assignedByEnv) createTask(t *testing.T, assignedBy domain.AssignmentSource, assigneeID *uuid.UUID) *domain.Task {
	t.Helper()
	// Fetch a valid statusID from the repo.
	statuses, _ := e.statusRepo.ListByProject(context.Background(), e.projectID)
	if len(statuses) == 0 {
		t.Fatal("no statuses seeded")
	}
	task := &domain.Task{
		ID:           uuid.New(),
		ProjectID:    e.projectID,
		StatusID:     statuses[0].ID,
		Title:        "pin test",
		AssigneeID:   assigneeID,
		AssigneeType: domain.AssigneeTypeAgent,
		AssignedBy:   assignedBy,
	}
	if err := e.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// TestAssignedBy_HumanPinBlocksRuleSource verifies that a rule source cannot override a human pin.
func TestAssignedBy_HumanPinBlocksRuleSource(t *testing.T) {
	e := newAssignedByEnv(t)
	task := e.createTask(t, domain.AssignmentSourceHuman, &e.agentA)

	err := e.svc.AssignTask(context.Background(), task.ID, AssignTaskInput{
		AssigneeID:   &e.agentB,
		AssigneeType: domain.AssigneeTypeAgent,
		Source:       domain.AssignmentSourceRule,
	})
	if err == nil {
		t.Fatal("expected AssignmentPinnedError, got nil")
	}
	var pinnedErr *AssignmentPinnedError
	if !errors.As(err, &pinnedErr) {
		t.Fatalf("expected AssignmentPinnedError, got %T: %v", err, err)
	}
}

// TestAssignedBy_HumanPinBlocksSystemSource verifies that a system source cannot override a human pin.
func TestAssignedBy_HumanPinBlocksSystemSource(t *testing.T) {
	e := newAssignedByEnv(t)
	task := e.createTask(t, domain.AssignmentSourceHuman, &e.agentA)

	err := e.svc.AssignTask(context.Background(), task.ID, AssignTaskInput{
		AssigneeID:   &e.agentB,
		AssigneeType: domain.AssigneeTypeAgent,
		Source:       domain.AssignmentSourceSystem,
	})
	if err == nil {
		t.Fatal("expected AssignmentPinnedError, got nil")
	}
	var pinnedErr *AssignmentPinnedError
	if !errors.As(err, &pinnedErr) {
		t.Fatalf("expected AssignmentPinnedError, got %T: %v", err, err)
	}
}

// TestAssignedBy_HumanPinBlocksOmittedSource verifies that an omitted source (defaults to system) also blocks.
func TestAssignedBy_HumanPinBlocksOmittedSource(t *testing.T) {
	e := newAssignedByEnv(t)
	task := e.createTask(t, domain.AssignmentSourceHuman, &e.agentA)

	err := e.svc.AssignTask(context.Background(), task.ID, AssignTaskInput{
		AssigneeID:   &e.agentB,
		AssigneeType: domain.AssigneeTypeAgent,
		// Source omitted → defaults to system
	})
	if err == nil {
		t.Fatal("expected AssignmentPinnedError, got nil")
	}
	var pinnedErr *AssignmentPinnedError
	if !errors.As(err, &pinnedErr) {
		t.Fatalf("expected AssignmentPinnedError, got %T: %v", err, err)
	}
}

// TestAssignedBy_HumanCanOverrideHumanPin verifies that a human source may reassign even when pinned.
func TestAssignedBy_HumanCanOverrideHumanPin(t *testing.T) {
	e := newAssignedByEnv(t)
	task := e.createTask(t, domain.AssignmentSourceHuman, &e.agentA)

	if err := e.svc.AssignTask(context.Background(), task.ID, AssignTaskInput{
		AssigneeID:   &e.agentB,
		AssigneeType: domain.AssigneeTypeAgent,
		Source:       domain.AssignmentSourceHuman,
	}); err != nil {
		t.Fatalf("human override of human pin failed: %v", err)
	}
	got, _ := e.taskRepo.GetByID(context.Background(), task.ID)
	if got.AssigneeID == nil || *got.AssigneeID != e.agentB {
		t.Errorf("expected assignee=%v, got %v", e.agentB, got.AssigneeID)
	}
	if got.AssignedBy != domain.AssignmentSourceHuman {
		t.Errorf("expected assigned_by=human, got %v", got.AssignedBy)
	}
}

// TestAssignedBy_RuleToRuleAllowed verifies that rule can overwrite an existing rule assignment.
func TestAssignedBy_RuleToRuleAllowed(t *testing.T) {
	e := newAssignedByEnv(t)
	task := e.createTask(t, domain.AssignmentSourceRule, &e.agentA)

	if err := e.svc.AssignTask(context.Background(), task.ID, AssignTaskInput{
		AssigneeID:   &e.agentB,
		AssigneeType: domain.AssigneeTypeAgent,
		Source:       domain.AssignmentSourceRule,
	}); err != nil {
		t.Fatalf("rule→rule assign failed: %v", err)
	}
	got, _ := e.taskRepo.GetByID(context.Background(), task.ID)
	if got.AssignedBy != domain.AssignmentSourceRule {
		t.Errorf("expected assigned_by=rule, got %v", got.AssignedBy)
	}
}

// TestAssignedBy_SystemToSystemAllowed verifies system→system works freely.
func TestAssignedBy_SystemToSystemAllowed(t *testing.T) {
	e := newAssignedByEnv(t)
	task := e.createTask(t, domain.AssignmentSourceSystem, &e.agentA)

	if err := e.svc.AssignTask(context.Background(), task.ID, AssignTaskInput{
		AssigneeID:   &e.agentB,
		AssigneeType: domain.AssigneeTypeAgent,
		Source:       domain.AssignmentSourceSystem,
	}); err != nil {
		t.Fatalf("system→system assign failed: %v", err)
	}
}

// TestAssignedBy_SetsPinWhenHuman verifies that AssignedBy is stored correctly after a human assignment.
func TestAssignedBy_SetsPinWhenHuman(t *testing.T) {
	e := newAssignedByEnv(t)
	task := e.createTask(t, domain.AssignmentSourceSystem, &e.agentA)

	if err := e.svc.AssignTask(context.Background(), task.ID, AssignTaskInput{
		AssigneeID:   &e.agentB,
		AssigneeType: domain.AssigneeTypeAgent,
		Source:       domain.AssignmentSourceHuman,
	}); err != nil {
		t.Fatalf("human assign failed: %v", err)
	}
	got, _ := e.taskRepo.GetByID(context.Background(), task.ID)
	if got.AssignedBy != domain.AssignmentSourceHuman {
		t.Errorf("expected assigned_by=human after human assign, got %v", got.AssignedBy)
	}
}
