package ws

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// stubAuthorizer answers the tenancy lookups from memory so the subscription and
// fan-out rules can be tested without a database. MayAccessProject runs the
// production rule (mayAccessProject) over those facts, so a test of this stub is
// a test of the rule, not of a copy of it.
type stubAuthorizer struct {
	projectWS map[uuid.UUID]uuid.UUID
	// roles is the workspace role of a user, keyed by {workspace, user}.
	roles map[[2]uuid.UUID]string
	// members holds project_members rows, keyed by {project, user or agent}.
	members map[[2]uuid.UUID]bool
	// failMembers makes every project_members lookup fail.
	failMembers bool

	mu      sync.Mutex
	lookups int // MayAccessProject calls
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

func (s *stubAuthorizer) UserWorkspaceRole(_ context.Context, wsID, userID uuid.UUID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roles[[2]uuid.UUID{wsID, userID}]
}

func (s *stubAuthorizer) ExistsMember(_ context.Context, projectID uuid.UUID, userID, agentID *uuid.UUID) (bool, error) {
	if s.failMembers {
		return false, errors.New("lookup failed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case userID != nil:
		return s.members[[2]uuid.UUID{projectID, *userID}], nil
	case agentID != nil:
		return s.members[[2]uuid.UUID{projectID, *agentID}], nil
	}
	return false, nil
}

func (s *stubAuthorizer) MayAccessProject(ctx context.Context, p Principal, projectID uuid.UUID) bool {
	s.mu.Lock()
	s.lookups++
	s.mu.Unlock()
	return mayAccessProject(ctx, s, p, projectID)
}

func (s *stubAuthorizer) setMember(projectID, principalID uuid.UUID, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members == nil {
		s.members = map[[2]uuid.UUID]bool{}
	}
	s.members[[2]uuid.UUID{projectID, principalID}] = on
}

func (s *stubAuthorizer) lookupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
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

	agentID := uuid.New()
	authz := &stubAuthorizer{
		projectWS: map[uuid.UUID]uuid.UUID{
			myProject:    mine,
			theirProject: theirs,
		},
		roles: map[[2]uuid.UUID]string{{mine, me}: "member"},
	}
	authz.setMember(myProject, me, true)
	authz.setMember(myProject, agentID, true)

	user := NewClient(nil, nil, Principal{
		UserID:        me,
		WorkspaceID:   mine,
		WorkspaceSlug: "ws-11111111",
	}, authz)

	agent := NewClient(nil, nil, Principal{
		AgentID:       agentID,
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
