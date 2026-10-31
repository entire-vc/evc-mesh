package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Contract tests for the bodies of public task events as an extension receives
// them (before delivery adds workspace_id). Keys may be added; removing or
// renaming one breaks receivers and needs the ADR and version bump from
// docs/extensions/api-version.md.
// Run all of them with: go test ./internal/... -run Contract

func eventKeys(t *testing.T, e recordedEvent) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(e.payload)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func requireEventKeys(t *testing.T, e recordedEvent, want ...string) {
	t.Helper()
	got := eventKeys(t, e)
	for _, k := range want {
		assert.Contains(t, got, k, "%s: contract key %q missing", e.event, k)
	}
}

func TestContract_EventPayload_TaskLifecycle(t *testing.T) {
	svc, taskRepo, f, d := setupTaskServiceWithWebhooks(t)
	ctx := context.Background()

	task := &domain.Task{ProjectID: f.projectID, StatusID: f.todoID, Title: "Contract", Priority: domain.PriorityMedium}
	require.NoError(t, svc.Create(ctx, task))
	requireEventKeys(t, d.waitFor(t, "task.created"), "task_id", "project_id", "title", "priority", "assignee_id", "status_id")

	updated := *taskRepo.items[task.ID]
	updated.Title = "Contract, renamed"
	require.NoError(t, svc.Update(ctx, &updated))
	upd := d.waitFor(t, "task.updated")
	requireEventKeys(t, upd, "task_id", "project_id", "changes")

	// Delivered to extensions as task.moved (publicEventName).
	require.NoError(t, svc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &f.inProgress}))
	moved := d.waitFor(t, "task.status_changed")
	requireEventKeys(t, moved, "task_id", "project_id", "old_status_id", "new_status_id")
	assert.Equal(t, "task.moved", publicEventName(moved.event))

	assignee := uuid.New()
	require.NoError(t, svc.AssignTask(ctx, task.ID, AssignTaskInput{AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser}))
	requireEventKeys(t, d.waitFor(t, "task.assigned"), "task_id", "project_id", "assignee_id", "assignee_type")
}

func TestContract_EventPayload_ProjectAndMember(t *testing.T) {
	ctx := context.Background()

	psvc, _, _ := setupProjectService()
	pd := &captureDispatcher{}
	psvc.SetEventDispatcher(pd)
	p := &domain.Project{WorkspaceID: uuid.New(), Name: "Contract"}
	require.NoError(t, psvc.Create(ctx, p))
	requireEventKeys(t, pd.waitFor(t, "project.created"), "workspace_id", "project_id", "slug", "name", "is_archived")
	p.Name = "Contract 2"
	require.NoError(t, psvc.Update(ctx, p))
	requireEventKeys(t, pd.waitFor(t, "project.updated"), "workspace_id", "project_id", "slug", "name", "is_archived")

	msvc, _, _ := newIdentityFixture()
	md := &captureDispatcher{}
	msvc.(*workspaceMemberService).SetEventDispatcher(md)
	wsID := uuid.New()
	m, err := msvc.AddMemberWithCreate(ctx, wsID, "contract@example.com", "Contract", domain.RoleMember, "StrongP4ss", uuid.Nil)
	require.NoError(t, err)
	requireEventKeys(t, md.waitFor(t, "workspace.member.added"), "workspace_id", "user_id", "role")
	require.NoError(t, msvc.RemoveMember(ctx, wsID, m.UserID))
	requireEventKeys(t, md.waitFor(t, "workspace.member.removed"), "workspace_id", "user_id", "role")
}
