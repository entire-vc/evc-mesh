package middleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Idempotency-Key with a fenced result write over the
// production chain (Idempotency → comment handler → service → comments table).
// A repeat of a write that happened replays its answer even after the lock is
// gone; a write that did not happen is not performed under a lost lock.

type fenceIdemFixture struct {
	db    *sqlx.DB
	e     *echo.Echo
	tasks *postgres.TaskRepo
	task  uuid.UUID
	agent uuid.UUID
}

func newFenceIdemFixture(t *testing.T) *fenceIdemFixture {
	t.Helper()
	db := idemTestDB(t)
	ctx := context.Background()
	ws, proj, st, task, agent := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sfx := uuid.New().String()[:8]
	exec := func(q string, args ...any) {
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO workspaces (id, name, slug, owner_id, settings) VALUES ($1,'fence','fence-'||$2,$3,'{}')`, ws, sfx, uuid.New())
	exec(`INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings) VALUES ($1,$2,'fence','fence-'||$3,'none','{}')`, proj, ws, sfx)
	exec(`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition)
		VALUES ($1,$2,'Open','open','#00FF00',0,'todo',true,'{}')`, st, proj)
	exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'fence agent',$3,'fixture-hash','test')`, agent, ws, "fence-"+sfx)
	tasks := postgres.NewTaskRepo(db)
	require.NoError(t, tasks.Create(ctx, &domain.Task{
		ID: task, ProjectID: proj, StatusID: st, Title: "fence task",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CustomFields: json.RawMessage(`{}`), CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil))
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE path LIKE '%'||$1||'%'`, task.String())
		_, _ = db.ExecContext(ctx, `DELETE FROM comments WHERE task_id=$1`, task)
		_, _ = db.ExecContext(ctx, `DELETE FROM activity_log WHERE workspace_id=$1`, ws)
		_, _ = db.ExecContext(ctx, `DELETE FROM tasks WHERE id=$1`, task)
		_, _ = db.ExecContext(ctx, `DELETE FROM agents WHERE id=$1`, agent)
		_, _ = db.ExecContext(ctx, `DELETE FROM task_statuses WHERE project_id=$1`, proj)
		_, _ = db.ExecContext(ctx, `DELETE FROM projects WHERE id=$1`, proj)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=$1`, ws)
	})

	svc := service.NewCommentService(postgres.NewCommentRepo(db), tasks, postgres.NewActivityLogRepo(db))
	h := handler.NewCommentHandler(svc, nil)
	agentAuth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAgentID, agent)
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), agent, domain.ActorTypeAgent)))
			return next(c)
		}
	}
	e := echo.New()
	e.POST("/api/v1/tasks/:task_id/comments", h.Create, agentAuth, mw.Idempotency(postgres.NewIdempotencyRepo(db), 16<<20))
	return &fenceIdemFixture{db: db, e: e, tasks: tasks, task: task, agent: agent}
}

func (f *fenceIdemFixture) post(t *testing.T, key string, token *uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+f.task.String()+"/comments", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if key != "" {
		req.Header.Set(mw.HeaderIdempotencyKey, key)
	}
	if token != nil {
		req.Header.Set(handler.HeaderCheckoutToken, token.String())
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

func (f *fenceIdemFixture) comments(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM comments WHERE task_id=$1`, f.task))
	return n
}

func TestIdempotency_FencedWriteAfterLostLock_DB(t *testing.T) {
	f := newFenceIdemFixture(t)
	ctx := context.Background()
	token := uuid.New()
	lease, err := f.tasks.AcquireCheckout(ctx, f.task, f.agent, token, time.Now().Add(30*time.Minute), domain.CheckoutScope{})
	require.NoError(t, err)
	require.Equal(t, token, *lease.Token)

	// The write happens under the live lock.
	body := `{"body":"результат: go test ok"}`
	first := f.post(t, "fence-1", &token, body)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	require.Equal(t, 1, f.comments(t))

	// The lock is lost (released; a takeover or force-release ends it the same way).
	_, err = f.tasks.CompareReleaseCheckout(ctx, f.task, domain.CheckoutExpectation{Token: &token})
	require.NoError(t, err)

	// A repeat of the write that happened replays its stored answer.
	again := f.post(t, "fence-1", &token, body)
	require.Equal(t, http.StatusCreated, again.Code, again.Body.String())
	assert.Equal(t, "true", again.Header().Get(mw.HeaderIdempotentReplayed))
	assert.JSONEq(t, first.Body.String(), again.Body.String())
	assert.Equal(t, 1, f.comments(t), "the replay writes nothing")

	// A write that did not happen is not performed under the lost lock ...
	lost := f.post(t, "fence-2", &token, `{"body":"поздний результат"}`)
	require.Equal(t, http.StatusConflict, lost.Code, lost.Body.String())
	assert.Contains(t, lost.Body.String(), domain.CodeCheckoutPreconditionFailed)
	assert.Equal(t, 1, f.comments(t))
	// ... nor on its retry with the same key.
	retry := f.post(t, "fence-2", &token, `{"body":"поздний результат"}`)
	require.Equal(t, http.StatusConflict, retry.Code, retry.Body.String())
	assert.Contains(t, retry.Body.String(), domain.CodeCheckoutPreconditionFailed)
	assert.Equal(t, 1, f.comments(t), "a write that never happened is refused, not performed")

	// Positive control: the same body without the precondition is written, so
	// the 409 above is the fence's doing.
	require.Equal(t, http.StatusCreated, f.post(t, "fence-3", nil, `{"body":"поздний результат"}`).Code)
	assert.Equal(t, 2, f.comments(t))
}
