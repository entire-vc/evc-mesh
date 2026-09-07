package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// Cross-project dependencies (#559270cf, scope addition by Riker 2026-09-07).
//
// The tenancy boundary on a dependency edge is the WORKSPACE, not the project. It was
// written as same-project because that is trivially inside the same workspace and no
// cross-project case had been asked for — but real blockers cross projects (Billing
// waiting on a Team Relay rollout; Spark ↔ Lab; Spark ↔ Argus, three refusals in one
// triage pass). With no cross-project `blocks` edge, the only way to park such a card
// is a due_date, which is an alarm clock standing in for an event: it fires whether or
// not the blocker cleared.
//
// What must NOT change is the security property the same-project check was written
// for: an edge into another tenant, and the id-enumeration oracle that comes with
// answering differently for ids that exist and ids that do not.
// ---------------------------------------------------------------------------

type crossProjectFixture struct {
	svc      *taskDependencyService
	depRepo  *MockTaskDependencyRepository
	taskRepo *MockTaskRepository
	projRepo *MockProjectRepository
}

func newCrossProjectFixture(t *testing.T) *crossProjectFixture {
	t.Helper()
	depRepo := NewMockTaskDependencyRepository()
	taskRepo := NewMockTaskRepository()
	projRepo := NewMockProjectRepository()
	svc := NewTaskDependencyService(depRepo, taskRepo, NewMockActivityLogRepository(), projRepo).(*taskDependencyService)
	return &crossProjectFixture{svc: svc, depRepo: depRepo, taskRepo: taskRepo, projRepo: projRepo}
}

// project registers a project in a workspace and returns its id.
func (f *crossProjectFixture) project(t *testing.T, workspaceID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, f.projRepo.Create(context.Background(), &domain.Project{
		ID: id, WorkspaceID: workspaceID, Name: "p", Slug: "p-" + id.String()[:8],
	}))
	return id
}

func (f *crossProjectFixture) task(t *testing.T, projectID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.taskRepo.items[id] = &domain.Task{ID: id, ProjectID: projectID, Title: "t"}
	return id
}

func (f *crossProjectFixture) create(taskID, dependsOn uuid.UUID, typ domain.DependencyType) error {
	return f.svc.Create(context.Background(), &domain.TaskDependency{
		TaskID: taskID, DependsOnTaskID: dependsOn, DependencyType: typ,
	})
}

// The change itself: two projects, one workspace, a blocks edge between them.
func TestCrossProject_BlocksWithinOneWorkspace_Allowed(t *testing.T) {
	f := newCrossProjectFixture(t)
	ws := uuid.New()
	billing := f.project(t, ws)
	teamRelay := f.project(t, ws)

	card := f.task(t, billing)
	blocker := f.task(t, teamRelay)

	require.NoError(t, f.create(card, blocker, domain.DependencyTypeBlocks),
		"a blocks edge between two projects of the same workspace must be allowed")
	assert.Len(t, f.depRepo.items, 1, "the edge was not persisted")
}

func TestCrossProject_RelatesToWithinOneWorkspace_Allowed(t *testing.T) {
	f := newCrossProjectFixture(t)
	ws := uuid.New()
	spark := f.project(t, ws)
	lab := f.project(t, ws)

	require.NoError(t, f.create(f.task(t, spark), f.task(t, lab), domain.DependencyTypeRelatesTo))
	assert.Len(t, f.depRepo.items, 1)
}

// The security property, unchanged. Two workspaces must stay separated.
func TestCrossProject_DifferentWorkspaces_Refused(t *testing.T) {
	f := newCrossProjectFixture(t)
	mine := f.project(t, uuid.New())
	theirs := f.project(t, uuid.New())

	err := f.create(f.task(t, mine), f.task(t, theirs), domain.DependencyTypeBlocks)
	require.Error(t, err, "an edge was written across the tenant boundary")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	assert.Empty(t, f.depRepo.items, "the edge was persisted anyway")
}

