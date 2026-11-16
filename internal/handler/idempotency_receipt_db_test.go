package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// A keyed write that happened is never repeated. The key is
// recorded with the written row in the write's own transaction, so a repeat
// whose write exists gets that write back whatever happened to the
// middleware's stored answer: a failed Complete, a 5xx after the commit, the
// 24 hours past, a claim taken over. Each case runs on the four write routes
// over the production chain (Idempotency → handler → service → repository).

// receiptRoute is one write route under test.
type receiptRoute struct {
	name  string
	body  string // a request body
	other string // a different body for the same route and task
	// serve posts one request through Idempotency(store) and wrap (between
	// the middleware and the handler).
	serve func(t *testing.T, store mw.IdempotencyStore, wrap echo.MiddlewareFunc, key, body string) *httptest.ResponseRecorder
	// written counts the writes the route made (rows, or reports added).
	written func(t *testing.T) int
	// idOf is the written object's id in an answer.
	idOf func(t *testing.T, rec *httptest.ResponseRecorder) string
	// ok is the route's success status.
	ok int
}

type receiptFixture struct {
	*finishSourceFixture
	ws   uuid.UUID
	task uuid.UUID
}

func newReceiptFixture(t *testing.T) *receiptFixture {
	t.Helper()
	f := finishSourceSetup(t)
	rf := &receiptFixture{finishSourceFixture: f}
	require.NoError(t, f.db.Get(&rf.ws, `SELECT workspace_id FROM projects WHERE id=$1`, f.project))
	rf.task = f.task(t)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM idempotency_keys WHERE actor_id=$1`, f.agent)
		_, _ = f.db.Exec(`DELETE FROM idempotency_receipts WHERE actor_id=$1`, f.agent)
		_, _ = f.db.Exec(`DELETE FROM agent_sessions WHERE agent_id=$1`, f.agent)
		_, _ = f.db.Exec(`DELETE FROM artifact_reservations WHERE task_id=$1`, rf.task)
	})
	return rf
}

func (f *receiptFixture) echoFor(store mw.IdempotencyStore, wrap echo.MiddlewareFunc, path string, h echo.HandlerFunc) *echo.Echo {
	if wrap == nil {
		wrap = func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAgentID, f.agent)
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), f.agent, domain.ActorTypeAgent)))
			mw.SetProjectAccess(c, openAccess)
			return next(c)
		}
	}
	e := echo.New()
	e.POST(path, h, auth, mw.Idempotency(store, 16<<20), wrap)
	return e
}

func postJSON(e *echo.Echo, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if key != "" {
		req.Header.Set(mw.HeaderIdempotencyKey, key)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func jsonField(t *testing.T, rec *httptest.ResponseRecorder, path ...string) string {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	for _, p := range path {
		m, ok := v.(map[string]any)
		require.True(t, ok, "no %q in %s", p, rec.Body.String())
		v = m[p]
	}
	s, ok := v.(string)
	require.True(t, ok, "%v is not a string in %s", path, rec.Body.String())
	return s
}

func (f *receiptFixture) commentRoute() receiptRoute {
	path := "/api/v1/tasks/" + f.task.String() + "/comments"
	svc := service.NewCommentService(postgres.NewCommentRepo(f.db), postgres.NewTaskRepo(f.db), postgres.NewActivityLogRepo(f.db))
	h := NewCommentHandler(svc, f.svc)
	return receiptRoute{
		name: "comment", ok: http.StatusCreated,
		body: `{"body":"результат: go test ok"}`, other: `{"body":"другой текст"}`,
		serve: func(_ *testing.T, store mw.IdempotencyStore, wrap echo.MiddlewareFunc, key, body string) *httptest.ResponseRecorder {
			return postJSON(f.echoFor(store, wrap, "/api/v1/tasks/:task_id/comments", h.Create), path, key, body)
		},
		written: func(t *testing.T) int {
			var n int
			require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM comments WHERE task_id=$1`, f.task))
			return n
		},
		idOf: func(t *testing.T, rec *httptest.ResponseRecorder) string { return jsonField(t, rec, "id") },
	}
}

