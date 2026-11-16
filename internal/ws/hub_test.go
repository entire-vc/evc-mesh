package ws

import (
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// subscribedClient registers a client on the hub with the given subscriptions.
func subscribedClient(h *Hub, userID uuid.UUID, channels ...string) *Client {
	c := NewClient(nil, h, Principal{UserID: userID, WorkspaceSlug: "ws-mine"}, nil)
	for _, ch := range channels {
		c.Subscriptions[ch] = true
	}
	h.clients.Store(c.ID, c)
	return c
}

func received(c *Client) bool {
	select {
	case <-c.Send:
		return true
	default:
		return false
	}
}

// TestHub_FanOut_PersonalChannelIsExactMatchOnly is the regression test for the
// agent-chat leak: a chat event published on one user's personal channel carries
// the project_id of its thread, and the project fallback delivered it to every
// member watching that project.
func TestHub_FanOut_PersonalChannelIsExactMatchOnly(t *testing.T) {
	h := NewHub(nil)
	project := uuid.New().String()
	owner := uuid.New()
	other := uuid.New()

	ownerClient := subscribedClient(h, owner, "ws:ws-mine", "project:"+project, personalChannelPrefix+owner.String())
	otherClient := subscribedClient(h, other, "ws:ws-mine", "project:"+project, personalChannelPrefix+other.String())

	h.fanOut(&redis.Message{
		Channel: personalChannelPrefix + owner.String(),
		Payload: `{"event_type":"chat.message.created","project_id":"` + project + `"}`,
	})

	assert.True(t, received(ownerClient), "owner must get their own personal event")
	assert.False(t, received(otherClient), "another project member must not get someone else's personal event")
}

// TestHub_FanOut_WorkspaceEventReachesProjectSubscribers pins the existing
// contract for project events published on the workspace channel.
func TestHub_FanOut_WorkspaceEventReachesProjectSubscribers(t *testing.T) {
	h := NewHub(nil)
	f := newProjectAccessFixture()
	project := f.project.String()

	projectOnly := clientOn(h, f.authz, f.member, "project:"+project)
	workspace := clientOn(h, f.authz, f.admin, "ws:"+f.slug)
	unrelated := clientOn(h, f.authz, f.member, "project:"+uuid.New().String())

	h.fanOut(&redis.Message{
		Channel: "ws:" + f.slug,
		Payload: `{"event_type":"task.updated","project_id":"` + project + `"}`,
	})

	assert.True(t, received(projectOnly), "project subscriber must get project events")
	assert.True(t, received(workspace), "workspace subscriber must get workspace events")
	assert.False(t, received(unrelated), "subscriber of another project must not get them")
}

// dashboard.updated (Dashboards §6.2) carries no project_id: it reaches every
// subscriber of the workspace channel and no one outside it.
func TestHub_FanOut_DashboardUpdatedReachesWorkspaceSubscribers(t *testing.T) {
	h := NewHub(nil)
	member := subscribedClient(h, uuid.New(), "ws:ws-mine")
	projectOnly := subscribedClient(h, uuid.New(), "project:"+uuid.New().String())
	otherWorkspace := subscribedClient(h, uuid.New(), "ws:ws-other")

	h.fanOut(&redis.Message{
		Channel: "ws:ws-mine",
		Payload: `{"event_type":"dashboard.updated","workspace_id":"` + uuid.New().String() + `","id":"` + uuid.New().String() + `","version":3}`,
	})

	select {
	case msg := <-member.Send:
		assert.Contains(t, string(msg), `"dashboard.updated"`)
		assert.Contains(t, string(msg), `"version":3`)
	default:
		t.Fatal("a workspace subscriber must get dashboard.updated")
	}
	assert.False(t, received(projectOnly), "no project_id: project-only subscribers are not addressed")
	assert.False(t, received(otherWorkspace), "another workspace must not get it")
}
