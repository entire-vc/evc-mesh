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

// tenant is one ordinary account with a workspace of its own and one of every
// object the composite routes are keyed on.
//
// Both sides of every test below are tenants: the point of this file is that the
// intruder is not a stranger with no access, but an ordinary user who is a full
// member of their OWN workspace. That is what made the composite holes work — the
// parent id in the path was genuinely theirs, the guard checked it, said yes, and
// the child id went to somebody else's row unexamined.
type tenant struct {
	env       *TestEnv
	agentKey  string
	wsID      string
	projectID string
	taskID    string
	otherTask string
	initID    string

	statusID string
	atrID    string
	depID    string
	inviteID string
}

func newTenant(t *testing.T, prefix string) *tenant {
	t.Helper()

	env := NewTestEnv(t)
	t.Cleanup(func() { env.Cleanup(t) })
	env.Register(t, uniqueEmail(prefix), "TestPass123", "Composite Tenant")

	tn := &tenant{env: env}

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	tn.wsID = workspaces[0]["id"].(string)

	tn.projectID = tn.createID(t, http.MethodPost, "/api/v1/workspaces/"+tn.wsID+"/projects",
		map[string]any{"name": prefix + " project"})
	env.OnCleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id IN (SELECT id FROM tasks WHERE project_id = $1)", tn.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM auto_transition_rules WHERE project_id = $1", tn.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", tn.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", tn.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM initiative_projects WHERE project_id = $1", tn.projectID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", tn.projectID)
	})

	tn.taskID = tn.createID(t, http.MethodPost, "/api/v1/projects/"+tn.projectID+"/tasks",
		map[string]any{"title": prefix + " confidential task"})
	tn.otherTask = tn.createID(t, http.MethodPost, "/api/v1/projects/"+tn.projectID+"/tasks",
		map[string]any{"title": prefix + " second task"})

	// A status of this tenant's own, named so that a cross-tenant rename is
	// visible in the database rather than inferred from a status code.
	tn.statusID = tn.createID(t, http.MethodPost, "/api/v1/projects/"+tn.projectID+"/statuses",
		map[string]any{"name": prefix + " own status", "color": "#123456", "category": "todo"})

	tn.atrID = tn.autoTransitionRule(t)

	tn.depID = tn.createID(t, http.MethodPost, "/api/v1/tasks/"+tn.taskID+"/dependencies",
		map[string]any{"depends_on_task_id": tn.otherTask})

	tn.inviteID = tn.createID(t, http.MethodPost, "/api/v1/workspaces/"+tn.wsID+"/invites",
		map[string]any{"email": uniqueEmail(prefix + "-invitee"), "role": "member"})
	env.OnCleanup(func() {
		_, _ = env.DB.ExecContext(context.Background(), "DELETE FROM user_invites WHERE id = $1", tn.inviteID)
	})

	resp = env.Post(t, "/api/v1/workspaces/"+tn.wsID+"/initiatives", map[string]any{"name": prefix + " initiative"})
	if resp.StatusCode == http.StatusCreated {
		var initiative map[string]any
		env.DecodeJSON(t, resp, &initiative)
		tn.initID = initiative["id"].(string)
		env.OnCleanup(func() {
			ctx := context.Background()
			_, _ = env.DB.ExecContext(ctx, "DELETE FROM initiative_projects WHERE initiative_id = $1", tn.initID)
			_, _ = env.DB.ExecContext(ctx, "DELETE FROM initiatives WHERE id = $1", tn.initID)
		})
	} else {
		_ = resp.Body.Close()
	}

	tn.agentKey = env.registerOwnAgent(t, prefix+"-agent")
	return tn
}

// autoTransitionRule returns the id of an auto-transition rule in this tenant's
// project, creating one if the project template did not already ship with the
// triggers taken (they are unique per (project, trigger)).
func (tn *tenant) autoTransitionRule(t *testing.T) string {
	t.Helper()

	resp := tn.env.Get(t, "/api/v1/projects/"+tn.projectID+"/auto-transition-rules")
	raw := tn.env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "cannot list auto-transition rules: %s", string(raw))

	var existing []map[string]any
	require.NoError(t, json.Unmarshal(raw, &existing))
	if len(existing) > 0 {
		id, _ := existing[0]["id"].(string)
		require.NotEmpty(t, id)
		return id
	}

	return tn.createID(t, http.MethodPost, "/api/v1/projects/"+tn.projectID+"/auto-transition-rules",
		map[string]any{"trigger": "all_subtasks_done", "target_status_id": tn.statusID})
}

