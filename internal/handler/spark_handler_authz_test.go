package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installCtx builds a POST .../install request for a Spark catalog id, targeting
// the workspace named in the body.
func installCtx(wsID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	body := fmt.Sprintf(`{"workspace_id":%q}`, wsID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/spark/agents/cat-1/install", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/spark/agents/:agent_id/install")
	c.SetParamNames("agent_id")
	c.SetParamValues("cat-1")
	return c, rec
}

// TestSparkInstall_RequiresRegisterAgentPermission pins the check the Spark routes
// have to make for themselves. They are the only routes exempt from
// RequireWorkspaceMemberScoped — :agent_id there is a catalog id in an external
// marketplace, not an agents.id — and Install takes its target workspace from the
// request body, where no middleware can see it. Without this check any
// authenticated stranger could register an agent into someone else's workspace
// and be handed its API key in the 201.
//
// A nil sparkClient is safe here: every case must be refused before the catalog
// is contacted, so reaching it would panic and fail the test loudly.
func TestSparkInstall_RequiresRegisterAgentPermission(t *testing.T) {
	victimWS := uuid.New()
	owner := uuid.New()
	plainMember := uuid.New()
	stranger := uuid.New()

	repo := &mockWorkspaceMemberRepo{members: map[string]string{
		fmt.Sprintf("%s/%s", victimWS, owner):       "owner",
		fmt.Sprintf("%s/%s", victimWS, plainMember): "member",
	}}
	h := NewSparkHandler(nil, nil, repo)

	tests := []struct {
		name    string
		auth    func(echo.Context)
		wantMsg string
	}{
		{
			name:    "unrelated user",
			auth:    func(c echo.Context) { asUser(c, stranger) },
			wantMsg: "not a workspace member",
		},
		{
			name: "member without the register-agent permission",
			auth: func(c echo.Context) { asUser(c, plainMember) },
			// A member of the workspace, but registering agents is owner/admin.
			wantMsg: "insufficient permissions",
		},
		{
			name:    "agent key",
			auth:    func(c echo.Context) { asAgent(c, uuid.New(), victimWS) },
			wantMsg: "agents cannot perform this action",
		},
		{
			name:    "no identity at all",
			auth:    func(echo.Context) {},
			wantMsg: "user context required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := installCtx(victimWS)
			tc.auth(c)

			require.NoError(t, h.Install(c))
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.wantMsg)
			assert.NotContains(t, rec.Body.String(), "agk_", "no agent key may be handed out")
		})
	}
}

// TestSparkInstall_MisconfiguredRepoFailsClosed: a handler wired without a member
// repository must refuse, not install. Wiring mistakes should cost the Spark
// integration, never another tenant's workspace.
func TestSparkInstall_MisconfiguredRepoFailsClosed(t *testing.T) {
	h := NewSparkHandler(nil, nil, nil)
	c, rec := installCtx(uuid.New())
	asUser(c, uuid.New())

	require.NoError(t, h.Install(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "workspace access denied")
}

// TestSparkInstall_RejectsMissingOrMalformedWorkspace keeps the membership check
// from being reachable with a workspace id the caller never really named.
func TestSparkInstall_RejectsMissingOrMalformedWorkspace(t *testing.T) {
	h := NewSparkHandler(nil, nil, &mockWorkspaceMemberRepo{})
	e := echo.New()

	for name, body := range map[string]string{
		"missing":   `{}`,
		"malformed": `{"workspace_id":"not-a-uuid"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("agent_id")
			c.SetParamValues("cat-1")
			asUser(c, uuid.New())

			require.NoError(t, h.Install(c))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}
