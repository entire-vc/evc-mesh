package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// setupTaskDependencyService returns a taskDependencyService wired to fresh mocks.
func setupTaskDependencyService() (*taskDependencyService, *MockTaskDependencyRepository, *MockTaskRepository) {
	depRepo := NewMockTaskDependencyRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewTaskDependencyService(depRepo, taskRepo, activityRepo).(*taskDependencyService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, depRepo, taskRepo
}

// helper to add a task to the mock repo.
func addTask(repo *MockTaskRepository, id uuid.UUID) {
	repo.items[id] = &domain.Task{ID: id, Title: "Task " + id.String()}
}

// ---------------------------------------------------------------------------
// TestTaskDependencyService_Create
// ---------------------------------------------------------------------------

func TestTaskDependencyService_Create(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency
		wantErr   bool
		errCode   int
		errMsg    string
		checkFunc func(t *testing.T, dep *domain.TaskDependency, depRepo *MockTaskDependencyRepository)
	}{
		{
			name: "success",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: taskB,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, dep *domain.TaskDependency, depRepo *MockTaskDependencyRepository) {
				assert.NotEqual(t, uuid.Nil, dep.ID)
				assert.Equal(t, frozenTime, dep.CreatedAt)
				stored := depRepo.items[dep.ID]
				require.NotNil(t, stored)
			},
		},
		{
			name: "error - self-reference",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				addTask(taskRepo, taskA)
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: taskA,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "cannot depend on itself",
		},
		{
			name: "error - task not found",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				addTask(taskRepo, taskA)
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: uuid.New(), // does not exist
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - duplicate dependency",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				// Add existing dependency.
				existingID := uuid.New()
				depRepo.items[existingID] = &domain.TaskDependency{
					ID:              existingID,
					TaskID:          taskA,
					DependsOnTaskID: taskB,
				}
				return &domain.TaskDependency{
					TaskID:          taskA,
					DependsOnTaskID: taskB,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusConflict,
			errMsg:  "already exists",
		},
		{
			name: "error - cycle detection (A->B->C, adding C->A)",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) *domain.TaskDependency {
				taskA := uuid.New()
				taskB := uuid.New()
				taskC := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				addTask(taskRepo, taskC)

				// A depends on B.
				depAB := uuid.New()
				depRepo.items[depAB] = &domain.TaskDependency{
					ID:              depAB,
					TaskID:          taskA,
					DependsOnTaskID: taskB,
				}
				// B depends on C.
				depBC := uuid.New()
				depRepo.items[depBC] = &domain.TaskDependency{
					ID:              depBC,
					TaskID:          taskB,
					DependsOnTaskID: taskC,
				}

				// Try to add C -> A which would create a cycle: C -> A -> B -> C.
				return &domain.TaskDependency{
					TaskID:          taskC,
					DependsOnTaskID: taskA,
					DependencyType:  domain.DependencyTypeBlocks,
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "cycle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, depRepo, taskRepo := setupTaskDependencyService()
			ctx := context.Background()
			dep := tt.setup(depRepo, taskRepo)

			err := svc.Create(ctx, dep)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				if tt.errMsg != "" {
					assert.Contains(t, apiErr.Message, tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, dep, depRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTaskDependencyService_Create_IsChildOf
//
// is_child_of is the one dependency type that must also set parent_task_id
// on the child task, since that's what the Subtasks tab and subtask_count
// actually read (see #5d670601: the "Child of" field recorded an edge
// nothing else consumed).
// ---------------------------------------------------------------------------

func TestTaskDependencyService_Create_IsChildOf(t *testing.T) {
	t.Run("sets parent_task_id on the child", func(t *testing.T) {
		svc, _, taskRepo := setupTaskDependencyService()
		ctx := context.Background()

		child := uuid.New()
		parent := uuid.New()
		addTask(taskRepo, child)
		addTask(taskRepo, parent)

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID:          child,
			DependsOnTaskID: parent,
			DependencyType:  domain.DependencyTypeIsChildOf,
		})
		require.NoError(t, err)

		got, err := taskRepo.GetByID(ctx, child)
		require.NoError(t, err)
		require.NotNil(t, got.ParentTaskID)
		assert.Equal(t, parent, *got.ParentTaskID)
	})

	t.Run("re-adding the same parent is idempotent", func(t *testing.T) {
		svc, depRepo, taskRepo := setupTaskDependencyService()
		ctx := context.Background()

		child := uuid.New()
		parent := uuid.New()
		addTask(taskRepo, child)
		addTask(taskRepo, parent)
		taskRepo.items[child].ParentTaskID = &parent

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID:          child,
			DependsOnTaskID: parent,
			DependencyType:  domain.DependencyTypeIsChildOf,
		})
		require.NoError(t, err)
		assert.Len(t, depRepo.items, 1)
	})

	t.Run("error - task already has a different parent", func(t *testing.T) {
		svc, _, taskRepo := setupTaskDependencyService()
		ctx := context.Background()

		child := uuid.New()
		existingParent := uuid.New()
		newParent := uuid.New()
		addTask(taskRepo, child)
		addTask(taskRepo, existingParent)
		addTask(taskRepo, newParent)
		taskRepo.items[child].ParentTaskID = &existingParent

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID:          child,
			DependsOnTaskID: newParent,
			DependencyType:  domain.DependencyTypeIsChildOf,
		})
		require.Error(t, err)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusConflict, apiErr.Code)
	})

	t.Run("error - would create a parent-tree cycle", func(t *testing.T) {
		svc, _, taskRepo := setupTaskDependencyService()
		ctx := context.Background()

		grandparent := uuid.New()
		parent := uuid.New()
		child := uuid.New()
		addTask(taskRepo, grandparent)
		addTask(taskRepo, parent)
		addTask(taskRepo, child)
		// parent is already a child of grandparent; child is already a child of parent.
		taskRepo.items[parent].ParentTaskID = &grandparent
		taskRepo.items[child].ParentTaskID = &parent

		// Now try to make grandparent a child of child: child -> parent -> grandparent -> child.
		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID:          grandparent,
			DependsOnTaskID: child,
			DependencyType:  domain.DependencyTypeIsChildOf,
		})
		require.Error(t, err)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Code)
		assert.Contains(t, apiErr.Message, "cycle")
	})

	t.Run("other dependency types do not touch parent_task_id", func(t *testing.T) {
		svc, _, taskRepo := setupTaskDependencyService()
		ctx := context.Background()

		taskA := uuid.New()
		taskB := uuid.New()
		addTask(taskRepo, taskA)
		addTask(taskRepo, taskB)

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID:          taskA,
			DependsOnTaskID: taskB,
			DependencyType:  domain.DependencyTypeBlocks,
		})
		require.NoError(t, err)

		got, err := taskRepo.GetByID(ctx, taskA)
		require.NoError(t, err)
		assert.Nil(t, got.ParentTaskID)
	})
}

