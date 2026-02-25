//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoTransition_SubtasksDone verifies that a parent task is automatically
// moved to the "done" status when all of its subtasks are completed.
//
// Scenario:
//  1. Create a project and obtain default statuses.
//  2. Create a parent task with status "in_progress".
//  3. Create 3 subtasks under the parent.
//  4. Move subtask 1 to "done" — assert parent remains "in_progress".
//  5. Move subtask 2 to "done" — assert parent remains "in_progress".
//  6. Move subtask 3 to "done" — assert parent is automatically moved to
//     the configured "done" status.
func TestAutoTransition_SubtasksDone(t *testing.T) {
	t.Skip("TODO: implement auto-transitions first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("auto-trans-subtasks")
	env.Register(t, email, "TestPass123", "AutoTransition Tester")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Create project.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "AutoTransition Subtask Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Fetch statuses — find in_progress and done IDs.
	resp = env.Get(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statuses []map[string]interface{}
	env.DecodeJSON(t, resp, &statuses)

	var inProgressStatusID, doneStatusID string
	for _, s := range statuses {
		switch s["category"] {
		case "in_progress":
			inProgressStatusID = s["id"].(string)
		case "done":
			doneStatusID = s["id"].(string)
		}
	}
	require.NotEmpty(t, inProgressStatusID, "project must have an in_progress status")
	require.NotEmpty(t, doneStatusID, "project must have a done status")

	// --- Step 1: Create parent task in "in_progress" ---
	var parentTaskID string
	t.Run("CreateParentTask", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":     "Parent Task",
			"priority":  "medium",
			"status_id": inProgressStatusID,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		parentTaskID = task["id"].(string)
		assert.Equal(t, inProgressStatusID, task["status_id"])
	})

	// --- Step 2: Create 3 subtasks ---
	subtaskIDs := make([]string, 0, 3)
	t.Run("CreateSubtasks", func(t *testing.T) {
		if parentTaskID == "" {
			t.Skip("parent task not created")
		}
		for i := 1; i <= 3; i++ {
			resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
				"title":          fmt.Sprintf("Subtask %d", i),
				"priority":       "low",
				"parent_task_id": parentTaskID,
			})
			if resp.StatusCode == http.StatusCreated {
				var subtask map[string]interface{}
				env.DecodeJSON(t, resp, &subtask)
				subtaskIDs = append(subtaskIDs, subtask["id"].(string))
			} else {
				resp.Body.Close()
			}
		}
		require.Len(t, subtaskIDs, 3, "all 3 subtasks must be created")
	})

	// --- Steps 3-4: Complete subtasks one by one; assert parent stays in_progress ---
	for i, subtaskID := range subtaskIDs[:2] {
		subtaskID := subtaskID
		idx := i + 1
		t.Run(fmt.Sprintf("CompleteSubtask%d_ParentUnchanged", idx), func(t *testing.T) {
			resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/move", subtaskID), map[string]interface{}{
				"status_id": doneStatusID,
			})
			require.Equal(t, http.StatusOK, resp.StatusCode)
			resp.Body.Close()

			// Parent must still be in_progress.
			resp = env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", parentTaskID))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			var parent map[string]interface{}
			env.DecodeJSON(t, resp, &parent)
			assert.Equal(t, inProgressStatusID, parent["status_id"],
				"parent must remain in_progress while subtasks are pending")
		})
	}

	// --- Step 5: Complete the last subtask; parent should auto-transition ---
	t.Run("CompleteLastSubtask_ParentAutoTransitions", func(t *testing.T) {
		if len(subtaskIDs) < 3 {
			t.Skip("not all subtasks created")
		}
		lastSubtaskID := subtaskIDs[2]

		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/move", lastSubtaskID), map[string]interface{}{
			"status_id": doneStatusID,
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()

		// Parent must now be in "done" (auto-transition triggered).
		resp = env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", parentTaskID))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var parent map[string]interface{}
		env.DecodeJSON(t, resp, &parent)
		assert.Equal(t, doneStatusID, parent["status_id"],
			"parent must auto-transition to done when all subtasks are done")
	})
}

