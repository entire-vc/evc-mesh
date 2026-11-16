package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// finish_task refused by «Agent must comment before done», the
// assignee comments, then repeats finish_task with the same arguments. The MCP
// derives the Idempotency-Key from the arguments, so the repeat carries the key
// of the refused call. The repeat must be checked against the comment that now
// exists (200), not get the stored 422 back.
func TestFinishTaskHTTP_RetryAfterCommentPassesRequireCommentRule_DB(t *testing.T) {
	f := finishSourceSetup(t)
	ctx := context.Background()

	var wsID uuid.UUID
	require.NoError(t, f.db.Get(&wsID, `SELECT workspace_id FROM projects WHERE id=$1`, f.project))
	rules := postgres.NewRuleRepo(f.db)
	require.NoError(t, rules.Create(ctx, &domain.Rule{
		ID: uuid.New(), WorkspaceID: wsID, Scope: domain.RuleScopeWorkspace,
		RuleType: "transition_gate.require_comment", Name: "Agent must comment before done",
		Config:              json.RawMessage(`{"min_comment_length": 20, "target_status_categories": ["done"]}`),
		AppliesToActorTypes: pq.StringArray{"agent"}, AppliesToRoles: pq.StringArray{},
		Enforcement: domain.RuleEnforcementBlock, IsEnabled: true,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	}))
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM rules WHERE workspace_id=$1`, wsID) })

	comments := postgres.NewCommentRepo(f.db)
	ruleSvc := service.NewRuleService(rules, postgres.NewActivityLogRepo(f.db), service.WithRuleCommentRepo(comments))
	statuses := postgres.NewTaskStatusRepo(f.db)
	svc := service.NewTaskService(postgres.NewTaskRepo(f.db), statuses, nil, postgres.NewActivityLogRepo(f.db),
		service.WithProjectRepo(postgres.NewProjectRepo(f.db)), service.WithRuleService(ruleSvc))

	id := f.task(t)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM idempotency_keys WHERE path LIKE '%'||$1||'%'`, id.String()) })
	actor := actorctx.WithActor(ctx, f.agent, domain.ActorTypeAgent)
	session, request := uuid.New(), uuid.New()
	_, err := svc.CheckoutTask(actor, id, 30, nil, domain.CheckoutScope{SessionID: &session, RequestID: &request})
	require.NoError(t, err)

	h := NewTypedOutcomeCommandHandler(svc.(service.TypedOutcomeCommands), svc)
	agentAuth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), f.agent, domain.ActorTypeAgent)))
			mw.SetProjectAccess(c, openAccess)
			return next(c)
		}
	}
	e := echo.New()
	e.POST("/api/v1/tasks/:task_id/finish", h.FinishTask, agentAuth, mw.Idempotency(postgres.NewIdempotencyRepo(f.db), 16<<20))
	body := `{"result_kind":"defect","evidence":"fixture finish behind the comment rule","to":"done"}`
	finish := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id.String()+"/finish", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(mw.HeaderIdempotencyKey, key)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	// Positive control: without the comment the rule really refuses.
	refused := finish("mcp-derived-key")
	require.Equal(t, http.StatusUnprocessableEntity, refused.Code, refused.Body.String())
	require.Contains(t, refused.Body.String(), "rule_violation")

	require.NoError(t, comments.Create(ctx, &domain.Comment{
		ID: uuid.New(), TaskID: id, AuthorID: f.agent, AuthorType: domain.ActorTypeAgent,
		Body: "Разбор сделан, действий не требуется, ссылки в закрывающем комментарии.", Metadata: json.RawMessage(`{}`),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))

	retry := finish("mcp-derived-key")
	assert.Empty(t, retry.Header().Get(mw.HeaderIdempotentReplayed), "the refusal must not be replayed")
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())

	var category string
	require.NoError(t, f.db.Get(&category, `SELECT ts.category FROM tasks t JOIN task_statuses ts ON ts.id=t.status_id WHERE t.id=$1`, id))
	assert.Equal(t, string(domain.StatusCategoryDone), category)

	// The successful close is now the stored answer: a further repeat replays
	// it and writes nothing.
	again := finish("mcp-derived-key")
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	assert.Equal(t, "true", again.Header().Get(mw.HeaderIdempotentReplayed))
}