// The oracle property. A target id whose project row does not exist, and a target id in
// another workspace, must be refused with the SAME status and the SAME message — or the
// difference between them answers "does this id exist?" for ids the caller does not own.
func TestCrossProject_RefusalIsIndistinguishable(t *testing.T) {
	f := newCrossProjectFixture(t)
	mine := f.project(t, uuid.New())
	theirs := f.project(t, uuid.New())

	otherWorkspace := f.create(f.task(t, mine), f.task(t, theirs), domain.DependencyTypeBlocks)

	// Same shape, but the far task's project was never registered.
	unregistered := uuid.New()
	unknownProject := f.create(f.task(t, mine), f.task(t, unregistered), domain.DependencyTypeBlocks)

	require.Error(t, otherWorkspace)
	require.Error(t, unknownProject)
	assert.Equal(t, otherWorkspace.Error(), unknownProject.Error(),
		"the two refusals differ, which tells the caller whether the target's project exists")
}

// Fail-closed: an unreadable project repository refuses rather than allowing. "Could
// not establish that these are the same tenant" must not become permission.
func TestCrossProject_ProjectLookupFails_Refused(t *testing.T) {
	f := newCrossProjectFixture(t)
	ws := uuid.New()
	a := f.project(t, ws)
	b := f.project(t, ws)
	card, blocker := f.task(t, a), f.task(t, b)

	f.projRepo.errToReturn = errors.New("db down")

	require.Error(t, f.create(card, blocker, domain.DependencyTypeBlocks),
		"an unreadable project repo must refuse the cross-project edge, not allow it")
	assert.Empty(t, f.depRepo.items)
}

// Fail-closed: no project repository wired at all is the same answer.
func TestCrossProject_NilProjectRepo_Refused(t *testing.T) {
	depRepo := NewMockTaskDependencyRepository()
	taskRepo := NewMockTaskRepository()
	svc := NewTaskDependencyService(depRepo, taskRepo, NewMockActivityLogRepository(), nil).(*taskDependencyService)

	a, b := uuid.New(), uuid.New()
	taskRepo.items[a] = &domain.Task{ID: a, ProjectID: uuid.New(), Title: "a"}
	taskRepo.items[b] = &domain.Task{ID: b, ProjectID: uuid.New(), Title: "b"}

	err := svc.Create(context.Background(), &domain.TaskDependency{
		TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks,
	})
	require.Error(t, err, "without a project repo the workspace cannot be checked, so the edge must be refused")
	assert.Empty(t, depRepo.items)
}

// Hierarchy does not cross projects even inside one workspace: is_child_of also writes
// parent_task_id, which drives subtask_count and the Subtasks tab, all read through a
// project-scoped lens. A parent elsewhere renders as a child count nobody can open.
func TestCrossProject_IsChildOfAcrossProjects_Refused(t *testing.T) {
	f := newCrossProjectFixture(t)
	ws := uuid.New()
	a := f.project(t, ws)
	b := f.project(t, ws)

	child, parent := f.task(t, a), f.task(t, b)
	err := f.create(child, parent, domain.DependencyTypeIsChildOf)
	require.Error(t, err, "is_child_of must not cross projects")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	assert.Nil(t, f.taskRepo.items[child].ParentTaskID, "a refused edge must not have set the parent")
}

// Positive control for the clause above: the same is_child_of edge inside ONE project
// still works. Without it, a guard that refused every is_child_of would look identical.
func TestCrossProject_IsChildOfSameProject_Allowed(t *testing.T) {
	f := newCrossProjectFixture(t)
	p := f.project(t, uuid.New())
	child, parent := f.task(t, p), f.task(t, p)

	require.NoError(t, f.create(child, parent, domain.DependencyTypeIsChildOf))
	require.NotNil(t, f.taskRepo.items[child].ParentTaskID)
	assert.Equal(t, parent, *f.taskRepo.items[child].ParentTaskID)
}
