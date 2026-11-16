package ws

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// projectAccessFixture is one workspace with project P and the principals whose
// access to P the REST rule decides differently.
type projectAccessFixture struct {
	authz   *stubAuthorizer
	ws      uuid.UUID
	slug    string
	project uuid.UUID

	outsider    Principal // workspace member, not in P
	member      Principal // workspace member with a P row
	owner       Principal // workspace owner, no P row
	admin       Principal // workspace admin, no P row
	adminAgent  Principal // agent whose id holds an admin role, no P row
	memberAgent Principal // agent with a P row
}

func newProjectAccessFixture() *projectAccessFixture {
	f := &projectAccessFixture{ws: uuid.New(), slug: "ws-aaaaaaaa", project: uuid.New()}
	user := func() Principal {
		return Principal{UserID: uuid.New(), WorkspaceID: f.ws, WorkspaceSlug: f.slug}
	}
	agent := func() Principal {
		return Principal{AgentID: uuid.New(), WorkspaceID: f.ws, WorkspaceSlug: f.slug}
	}
	f.outsider, f.member, f.owner, f.admin = user(), user(), user(), user()
	f.adminAgent, f.memberAgent = agent(), agent()

	f.authz = &stubAuthorizer{
		projectWS: map[uuid.UUID]uuid.UUID{f.project: f.ws},
		roles: map[[2]uuid.UUID]string{
			{f.ws, f.outsider.UserID}: "member",
			{f.ws, f.member.UserID}:   "member",
			{f.ws, f.owner.UserID}:    "owner",
			{f.ws, f.admin.UserID}:    "admin",
			// An agent's grant role is never consulted, but if the rule ever
			// looked the agent id up as a user it would find admin here.
			{f.ws, f.adminAgent.AgentID}: "admin",
		},
	}
	f.authz.setMember(f.project, f.member.UserID, true)
	f.authz.setMember(f.project, f.memberAgent.AgentID, true)
	return f
}

// clientOn registers a client of the principal on the hub, already holding the
// given subscriptions. They are written directly, not through CanSubscribe, so
// the fan-out filter is tested on its own: a subscription that exists (made
// before a membership was revoked, or by the handshake's auto-subscribe to
// ws:<slug>) must still not carry a project the principal may not open.
func clientOn(h *Hub, authz Authorizer, p Principal, channels ...string) *Client {
	c := NewClient(nil, h, p, authz)
	for _, ch := range channels {
		c.Subscriptions[ch] = true
	}
	h.clients.Store(c.ID, c)
	return c
}

func taskCreatedFrame(projectID string) string {
	return `{"event_type":"task.created","project_id":"` + projectID +
		`","payload":{"changes":{"title":{"new":"Secret roadmap item"}}}}`
}

// TestHub_FanOut_ProjectFramesReachOnlyProjectReaders is the regression test for
// A task.* frame carries the task title and changed fields, and the
// hub handed it to every subscriber of ws:<slug> or project:<id>, so a workspace
// member outside the project read the titles of its tasks live.
func TestHub_FanOut_ProjectFramesReachOnlyProjectReaders(t *testing.T) {
	f := newProjectAccessFixture()

	cases := []struct {
		name string
		p    Principal
		want bool
	}{
		{"workspace member outside the project", f.outsider, false},
		{"project member", f.member, true},
		{"workspace owner", f.owner, true},
		{"workspace admin", f.admin, true},
		{"agent with an admin role, not a member", f.adminAgent, false},
		{"agent project member", f.memberAgent, true},
	}
	channels := map[string]string{
		"via ws:<slug>":    workspaceChannelPrefix + f.slug,
		"via project:<id>": projectChannelPrefix + f.project.String(),
	}

	for _, tc := range cases {
		for via, channel := range channels {
			t.Run(tc.name+" "+via, func(t *testing.T) {
				h := NewHub(nil)
				c := clientOn(h, f.authz, tc.p, channel)

				h.fanOut(&redis.Message{Channel: workspaceChannelPrefix + f.slug, Payload: taskCreatedFrame(f.project.String())})

				assert.Equal(t, tc.want, received(c), "task.created of the project")
			})
		}
	}
}

