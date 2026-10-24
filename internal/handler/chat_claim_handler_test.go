package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Everything refused before the caller is parked is decided without the
// database, so the handler runs here with a nil repo: a request that reached it
// would panic instead of passing.

type runnerReq struct {
	query     string
	protocol  string
	runnerID  string
	agent     uuid.UUID
	chatToken bool
	notAgent  bool
	connector bool
}

func runnerClaim(t *testing.T, r runnerReq) (rec *httptest.ResponseRecorder, body map[string]any) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/runner/chat/claim?"+r.query, http.NoBody)
	if r.protocol != "" {
		req.Header.Set("X-Chat-Protocol", r.protocol)
	}
	if r.runnerID != "" {
		req.Header.Set(chatRunnerIDHeader, r.runnerID)
	}
	rec = httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if !r.notAgent {
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
		c.Set(mw.ContextKeyAgentID, r.agent)
		c.Set(mw.ContextKeyWorkspaceID, uuid.New())
	}
	if r.connector {
		c.Set(mw.ContextKeyOAuthConnectorUserID, uuid.New())
	}
	if r.chatToken {
		c.Set(mw.ContextKeyChatSession, &service.ChatSessionPrincipal{})
	}
	require.NoError(t, NewChatClaimHandler(nil, nil, nil).Claim(c))

	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func TestChatRunnerClaim_RefusalsBeforeParking(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	runner := uuid.NewString()

	t.Run("missing protocol header", func(t *testing.T) {
		rec, body := runnerClaim(t, runnerReq{query: "agents=" + me.String(), runnerID: runner, agent: me})
		assert.Equal(t, 400, rec.Code)
		assert.Equal(t, "chat_protocol_unsupported", body["error_code"])
		assert.Equal(t, "1", rec.Header().Get("X-Chat-Protocol-Supported"))
	})
	t.Run("a runner never claims for an agent it does not serve", func(t *testing.T) {
		rec, body := runnerClaim(t, runnerReq{query: "agents=" + me.String() + "," + other.String(), protocol: "1", runnerID: runner, agent: me})
		assert.Equal(t, 403, rec.Code)
		assert.Equal(t, "chat_agent_not_served", body["error_code"])
		ctx := body["context"].(map[string]any)
		assert.Equal(t, []any{other.String()}, ctx["agent_ids"])
	})
	t.Run("a chat-session token is not a runner", func(t *testing.T) {
		rec, _ := runnerClaim(t, runnerReq{query: "agents=" + me.String(), protocol: "1", runnerID: runner, agent: me, chatToken: true})
		assert.Equal(t, 401, rec.Code)
	})
	t.Run("an OAuth connector is not a runner", func(t *testing.T) {
		rec, _ := runnerClaim(t, runnerReq{query: "agents=" + me.String(), protocol: "1", runnerID: runner, agent: me, connector: true})
		assert.Equal(t, 403, rec.Code)
	})
	t.Run("a non-agent principal is not a runner", func(t *testing.T) {
		rec, _ := runnerClaim(t, runnerReq{query: "agents=" + me.String(), protocol: "1", runnerID: runner, notAgent: true})
		assert.Equal(t, 401, rec.Code)
	})
	t.Run("malformed input is reported at once", func(t *testing.T) {
		for _, q := range []string{"", "agents=", "agents=not-a-uuid", "agents=" + me.String() + "&timeout=x", "agents=" + me.String() + "&max=0"} {
			rec, _ := runnerClaim(t, runnerReq{query: q, protocol: "1", runnerID: runner, agent: me})
			assert.Equal(t, 400, rec.Code, q)
		}
		rec, _ := runnerClaim(t, runnerReq{query: "agents=" + me.String(), protocol: "1", runnerID: "nope", agent: me})
		assert.Equal(t, 400, rec.Code, "X-Chat-Runner-Id must be a UUID")
	})
}
