//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// notifActor is one ordinary account with a workspace of its own, a project in
// it, and a task to talk about.
//
// Both sides of the tests below are actors. The intruder is not a stranger with
// no credentials — they are a perfectly ordinary user, fully entitled inside
// their own workspace, who names somebody else's workspace in a request body.
type notifActor struct {
	env    *TestEnv
	userID string
	wsID   string
	projID string
	taskID string
}

func newNotifActor(t *testing.T, prefix string) *notifActor {
	t.Helper()

	env := NewTestEnv(t)
	t.Cleanup(func() { env.Cleanup(t) })
	env.Register(t, uniqueEmail(prefix), "TestPass123", "Notif Actor")

	a := &notifActor{env: env, userID: env.UserID}
	require.NotEmpty(t, a.userID, "registration returned no user id")

	resp := env.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var workspaces []map[string]any
	env.DecodeJSON(t, resp, &workspaces)
	require.NotEmpty(t, workspaces)
	a.wsID = workspaces[0]["id"].(string)

	env.OnCleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM notifications WHERE workspace_id = $1", a.wsID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM notification_preferences WHERE workspace_id = $1", a.wsID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM notifications WHERE user_id = $1", a.userID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM notification_preferences WHERE user_id = $1", a.userID)
	})

	resp = env.Post(t, "/api/v1/workspaces/"+a.wsID+"/projects", map[string]any{"name": prefix + " project"})
	raw := env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "project setup failed: %s", string(raw))
	var project map[string]any
	require.NoError(t, json.Unmarshal(raw, &project))
	a.projID = project["id"].(string)
	env.OnCleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM comments WHERE task_id IN (SELECT id FROM tasks WHERE project_id = $1)", a.projID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", a.projID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", a.projID)
		_, _ = env.DB.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", a.projID)
	})

	resp = env.Post(t, "/api/v1/projects/"+a.projID+"/tasks", map[string]any{"title": prefix + " renewal negotiation"})
	raw = env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "task setup failed: %s", string(raw))
	var task map[string]any
	require.NoError(t, json.Unmarshal(raw, &task))
	a.taskID = task["id"].(string)

	return a
}

// subscribe issues the PUT this whole file is about: the tenant it writes into is
// named in the body, where no route parameter can be seen.
func (a *notifActor) subscribe(t *testing.T, wsID string, events []string) *http.Response {
	t.Helper()
	return a.env.Put(t, "/api/v1/notifications/preferences", map[string]any{
		"workspace_id": wsID,
		"channel":      "web_push",
		"events":       events,
		"is_enabled":   true,
	})
}

// prefRows counts this actor's preference rows in the given workspace. The
// database is the assertion that matters — a refused-looking status code and a
// written row were entirely compatible before the guard existed.
func (a *notifActor) prefRows(t *testing.T, wsID string) int {
	t.Helper()
	var n int
	require.NoError(t, a.env.DB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM notification_preferences WHERE workspace_id = $1 AND user_id = $2",
		wsID, a.userID).Scan(&n))
	return n
}

// comment posts a comment on this actor's own task, in their own workspace. It is
// the event that fans out.
func (a *notifActor) comment(t *testing.T, body string) {
	t.Helper()
	resp := a.env.Post(t, "/api/v1/tasks/"+a.taskID+"/comments", map[string]any{"body": body})
	raw := a.env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "comment failed: %s", string(raw))
}

