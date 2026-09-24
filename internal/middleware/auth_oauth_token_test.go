package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ---------------------------------------------------------------------------
// MCP-OAuth 1/5: the `Authorization: Bearer mot_...` path through
// AgentKeyAuth / DualAuth / OptionalAuth, plus RequireUserAuth and
// GetOAuthConnectorUserID.
//
// fakeOAuthAuthenticator follows this package's established pattern for
// service dependencies (mockAgentService in auth_test.go): the middleware's
// contract is "given what the authenticator returns, set the context / answer
// 401", and the real token verification is proven end-to-end against Postgres
// in internal/handler/oauth_e2e_db_test.go. It also records calls, so tests
// can assert which credential path actually ran.
// ---------------------------------------------------------------------------

type fakeOAuthAuthenticator struct {
	agent *domain.Agent
	err   error
	calls []string
}

func (f *fakeOAuthAuthenticator) AuthenticateAccessToken(_ context.Context, raw string) (*domain.Agent, error) {
	f.calls = append(f.calls, raw)
	if f.err != nil {
		return nil, f.err
	}
	return f.agent, nil
}

// agentSvcMustNotRun fails the test if the X-Agent-Key path is consulted —
// used where a mot_ token must be decisive.
func agentSvcMustNotRun(t *testing.T) *mockAgentService {
	return &mockAgentService{
		AuthenticateFunc: func(_ context.Context, _, _ string) (*domain.Agent, error) {
			t.Error("X-Agent-Key path must not run when a mot_ bearer token is present")
			return nil, errors.New("unexpected")
		},
	}
}

func connectorAgent() (*domain.Agent, uuid.UUID) {
	userID := uuid.New()
	return &domain.Agent{
		ID: uuid.New(), WorkspaceID: uuid.New(), Name: "Claude — alice",
		WorkspaceRole: domain.RoleMember, OAuthConnectorUserID: &userID,
	}, userID
}

const testMotToken = "mot_abcdefghijklmnopqrstuvwxyz0123456789"

// captured is what the downstream handler saw in its context.
type captured struct {
	called      bool
	authType    interface{}
	agentID     interface{}
	wsID        interface{}
	authWsID    interface{}
	role        interface{}
	connectorID uuid.UUID
	hasConn     bool
	actorID     uuid.UUID
	actorType   domain.ActorType
	actorName   string
}

func capturingHandler(out *captured) echo.HandlerFunc {
	return func(c echo.Context) error {
		out.called = true
		out.authType = c.Get(ContextKeyAuthType)
		out.agentID = c.Get(ContextKeyAgentID)
		out.wsID = c.Get(ContextKeyWorkspaceID)
		out.authWsID = c.Get(ContextKeyAgentAuthWorkspaceID)
		out.role = c.Get(ContextKeyWorkspaceRole)
		out.connectorID, out.hasConn = GetOAuthConnectorUserID(c)
		out.actorID, out.actorType = actorctx.FromContext(c.Request().Context())
		out.actorName = actorctx.NameFromContext(c.Request().Context())
		return c.NoContent(http.StatusOK)
	}
}

func assertConnectorContext(t *testing.T, got captured, agent *domain.Agent, userID uuid.UUID) {
	t.Helper()
	require.True(t, got.called, "handler must run on a valid mot_ token")
	assert.Equal(t, AuthTypeAgent, got.authType)
	assert.Equal(t, agent.ID, got.agentID)
	assert.Equal(t, agent.WorkspaceID, got.wsID)
	assert.Equal(t, agent.WorkspaceID, got.authWsID)
	assert.Equal(t, agent.WorkspaceRole, got.role)
	assert.True(t, got.hasConn, "a mot_-authenticated agent must carry the consenting user id so RBAC can clamp it")
	assert.Equal(t, userID, got.connectorID)
	assert.Equal(t, agent.ID, got.actorID)
	assert.Equal(t, domain.ActorTypeAgent, got.actorType)
	assert.Equal(t, agent.Name, got.actorName)
}

func motRequest(token string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	return req, httptest.NewRecorder()
}

// --- AgentKeyAuth -----------------------------------------------------------