// Frames that name no project stay workspace-wide: the outsider still gets them,
// which is also the positive control that the outsider's client is live at all.
func TestHub_FanOut_FramesWithoutProjectStayWorkspaceWide(t *testing.T) {
	f := newProjectAccessFixture()
	h := NewHub(nil)
	outsider := clientOn(h, f.authz, f.outsider, workspaceChannelPrefix+f.slug)

	// EventBusMessage.ProjectID is a plain uuid: "no project" is the nil uuid.
	h.fanOut(&redis.Message{Channel: workspaceChannelPrefix + f.slug, Payload: `{"event_type":"workspace.updated","project_id":"00000000-0000-0000-0000-000000000000"}`})
	assert.True(t, received(outsider), "nil project_id is workspace-wide")

	h.fanOut(&redis.Message{Channel: workspaceChannelPrefix + f.slug, Payload: `{"event_type":"dashboard.updated","id":"x"}`})
	assert.True(t, received(outsider), "absent project_id is workspace-wide")

	h.fanOut(&redis.Message{Channel: workspaceChannelPrefix + f.slug, Payload: taskCreatedFrame("not-a-uuid")})
	assert.False(t, received(outsider), "an unreadable project_id is nobody's project")
	assert.Zero(t, f.authz.lookupCount(), "frames without a project ask nothing")
}

// TestClient_CanSubscribe_ProjectChannelNeedsProjectAccess: being in the same
// workspace is not enough to subscribe to project:<id>.
func TestClient_CanSubscribe_ProjectChannelNeedsProjectAccess(t *testing.T) {
	f := newProjectAccessFixture()
	channel := projectChannelPrefix + f.project.String()

	cases := []struct {
		name string
		p    Principal
		want bool
	}{
		{"workspace member outside the project", f.outsider, false},
		{"project member", f.member, true},
		{"workspace owner", f.owner, true},
		{"workspace admin", f.admin, true},
		{"agent with an admin role, not a member", f.adminAgent, false},
		{"agent project member", f.memberAgent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(nil, nil, tc.p, f.authz)
			assert.Equal(t, tc.want, c.CanSubscribe(context.Background(), channel))
		})
	}
}

// The verdict is cached for projectAccessTTL: one lookup per client and project
// per window, and a revoked membership stops the frames once the window ends.
func TestClient_MayReceiveProject_RevocationWindow(t *testing.T) {
	f := newProjectAccessFixture()
	c := NewClient(nil, nil, f.member, f.authz)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	assert.True(t, c.MayReceiveProject(f.project))
	assert.True(t, c.MayReceiveProject(f.project))
	assert.Equal(t, 1, f.authz.lookupCount(), "the second frame is answered from the cache")

	f.authz.setMember(f.project, f.member.UserID, false)
	now = now.Add(projectAccessTTL - time.Second)
	assert.True(t, c.MayReceiveProject(f.project), "inside the window the old verdict stands")

	now = now.Add(2 * time.Second)
	assert.False(t, c.MayReceiveProject(f.project), "after the window the revocation is seen")
	assert.Equal(t, 2, f.authz.lookupCount())
}

// A failed lookup refuses the frame.
func TestClient_MayReceiveProject_LookupErrorRefuses(t *testing.T) {
	f := newProjectAccessFixture()
	f.authz.failMembers = true

	assert.False(t, NewClient(nil, nil, f.member, f.authz).MayReceiveProject(f.project))
	assert.False(t, NewClient(nil, nil, f.memberAgent, f.authz).MayReceiveProject(f.project))
	// The owner/admin bypass needs no project_members lookup.
	assert.True(t, NewClient(nil, nil, f.admin, f.authz).MayReceiveProject(f.project))
	assert.False(t, NewClient(nil, nil, f.member, nil).MayReceiveProject(f.project), "no authorizer, no frames")
}

// A project of another workspace is refused even to that principal's own
// workspace owner.
func TestMayAccessProject_ProjectOfAnotherWorkspace(t *testing.T) {
	f := newProjectAccessFixture()
	foreign := uuid.New()
	f.authz.projectWS[foreign] = uuid.New()
	f.authz.setMember(foreign, f.member.UserID, true)

	assert.False(t, f.authz.MayAccessProject(context.Background(), f.owner, foreign))
	assert.False(t, f.authz.MayAccessProject(context.Background(), f.member, foreign))
}