// notificationsMentioning polls this actor's own notification feed for up to
// ~4 seconds and returns every notification whose title or body contains marker.
//
// Polling, because the fan-out is a goroutine the request does not wait for: a
// single read straight after the comment would go green on a delivery that simply
// had not happened yet, which is the failure mode that makes a leak test worthless.
func (a *notifActor) notificationsMentioning(t *testing.T, marker string) []map[string]any {
	t.Helper()

	deadline := time.Now().Add(4 * time.Second)
	for {
		resp := a.env.Get(t, "/api/v1/notifications")
		raw := a.env.ReadBody(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, "cannot read notifications: %s", string(raw))

		var feed struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(raw, &feed), "cannot decode notifications: %s", string(raw))

		var hits []map[string]any
		for _, item := range feed.Items {
			blob, _ := json.Marshal(item)
			if strings.Contains(string(blob), marker) {
				hits = append(hits, item)
			}
		}
		if len(hits) > 0 || !time.Now().Before(deadline) {
			return hits
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// victimSecret is the string the victim types into their own workspace and that
// nobody outside it should ever be able to read back.
const victimSecret = "SECRET-VICTIM-DATA: contract renewal price is $42000"

// TestNotificationPreferences_CrossTenantSubscription is the reported repro, in
// its two halves.
//
// PUT /notifications/preferences takes its workspace_id from the request body.
// There is no path parameter, so RequireWorkspaceMemberScoped has nothing to
// resolve and never runs — the same shape as POST /rules/evaluate, and invisible
// to any test that reads the router.
//
// What made it worth a P0 is not the write. It is the read: dispatch() loads
// every preference row in the event's workspace and filters on the channel and
// the event type, never on whether the row's owner belongs to that workspace at
// all. So the row is a standing subscription to a stranger's workspace, and every
// comment posted in it is delivered — title, body and all — to the outsider's
// notification feed.
func TestNotificationPreferences_CrossTenantSubscription(t *testing.T) {
	victim := newNotifActor(t, "np-victim")
	intruder := newNotifActor(t, "np-intruder")

	ctx := context.Background()
	events := []string{"comment.created", "task.assigned", "task.status_changed"}

	// The victim subscribes to their own workspace, legitimately. This is both a
	// control — it proves the fan-out is working at all, so a silent absence later
	// cannot be mistaken for a fix — and the check that the guard does not lock
	// out the people it is there to protect.
	resp := victim.subscribe(t, victim.wsID, events)
	raw := victim.env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the workspace owner cannot set their own preferences: %s", string(raw))

	t.Run("the write is refused", func(t *testing.T) {
		resp := intruder.subscribe(t, victim.wsID, events)
		body := string(intruder.env.ReadBody(t, resp))

		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a member of another workspace subscribed to this tenant's events (status %d, body %s)",
			resp.StatusCode, body)
		assert.Zero(t, intruder.prefRows(t, victim.wsID),
			"the cross-tenant subscription reached the table")
	})

	t.Run("a row planted by any other means is inert", func(t *testing.T) {
		// Inserted straight into the table, deliberately bypassing the handler, and
		// after clearing whatever the subtest above did or did not manage to write —
		// so this half stands on its own whether or not the write guard is present.
		//
		// The write guard is one layer; this is the other, and it is the one that
		// decides whether a row that got there by some route nobody has audited — a
		// future endpoint, an import, a row left over from before the guard — can
		// still be used to read another tenant's traffic.
		_, err := victim.env.DB.ExecContext(ctx,
			"DELETE FROM notification_preferences WHERE workspace_id = $1 AND user_id = $2",
			victim.wsID, intruder.userID)
		require.NoError(t, err)

		_, err = victim.env.DB.ExecContext(ctx, `
			INSERT INTO notification_preferences (id, workspace_id, user_id, channel, events, is_enabled)
			VALUES (gen_random_uuid(), $1, $2, 'web_push', $3, true)`,
			victim.wsID, intruder.userID, pgTextArray(events))
		require.NoError(t, err, "cannot plant the preference row")
		require.Equal(t, 1, intruder.prefRows(t, victim.wsID), "the planted row is not there")

		victim.comment(t, victimSecret)

		// The control first: if the victim's own delivery did not happen, the
		// intruder's empty feed proves nothing at all.
		require.NotEmpty(t, victim.notificationsMentioning(t, victimSecret),
			"the workspace owner did not receive their own notification — the fan-out is not running, "+
				"so the intruder's feed says nothing either way")

		leaked := intruder.notificationsMentioning(t, victimSecret)
		assert.Empty(t, leaked,
			"a user outside the workspace was delivered its private comment: %v", leaked)

		var n int
		require.NoError(t, victim.env.DB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND workspace_id = $2",
			intruder.userID, victim.wsID).Scan(&n))
		assert.Zero(t, n, "notifications for another tenant's workspace were written for this user")
	})
}

