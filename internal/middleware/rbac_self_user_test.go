package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Tests: RequireSelfUserOrPermission — PUT /workspaces/:ws_id/members/:user_id/profile.

func selfUserContext(authType string, callerID, wsID uuid.UUID, target string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPut, "/", http.NoBody), rec)
	c.SetParamNames("user_id")
	c.SetParamValues(target)
	c.Set(ContextKeyAuthType, authType)
	if authType == AuthTypeAgent {
		c.Set(ContextKeyAgentID, callerID)
	} else {
		c.Set(ContextKeyUserID, callerID)
	}
	c.Set(ContextKeyWorkspaceID, wsID)
	return c, rec
}

func TestRequireSelfUserOrPermission(t *testing.T) {
	wsID, me, other, admin := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := newRBACMockMemberRepo()
	repo.addMember(wsID, me, domain.RoleMember)
	repo.addMember(wsID, other, domain.RoleMember)
	repo.addMember(wsID, admin, domain.RoleAdmin)

	cases := []struct {
		name     string
		authType string
		caller   uuid.UUID
		target   string
		want     int
	}{
		{"own profile, no permission needed", AuthTypeUser, me, me.String(), http.StatusOK},
		{"member editing someone else", AuthTypeUser, me, other.String(), http.StatusForbidden},
		{"admin editing someone else", AuthTypeUser, admin, other.String(), http.StatusOK},
		{"malformed id falls to the permission check", AuthTypeUser, me, "not-a-uuid", http.StatusForbidden},
		{"agent whose id equals the path is not self", AuthTypeAgent, me, me.String(), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := selfUserContext(tc.authType, tc.caller, wsID, tc.target)
			h := RequireSelfUserOrPermission("user_id", PermManageMembers, repo)(okHandler)
			require.NoError(t, h(c))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