// memStorage is the object store (S3 in production): the true external boundary.
type memStorage struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (s *memStorage) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[key] = b
	return nil
}

func (s *memStorage) GetPresignedURL(context.Context, string, time.Duration, string, string) (string, error) {
	return "", errors.New("not used")
}

func (s *memStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objs, key)
	return nil
}

func (s *memStorage) Download(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

func (s *memStorage) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objs)
}

func (f *receiptFixture) artifactRoute(t *testing.T) receiptRoute {
	path := "/api/v1/tasks/" + f.task.String() + "/artifacts"
	store := &memStorage{objs: map[string][]byte{}}
	svc := service.NewArtifactService(postgres.NewArtifactRepo(f.db), store, postgres.NewActivityLogRepo(f.db))
	h := NewArtifactHandler(svc, f.svc)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM artifacts WHERE task_id=$1`, f.task) })
	return receiptRoute{
		name: "artifact", ok: http.StatusCreated,
		body:  `{"name":"report.txt","encoding":"text","content":"go test ok"}`,
		other: `{"name":"report.txt","encoding":"text","content":"something else"}`,
		serve: func(_ *testing.T, st mw.IdempotencyStore, wrap echo.MiddlewareFunc, key, body string) *httptest.ResponseRecorder {
			return postJSON(f.echoFor(st, wrap, "/api/v1/tasks/:task_id/artifacts", h.Upload), path, key, body)
		},
		written: func(t *testing.T) int {
			var n int
			require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM artifacts WHERE task_id=$1`, f.task))
			// A repeat that lost the race drops the object it stored.
			assert.Equal(t, n, store.count(), "stored objects match artifact rows")
			return n
		},
		idOf: func(t *testing.T, rec *httptest.ResponseRecorder) string { return jsonField(t, rec, "id") },
	}
}

