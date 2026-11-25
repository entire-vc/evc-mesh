package ws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// keyedAgents authenticates exactly the keys it holds. Only Authenticate is
// implemented; any other call panics on the nil embedded interface.
type keyedAgents struct {
	service.AgentService
	byKey map[string]*domain.Agent
}

func (k keyedAgents) Authenticate(_ context.Context, _, apiKey string) (*domain.Agent, error) {
	if a, ok := k.byKey[apiKey]; ok {
		return a, nil
	}
	return nil, errors.New("invalid agent key")
}

const (
	readerKey   = "agk_ws-11111111_readerkeyreaderkeyreaderkey" // pragma: allowlist secret
	ordinaryKey = "agk_ws-11111111_ordinarykeyordinarykeyordin" // pragma: allowlist secret
	unknownKey  = "agk_ws-11111111_unknownkeyunknownkeyunknown" // pragma: allowlist secret
)

func agentKeyFixture() keyedAgents {
	project := uuid.New()
	ws := uuid.New()
	return keyedAgents{byKey: map[string]*domain.Agent{
		readerKey:   {ID: uuid.New(), WorkspaceID: ws, ReaderProjectID: &project},
		ordinaryKey: {ID: uuid.New(), WorkspaceID: ws},
	}}
}

// callWithAgentKey runs /ws with the key in the X-Agent-Key header (header=true)
// or in the agent_key query fallback.
func callWithAgentKey(t *testing.T, key string, header bool) *httptest.ResponseRecorder {
	t.Helper()
	target := "/ws"
	if key != "" && !header {
		target += "?agent_key=" + url.QueryEscape(key)
	}
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	if key != "" && header {
		req.Header.Set("X-Agent-Key", key)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	authService := auth.NewService(nil, nil, nil, nil, testJWTSecret)
	authz := &handshakeAuthorizer{slug: "ws-11111111", wsID: uuid.New(), member: uuid.New()}
	require.NoError(t, Handler(NewHub(nil), authService, agentKeyFixture(), authz)(c))
	return rec
}

// The header is the transport a probe can use without writing the key into a
// URL (and so into an access log or a proxy log).
func TestHandler_AgentKeyHeaderIsRead(t *testing.T) {
	// Red control: no credential at all is "Authentication required".
	none := callWithAgentKey(t, "", true)
	assert.Equal(t, http.StatusUnauthorized, none.Code)
	assert.Contains(t, none.Body.String(), "Authentication required")

	// An unknown key in the header reaches key authentication: a handler that
	// ignored the header would answer "Authentication required" instead.
	unknown := callWithAgentKey(t, unknownKey, true)
	assert.Equal(t, http.StatusUnauthorized, unknown.Code)
	assert.Contains(t, unknown.Body.String(), "Invalid agent key")

	// An ordinary agent key in the header gets past every refusal to the
	// upgrade (which then fails on the non-hijackable recorder).
	ordinary := callWithAgentKey(t, ordinaryKey, true)
	assert.NotEqual(t, http.StatusUnauthorized, ordinary.Code, ordinary.Body.String())
	assert.NotEqual(t, http.StatusForbidden, ordinary.Code, ordinary.Body.String())
}

func TestHandler_ProjectReaderRefusedOnBothTransports(t *testing.T) {
	for _, header := range []bool{true, false} {
		rec := callWithAgentKey(t, readerKey, header)
		assert.Equal(t, http.StatusForbidden, rec.Code, "header=%v: %s", header, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "project reader keys have no event stream", "header=%v", header)
	}
}
