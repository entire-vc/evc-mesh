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

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsURL builds the WebSocket URL for the API under test.
func wsURL(base, query string) string {
	return strings.Replace(base, "http", "ws", 1) + "/ws?" + query
}

// dialWS opens a WebSocket connection, returning the handshake response on
// failure so the test can assert on the status code.
func dialWS(t *testing.T, url string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return websocket.Dial(ctx, url, nil) //nolint:bodyclose // the failure response body is drained by the library
}

// readEvents drains the socket for the given window and returns the raw frames.
// A closed or refused socket ends the window early — that is a legitimate outcome
// for the tests that expect to receive nothing.
func readEvents(conn *websocket.Conn, window time.Duration) []string {
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	var frames []string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return frames
		}
		frames = append(frames, string(data))
	}
}

// waitForEvent reads until a frame containing want appears or the window expires.
func waitForEvent(conn *websocket.Conn, want string, window time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return "", false
		}
		if strings.Contains(string(data), want) {
			return string(data), true
		}
	}
}

// TestCrossTenantWS_StrangerCannotSubscribeToAnotherWorkspace is the regression
// test for the severest finding. /ws is registered on the root Echo instance,
// ahead of the group that carries WorkspaceRLS and RequireWorkspaceMemberScoped,
// so no middleware guard could ever run on it — and the handler took the
// "workspace" query parameter at face value. A stranger with nothing but their own
// valid token and the victim's slug (which is public in every /w/<slug>/... URL)
// was subscribed to the victim's live event feed and received their task titles in
// real time.
func TestCrossTenantWS_StrangerCannotSubscribeToAnotherWorkspace(t *testing.T) {
	victim := newVictimFixture(t, "xtws-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xtws-intruder"), "TestPass123", "Intruder")

	url := wsURL(intruder.BaseURL, fmt.Sprintf("token=%s&workspace=%s",
		intruder.AuthToken, victim.wsSlug))

	conn, resp, err := dialWS(t, url)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		t.Fatalf("a stranger was allowed onto workspace %s's event feed", victim.wsSlug)
	}
	require.NotNil(t, resp, "expected an HTTP handshake failure, got: %v", err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"the handshake must be refused with 403, got %d", resp.StatusCode)
}

// TestCrossTenantWS_RejectionDoesNotRevealWhetherWorkspaceExists: slugs are
// ws-<8 hex>, short enough to be worth guessing. "Not a member" and "no such
// workspace" must be indistinguishable, or the refusal itself enumerates tenants.
func TestCrossTenantWS_RejectionDoesNotRevealWhetherWorkspaceExists(t *testing.T) {
	victim := newVictimFixture(t, "xtws-oracle-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xtws-oracle"), "TestPass123", "Intruder")

	real := wsURL(intruder.BaseURL, fmt.Sprintf("token=%s&workspace=%s", intruder.AuthToken, victim.wsSlug))
	fake := wsURL(intruder.BaseURL, fmt.Sprintf("token=%s&workspace=%s", intruder.AuthToken, "ws-deadbeef"))

	connReal, respReal, errReal := dialWS(t, real)
	if errReal == nil {
		_ = connReal.Close(websocket.StatusNormalClosure, "")
		t.Fatal("a stranger reached an existing workspace")
	}
	connFake, respFake, errFake := dialWS(t, fake)
	if errFake == nil {
		_ = connFake.Close(websocket.StatusNormalClosure, "")
		t.Fatal("a stranger reached a non-existent workspace")
	}

	require.NotNil(t, respReal)
	require.NotNil(t, respFake)
	assert.Equal(t, respFake.StatusCode, respReal.StatusCode,
		"an existing workspace answers %d and a non-existent one %d — the handshake is a slug oracle",
		respReal.StatusCode, respFake.StatusCode)
}

// TestCrossTenantWS_StrangerCannotSubscribeInBand covers the second door into the
// same leak. Fixing the handshake alone leaves the socket accepting any
// {"action":"subscribe"} that follows it, so a stranger connects legitimately to
// their own workspace and then asks for the victim's channels.
func TestCrossTenantWS_StrangerCannotSubscribeInBand(t *testing.T) {
	victim := newVictimFixture(t, "xtws-inband-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xtws-inband"), "TestPass123", "Intruder")
	resp := intruder.Get(t, "/api/v1/workspaces")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var intruderWorkspaces []map[string]any
	intruder.DecodeJSON(t, resp, &intruderWorkspaces)
	require.NotEmpty(t, intruderWorkspaces)
	intruderSlug := intruderWorkspaces[0]["slug"].(string)

	conn, _, err := dialWS(t, wsURL(intruder.BaseURL,
		fmt.Sprintf("token=%s&workspace=%s", intruder.AuthToken, intruderSlug)))
	require.NoError(t, err, "the intruder must still reach their OWN workspace")
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ask for everything that does not belong to this connection.
	for _, channel := range []string{
		"ws:" + victim.wsSlug,
		"project:" + victim.projectID,
		"ws:user:" + victim.env.UserID,
	} {
		payload, mErr := json.Marshal(map[string]string{"action": "subscribe", "channel": channel})
		require.NoError(t, mErr)
		require.NoError(t, conn.Write(ctx, websocket.MessageText, payload))
	}

	// Give the subscriptions a moment to be refused, then make the victim's
	// workspace emit something the intruder must not see.
	time.Sleep(500 * time.Millisecond)
	createResp := victim.env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", victim.projectID), map[string]any{
		"title": "Secret task the intruder must never see",
	})
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	_ = createResp.Body.Close()

	frames := readEvents(conn, 3*time.Second)
	denials := 0
	for _, frame := range frames {
		// A denial echoes the channel the client itself just asked for, which
		// tells it nothing it did not already know; only delivered events matter.
		if strings.Contains(frame, "subscribe.denied") {
			denials++
			continue
		}
		assert.NotContains(t, frame, "Secret task the intruder must never see",
			"the intruder received another tenant's task event: %s", frame)
		assert.NotContains(t, frame, "ws:"+victim.wsSlug,
			"the intruder received another tenant's workspace channel: %s", frame)
		assert.NotContains(t, frame, "project:"+victim.projectID,
			"the intruder received another tenant's project channel: %s", frame)
	}
	assert.Equal(t, 3, denials, "each refused subscribe should say so; frames: %v", frames)
}

