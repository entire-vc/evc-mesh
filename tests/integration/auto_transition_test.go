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
// moved to the "review" status when all of its subtasks are completed.
//
// Scenario:
//  1. Create a project and obtain default statuses.
//  2. Create a parent task with status "in_progress".
//  3. Create 3 subtasks under the parent.
//  4. Move subtask 1 to "done" — assert parent remains "in_progress".
//  5. Move subtask 2 to "done" — assert parent remains "in_progress".
//  6. Move subtask 3 to "done" — assert parent is automatically moved to
//     the configured "review" status (seeded default rule: all_subtasks_done → review).
func TestAutoTransition_SubtasksDone(t *testing.T) {
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
		env.DB.ExecContext(ctx, "DELETE FROM auto_transition_rules WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Fetch statuses — find in_progress, review, and done IDs.
	resp = env.Get(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statuses []map[string]interface{}
	env.DecodeJSON(t, resp, &statuses)

	var inProgressStatusID, reviewStatusID, doneStatusID string
	for _, s := range statuses {
		switch s["category"] {
		case "in_progress":
			inProgressStatusID = s["id"].(string)
		case "review":
			if reviewStatusID == "" {
				reviewStatusID = s["id"].(string)
			}
		case "done":
			doneStatusID = s["id"].(string)
		}
	}
	require.NotEmpty(t, inProgressStatusID, "project must have an in_progress status")
	require.NotEmpty(t, doneStatusID, "project must have a done status")
	// reviewStatusID may be empty if the project has no review status; fall back to done in that case.
	expectedParentStatusID := reviewStatusID
	if expectedParentStatusID == "" {
		expectedParentStatusID = doneStatusID
	}

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

	// --- Step 5: Complete the last subtask; parent should auto-transition to review ---
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

		// Parent must now be in "review" (auto-transition triggered by seeded default rule).
		resp = env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", parentTaskID))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var parent map[string]interface{}
		env.DecodeJSON(t, resp, &parent)
		assert.Equal(t, expectedParentStatusID, parent["status_id"],
			"parent must auto-transition to review (or done) when all subtasks are done")
	})
}