// createID performs a create as this tenant and returns the new object's id.
func (tn *tenant) createID(t *testing.T, method, path string, body map[string]any) string {
	t.Helper()
	resp := tn.env.doRequest(t, method, path, body)
	raw := tn.env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: %s %s returned %d: %s", method, path, resp.StatusCode, string(raw))

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode %s: %s", path, string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "%s returned no id: %s", path, string(raw))
	return id
}

// TestCrossTenant_CompositeRouteChildIDIsChecked is the reported repro, four
// routes of it, reproduced the way it was found: with two ordinary accounts.
//
// Each request pairs the intruder's OWN parent id — their project, their task,
// their workspace, all of which they are entitled to — with the victim's child id.
// Every one of them succeeded in production. The guard resolved the parent,
// confirmed the caller was a member of it, and never looked at the second id;
// TestEveryIdentifiedRouteIsWorkspaceScoped was green throughout, because "at
// least one parameter resolves" was true.
//
// The assertion that matters is the database one. A 204 says nothing here: the
// handlers deleted by id alone, so a refused-looking response and a deleted row
// were entirely compatible.
func TestCrossTenant_CompositeRouteChildIDIsChecked(t *testing.T) {
	victim := newTenant(t, "xtc-victim")
	intruder := newTenant(t, "xtc-intruder")

	ctx := context.Background()

	t.Run("PATCH /projects/:proj_id/statuses/:status_id", func(t *testing.T) {
		resp := intruder.env.Patch(t,
			fmt.Sprintf("/api/v1/projects/%s/statuses/%s", intruder.projectID, victim.statusID),
			map[string]any{"name": "hijacked", "category": "done"})
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace renamed this tenant's status (status %d, body %s)", resp.StatusCode, body)

		var name, category string
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT name, category FROM task_statuses WHERE id = $1", victim.statusID).Scan(&name, &category))
		assert.Equal(t, "xtc-victim own status", name, "the cross-tenant rename reached the row")
		assert.Equal(t, "todo", category,
			"the cross-tenant recategorise reached the row — moving a status to done closes that tenant's tasks")
	})

	t.Run("DELETE /projects/:proj_id/auto-transition-rules/:atr_id", func(t *testing.T) {
		resp := intruder.env.Delete(t,
			fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules/%s", intruder.projectID, victim.atrID))
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace deleted this tenant's auto-transition rule (status %d, body %s)",
			resp.StatusCode, body)

		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM auto_transition_rules WHERE id = $1", victim.atrID).Scan(&n))
		assert.Equal(t, 1, n, "the rule was deleted from another tenant")
	})

	t.Run("DELETE /tasks/:task_id/dependencies/:dep_id", func(t *testing.T) {
		resp := intruder.env.Delete(t,
			fmt.Sprintf("/api/v1/tasks/%s/dependencies/%s", intruder.taskID, victim.depID))
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a member of another workspace cut an edge out of this tenant's dependency graph (status %d, body %s)",
			resp.StatusCode, body)

		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM task_dependencies WHERE id = $1", victim.depID).Scan(&n))
		assert.Equal(t, 1, n, "the dependency edge was deleted from another tenant")
	})

	t.Run("DELETE /workspaces/:ws_id/invites/:invite_id", func(t *testing.T) {
		resp := intruder.env.Delete(t,
			fmt.Sprintf("/api/v1/workspaces/%s/invites/%s", intruder.wsID, victim.inviteID))
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"an admin of another workspace revoked this tenant's invite (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM user_invites WHERE id = $1", victim.inviteID).Scan(&n))
		assert.Equal(t, 1, n, "the invite was revoked from another tenant")
	})

	// Resend has no row to check afterwards — its effect is an email to somebody
	// else's invitee — so the refusal is the assertion.
	t.Run("POST /workspaces/:ws_id/invites/:invite_id/resend", func(t *testing.T) {
		resp := intruder.env.Post(t,
			fmt.Sprintf("/api/v1/workspaces/%s/invites/%s/resend", intruder.wsID, victim.inviteID),
			map[string]any{})
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"an admin of another workspace re-sent this tenant's invitation email (status %d, body %s)",
			resp.StatusCode, body)
	})
}