// TestCrossTenantWS_MemberStillReceivesOwnEvents is the "did we break the product"
// half: the workspace's own user must still get live updates over the same socket.
func TestCrossTenantWS_MemberStillReceivesOwnEvents(t *testing.T) {
	victim := newVictimFixture(t, "xtws-owner")

	conn, _, err := dialWS(t, wsURL(victim.env.BaseURL,
		fmt.Sprintf("token=%s&workspace=%s", victim.env.AuthToken, victim.wsSlug)))
	require.NoError(t, err, "the workspace owner was refused their own event feed")
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// Subscribing to a project inside the workspace must be allowed, as must the
	// user's own mention feed.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, channel := range []string{
		"project:" + victim.projectID,
		"ws:user:" + victim.env.UserID,
	} {
		payload, mErr := json.Marshal(map[string]string{"action": "subscribe", "channel": channel})
		require.NoError(t, mErr)
		require.NoError(t, conn.Write(ctx, websocket.MessageText, payload))
	}

	time.Sleep(300 * time.Millisecond)
	const title = "Owner event round trip"
	createResp := victim.env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", victim.projectID), map[string]any{
		"title": title,
	})
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	_ = createResp.Body.Close()

	frame, ok := waitForEvent(conn, title, 8*time.Second)
	require.True(t, ok, "the workspace owner received no event for a task created in their own workspace")
	assert.NotContains(t, frame, "subscribe.denied")
}

// TestCrossTenantWS_AgentKeyStillWorks is the fleet's canary. Our agents connect
// over /ws with X-Agent-Key credentials; if this path breaks, every agent goes
// dark at once. The agent must reach its own workspace and be refused another's.
func TestCrossTenantWS_AgentKeyStillWorks(t *testing.T) {
	victim := newVictimFixture(t, "xtws-agent")

	resp := victim.env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", victim.wsID), map[string]any{
		"name":       "ws-canary-agent",
		"agent_type": "claude_code",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	victim.env.DecodeJSON(t, resp, &registered)
	agentKey := registered["api_key"].(string)
	agentID := registered["agent"].(map[string]any)["id"].(string)
	victim.env.OnCleanup(func() {
		_, _ = victim.env.DB.ExecContext(context.Background(), "DELETE FROM agents WHERE id = $1", agentID)
	})

	conn, _, err := dialWS(t, wsURL(victim.env.BaseURL, "agent_key="+agentKey))
	require.NoError(t, err, "an agent was refused its own workspace event feed")
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// The agent may subscribe to a project in its own workspace.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(map[string]string{"action": "subscribe", "channel": "project:" + victim.projectID})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, payload))

	time.Sleep(300 * time.Millisecond)
	const title = "Agent event round trip"
	createResp := victim.env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", victim.projectID), map[string]any{
		"title": title,
	})
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	_ = createResp.Body.Close()

	frame, ok := waitForEvent(conn, title, 8*time.Second)
	require.True(t, ok, "an agent received no event for a task created in its own workspace")
	assert.NotContains(t, frame, "subscribe.denied")
}

// TestCrossTenantWS_AgentCannotReachAnotherWorkspace: the agent path reads its
// workspace out of the key (agk_{slug}_{random}) and authenticates against it, and
// must keep ignoring a "workspace" query parameter that says otherwise.
func TestCrossTenantWS_AgentCannotReachAnotherWorkspace(t *testing.T) {
	victim := newVictimFixture(t, "xtws-agent-victim")
	other := newVictimFixture(t, "xtws-agent-other")

	resp := other.env.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", other.wsID), map[string]any{
		"name":       "outsider-agent",
		"agent_type": "claude_code",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	other.env.DecodeJSON(t, resp, &registered)
	agentKey := registered["api_key"].(string)
	agentID := registered["agent"].(map[string]any)["id"].(string)
	other.env.OnCleanup(func() {
		_, _ = other.env.DB.ExecContext(context.Background(), "DELETE FROM agents WHERE id = $1", agentID)
	})

	conn, _, err := dialWS(t, wsURL(other.env.BaseURL,
		fmt.Sprintf("agent_key=%s&workspace=%s", agentKey, victim.wsSlug)))
	require.NoError(t, err, "the agent must still reach its own workspace")
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(map[string]string{"action": "subscribe", "channel": "ws:" + victim.wsSlug})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, payload))

	time.Sleep(300 * time.Millisecond)
	createResp := victim.env.Post(t, fmt.Sprintf("/api/v1/projects/%s/tasks", victim.projectID), map[string]any{
		"title": "Task the outsider agent must never see",
	})
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	_ = createResp.Body.Close()

	for _, frame := range readEvents(conn, 3*time.Second) {
		assert.NotContains(t, frame, "Task the outsider agent must never see",
			"an agent from another workspace received the victim's events: %s", frame)
	}
}
