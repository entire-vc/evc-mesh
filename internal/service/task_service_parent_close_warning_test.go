package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Closing a parent over open children is allowed, but the response says so.

type parentCloseFixture struct {
	svc        *taskService
	taskRepo   *MockTaskRepository
	projectID  uuid.UUID
	open, done domain.TaskStatus
	parent     *domain.Task
}

func newParentCloseFixture(t *testing.T) *parentCloseFixture {
	t.Helper()
	svc, taskRepo, statusRepo := setupTaskService()
	projectID := uuid.New()
	f := &parentCloseFixture{svc: svc, taskRepo: taskRepo, projectID: projectID}
	f.open = domain.TaskStatus{ID: uuid.New(), ProjectID: projectID, Category: domain.StatusCategoryInProgress}
	f.done = domain.TaskStatus{ID: uuid.New(), ProjectID: projectID, Category: domain.StatusCategoryDone}
	cancelled := domain.TaskStatus{ID: uuid.New(), ProjectID: projectID, Category: domain.StatusCategoryCancelled}
	for _, st := range []domain.TaskStatus{f.open, f.done, cancelled} {
		st := st
		statusRepo.items[st.ID] = &st
	}
	f.parent = &domain.Task{ID: uuid.New(), ProjectID: projectID, StatusID: f.open.ID, Title: "parent"}
	taskRepo.items[f.parent.ID] = f.parent
	return f
}

func (f *parentCloseFixture) addChild(statusID uuid.UUID) *domain.Task {
	c := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: statusID, ParentTaskID: &f.parent.ID, Title: "child"}
	f.taskRepo.items[c.ID] = c
	return c
}

func (f *parentCloseFixture) moveParentToDone(t *testing.T) []MoveWarning {
	t.Helper()
	ctx, collected := ContextWithMoveWarnings(context.Background())
	require.NoError(t, f.svc.MoveTask(ctx, f.parent.ID, MoveTaskInput{StatusID: &f.done.ID}))
	return collected.Items()
}

func TestMoveTask_ParentClosedOverOpenChildren_WarnsButSucceeds(t *testing.T) {
	f := newParentCloseFixture(t)
	c1 := f.addChild(f.open.ID)
	c2 := f.addChild(f.open.ID)
	f.addChild(f.done.ID)

	warnings := f.moveParentToDone(t)

	require.Len(t, warnings, 1)
	assert.Equal(t, MoveWarningOpenChildren, warnings[0].Code)
	assert.Equal(t, 2, warnings[0].OpenChildren)
	assert.ElementsMatch(t, []uuid.UUID{c1.ID, c2.ID}, warnings[0].ChildIDs)
	assert.Equal(t, f.done.ID, f.taskRepo.items[f.parent.ID].StatusID, "the move itself is not blocked")
	assert.Equal(t, f.open.ID, f.taskRepo.items[c1.ID].StatusID, "children are not touched")
}

func TestMoveTask_ParentClosedOverIsChildOfEdge_Warns(t *testing.T) {
	f := newParentCloseFixture(t)
	// A child recorded only as an is_child_of edge (no parent_task_id).
	child := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.open.ID, Title: "edge child"}
	f.taskRepo.items[child.ID] = child
	dep := &domain.TaskDependency{ID: uuid.New(), TaskID: child.ID, DependsOnTaskID: f.parent.ID, DependencyType: domain.DependencyTypeIsChildOf}
	f.svc.depRepo.(*MockTaskDependencyRepository).items[dep.ID] = dep
	// A blocks edge pointing at the parent is not a child.
	blocker := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.open.ID, Title: "dependent"}
	f.taskRepo.items[blocker.ID] = blocker
	bd := &domain.TaskDependency{ID: uuid.New(), TaskID: blocker.ID, DependsOnTaskID: f.parent.ID, DependencyType: domain.DependencyTypeBlocks}
	f.svc.depRepo.(*MockTaskDependencyRepository).items[bd.ID] = bd

	warnings := f.moveParentToDone(t)

	require.Len(t, warnings, 1)
	assert.Equal(t, 1, warnings[0].OpenChildren)
	assert.Equal(t, []uuid.UUID{child.ID}, warnings[0].ChildIDs)
}

func TestMoveTask_ParentClosedWithAllChildrenClosed_NoWarning(t *testing.T) {
	f := newParentCloseFixture(t)
	f.addChild(f.done.ID)

	assert.Empty(t, f.moveParentToDone(t))
}

func TestMoveTask_ChildWithOpenChildrenMovedToNonDone_NoWarning(t *testing.T) {
	f := newParentCloseFixture(t)
	f.addChild(f.open.ID)
	review := domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Category: domain.StatusCategoryInProgress}
	f.svc.statusRepo.(*MockTaskStatusRepository).items[review.ID] = &review

	ctx, collected := ContextWithMoveWarnings(context.Background())
	require.NoError(t, f.svc.MoveTask(ctx, f.parent.ID, MoveTaskInput{StatusID: &review.ID}))
	assert.Empty(t, collected.Items(), "only a move into a done category warns")
}

func TestMoveTask_WithoutCollector_StillSucceeds(t *testing.T) {
	f := newParentCloseFixture(t)
	f.addChild(f.open.ID)

	require.NoError(t, f.svc.MoveTask(context.Background(), f.parent.ID, MoveTaskInput{StatusID: &f.done.ID}))
	assert.Equal(t, f.done.ID, f.taskRepo.items[f.parent.ID].StatusID)
}
