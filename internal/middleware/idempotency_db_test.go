package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// DB-backed, no build tag (same convention as the other *_db_test.go files):
// skipped when Postgres is not reachable. The chain is the production one for
// POST /tasks/:task_id/comments — Idempotency over the real idempotency_keys
// table in front of the real comment handler, service and comments table — so
// "one record" is counted in the table the client cares about.

func idemTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type idemFixture struct {
	db     *sqlx.DB
	e      *echo.Echo
	repo   *postgres.IdempotencyRepo
	taskID uuid.UUID
}

// newIdemFixture seeds a workspace, project, status and task and mounts the
// comment route. The test's stand-in for authentication puts the X-Test-User
// header's user on the request, as JWTAuth does for a real token.
func newIdemFixture(t *testing.T) *idemFixture {
	t.Helper()
	db := idemTestDB(t)
	ctx := context.Background()
	ws, proj, st, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sfx := uuid.New().String()[:8]
	exec := func(q string, args ...any) {
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO workspaces (id, name, slug, owner_id, settings) VALUES ($1,'idem','idem-'||$2,$3,'{}')`, ws, sfx, uuid.New())
	exec(`INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings) VALUES ($1,$2,'idem','idem-'||$3,'none','{}')`, proj, ws, sfx)
	exec(`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition)
		VALUES ($1,$2,'Open','open','#00FF00',0,'todo',true,'{}')`, st, proj)
	// TaskRepo.Create, not a raw INSERT: it assigns task_number.
	require.NoError(t, postgres.NewTaskRepo(db).Create(ctx, &domain.Task{
		ID: task, ProjectID: proj, StatusID: st, Title: "idem task",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CustomFields: json.RawMessage(`{}`), CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil))
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE path LIKE '%'||$1||'%'`, task.String())
		_, _ = db.ExecContext(ctx, `DELETE FROM comments WHERE task_id=$1`, task)
		_, _ = db.ExecContext(ctx, `DELETE FROM tasks WHERE id=$1`, task)
		_, _ = db.ExecContext(ctx, `DELETE FROM task_statuses WHERE project_id=$1`, proj)
		_, _ = db.ExecContext(ctx, `DELETE FROM projects WHERE id=$1`, proj)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=$1`, ws)
	})

	repo := postgres.NewIdempotencyRepo(db)
	svc := service.NewCommentService(postgres.NewCommentRepo(db), postgres.NewTaskRepo(db), postgres.NewActivityLogRepo(db))
	h := handler.NewCommentHandler(svc, nil)
	testAuth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			uid := uuid.MustParse(c.Request().Header.Get("X-Test-User"))
			c.Set(mw.ContextKeyUserID, uid)
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), uid, domain.ActorTypeUser)))
			return next(c)
		}
	}
	e := echo.New()
	e.POST("/api/v1/tasks/:task_id/comments", h.Create, testAuth, mw.Idempotency(repo, 16<<20))
	return &idemFixture{db: db, e: e, repo: repo, taskID: task}
}

func (f *idemFixture) post(t *testing.T, user uuid.UUID, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+f.taskID.String()+"/comments", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Test-User", user.String())
	if key != "" {
		req.Header.Set(mw.HeaderIdempotencyKey, key)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

func (f *idemFixture) comments(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM comments WHERE task_id=$1`, f.taskID))
	return n
}

func commentID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var c struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c), rec.Body.String())
	return c.ID
}

// The client sent the comment, the answer was lost, the client retried with
// the same key: exactly one comment exists, and the retry gets the first
// answer (same id) marked Idempotent-Replayed.
func TestIdempotency_LostResponseRetryWritesOnce_DB(t *testing.T) {
	f := newIdemFixture(t)
	user := uuid.New()
	body := `{"body":"готово, проверено"}`

	first := f.post(t, user, "retry-1", body)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	assert.Empty(t, first.Header().Get(mw.HeaderIdempotentReplayed))

	again := f.post(t, user, "retry-1", body)
	require.Equal(t, http.StatusCreated, again.Code, again.Body.String())
	assert.Equal(t, "true", again.Header().Get(mw.HeaderIdempotentReplayed))
	assert.Equal(t, commentID(t, first), commentID(t, again))
	assert.JSONEq(t, first.Body.String(), again.Body.String())
	assert.Equal(t, 1, f.comments(t), "a retry with the same key writes nothing")

	// Positive control: the same body twice without a key is two comments, so
	// the count above is the key's doing, not a dedup somewhere else.
	require.Equal(t, http.StatusCreated, f.post(t, user, "", body).Code)
	require.Equal(t, http.StatusCreated, f.post(t, user, "", body).Code)
	assert.Equal(t, 3, f.comments(t))
}

