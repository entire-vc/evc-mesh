package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// projectViewerFromContext decides whose project rights initiative progress is
// counted in. A member must get a membership-scoped viewer, never AllProjects:
// that is the whole of the PRD §6.3 fix.
func TestProjectViewerFromContext(t *testing.T) {
	newCtx := func() echo.Context {
		return echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	}
	user := uuid.New()

	for _, role := range []string{domain.RoleMember, domain.RoleViewer} {
		c := newCtx()
		c.Set(mw.ContextKeyUserID, user)
		c.Set(mw.ContextKeyWorkspaceRole, role)
		v := projectViewerFromContext(c)
		assert.False(t, v.AllProjects, role)
		if assert.NotNil(t, v.UserID, role) {
			assert.Equal(t, user, *v.UserID)
		}
	}

	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin} {
		c := newCtx()
		c.Set(mw.ContextKeyUserID, user)
		c.Set(mw.ContextKeyWorkspaceRole, role)
		assert.True(t, projectViewerFromContext(c).AllProjects, role)
	}

	t.Run("an agent never gets the owner/admin bypass", func(t *testing.T) {
		agent := uuid.New()
		c := newCtx()
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
		c.Set(mw.ContextKeyAgentID, agent)
		c.Set(mw.ContextKeyWorkspaceRole, domain.RoleAdmin)
		v := projectViewerFromContext(c)
		assert.False(t, v.AllProjects)
		if assert.NotNil(t, v.AgentID) {
			assert.Equal(t, agent, *v.AgentID)
		}
	})

	t.Run("a project-bound extension is held to its connection", func(t *testing.T) {
		conn := uuid.New()
		c := newCtx()
		c.Set(mw.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{ConnectionID: conn, ProjectBound: true})
		v := projectViewerFromContext(c)
		assert.False(t, v.AllProjects)
		if assert.NotNil(t, v.ExtensionConnectionID) {
			assert.Equal(t, conn, *v.ExtensionConnectionID)
		}
	})

	t.Run("a workspace-wide extension sees every project", func(t *testing.T) {
		c := newCtx()
		c.Set(mw.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{ConnectionID: uuid.New()})
		assert.Equal(t, domain.AllProjectsViewer(), projectViewerFromContext(c))
	})

	t.Run("an agent request without an agent id sees nothing", func(t *testing.T) {
		c := newCtx()
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
		c.Set(mw.ContextKeyUserID, user)
		c.Set(mw.ContextKeyWorkspaceRole, domain.RoleOwner)
		assert.Equal(t, domain.ProjectViewer{}, projectViewerFromContext(c))
	})

	t.Run("no identity sees nothing", func(t *testing.T) {
		assert.Equal(t, domain.ProjectViewer{}, projectViewerFromContext(newCtx()))
	})
}