func (f *receiptFixture) finishRoute() receiptRoute {
	path := "/api/v1/tasks/" + f.task.String() + "/finish"
	h := NewTypedOutcomeCommandHandler(f.svc.(service.TypedOutcomeCommands), f.svc)
	return receiptRoute{
		name: "finish", ok: http.StatusOK,
		body:  `{"result_kind":"change","evidence":"go test ./... ok","to":"review"}`,
		other: `{"result_kind":"change","evidence":"another evidence","to":"review"}`,
		serve: func(_ *testing.T, store mw.IdempotencyStore, wrap echo.MiddlewareFunc, key, body string) *httptest.ResponseRecorder {
			return postJSON(f.echoFor(store, wrap, "/api/v1/tasks/:task_id/finish", h.FinishTask), path, key, body)
		},
		written: func(t *testing.T) int {
			var n int
			require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM turn_outcomes WHERE task_id=$1`, f.task))
			var c int
			require.NoError(t, f.db.Get(&c, `SELECT count(*) FROM comments WHERE task_id=$1 AND metadata->>'command'='finish_task'`, f.task))
			assert.Equal(t, n, c, "one closing comment per outcome")
			return n
		},
		idOf: func(t *testing.T, rec *httptest.ResponseRecorder) string { return jsonField(t, rec, "outcome", "id") },
	}
}

// reportTokens is the tokens_in of one test report: the reports added are
// the session's tokens_in divided by it.
const reportTokens = 1000

func (f *receiptFixture) sessionRoute() receiptRoute {
	path := "/api/v1/agents/me/sessions/report"
	h := NewAgentHandlerWithEvents(service.NewAgentService(postgres.NewAgentRepo(f.db), nil, nil, nil), nil, nil, nil, nil, postgres.NewSessionRepo(f.db))
	body := func(out int) string {
		return fmt.Sprintf(`{"tokens_in":%d,"tokens_out":%d,"estimated_cost":0.5,"model":"m","task_id":%q}`, reportTokens, out, f.task)
	}
	return receiptRoute{
		name: "session report", ok: http.StatusOK,
		body: body(10), other: body(20),
		serve: func(_ *testing.T, store mw.IdempotencyStore, wrap echo.MiddlewareFunc, key, b string) *httptest.ResponseRecorder {
			return postJSON(f.echoFor(store, wrap, path, h.ReportSession), path, key, b)
		},
		written: func(t *testing.T) int {
			var tokens int64
			require.NoError(t, f.db.Get(&tokens, `SELECT COALESCE(sum(tokens_in),0) FROM agent_sessions WHERE agent_id=$1`, f.agent))
			return int(tokens / reportTokens)
		},
		idOf: func(t *testing.T, rec *httptest.ResponseRecorder) string { return jsonField(t, rec, "session_id") },
	}
}

// eachRoute runs fn on every route, each on a fresh task.
func eachRoute(t *testing.T, fn func(t *testing.T, f *receiptFixture, r receiptRoute)) {
	for _, mk := range []func(t *testing.T, f *receiptFixture) receiptRoute{
		func(_ *testing.T, f *receiptFixture) receiptRoute { return f.commentRoute() },
		func(t *testing.T, f *receiptFixture) receiptRoute { return f.artifactRoute(t) },
		func(_ *testing.T, f *receiptFixture) receiptRoute { return f.finishRoute() },
		func(_ *testing.T, f *receiptFixture) receiptRoute { return f.sessionRoute() },
	} {
		f := newReceiptFixture(t)
		r := mk(t, f)
		t.Run(r.name, func(t *testing.T) { fn(t, f, r) })
	}
}

// failCompleteStore loses the stored answer once: the fault between the
// handler's commit and Complete.
type failCompleteStore struct {
	*postgres.IdempotencyRepo
	armed atomic.Bool
}

func (s *failCompleteStore) Complete(ctx context.Context, sc domain.IdempotencyScope, token uuid.UUID, status int, ct string, body []byte) error {
	if s.armed.CompareAndSwap(true, false) {
		return errors.New("injected: the process died before the answer was stored")
	}
	return s.IdempotencyRepo.Complete(ctx, sc, token, status, ct, body)
}

// takeoverStore hands every request a fresh claim, as if each one took over
// a stale claim: the middleware record protects nothing, only the receipt does.
type takeoverStore struct{ *postgres.IdempotencyRepo }

func (takeoverStore) Claim(context.Context, domain.IdempotencyScope, string) (*domain.IdempotencyRecord, uuid.UUID, error) {
	return nil, uuid.New(), nil
}
func (takeoverStore) Complete(context.Context, domain.IdempotencyScope, uuid.UUID, int, string, []byte) error {
	return nil
}
func (takeoverStore) Release(context.Context, domain.IdempotencyScope, uuid.UUID) error { return nil }

// fail5xxOnce lets the handler commit, then answers 500 instead of its answer.
func fail5xxOnce() echo.MiddlewareFunc {
	var armed atomic.Bool
	armed.Store(true)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !armed.CompareAndSwap(true, false) {
				return next(c)
			}
			res := c.Response()
			orig := res.Writer
			res.Writer = httptest.NewRecorder()
			if err := next(c); err != nil {
				c.Error(err)
			}
			res.Writer, res.Committed, res.Status = orig, false, 0
			return c.JSON(http.StatusInternalServerError, map[string]string{"message": "injected 500 after the commit"})
		}
	}
}

func assertReplayOf(t *testing.T, r receiptRoute, first string, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, r.ok, rec.Code, rec.Body.String())
	assert.Equal(t, "true", rec.Header().Get(mw.HeaderIdempotentReplayed))
	assert.Equal(t, first, r.idOf(t, rec), "the repeat answers with the write that happened")
}

func TestIdemReceipt_FaultBetweenCommitAndComplete_DB(t *testing.T) {
	eachRoute(t, func(t *testing.T, f *receiptFixture, r receiptRoute) {
		store := &failCompleteStore{IdempotencyRepo: postgres.NewIdempotencyRepo(f.db)}
		store.armed.Store(true)
		key := "fault-" + uuid.NewString()

		first := r.serve(t, store, nil, key, r.body)
		require.Equal(t, r.ok, first.Code, first.Body.String())
		require.False(t, store.armed.Load(), "the fault fired")
		require.Equal(t, 1, r.written(t))

		// The claim was never completed; once it is stale a retry takes it over.
		_, err := f.db.Exec(`UPDATE idempotency_keys SET claimed_at = now() - interval '10 minutes'
			WHERE actor_id=$1 AND idem_key=$2 AND state='in_flight'`, f.agent, key)
		require.NoError(t, err)

		assertReplayOf(t, r, r.idOf(t, first), r.serve(t, store, nil, key, r.body))
		assert.Equal(t, 1, r.written(t), "the repeat wrote nothing")
	})
}

func TestIdemReceipt_CommittedThen5xx_DB(t *testing.T) {
	eachRoute(t, func(t *testing.T, f *receiptFixture, r receiptRoute) {
		store := postgres.NewIdempotencyRepo(f.db)
		key := "5xx-" + uuid.NewString()

		first := r.serve(t, store, fail5xxOnce(), key, r.body)
		require.Equal(t, http.StatusInternalServerError, first.Code, first.Body.String())
		require.Equal(t, 1, r.written(t), "the handler committed before the 500")

		// The 500 released the key; the retry runs and finds the write.
		retry := r.serve(t, store, nil, key, r.body)
		require.Equal(t, r.ok, retry.Code, retry.Body.String())
		assert.Equal(t, "true", retry.Header().Get(mw.HeaderIdempotentReplayed))
		assert.Equal(t, 1, r.written(t), "the repeat wrote nothing")
	})
}

func TestIdemReceipt_RepeatAfterKeyTTL_DB(t *testing.T) {
	eachRoute(t, func(t *testing.T, f *receiptFixture, r receiptRoute) {
		store := postgres.NewIdempotencyRepo(f.db)
		key := "ttl-" + uuid.NewString()

		first := r.serve(t, store, nil, key, r.body)
		require.Equal(t, r.ok, first.Code, first.Body.String())

		// 24 hours later the middleware has forgotten the key.
		_, err := f.db.Exec(`UPDATE idempotency_keys SET expires_at = now() - interval '1 second'
			WHERE actor_id=$1 AND idem_key=$2`, f.agent, key)
		require.NoError(t, err)

		assertReplayOf(t, r, r.idOf(t, first), r.serve(t, store, nil, key, r.body))
		assert.Equal(t, 1, r.written(t), "the repeat wrote nothing")

		// Another body under the same key is still refused, from the receipt.
		_, err = f.db.Exec(`DELETE FROM idempotency_keys WHERE actor_id=$1 AND idem_key=$2`, f.agent, key)
		require.NoError(t, err)
		reused := r.serve(t, store, nil, key, r.other)
		require.Equal(t, http.StatusConflict, reused.Code, reused.Body.String())
		assert.Contains(t, reused.Body.String(), mw.CodeIdempotencyKeyReused)
		assert.Equal(t, 1, r.written(t))
	})
}

func TestIdemReceipt_ConcurrentRepeatsWriteOnce_DB(t *testing.T) {
	eachRoute(t, func(t *testing.T, f *receiptFixture, r receiptRoute) {
		store := takeoverStore{postgres.NewIdempotencyRepo(f.db)}
		key := "race-" + uuid.NewString()

		const n = 4
		recs := make([]*httptest.ResponseRecorder, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				recs[i] = r.serve(t, store, nil, key, r.body)
			}()
		}
		close(start)
		wg.Wait()

		assert.Equal(t, 1, r.written(t), "one write for one key")
		ids := map[string]bool{}
		for _, rec := range recs {
			require.Equal(t, r.ok, rec.Code, rec.Body.String())
			ids[r.idOf(t, rec)] = true
		}
		assert.Len(t, ids, 1, "every answer names the one write")
	})
}

// Positive control for the cases above: without the receipt the same faults
// write twice, and without a key every request writes, as before.
func TestIdemReceipt_NoKeyWritesEveryTime_DB(t *testing.T) {
	eachRoute(t, func(t *testing.T, f *receiptFixture, r receiptRoute) {
		if r.name == "finish" {
			t.Skip("a second finish on one card is a different move, covered by the move tests")
		}
		store := postgres.NewIdempotencyRepo(f.db)
		a := r.serve(t, store, nil, "", r.body)
		b := r.serve(t, store, nil, "", r.body)
		require.Equal(t, r.ok, a.Code, a.Body.String())
		require.Equal(t, r.ok, b.Code, b.Body.String())
		assert.Empty(t, b.Header().Get(mw.HeaderIdempotentReplayed))
		assert.Equal(t, 2, r.written(t))
		var receipts int
		require.NoError(t, f.db.Get(&receipts, `SELECT count(*) FROM idempotency_receipts WHERE actor_id=$1`, f.agent))
		assert.Zero(t, receipts, "a write without a key records no receipt")
	})
}

// Another actor with the same key is a separate write.
func TestIdemReceipt_OtherActorSameKeyIsSeparate_DB(t *testing.T) {
	f := newReceiptFixture(t)
	r := f.commentRoute()
	store := postgres.NewIdempotencyRepo(f.db)
	first := r.serve(t, store, nil, "shared-key", r.body)
	require.Equal(t, r.ok, first.Code, first.Body.String())

	other := uuid.New()
	_, err := f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'other',$3,'fixture-hash','test')`, other, f.ws, other.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM idempotency_keys WHERE actor_id=$1`, other)
		_, _ = f.db.Exec(`DELETE FROM idempotency_receipts WHERE actor_id=$1`, other)
		_, _ = f.db.Exec(`DELETE FROM comments WHERE author_id=$1`, other)
		_, _ = f.db.Exec(`DELETE FROM agents WHERE id=$1`, other)
	})
	f.agent, other = other, f.agent
	second := r.serve(t, store, nil, "shared-key", r.body)
	f.agent = other
	require.Equal(t, r.ok, second.Code, second.Body.String())
	assert.Empty(t, second.Header().Get(mw.HeaderIdempotentReplayed))
	assert.NotEqual(t, r.idOf(t, first), r.idOf(t, second))
	assert.Equal(t, 2, r.written(t))
}

// A caller reconciles by reading: its own write is found by the key, without
// sending the write again; a key that wrote nothing answers 404.
func TestIdemReceipt_ReconcileByReading_DB(t *testing.T) {
	f := newReceiptFixture(t)
	r := f.commentRoute()
	first := r.serve(t, postgres.NewIdempotencyRepo(f.db), nil, "read-me", r.body)
	require.Equal(t, r.ok, first.Code, first.Body.String())

	h := NewIdempotencyReceiptHandler(postgres.NewIdempotencyRepo(f.db))
	get := func(actor uuid.UUID, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/me/idempotency-receipts?"+query, http.NoBody)
		req = req.WithContext(actorctx.WithActor(req.Context(), actor, domain.ActorTypeAgent))
		rec := httptest.NewRecorder()
		require.NoError(t, h.Get(echo.New().NewContext(req, rec)))
		return rec
	}
	q := "route=comment.create&task_id=" + f.task.String() + "&key="

	found := get(f.agent, q+"read-me")
	require.Equal(t, http.StatusOK, found.Code, found.Body.String())
	assert.Equal(t, r.idOf(t, first), jsonField(t, found, "resource_id"))
	assert.Equal(t, r.idOf(t, first), jsonField(t, found, "response", "id"))
	assert.Equal(t, 1, r.written(t), "reading writes nothing")

	missing := get(f.agent, q+"never-sent")
	require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
	assert.Contains(t, missing.Body.String(), "idempotency_receipt_not_found")

	// Another actor does not see the caller's write under the same key.
	assert.Equal(t, http.StatusNotFound, get(uuid.New(), q+"read-me").Code)
	assert.Equal(t, http.StatusBadRequest, get(f.agent, "route=nope&key=read-me").Code)
}