// TestCrossTenant_CompositeRouteChildIDIsCheckedForAgentKeys runs the same four
// against an agent key rather than a user JWT.
//
// It is not redundant. Three of these routes carry rbac(...), which looks like a
// second line of defence and is not one: on an agent key rbac() short-circuits to
// a static capability map and never looks at the target object's workspace at all.
// Any agent key in any workspace — and every self-hoster's users can mint one in
// their own — walked through it.
func TestCrossTenant_CompositeRouteChildIDIsCheckedForAgentKeys(t *testing.T) {
	victim := newTenant(t, "xtca-victim")
	intruder := newTenant(t, "xtca-intruder")

	ctx := context.Background()

	writes := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{"rename another tenant's status", http.MethodPatch,
			fmt.Sprintf("/api/v1/projects/%s/statuses/%s", intruder.projectID, victim.statusID),
			map[string]any{"name": "hijacked"}},
		{"delete another tenant's auto-transition rule", http.MethodDelete,
			fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules/%s", intruder.projectID, victim.atrID), nil},
		{"delete another tenant's dependency edge", http.MethodDelete,
			fmt.Sprintf("/api/v1/tasks/%s/dependencies/%s", intruder.taskID, victim.depID), nil},
		{"revoke another tenant's invite", http.MethodDelete,
			fmt.Sprintf("/api/v1/workspaces/%s/invites/%s", intruder.wsID, victim.inviteID), nil},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			resp := intruder.env.doRequestAsAgent(t, w.method, w.path, w.body, intruder.agentKey)
			body := string(intruder.env.ReadBody(t, resp))
			assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
				"%s %s was allowed on an agent key (status %d, body %s)", w.method, w.path, resp.StatusCode, body)
		})
	}

	for _, check := range []struct {
		what  string
		query string
		arg   string
	}{
		{"status", `SELECT COUNT(*) FROM task_statuses WHERE id = $1 AND name = 'hijacked'`, victim.statusID},
	} {
		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx, check.query, check.arg).Scan(&n))
		assert.Zero(t, n, "an agent key rewrote another tenant's %s", check.what)
	}
	for _, check := range []struct {
		what  string
		query string
		arg   string
	}{
		{"auto-transition rule", `SELECT COUNT(*) FROM auto_transition_rules WHERE id = $1`, victim.atrID},
		{"dependency edge", `SELECT COUNT(*) FROM task_dependencies WHERE id = $1`, victim.depID},
		{"invite", `SELECT COUNT(*) FROM user_invites WHERE id = $1`, victim.inviteID},
	} {
		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx, check.query, check.arg).Scan(&n))
		assert.Equal(t, 1, n, "an agent key deleted another tenant's %s", check.what)
	}
}

