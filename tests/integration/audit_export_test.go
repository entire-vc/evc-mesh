//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requiredAuditColumns lists the CSV column headers that every audit export
// must contain.
var requiredAuditColumns = []string{
	"timestamp",
	"actor",
	"actor_type",
	"action",
	"entity_type",
	"entity_id",
	"changes",
}

// TestAuditExport_CSV validates that the workspace activity log can be exported
// as a well-formed CSV file containing all expected columns and the activity
// events generated during the test.
//
// Scenario:
//  1. Register a user; create a project and several tasks.
//  2. Perform status changes and add comments to generate diverse activity.
//  3. GET /workspaces/:ws_id/activity/export?format=csv.
//  4. Parse the CSV output.
//  5. Assert header row contains all required columns.
//  6. Assert at least the task-creation events are present in the rows.
func TestAuditExport_CSV(t *testing.T) {
	t.Skip("TODO: implement audit export first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("audit-csv")
	env.Register(t, email, "TestPass123", "Audit CSV Tester")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Create project.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "Audit CSV Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		env.DB.ExecContext(ctx, "DELETE FROM activity_log WHERE workspace_id = $1", wsID)
		env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Get a done status for status-change activity.
	resp = env.Get(t, fmt.Sprintf("/api/v1/projects/%s/statuses", projectID))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statuses []map[string]interface{}
	env.DecodeJSON(t, resp, &statuses)

	var doneStatusID string
	for _, s := range statuses {
		if s["category"] == "done" {
			doneStatusID = s["id"].(string)
			break
		}
	}

	// Generate activity: create 3 tasks, move one to done, add a comment.
	var taskIDs []string
	for i := 1; i <= 3; i++ {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":    fmt.Sprintf("Audit Task %d", i),
			"priority": "medium",
		})
		if resp.StatusCode == http.StatusCreated {
			var task map[string]interface{}
			env.DecodeJSON(t, resp, &task)
			taskIDs = append(taskIDs, task["id"].(string))
		} else {
			resp.Body.Close()
		}
	}
	require.NotEmpty(t, taskIDs, "at least one task must be created for audit data")

	// Status change on first task.
	if doneStatusID != "" && len(taskIDs) > 0 {
		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/move", taskIDs[0]), map[string]interface{}{
			"status_id": doneStatusID,
		})
		resp.Body.Close()
	}

	// Comment on second task.
	if len(taskIDs) > 1 {
		resp := env.Post(t, fmt.Sprintf("/api/v1/tasks/%s/comments", taskIDs[1]), map[string]interface{}{
			"body": "Audit test comment",
		})
		resp.Body.Close()
	}

	// --- Step 3: Export as CSV ---
	t.Run("ExportCSV", func(t *testing.T) {
		resp := env.Get(t, fmt.Sprintf("/api/v1/workspaces/%s/activity/export?format=csv", wsID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Content-Type should indicate CSV or octet-stream.
		ct := resp.Header.Get("Content-Type")
		assert.True(t,
			strings.Contains(ct, "text/csv") || strings.Contains(ct, "application/octet-stream"),
			"Content-Type must be text/csv or application/octet-stream, got: %s", ct)

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		require.NotEmpty(t, body, "CSV export must not be empty")

		// --- Step 4: Parse CSV ---
		reader := csv.NewReader(bytes.NewReader(body))
		records, err := reader.ReadAll()
		require.NoError(t, err, "CSV must be well-formed")
		require.GreaterOrEqual(t, len(records), 2, "CSV must have header + at least one data row")

		// --- Step 5: Assert header columns ---
		header := records[0]
		headerSet := make(map[string]bool, len(header))
		for _, col := range header {
			headerSet[strings.ToLower(col)] = true
		}
		for _, required := range requiredAuditColumns {
			assert.True(t, headerSet[required],
				"CSV header must contain column %q", required)
		}

		// --- Step 6: Assert task-creation events are present ---
		// Look for rows where entity_type == "task" and action == "created".
		entityTypeIdx := -1
		actionIdx := -1
		for i, col := range header {
			switch strings.ToLower(col) {
			case "entity_type":
				entityTypeIdx = i
			case "action":
				actionIdx = i
			}
		}

		if entityTypeIdx >= 0 && actionIdx >= 0 {
			createdTaskRows := 0
			for _, row := range records[1:] {
				if len(row) > entityTypeIdx && len(row) > actionIdx {
					if strings.ToLower(row[entityTypeIdx]) == "task" &&
						strings.ToLower(row[actionIdx]) == "created" {
						createdTaskRows++
					}
				}
			}
			assert.GreaterOrEqual(t, createdTaskRows, len(taskIDs),
				"CSV must contain a 'task created' row for each created task")
		}
	})
}

// TestAuditExport_JSON validates that the workspace activity log can be
// exported as a well-formed JSON array matching the activity_log domain model.
//
// Scenario:
//  1. Same setup as TestAuditExport_CSV (create tasks, status change, comment).
//  2. GET /workspaces/:ws_id/activity/export?format=json.
//  3. Parse the JSON array.
//  4. Assert each element contains: id, workspace_id, actor_id, actor_type,
//     action, entity_type, entity_id, created_at.
//  5. Assert at least one entry has action == "created" and entity_type == "task".
func TestAuditExport_JSON(t *testing.T) {
	t.Skip("TODO: implement audit export first")

	env := NewTestEnv(t)
	defer env.Cleanup(t)

	email := uniqueEmail("audit-json")
	env.Register(t, email, "TestPass123", "Audit JSON Tester")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]interface{}
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	wsID := workspaces[0]["id"].(string)

	// Create project.
	resp = env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/projects", wsID), map[string]interface{}{
		"name": "Audit JSON Project",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project map[string]interface{}
	env.DecodeJSON(t, resp, &project)
	projectID := project["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		env.DB.ExecContext(ctx, "DELETE FROM activity_log WHERE workspace_id = $1", wsID)
		env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", projectID)
		env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	})

	// Create tasks to generate activity.
	var taskIDs []string
	for i := 1; i <= 2; i++ {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", projectID), map[string]interface{}{
			"title":    fmt.Sprintf("Audit JSON Task %d", i),
			"priority": "low",
		})
		if resp.StatusCode == http.StatusCreated {
			var task map[string]interface{}
			env.DecodeJSON(t, resp, &task)
			taskIDs = append(taskIDs, task["id"].(string))
		} else {
			resp.Body.Close()
		}
	}
	require.NotEmpty(t, taskIDs)

	// --- Step 2: Export as JSON ---
	t.Run("ExportJSON", func(t *testing.T) {
		resp := env.Get(t, fmt.Sprintf("/api/v1/workspaces/%s/activity/export?format=json", wsID))
		require.Equal(t, http.StatusOK, resp.StatusCode)

		ct := resp.Header.Get("Content-Type")
		assert.Contains(t, ct, "application/json",
			"Content-Type must be application/json, got: %s", ct)

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		require.NotEmpty(t, body)

		// --- Step 3: Parse JSON array ---
		var entries []map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &entries), "export must be a valid JSON array")
		require.NotEmpty(t, entries, "JSON export must contain at least one entry")

		// --- Step 4: Assert required fields on each entry ---
		requiredFields := []string{
			"id", "workspace_id", "actor_id", "actor_type",
			"action", "entity_type", "entity_id", "created_at",
		}
		for _, entry := range entries {
			for _, field := range requiredFields {
				assert.Contains(t, entry, field,
					"activity log entry must contain field %q", field)
			}
		}

		// --- Step 5: Assert at least one task-creation entry ---
		found := false
		for _, entry := range entries {
			if entry["action"] == "created" && entry["entity_type"] == "task" {
				found = true
				break
			}
		}
		assert.True(t, found,
			"JSON export must include at least one task creation entry")
	})
}