// TestAutoTransition_DependencyResolved verifies that completing a blocking
// task generates an "unblocked" activity log entry for the dependent task
// (and optionally moves it to a ready-to-start status if configured).
//
// Scenario:
//  1. Create task A and task B in the same project.
//  2. Add a dependency: task B depends on task A (A blocks B).
//  3. Move task A to "done".
//  4. Assert that task B has an activity log entry indicating it was unblocked.
//  5. (Optional) Assert task B is auto-moved to "todo" if that rule is configured.
func TestAutoTransition_DependencyResolved(t *testing.T) {
	t.Skip("TODO: implement auto-transitions first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("auto-trans-dep")
	env.Register(t, email, "TestPass123", "Dependency Tester")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Create project.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "AutoTransition Dependency Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		env.DB.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id IN (SELECT id FROM tasks WHERE project_id = $1)", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Get statuses.
	resp = env.Get(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statuses []map[string]interface{}
	env.DecodeJSON(t, resp, &statuses)

	var todoStatusID, doneStatusID string
	for _, s := range statuses {
		switch s["category"] {
		case "todo":
			todoStatusID = s["id"].(string)
		case "done":
			doneStatusID = s["id"].(string)
		}
	}
	require.NotEmpty(t, doneStatusID, "project must have a done status")

	// --- Step 1: Create task A (the blocker) ---
	var taskAID string
	t.Run("CreateTaskA_Blocker", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":    "Task A (blocker)",
			"priority": "high",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		taskAID = task["id"].(string)
	})

	// --- Step 2: Create task B (the dependent) ---
	var taskBID string
	t.Run("CreateTaskB_Dependent", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":    "Task B (depends on A)",
			"priority": "medium",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		taskBID = task["id"].(string)
	})

	// --- Step 3: Add dependency — B depends on A (A blocks B) ---
	t.Run("AddDependency_ABlocksB", func(t *testing.T) {
		if taskAID == "" || taskBID == "" {
			t.Skip("tasks not created")
		}
		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/dependencies", taskBID), map[string]interface{}{
			"depends_on_task_id": taskAID,
		})
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 4: Move task A to "done" ---
	t.Run("CompleteTaskA", func(t *testing.T) {
		if taskAID == "" {
			t.Skip("task A not created")
		}
		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/move", taskAID), map[string]interface{}{
			"status_id": doneStatusID,
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 5: Verify task B has an "unblocked" activity log entry ---
	t.Run("TaskB_UnblockedActivityEntry", func(t *testing.T) {
		if taskBID == "" {
			t.Skip("task B not created")
		}
		resp := env.Get(t, fmt.Sprintf("/api/v1/tasks/%s/activity", taskBID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var activity []map[string]interface{}
		env.DecodeJSON(t, resp, &activity)

		found := false
		for _, entry := range activity {
			if entry["action"] == "unblocked" || entry["action"] == "dependency_resolved" {
				found = true
				break
			}
		}
		assert.True(t, found, "task B must have an unblocked activity log entry after A is completed")
	})

	// --- Step 6 (optional): Task B auto-moved to "todo" if rule configured ---
	t.Run("TaskB_OptionalAutoMoveToTodo", func(t *testing.T) {
		if taskBID == "" || todoStatusID == "" {
			t.Skip("prerequisites not met")
		}
		resp := env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", taskBID))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var taskB map[string]interface{}
		env.DecodeJSON(t, resp, &taskB)

		// This assertion is configuration-dependent; log the result rather than
		// hard-failing so the test can be tightened once the feature ships.
		t.Logf("Task B status_id after A completed: %v (expected %s if auto-move configured)",
			taskB["status_id"], todoStatusID)
	})
}