// TestCrossTenant_TenantFromRequestBodyIsChecked covers the other half of the
// class: the tenant arrives in the body, where no route parameter names it and
// therefore no test that reads the router can see it.
//
// POST /rules/evaluate is the extreme case — its path carries no parameter at all,
// so RequireWorkspaceMemberScoped treats it exactly like /auth/me and checks
// nothing, while the handler evaluates against whatever workspace_id was typed
// into the body and returns the matching rules by name.
func TestCrossTenant_TenantFromRequestBodyIsChecked(t *testing.T) {
	victim := newTenant(t, "xtb-victim")
	intruder := newTenant(t, "xtb-intruder")

	ctx := context.Background()

	// A private rule in the victim's workspace, whose name is the thing that leaked.
	const secretRule = "victim-private-rule-name"
	ruleID := victim.createID(t, http.MethodPost, "/api/v1/workspaces/"+victim.wsID+"/rules",
		map[string]any{"rule_type": "transition_gate.require_comment", "name": secretRule})
	victim.env.OnCleanup(func() {
		_, _ = victim.env.DB.ExecContext(ctx, "DELETE FROM rules WHERE id = $1", ruleID)
	})

	t.Run("POST /rules/evaluate", func(t *testing.T) {
		resp := intruder.env.Post(t, "/api/v1/rules/evaluate", map[string]any{
			"action":       "task.move",
			"workspace_id": victim.wsID,
		})
		body := string(intruder.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a member of another workspace evaluated against this tenant's rules (status %d, body %s)",
			resp.StatusCode, body)
		assert.NotContains(t, body, secretRule, "the response leaked another tenant's rule name")
	})

	t.Run("POST /rules/evaluate on an agent key", func(t *testing.T) {
		resp := intruder.env.doRequestAsAgent(t, http.MethodPost, "/api/v1/rules/evaluate",
			map[string]any{"action": "task.move", "workspace_id": victim.wsID}, intruder.agentKey)
		body := string(intruder.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"an agent key evaluated against another tenant's rules (status %d, body %s)", resp.StatusCode, body)
		assert.NotContains(t, body, secretRule, "the response leaked another tenant's rule name")
	})

	t.Run("POST /tasks/:task_id/dependencies with a foreign depends_on_task_id", func(t *testing.T) {
		resp := intruder.env.Post(t, "/api/v1/tasks/"+intruder.taskID+"/dependencies",
			map[string]any{"depends_on_task_id": victim.taskID})
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"an edge was written across the tenant boundary (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM task_dependencies WHERE depends_on_task_id = $1 AND task_id = $2",
			victim.taskID, intruder.taskID).Scan(&n))
		assert.Zero(t, n, "a dependency edge crosses the tenant boundary")
	})

	t.Run("POST /initiatives/:init_id/projects with a foreign project_id", func(t *testing.T) {
		if intruder.initID == "" || victim.projectID == "" {
			t.Skip("no initiative fixture")
		}
		resp := intruder.env.Post(t, "/api/v1/initiatives/"+intruder.initID+"/projects",
			map[string]any{"project_id": victim.projectID})
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"another tenant's project was linked into this initiative (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM initiative_projects WHERE initiative_id = $1 AND project_id = $2",
			intruder.initID, victim.projectID).Scan(&n))
		assert.Zero(t, n, "an initiative holds another tenant's project")
	})

	t.Run("POST /tasks/:task_id/move-to-project into a foreign project", func(t *testing.T) {
		resp := intruder.env.Post(t, "/api/v1/tasks/"+intruder.otherTask+"/move-to-project",
			map[string]any{"project_id": victim.projectID})
		body := string(intruder.env.ReadBody(t, resp))
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"a task was moved into another tenant's project (status %d, body %s)", resp.StatusCode, body)

		var projectID string
		require.NoError(t, intruder.env.DB.QueryRowContext(ctx,
			"SELECT project_id FROM tasks WHERE id = $1", intruder.otherTask).Scan(&projectID))
		assert.Equal(t, intruder.projectID, projectID, "the task landed in another tenant's project")
	})
}