// ---------------------------------------------------------------------------
// TestTaskDependencyService_CheckCycle
// ---------------------------------------------------------------------------

func TestTaskDependencyService_CheckCycle(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (taskID, dependsOnTaskID uuid.UUID)
		wantCycle bool
	}{
		{
			name: "no cycle - independent tasks",
			setup: func(_ *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (uuid.UUID, uuid.UUID) {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				return taskA, taskB
			},
			wantCycle: false,
		},
		{
			name: "no cycle - linear chain A->B->C, adding D->A",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (uuid.UUID, uuid.UUID) {
				taskA := uuid.New()
				taskB := uuid.New()
				taskC := uuid.New()
				taskD := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)
				addTask(taskRepo, taskC)
				addTask(taskRepo, taskD)

				depAB := uuid.New()
				depRepo.items[depAB] = &domain.TaskDependency{ID: depAB, TaskID: taskA, DependsOnTaskID: taskB}
				depBC := uuid.New()
				depRepo.items[depBC] = &domain.TaskDependency{ID: depBC, TaskID: taskB, DependsOnTaskID: taskC}

				// Adding D -> A: from A, can we reach D? No.
				return taskD, taskA
			},
			wantCycle: false,
		},
		{
			name: "cycle - A->B, adding B->A",
			setup: func(depRepo *MockTaskDependencyRepository, taskRepo *MockTaskRepository) (uuid.UUID, uuid.UUID) {
				taskA := uuid.New()
				taskB := uuid.New()
				addTask(taskRepo, taskA)
				addTask(taskRepo, taskB)

				depAB := uuid.New()
				depRepo.items[depAB] = &domain.TaskDependency{ID: depAB, TaskID: taskA, DependsOnTaskID: taskB}

				// Adding B -> A: from A, can we reach B? Yes (A -> B).
				return taskB, taskA
			},
			wantCycle: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, depRepo, taskRepo := setupTaskDependencyService()
			ctx := context.Background()
			taskID, dependsOnTaskID := tt.setup(depRepo, taskRepo)

			hasCycle, err := svc.CheckCycle(ctx, taskID, dependsOnTaskID)

			require.NoError(t, err)
			assert.Equal(t, tt.wantCycle, hasCycle)
		})
	}
}

