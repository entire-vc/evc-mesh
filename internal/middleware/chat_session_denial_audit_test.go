package middleware

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
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type captureActivityRepo struct{ rows []*domain.ActivityLog }

func (r *captureActivityRepo) Create(_ context.Context, e *domain.ActivityLog) error {
	r.rows = append(r.rows, e)
	return nil
}
func (r *captureActivityRepo) List(context.Context, uuid.UUID, repository.ActivityLogFilter, pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	return nil, nil
}
func (r *captureActivityRepo) ListByTask(context.Context, uuid.UUID, pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	return nil, nil
}
func (r *captureActivityRepo) Export(context.Context, uuid.UUID, repository.ActivityLogFilter, int) ([]domain.ActivityLog, error) {
	return nil, nil
}

func runDenialAudit(t *testing.T, chat bool, handler echo.HandlerFunc) (*captureActivityRepo, *domain.ChatSessionToken) {
	t.Helper()
	repo := &captureActivityRepo{}
	tok := &domain.ChatSessionToken{UserID: uuid.New(), AgentID: uuid.New(), WorkspaceID: uuid.New(),
		ChatThreadID: uuid.New(), ChatSessionID: uuid.New()}
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/api/v1/tasks/x/move", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id/move")
	if chat {
		c.Set(ContextKeyChatSession, &service.ChatSessionPrincipal{Token: tok})
	}
	_ = ChatSessionDenialAudit(repo)(handler)(c)
	return repo, tok
}

func TestChatSessionDenialAudit(t *testing.T) {
	deny := func(c echo.Context) error { return c.JSON(http.StatusForbidden, apierror.Forbidden("no")) }
	denyErr := func(echo.Context) error { return apierror.Forbidden("no") }
	ok := func(c echo.Context) error { return c.NoContent(http.StatusOK) }

	for name, h := range map[string]echo.HandlerFunc{"written": deny, "returned": denyErr} {
		repo, tok := runDenialAudit(t, true, h)
		require.Len(t, repo.rows, 1, name)
		row := repo.rows[0]
		assert.Equal(t, "chat_session.denied", row.Action)
		assert.Equal(t, tok.AgentID, row.ActorID)
		assert.Equal(t, tok.ChatSessionID, row.EntityID)
		assert.Contains(t, string(row.Changes), tok.UserID.String())
		assert.Contains(t, string(row.Changes), "/api/v1/tasks/:task_id/move")
	}

	repo, _ := runDenialAudit(t, true, ok)
	assert.Empty(t, repo.rows, "an allowed call is not a denial")
	repo, _ = runDenialAudit(t, false, deny)
	assert.Empty(t, repo.rows, "only chat-session requests are audited here")
}
