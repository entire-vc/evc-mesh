package middleware_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

// DB-backed: the production chain for POST /agents/me/sessions/report —
// Idempotency over the real idempotency_keys table in front of the real
// handler and the real agent_sessions table.

type reportFixture struct {
	db      *sqlx.DB
	e       *echo.Echo
	agentID uuid.UUID
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	db := idemTestDB(t)
	ctx := context.Background()
	ws, agent := uuid.New(), uuid.New()
	sfx := uuid.New().String()[:8]
	_, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug, owner_id, settings) VALUES ($1,'rep','rep-'||$2,$3,'{}')`, ws, sfx, uuid.New())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		VALUES ($1,$2,'Report Agent',$3,'not-a-real-hash','test')`, agent, ws, "rep-agent-"+sfx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE actor_id=$1`, agent)
		_, _ = db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE agent_id=$1`, agent)
		_, _ = db.ExecContext(ctx, `DELETE FROM agents WHERE id=$1`, agent)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=$1`, ws)
	})

	h := handler.NewAgentHandlerWithEvents(service.NewAgentService(postgres.NewAgentRepo(db), nil, nil, nil), nil, nil, nil, nil, postgres.NewSessionRepo(db))
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAgentID, agent)
			c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), agent, domain.ActorTypeAgent)))
			return next(c)
		}
	}
	e := echo.New()
	e.POST("/api/v1/agents/me/sessions/report", h.ReportSession, auth, mw.Idempotency(postgres.NewIdempotencyRepo(db), 16<<20))
	return &reportFixture{db: db, e: e, agentID: agent}
}

func (f *reportFixture) post(key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/me/sessions/report", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if key != "" {
		req.Header.Set(mw.HeaderIdempotencyKey, key)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

func (f *reportFixture) totals(t *testing.T) (sessions int, in, out int64) {
	t.Helper()
	require.NoError(t, f.db.QueryRow(`SELECT count(*), COALESCE(sum(tokens_in),0), COALESCE(sum(tokens_out),0)
		FROM agent_sessions WHERE agent_id=$1`, f.agentID).Scan(&sessions, &in, &out))
	return
}

func TestIdempotency_SessionReport_RepeatAddsNothing_DB(t *testing.T) {
	f := newReportFixture(t)
	body := `{"tokens_in":100,"tokens_out":10,"estimated_cost":0.5,"model":"m"}`

	first := f.post("rep-key-1", body)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	again := f.post("rep-key-1", body)
	require.Equal(t, http.StatusOK, again.Code)
	assert.Equal(t, "true", again.Header().Get(mw.HeaderIdempotentReplayed))
	assert.JSONEq(t, first.Body.String(), again.Body.String(), "the retry gets the first answer")

	n, in, out := f.totals(t)
	assert.Equal(t, 1, n)
	assert.EqualValues(t, 100, in, "one report counted once")
	assert.EqualValues(t, 10, out)

	// Another body under the same key: 409, totals unchanged.
	reused := f.post("rep-key-1", `{"tokens_in":7,"tokens_out":1}`)
	assert.Equal(t, http.StatusConflict, reused.Code)
	assert.Contains(t, reused.Body.String(), mw.CodeIdempotencyKeyReused)
	_, in, out = f.totals(t)
	assert.EqualValues(t, 100, in)
	assert.EqualValues(t, 10, out)

	// No header: behaves as before (adds).
	require.Equal(t, http.StatusOK, f.post("", body).Code)
	_, in, _ = f.totals(t)
	assert.EqualValues(t, 200, in)
}

func TestSessionReport_ConcurrentReportsSumExactly_DB(t *testing.T) {
	// Several rounds, each with a fresh agent (so every round starts at "no
	// active session") and a common start line, to hit the first-report race.
	for round := 0; round < 8; round++ {
		f := newReportFixture(t)
		const n = 30
		var wg sync.WaitGroup
		start := make(chan struct{})
		codes := make([]int, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				codes[i] = f.post(fmt.Sprintf("conc-%d-%d", round, i), `{"tokens_in":3,"tokens_out":2}`).Code
			}(i)
		}
		close(start)
		wg.Wait()
		for i, c := range codes {
			require.Equal(t, http.StatusOK, c, "round %d report %d", round, i)
		}
		sessions, in, out := f.totals(t)
		require.Equal(t, 1, sessions, "round %d: concurrent first reports must not create two active sessions", round)
		require.EqualValues(t, 3*n, in, "round %d", round)
		require.EqualValues(t, 2*n, out, "round %d", round)
	}
}