// TestTaskDependencyService_Create_RefusesAnotherProjectsTask is the cross-tenant
// repro at the service.
//
// depends_on_task_id arrives in the request body, so no route parameter names it
// and the workspace guard — which reads route parameters — never sees it; :task_id
// is the caller's own task and resolves to the caller's own workspace. "Both tasks
// exist" was the entire check, which made this two things at once: an edge written
// across the tenant boundary, and an oracle that answered 404 for a task id that
// does not exist and 201 for one that does, in anybody's workspace.
func TestTaskDependencyService_Create_RefusesAnotherProjectsTask(t *testing.T) {
	svc, depRepo, taskRepo := setupTaskDependencyService()

	ownProject := uuid.New()
	foreignProject := uuid.New()

	ownTask := uuid.New()
	foreignTask := uuid.New()
	taskRepo.items[ownTask] = &domain.Task{ID: ownTask, ProjectID: ownProject, Title: "Mine"}
	taskRepo.items[foreignTask] = &domain.Task{ID: foreignTask, ProjectID: foreignProject, Title: "Theirs"}

	err := svc.Create(context.Background(), &domain.TaskDependency{
		TaskID:          ownTask,
		DependsOnTaskID: foreignTask,
		DependencyType:  domain.DependencyTypeBlocks,
	})
	require.Error(t, err, "an edge was written across the tenant boundary")

	var apiErr *apierror.Error
	if assert.ErrorAs(t, err, &apiErr) {
		assert.Equal(t, http.StatusBadRequest, apiErr.Code)
	}
	assert.Empty(t, depRepo.items, "the edge was persisted anyway")
}

// Delete has to undo what Create did, or the pair is a one-way trap: the
// is_child_of edge is the only UI that can set a parent, Create refuses a second
// one while the first is set, and updateTaskRequest carries no parent_task_id.
// So a task whose edge is deleted without clearing the parent stays parented
// with nothing left to remove.
func TestTaskDependencyService_Delete_IsChildOf(t *testing.T) {
	newParentedPair := func(t *testing.T) (*taskDependencyService, *MockTaskDependencyRepository, *MockTaskRepository, uuid.UUID, uuid.UUID, uuid.UUID) {
		t.Helper()
		svc, depRepo, taskRepo := setupTaskDependencyService()
		child, parent := uuid.New(), uuid.New()
		addTask(taskRepo, child)
		addTask(taskRepo, parent)

		dep := &domain.TaskDependency{
			TaskID:          child,
			DependsOnTaskID: parent,
			DependencyType:  domain.DependencyTypeIsChildOf,
		}
		require.NoError(t, svc.Create(context.Background(), dep))
		require.NotNil(t, taskRepo.items[child].ParentTaskID, "precondition: Create must have set the parent")
		return svc, depRepo, taskRepo, dep.ID, child, parent
	}

	t.Run("clears parent_task_id", func(t *testing.T) {
		svc, depRepo, taskRepo, depID, child, _ := newParentedPair(t)

		require.NoError(t, svc.Delete(context.Background(), depID))

		assert.Nil(t, taskRepo.items[child].ParentTaskID,
			"removing the Child-of edge must un-parent the task — it is the only way back")
		assert.Empty(t, depRepo.items, "the edge itself must still be gone")
	})

	t.Run("the task can then be re-parented", func(t *testing.T) {
		svc, _, taskRepo, depID, child, _ := newParentedPair(t)
		ctx := context.Background()
		require.NoError(t, svc.Delete(ctx, depID))

		newParent := uuid.New()
		addTask(taskRepo, newParent)
		require.NoError(t, svc.Create(ctx, &domain.TaskDependency{
			TaskID:          child,
			DependsOnTaskID: newParent,
			DependencyType:  domain.DependencyTypeIsChildOf,
		}), "re-parenting after removing the old edge must not hit 'already has a parent'")

		require.NotNil(t, taskRepo.items[child].ParentTaskID)
		assert.Equal(t, newParent, *taskRepo.items[child].ParentTaskID)
	})

	// A stale edge must not detach a task that has since been re-parented, or
	// deleting old history would silently break a current relationship.
	t.Run("leaves a parent this edge did not set", func(t *testing.T) {
		svc, _, taskRepo, depID, child, _ := newParentedPair(t)

		otherParent := uuid.New()
		taskRepo.items[child].ParentTaskID = &otherParent

		require.NoError(t, svc.Delete(context.Background(), depID))

		require.NotNil(t, taskRepo.items[child].ParentTaskID,
			"a stale edge must not detach a task that was re-parented elsewhere")
		assert.Equal(t, otherParent, *taskRepo.items[child].ParentTaskID)
	})

	// Only is_child_of carries the side effect; every other edge type must leave
	// the hierarchy alone.
	t.Run("other dependency types leave parent_task_id untouched", func(t *testing.T) {
		svc, _, taskRepo := setupTaskDependencyService()
		ctx := context.Background()

		child, parent, blocker := uuid.New(), uuid.New(), uuid.New()
		addTask(taskRepo, child)
		addTask(taskRepo, parent)
		addTask(taskRepo, blocker)
		taskRepo.items[child].ParentTaskID = &parent

		blocks := &domain.TaskDependency{
			TaskID:          child,
			DependsOnTaskID: blocker,
			DependencyType:  domain.DependencyTypeBlocks,
		}
		require.NoError(t, svc.Create(ctx, blocks))
		require.NoError(t, svc.Delete(ctx, blocks.ID))

		require.NotNil(t, taskRepo.items[child].ParentTaskID,
			"deleting a blocks edge must not touch the parent hierarchy")
		assert.Equal(t, parent, *taskRepo.items[child].ParentTaskID)
	})
}

