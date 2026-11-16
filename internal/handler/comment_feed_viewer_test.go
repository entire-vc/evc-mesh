package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Both comment feeds must tell the repository who is reading, so rows of
// projects the reader cannot open never leave the database. The zero viewer
// would serve nothing; a missing viewer here is the leak this guards.

func recentViewer(t *testing.T, setup func(c echo.Context)) domain.ProjectViewer {
	t.Helper()
	var got domain.ProjectViewer
	h, e := setupCommentTest(&MockCommentService{
		ListRecentByWorkspaceFunc: func(_ context.Context, _ uuid.UUID, f repository.CommentViewFilter) (*domain.CommentViewPage, error) {
			got = f.Viewer
			return &domain.CommentViewPage{}, nil
		},
	})
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.NewString())
	setup(c)
	require.NoError(t, h.GetRecentByWorkspace(c))
	require.Equal(t, http.StatusOK, rec.Code)
	return got
}

func TestGetRecentByWorkspace_PassesTheRequestViewer(t *testing.T) {
	userID := uuid.New()

	t.Run("member: only their memberships", func(t *testing.T) {
		v := recentViewer(t, func(c echo.Context) {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
			c.Set(mw.ContextKeyUserID, userID)
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleMember)
		})
		require.NotNil(t, v.UserID)
		assert.Equal(t, userID, *v.UserID)
		assert.False(t, v.AllProjects)
	})

	t.Run("admin: every project", func(t *testing.T) {
		v := recentViewer(t, func(c echo.Context) {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
			c.Set(mw.ContextKeyUserID, userID)
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleAdmin)
		})
		assert.True(t, v.AllProjects)
	})

	t.Run("agent with an admin grant: memberships only", func(t *testing.T) {
		agentID := uuid.New()
		v := recentViewer(t, func(c echo.Context) {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			c.Set(mw.ContextKeyAgentID, agentID)
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleAdmin)
		})
		require.NotNil(t, v.AgentID)
		assert.Equal(t, agentID, *v.AgentID)
		assert.False(t, v.AllProjects)
	})
}

func myCommentsViewer(t *testing.T, actorID uuid.UUID, kind domain.ActorType, setup func(c echo.Context)) domain.ProjectViewer {
	t.Helper()
	var got domain.ProjectViewer
	h, e := setupCommentTest(&MockCommentService{
		ListByAuthorFunc: func(_ context.Context, _ uuid.UUID, f repository.CommentViewFilter) (*domain.CommentViewPage, error) {
			got = f.Viewer
			return &domain.CommentViewPage{}, nil
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/comments", http.NoBody)
	req = req.WithContext(actorctx.WithActor(req.Context(), actorID, kind))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setup(c)
	require.NoError(t, h.GetMyComments(c))
	require.Equal(t, http.StatusOK, rec.Code)
	return got
}

func TestGetMyComments_PassesTheAuthorAsViewer(t *testing.T) {
	t.Run("a human is checked per row, never widened by a request role", func(t *testing.T) {
		userID := uuid.New()
		v := myCommentsViewer(t, userID, domain.ActorTypeUser, func(c echo.Context) {
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleOwner)
		})
		require.NotNil(t, v.UserID)
		assert.Equal(t, userID, *v.UserID)
		assert.False(t, v.AllProjects)
	})

	t.Run("an agent: its memberships", func(t *testing.T) {
		agentID := uuid.New()
		v := myCommentsViewer(t, agentID, domain.ActorTypeAgent, func(c echo.Context) {
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleAdmin)
		})
		require.NotNil(t, v.AgentID)
		assert.Equal(t, agentID, *v.AgentID)
		assert.False(t, v.AllProjects)
		assert.Nil(t, v.UserID)
	})
}

type viewerCapturingMentionService struct {
	service.MentionService
	listViewer, countViewer domain.ProjectViewer
}

func (m *viewerCapturingMentionService) List(_ context.Context, _ uuid.UUID, _ string, f repository.MentionFilter) ([]domain.CommentMentionView, error) {
	m.listViewer = f.Viewer
	return []domain.CommentMentionView{}, nil
}

func (m *viewerCapturingMentionService) CountUnseen(_ context.Context, _ uuid.UUID, _ string, _ uuid.UUID, v domain.ProjectViewer) (int64, error) {
	m.countViewer = v
	return 0, nil
}

func TestMentions_PassTheAddresseeAsViewer(t *testing.T) {
	userID := uuid.New()
	svc := &viewerCapturingMentionService{}
	h := NewMentionHandler(svc)
	e := echo.New()
	for _, path := range []string{"/api/v1/me/mentions", "/api/v1/me/mentions/unseen_count"} {
		req := httptest.NewRequest(http.MethodGet, path+"?workspace_id="+uuid.NewString(), http.NoBody)
		req = req.WithContext(actorctx.WithActor(req.Context(), userID, domain.ActorTypeUser))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(mw.ContextKeyWorkspaceRole, domain.RoleOwner)
		if path == "/api/v1/me/mentions" {
			require.NoError(t, h.List(c))
		} else {
			require.NoError(t, h.UnseenCount(c))
		}
		require.Equal(t, http.StatusOK, rec.Code, path)
	}
	for name, v := range map[string]domain.ProjectViewer{"list": svc.listViewer, "count": svc.countViewer} {
		require.NotNil(t, v.UserID, name)
		assert.Equal(t, userID, *v.UserID, name)
		assert.False(t, v.AllProjects, name+": a request role must not widen a /me list")
	}
}
