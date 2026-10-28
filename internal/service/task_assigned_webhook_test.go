package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// captureWebhookService records Dispatch; the rest of WebhookService is never
// reached from the task service.
type captureWebhookService struct {
	WebhookService
	*captureDispatcher
}

func (c captureWebhookService) Dispatch(ctx context.Context, ws uuid.UUID, event string, payload any) {
	c.captureDispatcher.Dispatch(ctx, ws, event, payload)
}

func setupTaskServiceWithWebhooks(t *testing.T) (*taskService, *MockTaskRepository, subtaskFixture, *captureDispatcher) {
	t.Helper()
	svc, taskRepo, statusRepo := setupTaskService()
	d := &captureDispatcher{}
	svc.webhookSvc = captureWebhookService{captureDispatcher: d}
	return svc, taskRepo, newSubtaskFixture(taskRepo, statusRepo), d
}

// settled gives the fire-and-forget Dispatch goroutines time to land before a
// test asserts that something was NOT sent.
func (d *captureDispatcher) settled() *captureDispatcher {
	time.Sleep(100 * time.Millisecond)
	return d
}

func assertAssignedPayload(t *testing.T, e recordedEvent, task *domain.Task, assignee uuid.UUID) {
	t.Helper()
	assert.Equal(t, testDefaultWorkspaceID, e.ws)
	assert.Equal(t, task.ID, e.payload["task_id"])
	assert.Equal(t, task.ProjectID, e.payload["project_id"])
	got, ok := e.payload["assignee_id"].(*uuid.UUID)
	require.True(t, ok, "assignee_id must be the same *uuid.UUID Update sends, got %T", e.payload["assignee_id"])
	require.NotNil(t, got)
	assert.Equal(t, assignee, *got)
	assert.Equal(t, string(domain.AssigneeTypeUser), e.payload["assignee_type"])
}

func TestTaskAssignedWebhook_AssignTask(t *testing.T) {
	svc, taskRepo, f, d := setupTaskServiceWithWebhooks(t)
	ctx := context.Background()
	assignee := uuid.New()

	require.NoError(t, svc.AssignTask(ctx, f.parentID, AssignTaskInput{AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser}))
	assertAssignedPayload(t, d.waitFor(t, "task.assigned"), taskRepo.items[f.parentID], assignee)

	// Same assignee again: nothing changed, nothing sent.
	require.NoError(t, svc.AssignTask(ctx, f.parentID, AssignTaskInput{AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser}))
	d.settled()
	assert.Equal(t, 1, d.count("task.assigned"))
	assert.Zero(t, d.count("task.updated"), "the fix must not add task.updated")
}

func TestTaskAssignedWebhook_MoveWithReassign(t *testing.T) {
	svc, taskRepo, f, d := setupTaskServiceWithWebhooks(t)
	ctx := context.Background()
	assignee := uuid.New()

	require.NoError(t, svc.MoveTask(ctx, f.parentID, MoveTaskInput{
		StatusID: &f.todoID, AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser,
	}))
	assertAssignedPayload(t, d.waitFor(t, "task.assigned"), taskRepo.items[f.parentID], assignee)

	// Status-only move keeps the assignee: no task.assigned.
	require.NoError(t, svc.MoveTask(ctx, f.parentID, MoveTaskInput{StatusID: &f.inProgress}))
	d.settled()
	assert.Equal(t, 1, d.count("task.assigned"))
	assert.Zero(t, d.count("task.updated"), "the fix must not add task.updated")
}

func TestTaskAssignedWebhook_CreateWithAssignee(t *testing.T) {
	svc, _, f, d := setupTaskServiceWithWebhooks(t)
	ctx := context.Background()
	assignee := uuid.New()

	task := &domain.Task{
		ProjectID: f.projectID, StatusID: f.todoID, Title: "Assigned on create",
		Priority: domain.PriorityMedium, AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser,
	}
	require.NoError(t, svc.Create(ctx, task))
	assertAssignedPayload(t, d.waitFor(t, "task.assigned"), task, assignee)

	unassigned := &domain.Task{ProjectID: f.projectID, StatusID: f.todoID, Title: "Nobody", Priority: domain.PriorityMedium}
	require.NoError(t, svc.Create(ctx, unassigned))
	d.waitFor(t, "task.created")
	d.settled()
	assert.Equal(t, 1, d.count("task.assigned"), "a task created without an assignee is not assigned")
}

func TestTaskAssignedWebhook_CreateSubtaskWithAssignee(t *testing.T) {
	svc, _, f, d := setupTaskServiceWithWebhooks(t)
	ctx := context.Background()
	assignee := uuid.New()

	child, err := svc.CreateSubtask(ctx, f.parentID, CreateSubtaskInput{
		Title: "Child", Priority: domain.PriorityMedium, AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser,
	})
	require.NoError(t, err)
	assertAssignedPayload(t, d.waitFor(t, "task.assigned"), child, assignee)

	_, err = svc.CreateSubtask(ctx, f.parentID, CreateSubtaskInput{Title: "Unassigned child", Priority: domain.PriorityMedium})
	require.NoError(t, err)
	d.settled()
	assert.Equal(t, 1, d.count("task.assigned"), "a subtask created without an assignee is not assigned")
}

// Update already sent task.assigned; the shared helper must keep its payload.
func TestTaskAssignedWebhook_UpdateUnchanged(t *testing.T) {
	svc, taskRepo, f, d := setupTaskServiceWithWebhooks(t)
	ctx := context.Background()
	assignee := uuid.New()

	updated := *taskRepo.items[f.parentID]
	updated.AssigneeID, updated.AssigneeType = &assignee, domain.AssigneeTypeUser
	require.NoError(t, svc.Update(ctx, &updated))
	assertAssignedPayload(t, d.waitFor(t, "task.assigned"), &updated, assignee)
	d.settled()
	assert.Equal(t, 1, d.count("task.assigned"))
}