// ---------------------------------------------------------------------------
// Error paths. The mocks carry a single blanket errToReturn, which cannot fail
// one method while another succeeds, so these embed a mock and override the one
// call under test — the pattern used in invite_accept_username_test.go.
// ---------------------------------------------------------------------------

// updateFailTaskRepo fails Update while reads keep working.
type updateFailTaskRepo struct {
	*MockTaskRepository
	err error
}

func (r *updateFailTaskRepo) Update(context.Context, *domain.Task) error { return r.err }

// nthGetFailTaskRepo fails the Nth GetByID (1-based) and serves the rest normally.
// Create reads both endpoints before walking the parent chain, so failing an
// early call would test the wrong branch.
type nthGetFailTaskRepo struct {
	*MockTaskRepository
	failOn int
	calls  int
	err    error
}

func (r *nthGetFailTaskRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	r.calls++
	if r.calls == r.failOn {
		return nil, r.err
	}
	return r.MockTaskRepository.GetByID(ctx, id)
}

type createFailDepRepo struct {
	*MockTaskDependencyRepository
	err error
}

func (r *createFailDepRepo) Create(context.Context, *domain.TaskDependency) error { return r.err }

type deleteFailDepRepo struct {
	*MockTaskDependencyRepository
	err error
}

func (r *deleteFailDepRepo) Delete(context.Context, uuid.UUID) error { return r.err }

type getFailDepRepo struct {
	*MockTaskDependencyRepository
	err error
}

func (r *getFailDepRepo) GetByID(context.Context, uuid.UUID) (*domain.TaskDependency, error) {
	return nil, r.err
}

// seedPair returns two tasks in one project, ready to be linked.
func seedPair(taskRepo *MockTaskRepository) (child, parent uuid.UUID) {
	child, parent = uuid.New(), uuid.New()
	addTask(taskRepo, child)
	addTask(taskRepo, parent)
	return child, parent
}