// TestDeliverablePreferences_ExcludesOutsiders exercises the dispatch query
// itself, rather than what a comment happens to make visible.
//
// It exists because the black-box test above can only see the rows that produce
// an in-app notification, and those are the user rows. A preference row may name
// an agent instead, agents belong to exactly one workspace, and a row naming an
// agent from somewhere else is no more legitimate than one naming an outside
// user. Browser push reads the same query, so a row this returns is a row that
// reaches a device. The only honest way to assert on the agent branch is to call
// the function the service calls.
func TestDeliverablePreferences_ExcludesOutsiders(t *testing.T) {
	insider := newNotifActor(t, "npd-insider")
	outsider := newNotifActor(t, "npd-outsider")

	ctx := context.Background()
	wsID := uuid.MustParse(insider.wsID)

	ownAgent := insider.registerAgent(t, "npd-own-agent")
	foreignAgent := outsider.registerAgent(t, "npd-foreign-agent")

	// Four rows in the insider's workspace: two that belong there and two that do
	// not. Only the first two may come back.
	plantUserPref(t, insider, insider.wsID, insider.userID)
	plantAgentPref(t, insider, insider.wsID, ownAgent)
	plantUserPref(t, insider, insider.wsID, outsider.userID)
	plantAgentPref(t, insider, insider.wsID, foreignAgent)

	repo := postgres.NewNotificationRepo(insider.env.DB)
	prefs, err := repo.GetDeliverablePreferences(ctx, wsID)
	require.NoError(t, err)

	var users, agents []string
	for _, p := range prefs {
		if p.UserID != nil {
			users = append(users, p.UserID.String())
		}
		if p.AgentID != nil {
			agents = append(agents, p.AgentID.String())
		}
	}

	assert.Contains(t, users, insider.userID, "the workspace's own member was filtered out")
	assert.Contains(t, agents, ownAgent, "the workspace's own agent was filtered out")
	assert.NotContains(t, users, outsider.userID, "a user from another workspace is eligible for delivery")
	assert.NotContains(t, agents, foreignAgent, "an agent from another workspace is eligible for delivery")
}

// registerAgent creates an agent in this actor's own workspace and returns its id.
func (a *notifActor) registerAgent(t *testing.T, name string) string {
	t.Helper()

	resp := a.env.Post(t, "/api/v1/workspaces/"+a.wsID+"/agents", map[string]any{
		"name":       name + "-" + fmt.Sprint(time.Now().UnixNano()),
		"agent_type": "claude_code",
	})
	raw := a.env.ReadBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "agent registration failed: %s", string(raw))

	var registered map[string]any
	require.NoError(t, json.Unmarshal(raw, &registered))
	agent, ok := registered["agent"].(map[string]any)
	require.True(t, ok, "no agent in the registration response: %s", string(raw))
	agentID, _ := agent["id"].(string)
	require.NotEmpty(t, agentID)

	a.env.OnCleanup(func() {
		ctx := context.Background()
		_, _ = a.env.DB.ExecContext(ctx, "DELETE FROM notification_preferences WHERE agent_id = $1", agentID)
		_, _ = a.env.DB.ExecContext(ctx, "DELETE FROM agents WHERE id = $1", agentID)
	})
	return agentID
}

func plantUserPref(t *testing.T, a *notifActor, wsID, userID string) {
	t.Helper()
	_, err := a.env.DB.ExecContext(context.Background(), `
		INSERT INTO notification_preferences (id, workspace_id, user_id, channel, events, is_enabled)
		VALUES (gen_random_uuid(), $1, $2, 'web_push', $3, true)
		ON CONFLICT (workspace_id, user_id, channel) WHERE user_id IS NOT NULL DO NOTHING`,
		wsID, userID, pgTextArray([]string{"comment.created"}))
	require.NoError(t, err, "cannot plant the user preference row")
}

func plantAgentPref(t *testing.T, a *notifActor, wsID, agentID string) {
	t.Helper()
	_, err := a.env.DB.ExecContext(context.Background(), `
		INSERT INTO notification_preferences (id, workspace_id, agent_id, channel, events, is_enabled)
		VALUES (gen_random_uuid(), $1, $2, 'web_push', $3, true)
		ON CONFLICT (workspace_id, agent_id, channel) WHERE agent_id IS NOT NULL DO NOTHING`,
		wsID, agentID, pgTextArray([]string{"comment.created"}))
	require.NoError(t, err, "cannot plant the agent preference row")
}

// TestNotificationPreferences_RepeatedUpdateKeepsOneRow covers the other defect in
// the same handler, which is not a security bug but is why nobody could tell the
// security bug from ordinary use: the upsert conflicts on the primary key, and the
// handler never looks an existing row up, so pref.ID is always a fresh uuid and
// the conflict can never fire. Every PUT inserted another row. Preferences did not
// update, they accumulated.
func TestNotificationPreferences_RepeatedUpdateKeepsOneRow(t *testing.T) {
	owner := newNotifActor(t, "np-repeat")
	ctx := context.Background()

	resp := owner.subscribe(t, owner.wsID, []string{"comment.created"})
	raw := owner.env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "first PUT failed: %s", string(raw))

	resp = owner.subscribe(t, owner.wsID, []string{"task.assigned", "task.status_changed"})
	raw = owner.env.ReadBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "second PUT failed: %s", string(raw))

	assert.Equal(t, 1, owner.prefRows(t, owner.wsID),
		"the second update inserted a second row instead of updating the first")

	var events []string
	require.NoError(t, owner.env.DB.QueryRowContext(ctx,
		"SELECT events FROM notification_preferences WHERE workspace_id = $1 AND user_id = $2 AND channel = 'web_push'",
		owner.wsID, owner.userID).Scan(pgArrayScanner(&events)))
	assert.ElementsMatch(t, []string{"task.assigned", "task.status_changed"}, events,
		"the row kept the first PUT's events — the update did not land")
}

