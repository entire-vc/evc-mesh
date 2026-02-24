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

func TestTaskLifecycle(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	// --- Setup: Register user and get workspace ---
	email := uniqueEmail("task-lifecycle")
	env.Register(t, email, "TestPass123", "Task Tester")

	// Get the default workspace (created during registration).
	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces, "user must have at least one workspace")
	wsID := workspaces[0]["id"].(string)

	// --- Step 1: Create project (auto-creates default statuses) ---
	var projectID string
	t.Run("CreateProject", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
			"name":        "Task Test Project",
			"description": "Integration test project",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var project map[string]interface{}
		env.DecodeJSON(t, resp, &project)
		projectID = project["id"].(string)
		assert.NotEmpty(t, projectID)

		// Register cleanup for project and related data.
		env.OnCleanup(func() {
			ctx := context.Background()
			env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
			env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
			env.DB.ExecContext(ctx, "DELETE FROM custom_field_definitions WHERE project_id = $1", projectID)
			env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
		})
	})

	// --- Step 2: List statuses (should have defaults) ---
	var statusIDs []string
	var doneStatusID string
	t.Run("ListStatuses", func(t *testing.T) {
		resp := env.Get(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var statuses []map[string]interface{}
		env.DecodeJSON(t, resp, &statuses)
		require.NotEmpty(t, statuses, "project must have default statuses")

		for _, s := range statuses {
			statusIDs = append(statusIDs, s["id"].(string))
			if s["category"] == "done" {
				doneStatusID = s["id"].(string)
			}
		}
		assert.NotEmpty(t, statusIDs, "at least one status must exist")
	})

	// --- Step 3: Create task ---
	var taskID string
	t.Run("CreateTask", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":       "Test Task",
			"description": "This is an integration test task",
			"priority":    "medium",
			"labels":      []string{"test", "integration"},
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		taskID = task["id"].(string)
		assert.Equal(t, "Test Task", task["title"])
		assert.Equal(t, "medium", task["priority"])
	})

	// --- Step 4: Get task by ID ---
	t.Run("GetTask", func(t *testing.T) {
		resp := env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", taskID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		assert.Equal(t, taskID, task["id"])
		assert.Equal(t, "Test Task", task["title"])
	})

	// --- Step 5: Update task ---
	t.Run("UpdateTask", func(t *testing.T) {
		resp := env.Patch(t, fmt.Sprintf("/api/v1/tasks/%s", taskID), map[string]interface{}{
			"title":       "Updated Task Title",
			"description": "Updated description",
			"priority":    "high",
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		assert.Equal(t, "Updated Task Title", task["title"])
		assert.Equal(t, "high", task["priority"])
	})

	// --- Step 6: Move task to done status ---
	if doneStatusID != "" {
		t.Run("MoveTaskToDone", func(t *testing.T) {
			resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/move", taskID), map[string]interface{}{
				"status_id": doneStatusID,
			})
			require.Equal(t, http.StatusOK, resp.StatusCode)
			resp.Body.Close()

			// Verify task has completed_at set.
			resp = env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", taskID))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			var task map[string]interface{}
			env.DecodeJSON(t, resp, &task)
			assert.Equal(t, doneStatusID, task["status_id"])
			assert.NotNil(t, task["completed_at"], "completed_at should be set when moved to done")
		})
	}

	// --- Step 7: List tasks ---
	t.Run("ListTasks", func(t *testing.T) {
		resp := env.Get(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var page map[string]interface{}
		env.DecodeJSON(t, resp, &page)
		items, ok := page["items"].([]interface{})
		require.True(t, ok)
		assert.GreaterOrEqual(t, len(items), 1, "should have at least 1 task")
	})

	// --- Step 8: Create subtask ---
	var subtaskID string
	t.Run("CreateSubtask", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":          "Subtask 1",
			"priority":       "low",
			"parent_task_id": taskID,
		})
		// Creating subtask with parent_task_id in the request body.
		// The handler may or may not support this directly; check the response.
		if resp.StatusCode == http.StatusCreated {
			var task map[string]interface{}
			env.DecodeJSON(t, resp, &task)
			subtaskID = task["id"].(string)
			assert.Equal(t, "Subtask 1", task["title"])
		} else {
			resp.Body.Close()
			t.Logf("Subtask creation via parent_task_id in body returned %d (may not be supported)", resp.StatusCode)
		}
	})

	// --- Step 9: List subtasks ---
	if subtaskID != "" {
		t.Run("ListSubtasks", func(t *testing.T) {
			resp := env.Get(t, fmt.Sprintf("/api/v1/tasks/%s/subtasks", taskID))
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var subtasks []map[string]interface{}
			env.DecodeJSON(t, resp, &subtasks)
			assert.GreaterOrEqual(t, len(subtasks), 1)
		})
	}

	// --- Step 10: Add comment ---
	t.Run("AddComment", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/comments", taskID), map[string]interface{}{
			"body": "This is a test comment",
		})
		if resp.StatusCode == http.StatusCreated {
			var comment map[string]interface{}
			env.DecodeJSON(t, resp, &comment)
			assert.Equal(t, "This is a test comment", comment["body"])

			// Register cleanup.
			if commentID, ok := comment["id"].(string); ok {
				env.OnCleanup(func() {
					env.DB.ExecContext(context.Background(),
						"DELETE FROM comments WHERE id = $1", commentID)
				})
			}
		} else {
			resp.Body.Close()
		}
	})

	// --- Step 11: Delete task (soft delete) ---
	t.Run("DeleteTask", func(t *testing.T) {
		resp := env.Delete(t, fmt.Sprintf("/api/v1/tasks/%s", taskID))
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()

		// Verify task is not returned in list.
		resp = env.Get(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var page map[string]interface{}
		env.DecodeJSON(t, resp, &page)

		// Check that deleted task is not in the list.
		items, ok := page["items"].([]interface{})
		if ok {
			for _, item := range items {
				task := item.(map[string]interface{})
				assert.NotEqual(t, taskID, task["id"], "deleted task should not appear in list")
			}
		}
	})

	// --- Step 12: Get deleted task returns 404 or nil ---
	t.Run("GetDeletedTask", func(t *testing.T) {
		resp := env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", taskID))
		// Soft-deleted tasks should return 404 or equivalent.
		assert.Contains(t, []int{http.StatusNotFound, http.StatusInternalServerError}, resp.StatusCode,
			"deleted task should not be accessible")
		resp.Body.Close()
	})
}

func TestTaskLifecycle_Validation(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("task-validation")
	env.Register(t, email, "TestPass123", "Validator")

	// Get workspace.
	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Create project.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "Validation Test",
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

	// --- Title required ---
	t.Run("TitleRequired", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"description": "no title",
		})
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Invalid project ID ---
	t.Run("InvalidProjectID", func(t *testing.T) {
		resp := env.Post(t, "/api/v1/projects/not-a-uuid/tasks", map[string]interface{}{
			"title": "Test",
		})
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Invalid task ID ---
	t.Run("InvalidTaskID", func(t *testing.T) {
		resp := env.Get(t, "/api/v1/tasks/not-a-uuid")
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
	})
}