func TestTaskDependencyService_Create_ErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("dependency write failure propagates and sets no parent", func(t *testing.T) {
		taskRepo := NewMockTaskRepository()
		depRepo := &createFailDepRepo{NewMockTaskDependencyRepository(), assert.AnError}
		svc := NewTaskDependencyService(depRepo, taskRepo, NewMockActivityLogRepository())
		child, parent := seedPair(taskRepo)

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID: child, DependsOnTaskID: parent,
			DependencyType: domain.DependencyTypeIsChildOf,
		})

		require.ErrorIs(t, err, assert.AnError)
		assert.Nil(t, taskRepo.items[child].ParentTaskID,
			"the parent must not be set when the edge itself was never written")
	})

	// Documents the known non-atomicity: the edge is already written when the
	// parent write fails. Pinned so the behaviour is a decision, not a surprise.
	t.Run("parent write failure propagates", func(t *testing.T) {
		base := NewMockTaskRepository()
		taskRepo := &updateFailTaskRepo{base, assert.AnError}
		depRepo := NewMockTaskDependencyRepository()
		svc := NewTaskDependencyService(depRepo, taskRepo, NewMockActivityLogRepository())
		child, parent := seedPair(base)

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID: child, DependsOnTaskID: parent,
			DependencyType: domain.DependencyTypeIsChildOf,
		})

		require.ErrorIs(t, err, assert.AnError)
		assert.Len(t, depRepo.items, 1, "the edge is written before the parent — not atomic today")
	})

	t.Run("cycle walk read failure propagates", func(t *testing.T) {
		base := NewMockTaskRepository()
		child, parent := seedPair(base)
		// Calls 1 and 2 are Create's own endpoint reads; the walk is call 3.
		taskRepo := &nthGetFailTaskRepo{MockTaskRepository: base, failOn: 3, err: assert.AnError}
		svc := NewTaskDependencyService(NewMockTaskDependencyRepository(), taskRepo, NewMockActivityLogRepository())

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID: child, DependsOnTaskID: parent,
			DependencyType: domain.DependencyTypeIsChildOf,
		})

		require.ErrorIs(t, err, assert.AnError,
			"an unreadable ancestor must fail closed — we cannot prove the absence of a cycle")
	})

	// A pre-existing cycle among ancestors must terminate the walk rather than
	// spin. Without the visited set this call would not return.
	t.Run("pre-existing ancestor cycle terminates the walk", func(t *testing.T) {
		taskRepo := NewMockTaskRepository()
		svc := NewTaskDependencyService(NewMockTaskDependencyRepository(), taskRepo, NewMockActivityLogRepository())

		child, a := seedPair(taskRepo)
		b := uuid.New()
		addTask(taskRepo, b)
		taskRepo.items[a].ParentTaskID = &b
		taskRepo.items[b].ParentTaskID = &a

		err := svc.Create(ctx, &domain.TaskDependency{
			TaskID: child, DependsOnTaskID: a,
			DependencyType: domain.DependencyTypeIsChildOf,
		})

		require.NoError(t, err, "the child is not in that cycle, so the edge is allowed")
		require.NotNil(t, taskRepo.items[child].ParentTaskID)
		assert.Equal(t, a, *taskRepo.items[child].ParentTaskID)
	})
}

func TestTaskDependencyService_Delete_ErrorPaths(t *testing.T) {
	ctx := context.Background()

	// An unreadable edge must not block its own removal — see Delete's comment.
	t.Run("edge read failure still deletes and is not an error", func(t *testing.T) {
		taskRepo := NewMockTaskRepository()
		inner := NewMockTaskDependencyRepository()
		depRepo := &getFailDepRepo{inner, assert.AnError}
		svc := NewTaskDependencyService(depRepo, taskRepo, NewMockActivityLogRepository())

		child, parent := seedPair(taskRepo)
		dep := &domain.TaskDependency{
			ID: uuid.New(), TaskID: child, DependsOnTaskID: parent,
			DependencyType: domain.DependencyTypeIsChildOf,
		}
		require.NoError(t, inner.Create(ctx, dep))
		taskRepo.items[child].ParentTaskID = &parent

		require.NoError(t, svc.Delete(ctx, dep.ID),
			"a read failure must not turn into a refusal to delete")
		assert.Empty(t, inner.items, "the edge must still be gone")
		assert.NotNil(t, taskRepo.items[child].ParentTaskID,
			"unknown type means nothing to undo — the parent is left as-is")
	})

	t.Run("delete failure propagates", func(t *testing.T) {
		depRepo := &deleteFailDepRepo{NewMockTaskDependencyRepository(), assert.AnError}
		svc := NewTaskDependencyService(depRepo, NewMockTaskRepository(), NewMockActivityLogRepository())

		require.ErrorIs(t, svc.Delete(ctx, uuid.New()), assert.AnError)
	})

	t.Run("task read failure propagates", func(t *testing.T) {
		base := NewMockTaskRepository()
		child, parent := seedPair(base)
		depRepo := NewMockTaskDependencyRepository()
		svc := NewTaskDependencyService(depRepo, base, NewMockActivityLogRepository())
		dep := &domain.TaskDependency{
			TaskID: child, DependsOnTaskID: parent,
			DependencyType: domain.DependencyTypeIsChildOf,
		}
		require.NoError(t, svc.Create(ctx, dep))

		// Fail the read Delete performs to find the child.
		failing := &nthGetFailTaskRepo{MockTaskRepository: base, failOn: 1, err: assert.AnError}
		svc2 := NewTaskDependencyService(depRepo, failing, NewMockActivityLogRepository())

		require.ErrorIs(t, svc2.Delete(ctx, dep.ID), assert.AnError)
	})
}