// The same key with another body is a client bug, not a retry: 409, and no
// comment is written.
func TestIdempotency_SameKeyOtherBodyIsConflict_DB(t *testing.T) {
	f := newIdemFixture(t)
	user := uuid.New()
	require.Equal(t, http.StatusCreated, f.post(t, user, "k-body", `{"body":"one"}`).Code)

	rec := f.post(t, user, "k-body", `{"body":"two"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), mw.CodeIdempotencyKeyReused)
	assert.Equal(t, 1, f.comments(t))
}

// A key belongs to its actor: another user's request with the same key and
// body is a separate write.
func TestIdempotency_KeyIsScopedToActor_DB(t *testing.T) {
	f := newIdemFixture(t)
	body := `{"body":"same text"}`
	a := f.post(t, uuid.New(), "shared-key", body)
	b := f.post(t, uuid.New(), "shared-key", body)
	require.Equal(t, http.StatusCreated, a.Code)
	require.Equal(t, http.StatusCreated, b.Code)
	assert.Empty(t, b.Header().Get(mw.HeaderIdempotentReplayed))
	assert.NotEqual(t, commentID(t, a), commentID(t, b))
	assert.Equal(t, 2, f.comments(t))
}

// A retry that arrives while the first request still runs is refused with
// Retry-After rather than run twice; once the claim is stale (its request died
// with the process), the retry takes the key over and runs.
func TestIdempotency_InFlightAndStaleClaim_DB(t *testing.T) {
	f := newIdemFixture(t)
	user := uuid.New()
	body := `{"body":"in flight"}`

	// Claim the key the way the first request would, using that request's
	// fingerprint: answer a first real request, then turn its row back into a
	// live claim.
	require.Equal(t, http.StatusCreated, f.post(t, user, "k-flight", body).Code)
	_, err := f.db.Exec(`UPDATE idempotency_keys SET state='in_flight', response_status=NULL, response_type=NULL, response_body=NULL
		WHERE idem_key='k-flight' AND actor_id=$1`, user)
	require.NoError(t, err)

	rec := f.post(t, user, "k-flight", body)
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), mw.CodeIdempotencyInProgress)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.Equal(t, 1, f.comments(t))

	_, err = f.db.Exec(`UPDATE idempotency_keys SET claimed_at = now() - interval '10 minutes' WHERE idem_key='k-flight' AND actor_id=$1`, user)
	require.NoError(t, err)
	rec = f.post(t, user, "k-flight", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Header().Get(mw.HeaderIdempotentReplayed))
	assert.Equal(t, 2, f.comments(t))
}

// After the TTL the key is forgotten and may be used again.
func TestIdempotency_ExpiredKeyRunsAgain_DB(t *testing.T) {
	f := newIdemFixture(t)
	user := uuid.New()
	require.Equal(t, http.StatusCreated, f.post(t, user, "k-ttl", `{"body":"x"}`).Code)
	var ttl time.Duration
	require.NoError(t, f.db.QueryRow(`SELECT EXTRACT(EPOCH FROM expires_at - claimed_at)::bigint * 1000000000
		FROM idempotency_keys WHERE idem_key='k-ttl' AND actor_id=$1`, user).Scan(&ttl))
	assert.Equal(t, 24*time.Hour, ttl)

	_, err := f.db.Exec(`UPDATE idempotency_keys SET expires_at = now() - interval '1 second' WHERE idem_key='k-ttl' AND actor_id=$1`, user)
	require.NoError(t, err)
	rec := f.post(t, user, "k-ttl", `{"body":"x"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Empty(t, rec.Header().Get(mw.HeaderIdempotentReplayed))
	assert.Equal(t, 2, f.comments(t))
}

// A server error is not an answer to keep: the key is released and the retry
// runs. Counted on a route whose handler fails once, then succeeds.
func TestIdempotency_ServerErrorReleasesKey_DB(t *testing.T) {
	db := idemTestDB(t)
	repo := postgres.NewIdempotencyRepo(db)
	user := uuid.New()
	path := "/idem-5xx/" + uuid.New().String()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM idempotency_keys WHERE path=$1`, path) })
	calls := 0
	h := func(c echo.Context) error {
		calls++
		if calls == 1 {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "boom"})
		}
		return c.JSON(http.StatusCreated, map[string]int{"call": calls})
	}
	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), user, domain.ActorTypeAgent)))
			return next(c)
		}
	}
	e.POST(path, h, auth, mw.Idempotency(repo, 0))
	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(mw.HeaderIdempotencyKey, "k-5xx")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	assert.Equal(t, http.StatusInternalServerError, do().Code)
	second := do()
	assert.Equal(t, http.StatusCreated, second.Code)
	third := do()
	assert.Equal(t, http.StatusCreated, third.Code)
	assert.Equal(t, "true", third.Header().Get(mw.HeaderIdempotentReplayed))
	assert.Equal(t, 2, calls, "the 500 was retried once, the success was replayed")
}

// A handler that panics answers 500 through Recover; the key is released on
// the way out, so the retry runs at once instead of waiting out the stale
// window behind idempotency_request_in_progress.
func TestIdempotency_PanicReleasesKey_DB(t *testing.T) {
	db := idemTestDB(t)
	repo := postgres.NewIdempotencyRepo(db)
	user := uuid.New()
	path := "/idem-panic/" + uuid.New().String()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM idempotency_keys WHERE path=$1`, path) })
	calls := 0
	h := func(c echo.Context) error {
		calls++
		if calls == 1 {
			panic("handler bug")
		}
		return c.JSON(http.StatusCreated, map[string]int{"call": calls})
	}
	e := echo.New()
	e.Use(echomw.Recover())
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), user, domain.ActorTypeAgent)))
			return next(c)
		}
	}
	e.POST(path, h, auth, mw.Idempotency(repo, 0))
	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(mw.HeaderIdempotencyKey, "k-panic")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	assert.Equal(t, http.StatusInternalServerError, do().Code)
	retry := do()
	assert.Equal(t, http.StatusCreated, retry.Code, retry.Body.String())
	assert.Equal(t, 2, calls)
}

