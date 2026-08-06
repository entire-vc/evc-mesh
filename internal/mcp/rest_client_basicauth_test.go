package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A self-hosted Mesh instance can sit behind an HTTP basic-auth gate on its
// reverse proxy. NewRESTClient learns the credential from MESH_BASIC_AUTH and
// must attach it to EVERY request path — while leaving the fleet default
// (unset → no Authorization header) untouched. These tests pin both halves,
// because the failure mode of the "off" half is fleet-wide: a stray basic-auth
// header on requests to mesh.entire.host would be sent to an endpoint that does
// not expect it.

func TestNewRESTClient_BasicAuth_attachedWhenSet(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	var gotAgentKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		gotAgentKey = r.Header.Get("X-Agent-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// A password containing a colon proves the SplitN-on-first-colon parse:
	// naive Split would drop everything after the second colon.
	t.Setenv("MESH_BASIC_AUTH", "gateuser:p:ss:word")

	c := NewRESTClient(srv.URL, "agk_test_key")
	var out map[string]any
	require.NoError(t, c.doJSON(context.Background(), http.MethodGet, "/api/v1/agents/me", nil, &out))

	assert.True(t, gotOK, "basic-auth header must be present when MESH_BASIC_AUTH is set")
	assert.Equal(t, "gateuser", gotUser)
	assert.Equal(t, "p:ss:word", gotPass, "password must keep every colon after the first")
	assert.Equal(t, "agk_test_key", gotAgentKey, "the agent key must still ride alongside basic-auth")
}

func TestNewRESTClient_BasicAuth_absentByDefault(t *testing.T) {
	var sawBasicAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, sawBasicAuth = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("MESH_BASIC_AUTH", "") // explicit: the whole fleet runs like this

	c := NewRESTClient(srv.URL, "agk_test_key")
	var out map[string]any
	require.NoError(t, c.doJSON(context.Background(), http.MethodGet, "/api/v1/agents/me", nil, &out))

	assert.False(t, sawBasicAuth,
		"no Authorization header may be sent when MESH_BASIC_AUTH is unset — the mesh.entire.host default")
}

func TestNewRESTClient_BasicAuth_malformedIgnored(t *testing.T) {
	// A value with no colon is not a valid user:pass. It must be ignored, not
	// sent as a username with an empty password — silently authenticating as
	// "whatever the operator fat-fingered" is worse than not authenticating.
	var sawBasicAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, sawBasicAuth = r.BasicAuth()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("MESH_BASIC_AUTH", "no-colon-here")

	c := NewRESTClient(srv.URL, "agk_test_key")
	var out map[string]any
	require.NoError(t, c.doJSON(context.Background(), http.MethodGet, "/api/v1/agents/me", nil, &out))

	assert.False(t, sawBasicAuth, "a colon-less MESH_BASIC_AUTH must be treated as disabled")
}