// TestNotificationPreferences_Delete covers the third gap: until now a
// subscription could be created through the API and never removed through it, by
// anybody — not the subscriber, not the workspace owner.
func TestNotificationPreferences_Delete(t *testing.T) {
	owner := newNotifActor(t, "np-del-owner")
	outsider := newNotifActor(t, "np-del-outsider")

	t.Run("a subscriber removes their own", func(t *testing.T) {
		resp := owner.subscribe(t, owner.wsID, []string{"comment.created"})
		_ = owner.env.ReadBody(t, resp)
		require.Equal(t, 1, owner.prefRows(t, owner.wsID))

		resp = owner.env.doRequest(t, http.MethodDelete, "/api/v1/notifications/preferences",
			map[string]any{"workspace_id": owner.wsID, "channel": "web_push"})
		body := string(owner.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode, "delete failed: %s", body)
		assert.Zero(t, owner.prefRows(t, owner.wsID), "the row survived its owner deleting it")
	})

	t.Run("an outsider cannot reach into the workspace", func(t *testing.T) {
		resp := owner.subscribe(t, owner.wsID, []string{"comment.created"})
		_ = owner.env.ReadBody(t, resp)
		require.Equal(t, 1, owner.prefRows(t, owner.wsID))

		resp = outsider.env.doRequest(t, http.MethodDelete, "/api/v1/notifications/preferences",
			map[string]any{"workspace_id": owner.wsID, "channel": "web_push", "user_id": owner.userID})
		body := string(outsider.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a non-member deleted a subscription inside this workspace (status %d, body %s)", resp.StatusCode, body)
		assert.Equal(t, 1, owner.prefRows(t, owner.wsID), "the cross-tenant delete reached the row")
	})

	t.Run("the workspace owner removes somebody else's", func(t *testing.T) {
		// The workspace owner is the only party who can currently evict a rogue
		// subscription, and they can already do it the blunt way by removing the
		// member (the FK cascades). Naming the row is the same authority, less
		// destructively applied.
		_, err := owner.env.DB.ExecContext(context.Background(), `
			INSERT INTO notification_preferences (id, workspace_id, user_id, channel, events, is_enabled)
			VALUES (gen_random_uuid(), $1, $2, 'browser_push', $3, true)`,
			owner.wsID, outsider.userID, pgTextArray([]string{"comment.created"}))
		require.NoError(t, err)
		require.Equal(t, 1, outsider.prefRows(t, owner.wsID))

		resp := owner.env.doRequest(t, http.MethodDelete, "/api/v1/notifications/preferences",
			map[string]any{"workspace_id": owner.wsID, "channel": "browser_push", "user_id": outsider.userID})
		body := string(owner.env.ReadBody(t, resp))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode,
			"the workspace owner cannot evict a subscription from their own workspace: %s", body)
		assert.Zero(t, outsider.prefRows(t, owner.wsID), "the row survived the workspace owner deleting it")
	})
}

// pgTextArray renders a Go slice as a PostgreSQL text[] literal, so the tests can
// write preference rows without importing the driver's array types.
func pgTextArray(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, s := range items {
		quoted = append(quoted, `"`+strings.ReplaceAll(s, `"`, `\"`)+`"`)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// pgArrayScanner reads a text[] back into a []string without the driver types.
func pgArrayScanner(dst *[]string) any {
	return &textArrayScanner{dst: dst}
}

type textArrayScanner struct{ dst *[]string }

func (s *textArrayScanner) Scan(src any) error {
	var raw string
	switch v := src.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	case nil:
		*s.dst = nil
		return nil
	default:
		return fmt.Errorf("cannot scan %T as text[]", src)
	}
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "{"), "}")
	if raw == "" {
		*s.dst = nil
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.Trim(p, `"`))
	}
	*s.dst = out
	return nil
}
