package ws

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// stubAuthorizer answers from a fixed map so the subscription rules can be tested
// without a database.
type stubAuthorizer struct {
	projectWS map[uuid.UUID]uuid.UUID
}

func (s *stubAuthorizer) WorkspaceIDBySlug(_ context.Context, _ string) (uuid.UUID, error) {
	return uuid.Nil, ErrWorkspaceNotFound
}

func (s *stubAuthorizer) UserIsWorkspaceMember(_ context.Context, _, _ uuid.UUID) bool {
	return false
}

func (s *stubAuthorizer) ProjectWorkspaceID(_ context.Context, projectID uuid.UUID) (uuid.UUID, error) {
	if ws, ok := s.projectWS[projectID]; ok {
		return ws, nil
	}
	return uuid.Nil, ErrWorkspaceNotFound
}

// TestClient_CanSubscribe is the regression test for the in-band half of the
// WebSocket leak. Fixing only the handshake would have left the same hole one
// message later: the socket accepted any "subscribe" a connected client sent, so
// a stranger could open a legitimate connection to their own workspace and then
// ask for the victim's workspace or project channel.
func TestClient_CanSubscribe(t *testing.T) {
	mine := uuid.New()
	theirs := uuid.New()
	myProject := uuid.New()
	theirProject := uuid.New()
	me := uuid.New()
	someoneElse := uuid.New()

	authz := &stubAuthorizer{projectWS: map[uuid.UUID]uuid.UUID{
		myProject:    mine,
		theirProject: theirs,
	}}

	user := NewClient(nil, nil, Principal{
		UserID:        me,
		WorkspaceID:   mine,
		WorkspaceSlug: "ws-11111111",
	}, authz)

	agent := NewClient(nil, nil, Principal{
		AgentID:       uuid.New(),
		WorkspaceID:   mine,
		WorkspaceSlug: "ws-11111111",
	}, authz)

	// A connection that named no workspace: authenticated, but entitled to
	// nothing but its own mention feed.
	bare := NewClient(nil, nil, Principal{UserID: me}, authz)

	tests := []struct {
		name    string
		client  *Client
		channel string
		want    bool
	}{
		{"own workspace", user, "ws:ws-11111111", true},
		{"another workspace", user, "ws:ws-22222222", false},
		{"own project", user, "project:" + myProject.String(), true},
		{"another workspace's project", user, "project:" + theirProject.String(), false},
		{"unknown project", user, "project:" + uuid.New().String(), false},
		{"own mention feed", user, "ws:user:" + me.String(), true},
		{"someone else's mention feed", user, "ws:user:" + someoneElse.String(), false},
		{"malformed project id", user, "project:not-a-uuid", false},
		{"unrecognised channel", user, "tasks:*", false},
		{"empty channel", user, "", false},

		{"agent own workspace", agent, "ws:ws-11111111", true},
		{"agent another workspace", agent, "ws:ws-22222222", false},
		{"agent own project", agent, "project:" + myProject.String(), true},
		// An agent has no user identity, so no personal feed belongs to it.
		{"agent mention feed", agent, "ws:user:" + me.String(), false},

		{"no workspace named: workspace channel", bare, "ws:ws-11111111", false},
		{"no workspace named: project channel", bare, "project:" + myProject.String(), false},
		{"no workspace named: own mentions", bare, "ws:user:" + me.String(), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.client.CanSubscribe(context.Background(), tc.channel))
		})
	}
}

// TestClient_CanSubscribe_NilAuthorizerDeniesProjects: a miswired caller must lose
// live updates, not gain another tenant's.
func TestClient_CanSubscribe_NilAuthorizerDeniesProjects(t *testing.T) {
	c := NewClient(nil, nil, Principal{
		UserID:        uuid.New(),
		WorkspaceID:   uuid.New(),
		WorkspaceSlug: "ws-11111111",
	}, nil)

	assert.False(t, c.CanSubscribe(context.Background(), "project:"+uuid.New().String()))
	// The workspace channel needs no authorizer: the handshake already proved it.
	assert.True(t, c.CanSubscribe(context.Background(), "ws:ws-11111111"))
}