// ---------------------------------------------------------------------------
// TestTaskDependencyService_ListByTaskBothDirections
// ---------------------------------------------------------------------------

func TestTaskDependencyService_ListByTaskBothDirections(t *testing.T) {
	ctx := context.Background()

	t.Run("parent sees its subtask as incoming, child sees its parent as outgoing", func(t *testing.T) {
		svc, _, taskRepo := setupTaskDependencyService()
		child, parent := seedPair(taskRepo)
		taskRepo.items[parent].Title = "Parent task"
		taskRepo.items[child].Title = "Child task"

		dep := &domain.TaskDependency{TaskID: child, DependsOnTaskID: parent, DependencyType: domain.DependencyTypeIsChildOf}
		require.NoError(t, svc.Create(ctx, dep))

		// From the child's side: the edge it created is outgoing.
		outFromChild, inFromChild, err := svc.ListByTaskBothDirections(ctx, child)
		require.NoError(t, err)
		require.Len(t, outFromChild, 1)
		assert.Empty(t, inFromChild)
		assert.Equal(t, parent, outFromChild[0].DependsOnTaskID)
		require.NotNil(t, outFromChild[0].RelatedTaskTitle)
		assert.Equal(t, "Parent task", *outFromChild[0].RelatedTaskTitle)
		require.NotNil(t, outFromChild[0].RelatedTaskStatusID)
		assert.Equal(t, taskRepo.items[parent].StatusID, *outFromChild[0].RelatedTaskStatusID)

		// From the parent's side: the same edge only shows up as incoming — this
		// is exactly what ListByTask (task_id = $1 only) could never surface.
		outFromParent, inFromParent, err := svc.ListByTaskBothDirections(ctx, parent)
		require.NoError(t, err)
		assert.Empty(t, outFromParent)
		require.Len(t, inFromParent, 1)
		assert.Equal(t, child, inFromParent[0].TaskID)
		require.NotNil(t, inFromParent[0].RelatedTaskTitle)
		assert.Equal(t, "Child task", *inFromParent[0].RelatedTaskTitle)
	})

	t.Run("related task deleted out from under the edge — edge kept, title nil", func(t *testing.T) {
		svc, depRepo, taskRepo := setupTaskDependencyService()
		child, parent := seedPair(taskRepo)
		dep := &domain.TaskDependency{ID: uuid.New(), TaskID: child, DependsOnTaskID: parent, DependencyType: domain.DependencyTypeBlocks}
		require.NoError(t, depRepo.Create(ctx, dep))

		delete(taskRepo.items, parent) // simulate a hard-deleted related task

		outgoing, _, err := svc.ListByTaskBothDirections(ctx, child)
		require.NoError(t, err)
		require.Len(t, outgoing, 1, "the edge itself must still be returned")
		assert.Nil(t, outgoing[0].RelatedTaskTitle)
		assert.Nil(t, outgoing[0].RelatedTaskStatusID)
	})

	t.Run("outgoing read failure propagates", func(t *testing.T) {
		taskRepo := NewMockTaskRepository()
		depRepo := &listByTaskFailDepRepo{NewMockTaskDependencyRepository(), assert.AnError}
		svc := NewTaskDependencyService(depRepo, taskRepo, NewMockActivityLogRepository())

		_, _, err := svc.ListByTaskBothDirections(ctx, uuid.New())
		require.ErrorIs(t, err, assert.AnError)
	})

	t.Run("incoming read failure propagates", func(t *testing.T) {
		taskRepo := NewMockTaskRepository()
		depRepo := &listDependentsFailDepRepo{NewMockTaskDependencyRepository(), assert.AnError}
		svc := NewTaskDependencyService(depRepo, taskRepo, NewMockActivityLogRepository())

		_, _, err := svc.ListByTaskBothDirections(ctx, uuid.New())
		require.ErrorIs(t, err, assert.AnError)
	})
}

// listByTaskFailDepRepo fails only ListByTask; every other method is the real mock.
type listByTaskFailDepRepo struct {
	*MockTaskDependencyRepository
	err error
}

func (r *listByTaskFailDepRepo) ListByTask(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
	return nil, r.err
}

// listDependentsFailDepRepo fails only ListDependents; every other method is the real mock.
type listDependentsFailDepRepo struct {
	*MockTaskDependencyRepository
	err error
}

func (r *listDependentsFailDepRepo) ListDependents(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
	return nil, r.err
}