// TestAutoTransition_DependencyResolved verifies that completing a blocking
// task generates an "unblocked" activity log entry for the dependent task
// and moves it from "backlog" to "todo" via the seeded default rule.
//
// Scenario:
//  1. Create task A and task B in the same project.
//  2. Task B is explicitly placed in "backlog" (required for the unblock rule).
//  3. Add a dependency: task B depends on task A (A blocks B).
//  4. Move task A to "done".
//  5. Assert that task B has an activity log entry indicating it was unblocked.
//  6. Assert task B is auto-moved to "todo" (default blocking_dep_resolved rule is seeded).
func TestAutoTransition_DependencyResolved(t *testing.T) {
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
		env.DB.ExecContext(ctx, "DELETE FROM auto_transition_rules WHERE project_id = $1", projectID)
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

	var backlogStatusID, todoStatusID, doneStatusID string
	for _, s := range statuses {
		switch s["category"] {
		case "backlog":
			if backlogStatusID == "" {
				backlogStatusID = s["id"].(string)
			}
		case "todo":
			if todoStatusID == "" {
				todoStatusID = s["id"].(string)
			}
		case "done":
			doneStatusID = s["id"].(string)
		}
	}
	require.NotEmpty(t, backlogStatusID, "project must have a backlog status")
	require.NotEmpty(t, todoStatusID, "project must have a todo status")
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

	// --- Step 2: Create task B (the dependent) in "backlog" so the unblock rule applies ---
	var taskBID string
	t.Run("CreateTaskB_Dependent", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":     "Task B (depends on A)",
			"priority":  "medium",
			"status_id": backlogStatusID,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		taskBID = task["id"].(string)
		assert.Equal(t, backlogStatusID, task["status_id"],
			"task B must start in backlog for the unblock rule to apply")
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

	// --- Step 6: Task B must be auto-moved to "todo" by the seeded blocking_dep_resolved rule ---
	t.Run("TaskB_AutoMovedToTodo", func(t *testing.T) {
		if taskBID == "" || todoStatusID == "" {
			t.Skip("prerequisites not met")
		}
		resp := env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", taskBID))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var taskB map[string]interface{}
		env.DecodeJSON(t, resp, &taskB)

		assert.Equal(t, todoStatusID, taskB["status_id"],
			"task B must be auto-moved to todo when its only blocker (task A) is completed")
	})
}

// TestAutoTransition_RuleCRUD verifies the REST API for auto-transition rules,
// including the 2 default rules seeded on project creation, CRUD operations,
// and the UNIQUE constraint on (project_id, trigger).
func TestAutoTransition_RuleCRUD(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("auto-trans-crud")
	env.Register(t, email, "TestPass123", "CRUD Tester")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Create project.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "AutoTransition CRUD Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		env.DB.ExecContext(ctx, "DELETE FROM auto_transition_rules WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Fetch statuses so we can reference status IDs in rule bodies.
	resp = env.Get(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statuses []map[string]interface{}
	env.DecodeJSON(t, resp, &statuses)

	var reviewStatusID, todoStatusID, doneStatusID string
	for _, s := range statuses {
		switch s["category"] {
		case "review":
			if reviewStatusID == "" {
				reviewStatusID = s["id"].(string)
			}
		case "todo":
			if todoStatusID == "" {
				todoStatusID = s["id"].(string)
			}
		case "done":
			doneStatusID = s["id"].(string)
		}
	}
	require.NotEmpty(t, todoStatusID, "project must have a todo status")
	require.NotEmpty(t, doneStatusID, "project must have a done status")

	rulesURL := fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules", projectID)

	// --- Step 1: List rules — expect 2 default rules seeded on project creation ---
	var initialRules []map[string]interface{}
	t.Run("ListDefaultRules", func(t *testing.T) {
		resp := env.Get(t, rulesURL)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		env.DecodeJSON(t, resp, &initialRules)

		assert.Len(t, initialRules, 2, "two default rules must be seeded when a project is created")

		triggers := make(map[string]bool)
		for _, r := range initialRules {
			trigger, ok := r["trigger"].(string)
			require.True(t, ok, "rule must have a string trigger field")
			triggers[trigger] = true
			assert.True(t, r["is_enabled"].(bool), "default rules must be enabled")
		}
		assert.True(t, triggers["all_subtasks_done"], "default rule all_subtasks_done must exist")
		assert.True(t, triggers["blocking_dep_resolved"], "default rule blocking_dep_resolved must exist")
	})

	// --- Step 2: Verify default rule targets ---
	t.Run("VerifyDefaultRuleTargets", func(t *testing.T) {
		if len(initialRules) < 2 {
			t.Skip("default rules not seeded")
		}
		for _, r := range initialRules {
			trigger := r["trigger"].(string)
			targetStatusID := r["target_status_id"].(string)
			switch trigger {
			case "all_subtasks_done":
				// Default target is review (or done if no review status exists).
				expectedTarget := reviewStatusID
				if expectedTarget == "" {
					expectedTarget = doneStatusID
				}
				assert.Equal(t, expectedTarget, targetStatusID,
					"all_subtasks_done rule must target the review status by default")
			case "blocking_dep_resolved":
				assert.Equal(t, todoStatusID, targetStatusID,
					"blocking_dep_resolved rule must target the todo status by default")
			}
		}
	})

	// --- Step 3: Update one rule — disable the all_subtasks_done rule ---
	var subtasksDoneRuleID string
	t.Run("DisableAllSubtasksDoneRule", func(t *testing.T) {
		if len(initialRules) < 2 {
			t.Skip("default rules not seeded")
		}
		for _, r := range initialRules {
			if r["trigger"].(string) == "all_subtasks_done" {
				subtasksDoneRuleID = r["id"].(string)
				break
			}
		}
		require.NotEmpty(t, subtasksDoneRuleID, "all_subtasks_done rule must exist")

		isEnabled := false
		resp := env.Put(t, fmt.Sprintf("%s/%s", rulesURL, subtasksDoneRuleID), map[string]interface{}{
			"is_enabled": isEnabled,
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var updated map[string]interface{}
		env.DecodeJSON(t, resp, &updated)
		assert.Equal(t, false, updated["is_enabled"],
			"rule must be disabled after update")
	})

	// --- Step 4: List rules — verify is_enabled=false on the updated rule ---
	t.Run("ListRules_AfterDisable", func(t *testing.T) {
		if subtasksDoneRuleID == "" {
			t.Skip("rule not updated")
		}
		resp := env.Get(t, rulesURL)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var rules []map[string]interface{}
		env.DecodeJSON(t, resp, &rules)

		assert.Len(t, rules, 2)
		for _, r := range rules {
			if r["id"].(string) == subtasksDoneRuleID {
				assert.Equal(t, false, r["is_enabled"],
					"rule must remain disabled after listing")
			}
		}
	})

	// --- Step 5: Delete the disabled rule ---
	t.Run("DeleteAllSubtasksDoneRule", func(t *testing.T) {
		if subtasksDoneRuleID == "" {
			t.Skip("rule not created")
		}
		resp := env.Delete(t, fmt.Sprintf("%s/%s", rulesURL, subtasksDoneRuleID))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()
	})

	// --- Step 6: List rules — should return 1 rule ---
	t.Run("ListRules_AfterDelete", func(t *testing.T) {
		if subtasksDoneRuleID == "" {
			t.Skip("rule not deleted")
		}
		resp := env.Get(t, rulesURL)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var rules []map[string]interface{}
		env.DecodeJSON(t, resp, &rules)
		assert.Len(t, rules, 1, "only one rule should remain after deleting the other")
	})

	// --- Step 7: Create a new rule for the deleted trigger ---
	var newRuleID string
	t.Run("CreateAllSubtasksDoneRule", func(t *testing.T) {
		targetID := doneStatusID
		resp := env.Post(t, rulesURL, map[string]interface{}{
			"trigger":          "all_subtasks_done",
			"target_status_id": targetID,
			"is_enabled":       true,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var rule map[string]interface{}
		env.DecodeJSON(t, resp, &rule)
		newRuleID = rule["id"].(string)
		assert.Equal(t, "all_subtasks_done", rule["trigger"])
		assert.Equal(t, targetID, rule["target_status_id"])
		assert.Equal(t, true, rule["is_enabled"])
	})
	_ = newRuleID

	// --- Step 8: Verify UNIQUE constraint — duplicate trigger must return an error status ---
	t.Run("DuplicateTrigger_ReturnsError", func(t *testing.T) {
		// The project already has an all_subtasks_done rule (just created above).
		// Attempting to create a second one with the same trigger must fail.
		resp := env.Post(t, rulesURL, map[string]interface{}{
			"trigger":          "all_subtasks_done",
			"target_status_id": doneStatusID,
			"is_enabled":       true,
		})
		defer resp.Body.Close()
		assert.GreaterOrEqual(t, resp.StatusCode, 400,
			"creating a duplicate trigger must return an error (4xx or 5xx from DB unique constraint)")
	})
}

// TestAutoTransition_DisabledRule verifies that a disabled auto-transition rule
// does NOT trigger when its condition is met.
//
// Scenario:
//  1. Create a project (default rules are seeded).
//  2. Disable the all_subtasks_done rule via PUT.
//  3. Create a parent task in "in_progress" + 2 subtasks.
//  4. Complete both subtasks.
//  5. Assert parent STAYS in "in_progress" (disabled rule must not fire).
func TestAutoTransition_DisabledRule(t *testing.T) {
	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("auto-trans-disabled")
	env.Register(t, email, "TestPass123", "Disabled Rule Tester")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Create project.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "AutoTransition Disabled Rule Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		env.DB.ExecContext(ctx, "DELETE FROM auto_transition_rules WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Fetch statuses.
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

	rulesURL := fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules", projectID)

	// --- Step 1: Find and disable the all_subtasks_done rule ---
	var ruleID string
	t.Run("DisableAllSubtasksDoneRule", func(t *testing.T) {
		resp := env.Get(t, rulesURL)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var rules []map[string]interface{}
		env.DecodeJSON(t, resp, &rules)
		require.NotEmpty(t, rules, "default rules must be seeded")

		for _, r := range rules {
			if r["trigger"].(string) == "all_subtasks_done" {
				ruleID = r["id"].(string)
				break
			}
		}
		require.NotEmpty(t, ruleID, "all_subtasks_done rule must be seeded")

		isEnabled := false
		resp = env.Put(t, fmt.Sprintf("%s/%s", rulesURL, ruleID), map[string]interface{}{
			"is_enabled": isEnabled,
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var updated map[string]interface{}
		env.DecodeJSON(t, resp, &updated)
		assert.Equal(t, false, updated["is_enabled"])
	})

	// --- Step 2: Create parent task in in_progress ---
	var parentTaskID string
	t.Run("CreateParentTask", func(t *testing.T) {
		if ruleID == "" {
			t.Skip("rule not disabled")
		}
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":     "Parent Task (should stay in_progress)",
			"priority":  "medium",
			"status_id": inProgressStatusID,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task map[string]interface{}
		env.DecodeJSON(t, resp, &task)
		parentTaskID = task["id"].(string)
	})

	// --- Step 3: Create 2 subtasks ---
	subtaskIDs := make([]string, 0, 2)
	t.Run("CreateSubtasks", func(t *testing.T) {
		if parentTaskID == "" {
			t.Skip("parent task not created")
		}
		for i := 1; i <= 2; i++ {
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
		require.Len(t, subtaskIDs, 2, "both subtasks must be created")
	})

	// --- Step 4: Complete both subtasks ---
	t.Run("CompleteBothSubtasks", func(t *testing.T) {
		if len(subtaskIDs) < 2 {
			t.Skip("subtasks not created")
		}
		for _, subtaskID := range subtaskIDs {
			resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/move", subtaskID), map[string]interface{}{
				"status_id": doneStatusID,
			})
			require.Equal(t, http.StatusOK, resp.StatusCode)
			resp.Body.Close()
		}
	})

	// --- Step 5: Assert parent STAYS in in_progress (disabled rule must not fire) ---
	t.Run("ParentStaysInProgress_DisabledRuleDoesNotFire", func(t *testing.T) {
		if parentTaskID == "" {
			t.Skip("parent task not created")
		}
		resp := env.Get(t, fmt.Sprintf("/api/v1/tasks/%s", parentTaskID))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var parent map[string]interface{}
		env.DecodeJSON(t, resp, &parent)
		assert.Equal(t, inProgressStatusID, parent["status_id"],
			"parent must remain in_progress because the all_subtasks_done rule is disabled")
	})
}
