package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// NewAgentSession
// ---------------------------------------------------------------------------

func TestNewAgentSession_Valid(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()

	session, err := NewAgentSession(agentID.String(), wsID.String(), "bot", "claude_code")
	require.NoError(t, err)
	assert.Equal(t, agentID, session.AgentID)
	assert.Equal(t, wsID, session.WorkspaceID)
	assert.Equal(t, "bot", session.AgentName)
	assert.Equal(t, "claude_code", session.AgentType)
}

func TestNewAgentSession_InvalidAgentID(t *testing.T) {
	_, err := NewAgentSession("bad", uuid.New().String(), "x", "y")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid agent_id")
}

func TestNewAgentSession_InvalidWorkspaceID(t *testing.T) {
	_, err := NewAgentSession(uuid.New().String(), "bad", "x", "y")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workspace_id")
}

func TestNewAgentSession_EmptyAgentID(t *testing.T) {
	_, err := NewAgentSession("", uuid.New().String(), "x", "y")
	require.Error(t, err)
}

func TestNewAgentSession_EmptyWorkspaceID(t *testing.T) {
	_, err := NewAgentSession(uuid.New().String(), "", "x", "y")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Context helpers: ContextWithSession / SessionFromContext
// ---------------------------------------------------------------------------

func TestSessionContext_RoundTrip(t *testing.T) {
	session := &AgentSession{
		AgentID:     uuid.New(),
		WorkspaceID: uuid.New(),
		AgentName:   "test",
		AgentType:   "claude_code",
	}

	ctx := ContextWithSession(context.Background(), session)
	got := SessionFromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, session.AgentID, got.AgentID)
	assert.Equal(t, session.WorkspaceID, got.WorkspaceID)
}

func TestSessionFromContext_Empty(t *testing.T) {
	got := SessionFromContext(context.Background())
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// Context helpers: ContextWithRESTClient / RESTClientFromContext
// ---------------------------------------------------------------------------

func TestRESTClientContext_RoundTrip(t *testing.T) {
	client := NewRESTClient("http://localhost:8005", "agk_test_key")

	ctx := ContextWithRESTClient(context.Background(), client)
	got := RESTClientFromContext(ctx)
	assert.Equal(t, client, got)
}

func TestRESTClientFromContext_Empty(t *testing.T) {
	got := RESTClientFromContext(context.Background())
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// Server.getSession / Server.getRESTClient fallback logic
// ---------------------------------------------------------------------------

func TestServer_GetSession_PrefersContext(t *testing.T) {
	staticSession := &AgentSession{AgentID: uuid.New(), AgentName: "static"}
	ctxSession := &AgentSession{AgentID: uuid.New(), AgentName: "context"}

	srv := &Server{session: staticSession}

	// Without context session, falls back to static.
	got := srv.getSession(context.Background())
	assert.Equal(t, "static", got.AgentName)

	// With context session, uses context.
	ctx := ContextWithSession(context.Background(), ctxSession)
	got = srv.getSession(ctx)
	assert.Equal(t, "context", got.AgentName)
}

func TestServer_GetRESTClient_PrefersContext(t *testing.T) {
	staticClient := NewRESTClient("http://static", "agk_static")
	ctxClient := NewRESTClient("http://context", "agk_context")

	srv := &Server{restClient: staticClient}

	// Without context client, falls back to static.
	got := srv.getRESTClient(context.Background())
	assert.Equal(t, staticClient, got)

	// With context client, uses context.
	ctx := ContextWithRESTClient(context.Background(), ctxClient)
	got = srv.getRESTClient(ctx)
	assert.Equal(t, ctxClient, got)
}

// ---------------------------------------------------------------------------
// Server profiles
// ---------------------------------------------------------------------------

func TestNewServer_CoreProfile(t *testing.T) {
	client := NewRESTClient("http://localhost:8005", "agk_test")
	srv := NewServer(ServerConfig{
		RESTClient: client,
		Profile:    ProfileCore,
	})
	assert.Equal(t, ProfileCore, srv.profile)
	assert.NotNil(t, srv.tracker)
}

func TestNewServer_FullProfile(t *testing.T) {
	client := NewRESTClient("http://localhost:8005", "agk_test")
	srv := NewServer(ServerConfig{
		RESTClient: client,
		Profile:    ProfileFull,
	})
	assert.Equal(t, ProfileFull, srv.profile)
}

func TestNewServer_DefaultProfile(t *testing.T) {
	client := NewRESTClient("http://localhost:8005", "agk_test")
	srv := NewServer(ServerConfig{
		RESTClient: client,
	})
	assert.Equal(t, ProfileFull, srv.profile, "empty profile should default to full")
}

// ---------------------------------------------------------------------------
// SessionTracker basics
// ---------------------------------------------------------------------------

func TestSessionTracker_ComplianceScore_Empty(t *testing.T) {
	tracker := NewSessionTracker()
	score, checks := tracker.ComplianceScore()
	assert.Equal(t, 0.0, score)
	for name, val := range checks {
		assert.False(t, val, "check %s should be false initially", name)
	}
}

func TestSessionTracker_ComplianceScore_FullACP(t *testing.T) {
	tracker := NewSessionTracker()

	// Simulate a full ACP session.
	tracker.RecordToolCall("heartbeat")
	tracker.RecordToolCall("get_project_knowledge")
	tracker.RecordToolCall("get_my_rules")
	tracker.RecordToolCall("get_context")
	tracker.RecordToolCall("get_my_tasks")
	tracker.RecordToolCall("publish_event")
	tracker.RecordToolCall("remember")

	score, checks := tracker.ComplianceScore()
	assert.Equal(t, 1.0, score)
	for name, val := range checks {
		assert.True(t, val, "check %s should be true for full ACP", name)
	}
}