// A handler that returns an error instead of writing one: a 4xx is the answer
// to this request and is replayed, the handler does not run again; a 5xx
// error frees the key.
func TestIdempotency_ReturnedErrorsReplayOrRelease_DB(t *testing.T) {
	db := idemTestDB(t)
	repo := postgres.NewIdempotencyRepo(db)
	user := uuid.New()
	base := "/idem-err/" + uuid.New().String()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM idempotency_keys WHERE path LIKE $1||'%'`, base) })
	calls := map[string]int{}
	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), user, domain.ActorTypeAgent)))
			return next(c)
		}
	}
	e.POST(base+"/4xx", func(c echo.Context) error {
		calls["4xx"]++
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "bad input")
	}, auth, mw.Idempotency(repo, 0))
	e.POST(base+"/5xx", func(c echo.Context) error {
		calls["5xx"]++
		return errors.New("db down")
	}, auth, mw.Idempotency(repo, 0))
	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, base+path, strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(mw.HeaderIdempotencyKey, "k-err")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	first := do("/4xx")
	require.Equal(t, http.StatusUnprocessableEntity, first.Code)
	again := do("/4xx")
	assert.Equal(t, http.StatusUnprocessableEntity, again.Code)
	assert.Equal(t, "true", again.Header().Get(mw.HeaderIdempotentReplayed))
	assert.JSONEq(t, first.Body.String(), again.Body.String())
	assert.Equal(t, 1, calls["4xx"], "a returned 4xx is replayed, not re-run")

	assert.Equal(t, http.StatusInternalServerError, do("/5xx").Code)
	retry := do("/5xx")
	assert.Equal(t, http.StatusInternalServerError, retry.Code)
	assert.Empty(t, retry.Header().Get(mw.HeaderIdempotentReplayed))
	assert.Equal(t, 2, calls["5xx"], "a returned 5xx frees the key")
}
