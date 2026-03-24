package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpserver "github.com/entire-vc/evc-mesh/internal/mcp"
)

// ---------------------------------------------------------------------------
// extractAgentKeyFromRequest
// ---------------------------------------------------------------------------

func TestExtractAgentKey_BearerHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("Authorization", "Bearer agk_acme_deadbeef1234")

	got := extractAgentKeyFromRequest(req)
	assert.Equal(t, "agk_acme_deadbeef1234", got)
}

func TestExtractAgentKey_BearerHeader_NonAgentToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.xxx")

	got := extractAgentKeyFromRequest(req)
	assert.Empty(t, got, "non-agk Bearer tokens must be ignored")
}

func TestExtractAgentKey_XAgentKeyHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("X-Agent-Key", "agk_ws_abc123")

	got := extractAgentKeyFromRequest(req)
	assert.Equal(t, "agk_ws_abc123", got)
}

func TestExtractAgentKey_XAgentKeyHeader_BadPrefix(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("X-Agent-Key", "not_an_agent_key")

	got := extractAgentKeyFromRequest(req)
	assert.Empty(t, got)
}

func TestExtractAgentKey_QueryParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse?agent_key=agk_demo_ffffffff", http.NoBody)

	got := extractAgentKeyFromRequest(req)
	assert.Equal(t, "agk_demo_ffffffff", got)
}

func TestExtractAgentKey_QueryParam_BadPrefix(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse?agent_key=bad_key", http.NoBody)

	got := extractAgentKeyFromRequest(req)
	assert.Empty(t, got)
}

func TestExtractAgentKey_Priority_BearerOverXAgentKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("Authorization", "Bearer agk_ws_from_bearer")
	req.Header.Set("X-Agent-Key", "agk_ws_from_header")

	got := extractAgentKeyFromRequest(req)
	assert.Equal(t, "agk_ws_from_bearer", got, "Bearer header should take priority")
}

func TestExtractAgentKey_NoCredentials(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)

	got := extractAgentKeyFromRequest(req)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// safeKeyPrefix
// ---------------------------------------------------------------------------

func TestSafeKeyPrefix(t *testing.T) {
	assert.Equal(t, "agk_acme_dea", safeKeyPrefix("agk_acme_deadbeef1234"))
	assert.Equal(t, "short", safeKeyPrefix("short"))
	assert.Equal(t, "", safeKeyPrefix(""))
}

// ---------------------------------------------------------------------------
// buildSession
// ---------------------------------------------------------------------------

func TestBuildSession_Valid(t *testing.T) {
	agentID := uuid.New().String()
	wsID := uuid.New().String()

	session, err := buildSession(agentID, wsID, "test-agent", "claude_code")
	require.NoError(t, err)
	assert.Equal(t, agentID, session.AgentID.String())
	assert.Equal(t, wsID, session.WorkspaceID.String())
	assert.Equal(t, "test-agent", session.AgentName)
	assert.Equal(t, "claude_code", session.AgentType)
}

func TestBuildSession_InvalidAgentID(t *testing.T) {
	_, err := buildSession("not-a-uuid", uuid.New().String(), "x", "y")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid agent_id")
}

func TestBuildSession_InvalidWorkspaceID(t *testing.T) {
	_, err := buildSession(uuid.New().String(), "bad-ws", "x", "y")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workspace_id")
}

// ---------------------------------------------------------------------------
// agentSessionCache
// ---------------------------------------------------------------------------

func mockMeshAPI(agentID, wsID uuid.UUID) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agents/me" {
			key := r.Header.Get("X-Agent-Key")
			if key == "" || key == "agk_bad_key" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":           agentID.String(),
				"workspace_id": wsID.String(),
				"name":         "test-agent",
				"agent_type":   "claude_code",
			})
			return
		}
		http.NotFound(w, r)
	}))
}

func TestSessionCache_AuthenticateAndCache(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()
	api := mockMeshAPI(agentID, wsID)
	defer api.Close()

	cache := &agentSessionCache{apiURL: api.URL}

	// First call should authenticate via REST.
	session, err := cache.GetOrAuthenticate(context.Background(), "agk_test_validkey123")
	require.NoError(t, err)
	assert.Equal(t, agentID, session.AgentID)
	assert.Equal(t, wsID, session.WorkspaceID)

	// Second call should return cached session (no REST call needed).
	session2, err := cache.GetOrAuthenticate(context.Background(), "agk_test_validkey123")
	require.NoError(t, err)
	assert.Equal(t, session, session2, "should return the same cached session")
}

