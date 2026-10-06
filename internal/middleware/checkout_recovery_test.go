package middleware

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestCheckoutRecoveryPermission(t *testing.T) {
	for _, tc := range []struct {
		name, role   string
		agent, force bool
		status       int
	}{
		{"agent-force", domain.RoleAdmin, true, true, http.StatusForbidden},
		{"agent-owner-cleanup", "", true, false, http.StatusOK},
		{"member-force", domain.RoleMember, false, true, http.StatusForbidden},
		{"viewer-force", domain.RoleViewer, false, true, http.StatusForbidden},
		{"admin-recovery", domain.RoleAdmin, false, true, http.StatusOK},
		{"owner-recovery", domain.RoleOwner, false, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller, workspace := uuid.New(), uuid.New()
			members := newRBACMockMemberRepo()
			members.addMember(workspace, caller, tc.role)
			c, rec := newRBACEchoContext(caller, workspace)
			if tc.agent {
				c, rec = newRBACAgentContext(caller, workspace)
			}
			if tc.force {
				c.Request().URL.RawQuery = "force=true"
			}
			reached := false
			require.NoError(t, RequireCheckoutRecoveryPermission(members)(func(c echo.Context) error {
				reached = true
				return c.NoContent(http.StatusOK)
			})(c))
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.status == http.StatusOK, reached)
		})
	}
}