// TestCrossTenant_CompositeRoutesStillWorkForTheirOwner is the other half of the
// fix, and the half that decides whether it can ship: requiring every id in a path
// to resolve to the same tenant must not refuse the tenant those ids belong to.
// Every one of these is the ordinary product flow — editing a status, deleting an
// auto-transition rule, removing a dependency, revoking an invite you sent.
func TestCrossTenant_CompositeRoutesStillWorkForTheirOwner(t *testing.T) {
	owner := newTenant(t, "xtco-owner")
	ctx := context.Background()

	t.Run("PATCH own status", func(t *testing.T) {
		resp := owner.env.Patch(t,
			fmt.Sprintf("/api/v1/projects/%s/statuses/%s", owner.projectID, owner.statusID),
			map[string]any{"name": "renamed by its owner"})
		body := string(owner.env.ReadBody(t, resp))
		require.Less(t, resp.StatusCode, http.StatusBadRequest,
			"the owner was refused their own status (status %d, body %s)", resp.StatusCode, body)

		var name string
		require.NoError(t, owner.env.DB.QueryRowContext(ctx,
			"SELECT name FROM task_statuses WHERE id = $1", owner.statusID).Scan(&name))
		assert.Equal(t, "renamed by its owner", name, "the owner's own edit did not land")
	})

	t.Run("POST own dependency then DELETE it", func(t *testing.T) {
		resp := owner.env.Delete(t,
			fmt.Sprintf("/api/v1/tasks/%s/dependencies/%s", owner.taskID, owner.depID))
		body := string(owner.env.ReadBody(t, resp))
		require.Less(t, resp.StatusCode, http.StatusBadRequest,
			"the owner was refused their own dependency (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, owner.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM task_dependencies WHERE id = $1", owner.depID).Scan(&n))
		assert.Zero(t, n, "the owner's own delete did not land")
	})

	t.Run("DELETE own auto-transition rule", func(t *testing.T) {
		resp := owner.env.Delete(t,
			fmt.Sprintf("/api/v1/projects/%s/auto-transition-rules/%s", owner.projectID, owner.atrID))
		body := string(owner.env.ReadBody(t, resp))
		require.Less(t, resp.StatusCode, http.StatusBadRequest,
			"the owner was refused their own auto-transition rule (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, owner.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM auto_transition_rules WHERE id = $1", owner.atrID).Scan(&n))
		assert.Zero(t, n, "the owner's own delete did not land")
	})

	t.Run("POST own invite resend, then DELETE it", func(t *testing.T) {
		resp := owner.env.Delete(t,
			fmt.Sprintf("/api/v1/workspaces/%s/invites/%s", owner.wsID, owner.inviteID))
		body := string(owner.env.ReadBody(t, resp))
		require.Less(t, resp.StatusCode, http.StatusBadRequest,
			"the owner was refused their own invite (status %d, body %s)", resp.StatusCode, body)

		var n int
		require.NoError(t, owner.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM user_invites WHERE id = $1", owner.inviteID).Scan(&n))
		assert.Zero(t, n, "the owner's own revoke did not land")
	})

	t.Run("PATCH own workspace member role", func(t *testing.T) {
		// The owner acting on their own membership row: the containment check on
		// :user_id must not turn this into a 403.
		resp := owner.env.Patch(t,
			fmt.Sprintf("/api/v1/workspaces/%s/members/%s", owner.wsID, owner.env.UserID),
			map[string]any{"role": "admin"})
		body := string(owner.env.ReadBody(t, resp))
		// The last-owner rule refuses the role change on its own merits (400); what
		// must not happen is a 403 from the guard.
		assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
			"the containment check on :user_id locked the owner out of their own membership row (body %s)", body)
	})

	t.Run("POST own rules/evaluate", func(t *testing.T) {
		resp := owner.env.Post(t, "/api/v1/rules/evaluate", map[string]any{
			"action":       "task.move",
			"workspace_id": owner.wsID,
		})
		body := string(owner.env.ReadBody(t, resp))
		assert.Less(t, resp.StatusCode, http.StatusBadRequest,
			"the owner was refused evaluation against their own workspace (status %d, body %s)",
			resp.StatusCode, body)
	})

	t.Run("POST own move-to-project", func(t *testing.T) {
		second := owner.createID(t, http.MethodPost, "/api/v1/workspaces/"+owner.wsID+"/projects",
			map[string]any{"name": "xtco second project"})
		owner.env.OnCleanup(func() {
			_, _ = owner.env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", second)
			_, _ = owner.env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", second)
			_, _ = owner.env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", second)
		})

		resp := owner.env.Post(t, "/api/v1/tasks/"+owner.otherTask+"/move-to-project",
			map[string]any{"project_id": second})
		body := string(owner.env.ReadBody(t, resp))
		assert.Less(t, resp.StatusCode, http.StatusBadRequest,
			"the owner was refused a move between two of their own projects (status %d, body %s)",
			resp.StatusCode, body)
	})
}
