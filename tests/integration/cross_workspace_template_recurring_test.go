//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The task write-path tenancy guard (cross_workspace_assignee_test.go, PR #527)
// closed the fourth tenant channel for every path that writes assignee_id onto a
// TASK. It did not close it for the two paths that write assignee_id onto a
// TEMPLATE or a RECURRING SCHEDULE row instead — neither is a task, so neither
// ever calls taskService.Create/Update/AssignTask/MoveTask, and the guard living
// inside ensureAssigneeProjectMember never sees them.
//
// Before the fix in this file's companion change, a template or schedule created
// with a foreign assignee is accepted and stored. The refusal still eventually
// happens — CreateTaskFromTemplate and createInstance both route through
// taskService.Create — but by then the caller who named the foreign assignee is
// long gone: for a template it surfaces as a failed POST .../create-task with no
// link back to why; for a recurring schedule it surfaces as a scheduler-tick
// failure with no caller in the loop at all, and three of those in a row
// quarantines the schedule (recurringService.maxConsecutiveFailures).
//
// These tests prove the row itself is refused at write time, not merely at
// eventual materialization.

// assertTemplateOrRecurringRefused mirrors assertAssigneeRefused (same file
// family, same guard, same response shape) — kept separate rather than reused so
// the "what" argument reads naturally for a template/schedule write instead of a
// task write.
func assertTemplateOrRecurringRefused(t *testing.T, env *TestEnv, resp *http.Response, what string) {
	t.Helper()
	body := string(env.ReadBody(t, resp))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode,
		"%s must be refused with 422, got %d: %s", what, resp.StatusCode, body)
	assert.Contains(t, body, "assignee_not_in_workspace",
		"%s must be refused by the assignee tenancy guard specifically, not incidentally: %s", what, body)
}

func TestCrossWorkspaceAssignee_TemplateAndRecurring_RefusedOnWrite(t *testing.T) {
	f := newTwoWorkspaceFixture(t)
	env := f.env

	t.Run("POST /projects/:proj_id/templates with a foreign assignee", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/templates", f.homeProject), map[string]interface{}{
			"name": "planted template", "title_template": "planted at create",
			"assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertTemplateOrRecurringRefused(t, env, resp, "create template with a foreign assignee")
	})

	t.Run("PATCH /templates/:tmpl_id with a foreign assignee", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/templates", f.homeProject), map[string]interface{}{
			"name": "patch target template", "title_template": "patch target",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var tmpl map[string]interface{}
		env.DecodeJSON(t, resp, &tmpl)
		tmplID := tmpl["id"].(string)
		env.OnCleanup(func() {
			_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM task_templates WHERE id = $1", tmplID)
		})

		resp = env.Patch(t, fmt.Sprintf("/api/v1/templates/%s", tmplID), map[string]interface{}{
			"assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertTemplateOrRecurringRefused(t, env, resp, "PATCH template with a foreign assignee")

		var storedAssignee *string
		require.NoError(t, env.DB.QueryRowContext(context.Background(),
			"SELECT assignee_id::text FROM task_templates WHERE id = $1", tmplID).Scan(&storedAssignee))
		assert.Nil(t, storedAssignee, "a refused PATCH must not leave the foreign assignee on the row")
	})

	t.Run("POST /projects/:proj_id/recurring with a foreign assignee", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/recurring", f.homeProject), map[string]interface{}{
			"title_template": "planted schedule", "frequency": "daily",
			"assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertTemplateOrRecurringRefused(t, env, resp, "create recurring schedule with a foreign assignee")
	})

	t.Run("PATCH /recurring/:recurring_id with a foreign assignee", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/recurring", f.homeProject), map[string]interface{}{
			"title_template": "patch target schedule", "frequency": "daily",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var sched map[string]interface{}
		env.DecodeJSON(t, resp, &sched)
		schedID := sched["id"].(string)
		env.OnCleanup(func() {
			_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM recurring_schedules WHERE id = $1", schedID)
		})

		resp = env.Patch(t, fmt.Sprintf("/api/v1/recurring/%s", schedID), map[string]interface{}{
			"assignee_id": f.foreignAgentID, "assignee_type": "agent",
		})
		assertTemplateOrRecurringRefused(t, env, resp, "PATCH recurring schedule with a foreign assignee")

		var storedAssignee *string
		require.NoError(t, env.DB.QueryRowContext(context.Background(),
			"SELECT assignee_id::text FROM recurring_schedules WHERE id = $1", schedID).Scan(&storedAssignee))
		assert.Nil(t, storedAssignee, "a refused PATCH must not leave the foreign assignee on the row")
	})

	t.Run("no template or schedule row survives any of it", func(t *testing.T) {
		var n int
		require.NoError(t, env.DB.QueryRowContext(context.Background(),
			"SELECT count(*) FROM task_templates WHERE project_id = $1 AND assignee_id = $2",
			f.homeProject, f.foreignAgentID).Scan(&n))
		assert.Zero(t, n, "a refused template create must leave no row behind at all")

		require.NoError(t, env.DB.QueryRowContext(context.Background(),
			"SELECT count(*) FROM recurring_schedules WHERE project_id = $1 AND assignee_id = $2",
			f.homeProject, f.foreignAgentID).Scan(&n))
		assert.Zero(t, n, "a refused schedule create must leave no row behind at all")
	})
}