func TestSessionCache_AuthFailure(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer api.Close()

	cache := &agentSessionCache{apiURL: api.URL}

	_, err := cache.GetOrAuthenticate(context.Background(), "agk_bad_key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestSessionCache_Cleanup(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()
	api := mockMeshAPI(agentID, wsID)
	defer api.Close()

	cache := &agentSessionCache{apiURL: api.URL}

	// Populate cache.
	_, err := cache.GetOrAuthenticate(context.Background(), "agk_test_stale")
	require.NoError(t, err)

	// Manually set lastUsed far in the past.
	cache.mu.Lock()
	cache.cache["agk_test_stale"].lastUsed = time.Now().Add(-1 * time.Hour)
	cache.mu.Unlock()

	// Cleanup with 30-minute threshold should evict the stale entry.
	cache.cleanup(30 * time.Minute)

	cache.mu.RLock()
	_, exists := cache.cache["agk_test_stale"]
	cache.mu.RUnlock()
	assert.False(t, exists, "stale session should be evicted")
}

func TestSessionCache_CleanupKeepsRecent(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()
	api := mockMeshAPI(agentID, wsID)
	defer api.Close()

	cache := &agentSessionCache{apiURL: api.URL}

	_, err := cache.GetOrAuthenticate(context.Background(), "agk_test_recent")
	require.NoError(t, err)

	// Cleanup with 30-minute threshold should keep the recent entry.
	cache.cleanup(30 * time.Minute)

	cache.mu.RLock()
	_, exists := cache.cache["agk_test_recent"]
	cache.mu.RUnlock()
	assert.True(t, exists, "recent session should not be evicted")
}

func TestSessionCache_ConcurrentAccess(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()
	api := mockMeshAPI(agentID, wsID)
	defer api.Close()

	cache := &agentSessionCache{apiURL: api.URL}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := cache.GetOrAuthenticate(context.Background(), "agk_test_concurrent")
			assert.NoError(t, err)
			if session != nil {
				assert.Equal(t, agentID, session.AgentID)
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// serverRegistry
// ---------------------------------------------------------------------------

func TestServerRegistry_GetClient(t *testing.T) {
	reg := &serverRegistry{apiURL: "http://localhost:8005"}

	client1 := reg.GetClient("agk_ws_key1")
	assert.NotNil(t, client1)

	// Same key should return cached client.
	client2 := reg.GetClient("agk_ws_key1")
	assert.Equal(t, client1, client2, "should return the same cached REST client")

	// Different key should return a different client.
	client3 := reg.GetClient("agk_ws_key2")
	assert.NotEqual(t, client1, client3, "different keys should get different clients")
}

func TestServerRegistry_Cleanup(t *testing.T) {
	reg := &serverRegistry{apiURL: "http://localhost:8005"}

	_ = reg.GetClient("agk_ws_stale")

	// Set lastUsed far in the past.
	reg.mu.Lock()
	reg.cache["agk_ws_stale"].lastUsed = time.Now().Add(-1 * time.Hour)
	reg.mu.Unlock()

	reg.cleanup(30 * time.Minute)

	reg.mu.RLock()
	_, exists := reg.cache["agk_ws_stale"]
	reg.mu.RUnlock()
	assert.False(t, exists, "stale client should be evicted")
}

// ---------------------------------------------------------------------------
// SSE endpoint auth integration
// ---------------------------------------------------------------------------

func TestSSEEndpoint_MissingKey_Returns401(t *testing.T) {
	// Simulate the /sse handler logic.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := extractAgentKeyFromRequest(r)
		if key == "" {
			http.Error(w, "Missing agent key", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSSEEndpoint_InvalidKey_Returns403(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer api.Close()

	sessionCache := &agentSessionCache{apiURL: api.URL}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := extractAgentKeyFromRequest(r)
		if key == "" {
			http.Error(w, "Missing agent key", http.StatusUnauthorized)
			return
		}
		_, err := sessionCache.GetOrAuthenticate(r.Context(), key)
		if err != nil {
			http.Error(w, "Authentication failed", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("X-Agent-Key", "agk_bad_invalidkey")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSSEEndpoint_ValidKey_Passes(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()
	api := mockMeshAPI(agentID, wsID)
	defer api.Close()

	sessionCache := &agentSessionCache{apiURL: api.URL}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := extractAgentKeyFromRequest(r)
		if key == "" {
			http.Error(w, "Missing agent key", http.StatusUnauthorized)
			return
		}
		_, err := sessionCache.GetOrAuthenticate(r.Context(), key)
		if err != nil {
			http.Error(w, "Authentication failed", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("X-Agent-Key", "agk_test_goodkey")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ---------------------------------------------------------------------------
// SSE context function: session + REST client injection
// ---------------------------------------------------------------------------

func TestSSEContextFunc_InjectsSessionAndClient(t *testing.T) {
	agentID := uuid.New()
	wsID := uuid.New()
	api := mockMeshAPI(agentID, wsID)
	defer api.Close()

	sessionCache := &agentSessionCache{apiURL: api.URL}
	srvRegistry := &serverRegistry{apiURL: api.URL}

	sseContextFunc := func(ctx context.Context, r *http.Request) context.Context {
		key := extractAgentKeyFromRequest(r)
		if key == "" {
			return ctx
		}
		session, err := sessionCache.GetOrAuthenticate(ctx, key)
		if err != nil {
			return ctx
		}
		perAgentClient := srvRegistry.GetClient(key)
		ctx = mcpserver.ContextWithSession(ctx, session)
		ctx = mcpserver.ContextWithRESTClient(ctx, perAgentClient)
		return ctx
	}

	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	req.Header.Set("X-Agent-Key", "agk_test_ctxkey")

	ctx := sseContextFunc(context.Background(), req)

	session := mcpserver.SessionFromContext(ctx)
	require.NotNil(t, session, "session should be injected into context")
	assert.Equal(t, agentID, session.AgentID)
	assert.Equal(t, wsID, session.WorkspaceID)

	client := mcpserver.RESTClientFromContext(ctx)
	assert.NotNil(t, client, "REST client should be injected into context")
}

func TestSSEContextFunc_NoKey_ReturnsOriginalContext(t *testing.T) {
	sseContextFunc := func(ctx context.Context, r *http.Request) context.Context {
		key := extractAgentKeyFromRequest(r)
		if key == "" {
			return ctx
		}
		return ctx
	}

	req := httptest.NewRequest(http.MethodGet, "/sse", http.NoBody)
	ctx := context.Background()
	result := sseContextFunc(ctx, req)
	assert.Equal(t, ctx, result, "context should not be modified without a key")
}