func TestAgentKeyAuth_MotToken_Valid_SetsConnectorContext(t *testing.T) {
	agent, userID := connectorAgent()
	oa := &fakeOAuthAuthenticator{agent: agent}
	req, rec := motRequest(testMotToken)
	req.Header.Set("X-Agent-Key", "agk_ws_ignored")

	var got captured
	require.NoError(t, AgentKeyAuth(agentSvcMustNotRun(t), oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{testMotToken}, oa.calls, "the full raw token (prefix included) is what gets verified")
	assertConnectorContext(t, got, agent, userID)
}

func TestAgentKeyAuth_MotToken_Invalid_401NoFallbackToAgentKey(t *testing.T) {
	oa := &fakeOAuthAuthenticator{err: errors.New("token revoked")}
	req, rec := motRequest(testMotToken)
	// A valid-looking agent key alongside must NOT rescue a bad mot_ token.
	req.Header.Set("X-Agent-Key", "agk_test-ws_abcdefghijklmnopqrstuvwxyz123456")

	var got captured
	require.NoError(t, AgentKeyAuth(agentSvcMustNotRun(t), oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, got.called)
	assert.Contains(t, rec.Body.String(), "Invalid access token")
	assert.NotContains(t, rec.Body.String(), "token revoked", "the authenticator's internal error must not be echoed")
}

func TestAgentKeyAuth_OAuthWired_NonMotBearerFallsThroughToAgentKey(t *testing.T) {
	oa := &fakeOAuthAuthenticator{err: errors.New("must not be called")}
	agentID := uuid.New()
	agentSvc := &mockAgentService{
		AuthenticateFunc: func(_ context.Context, _, _ string) (*domain.Agent, error) {
			return &domain.Agent{ID: agentID, WorkspaceID: uuid.New()}, nil
		},
	}
	req, rec := motRequest("eyJhbGciOiJIUzI1NiJ9.not-a-mot-token")
	req.Header.Set("X-Agent-Key", "agk_test-ws_abcdefghijklmnopqrstuvwxyz123456")

	var got captured
	require.NoError(t, AgentKeyAuth(agentSvc, oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, oa.calls, "a Bearer value without the mot_ prefix never reaches the OAuth authenticator")
	assert.Equal(t, agentID, got.agentID)
	assert.False(t, got.hasConn, "a trusted X-Agent-Key agent carries no connector user id")
}

func TestAgentKeyAuth_OAuthWired_NoBearerNoKey_401(t *testing.T) {
	oa := &fakeOAuthAuthenticator{}
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Basic Zm9vOmJhcg==")
	rec := httptest.NewRecorder()

	var got captured
	require.NoError(t, AgentKeyAuth(&mockAgentService{}, oa)(capturingHandler(&got))(newEchoContext(req, rec)))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, got.called)
	assert.Empty(t, oa.calls)
}

func TestAgentKeyAuth_OAuthUnwired_MotTokenIsNotAccepted(t *testing.T) {
	// nil authenticator = pre-OAuth behaviour: a mot_ token is just an
	// unrecognised header, and with no X-Agent-Key the request is refused.
	req, rec := motRequest(testMotToken)
	var got captured
	require.NoError(t, AgentKeyAuth(&mockAgentService{}, nil)(capturingHandler(&got))(newEchoContext(req, rec)))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, got.called)
	assert.Contains(t, rec.Body.String(), "Agent API key required")
}

// --- DualAuth ---------------------------------------------------------------

func TestDualAuth_MotToken_Valid_SetsConnectorContext(t *testing.T) {
	agent, userID := connectorAgent()
	oa := &fakeOAuthAuthenticator{agent: agent}
	req, rec := motRequest(testMotToken)

	var got captured
	require.NoError(t, DualAuth(newTestAuthService(), agentSvcMustNotRun(t), oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assertConnectorContext(t, got, agent, userID)
}

func TestDualAuth_MotToken_Invalid_401(t *testing.T) {
	oa := &fakeOAuthAuthenticator{err: errors.New("expired")}
	req, rec := motRequest(testMotToken)
	req.Header.Set("X-Agent-Key", "agk_test-ws_abcdefghijklmnopqrstuvwxyz123456")

	var got captured
	require.NoError(t, DualAuth(newTestAuthService(), agentSvcMustNotRun(t), oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, got.called)
	assert.Contains(t, rec.Body.String(), "Invalid access token")
}

func TestDualAuth_OAuthWired_JWTStillAuthenticatesUser(t *testing.T) {
	svc := newTestAuthService()
	jwt := registerAndGetToken(t, svc)
	oa := &fakeOAuthAuthenticator{err: errors.New("must not be called")}
	req, rec := motRequest(jwt)

	var got captured
	require.NoError(t, DualAuth(svc, &mockAgentService{}, oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, oa.calls, "a JWT is not routed to the OAuth authenticator")
	assert.Equal(t, AuthTypeUser, got.authType)
	assert.False(t, got.hasConn)
}

// --- OptionalAuth -----------------------------------------------------------

func TestOptionalAuth_MotToken_Valid_SetsConnectorContext(t *testing.T) {
	agent, userID := connectorAgent()
	oa := &fakeOAuthAuthenticator{agent: agent}
	req, rec := motRequest(testMotToken)

	var got captured
	require.NoError(t, OptionalAuth(newTestAuthService(), agentSvcMustNotRun(t), oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assertConnectorContext(t, got, agent, userID)
}

func TestOptionalAuth_MotToken_Invalid_PassesThroughUnauthenticated(t *testing.T) {
	oa := &fakeOAuthAuthenticator{err: errors.New("revoked")}
	req, rec := motRequest(testMotToken)

	var got captured
	require.NoError(t, OptionalAuth(newTestAuthService(), &mockAgentService{}, oa)(capturingHandler(&got))(newEchoContext(req, rec)))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, got.called, "OptionalAuth never rejects")
	assert.Nil(t, got.authType, "a bad mot_ token must leave the request unauthenticated, not half-authenticated")
	assert.Nil(t, got.agentID)
	assert.False(t, got.hasConn)
	assert.Len(t, oa.calls, 1)
}

// --- setAgentAuthContext / GetOAuthConnectorUserID ---------------------------

func TestSetAgentAuthContext_TrustedAgentHasNoConnectorUser(t *testing.T) {
	c := newEchoContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	setAgentAuthContext(c, &domain.Agent{ID: uuid.New(), WorkspaceID: uuid.New()})
	_, ok := GetOAuthConnectorUserID(c)
	assert.False(t, ok)
	assert.Nil(t, c.Get(ContextKeyOAuthConnectorUserID))
}

func TestGetOAuthConnectorUserID(t *testing.T) {
	c := newEchoContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())

	id, ok := GetOAuthConnectorUserID(c)
	assert.False(t, ok)
	assert.Equal(t, uuid.Nil, id)

	c.Set(ContextKeyOAuthConnectorUserID, "not-a-uuid-value")
	_, ok = GetOAuthConnectorUserID(c)
	assert.False(t, ok, "a wrongly-typed value is not a connector id")

	want := uuid.New()
	c.Set(ContextKeyOAuthConnectorUserID, want)
	id, ok = GetOAuthConnectorUserID(c)
	assert.True(t, ok)
	assert.Equal(t, want, id)
}

// --- RequireUserAuth --------------------------------------------------------

func TestRequireUserAuth(t *testing.T) {
	cases := []struct {
		name     string
		authType interface{}
		wantCode int
	}{
		{"user passes", AuthTypeUser, http.StatusOK},
		{"agent (incl. mot_ connector) rejected", AuthTypeAgent, http.StatusUnauthorized},
		{"unauthenticated rejected", nil, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := newEchoContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
			if tc.authType != nil {
				c.Set(ContextKeyAuthType, tc.authType)
			}
			called := false
			require.NoError(t, RequireUserAuth()(func(c echo.Context) error {
				called = true
				return c.NoContent(http.StatusOK)
			})(c))
			assert.Equal(t, tc.wantCode, rec.Code)
			assert.Equal(t, tc.wantCode == http.StatusOK, called)
			if tc.wantCode != http.StatusOK {
				assert.Contains(t, rec.Body.String(), "User authentication required")
			}
		})
	}
}

// --- RequirePermission clamp: the two failure branches not covered by
// rbac_oauth_connector_test.go ---------------------------------------------

func TestRBAC_OAuthConnector_MissingAuthWorkspace_403(t *testing.T) {
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()
	repo.addMember(wsID, userID, domain.RoleOwner)

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	c.Set(ContextKeyAgentAuthWorkspaceID, nil) // context lost the auth workspace
	require.NoError(t, RequirePermission(PermCreateTask, repo)(okHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, "no auth workspace ⇒ fail closed, even for an owner")
	assert.Contains(t, rec.Body.String(), "workspace context required")
}

func TestRBAC_OAuthConnector_UserNoLongerMember_403(t *testing.T) {
	// The consenting user was removed from the workspace after consent:
	// GetRole errors, and the connector must lose everything with them.
	repo := newRBACMockMemberRepo()
	wsID, agentID, userID := uuid.New(), uuid.New(), uuid.New()

	c, rec := newRBACOAuthConnectorContext(agentID, wsID, userID)
	require.NoError(t, RequirePermission(PermCreateTask, repo)(okHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