// TestCrossWorkspaceAssignee_TemplateAndRecurring_NativeStillWorks is the
// positive control. Without it every assertion above is satisfied by a guard
// that refuses every assignee, which would look green here and break template-
// and schedule-based task creation for the whole fleet in production — the
// dispatcher assigns nearly every task it creates through one of these two
// paths.
func TestCrossWorkspaceAssignee_TemplateAndRecurring_NativeStillWorks(t *testing.T) {
	f := newTwoWorkspaceFixture(t)
	env := f.env
	ctx := context.Background()

	t.Run("template: native assignee is created, stored, and materializes", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/templates", f.homeProject), map[string]interface{}{
			"name": "native template", "title_template": "native materialization target",
			"status_id": f.homeTodoStatus, "assignee_id": f.homeAgentID, "assignee_type": "agent",
		})
		rawBody := env.ReadBody(t, resp)
		require.Equal(t, http.StatusCreated, resp.StatusCode, "a template assignee native to this workspace must still be creatable: %s", rawBody)
		var tmpl map[string]interface{}
		require.NoError(t, json.Unmarshal(rawBody, &tmpl))
		tmplID := tmpl["id"].(string)
		env.OnCleanup(func() {
			_, _ = env.DB.ExecContext(ctx, "DELETE FROM task_templates WHERE id = $1", tmplID)
		})
		assert.Equal(t, f.homeAgentID, tmpl["assignee_id"], "the stored assignee must be the one that was sent")

		resp = env.Post(t, fmt.Sprintf("/api/v1/templates/%s/create-task", tmplID), map[string]interface{}{})
		rawBody = env.ReadBody(t, resp)
		require.Equal(t, http.StatusCreated, resp.StatusCode, "materializing a template with a native assignee must still work: %s", rawBody)
		var task map[string]interface{}
		require.NoError(t, json.Unmarshal(rawBody, &task))
		assert.Equal(t, f.homeAgentID, task["assignee_id"], "the materialized task must carry the template's assignee")

		var n int
		require.NoError(t, env.DB.QueryRowContext(ctx,
			"SELECT count(*) FROM project_members WHERE project_id = $1 AND agent_id = $2",
			f.homeProject, f.homeAgentID).Scan(&n))
		assert.Equal(t, 1, n, "and the assignee must still be auto-enrolled in the project")
	})

	t.Run("recurring: native assignee is created, stored, and materializes via trigger", func(t *testing.T) {
		resp := env.Post(t, fmt.Sprintf("/api/v1/projects/%s/recurring", f.homeProject), map[string]interface{}{
			"title_template": "native schedule materialization target", "frequency": "daily",
			"status_id": f.homeTodoStatus, "assignee_id": f.homeAgentID, "assignee_type": "agent",
		})
		rawBody := env.ReadBody(t, resp)
		require.Equal(t, http.StatusCreated, resp.StatusCode, "a schedule assignee native to this workspace must still be creatable: %s", rawBody)
		var sched map[string]interface{}
		require.NoError(t, json.Unmarshal(rawBody, &sched))
		schedID := sched["id"].(string)
		env.OnCleanup(func() {
			_, _ = env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE recurring_schedule_id = $1", schedID)
			_, _ = env.DB.ExecContext(ctx, "DELETE FROM recurring_schedules WHERE id = $1", schedID)
		})
		assert.Equal(t, f.homeAgentID, sched["assignee_id"], "the stored assignee must be the one that was sent")

		resp = env.Post(t, fmt.Sprintf("/api/v1/recurring/%s/trigger", schedID), map[string]interface{}{})
		rawBody = env.ReadBody(t, resp)
		require.Equal(t, http.StatusCreated, resp.StatusCode, "triggering a schedule with a native assignee must still work: %s", rawBody)
		var triggered map[string]interface{}
		require.NoError(t, json.Unmarshal(rawBody, &triggered))
		taskObj, ok := triggered["task"].(map[string]interface{})
		require.True(t, ok, "trigger response must carry the materialized task: %s", rawBody)
		assert.Equal(t, f.homeAgentID, taskObj["assignee_id"], "the materialized instance must carry the schedule's assignee")
	})
}
