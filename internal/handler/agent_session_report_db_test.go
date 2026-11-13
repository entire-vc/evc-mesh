package handler

import (
	"context"
	"math"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// POST /agents/me/sessions/report against real Postgres: the refusals around
// the atomic addition, with the real agent lookup and the real session table.

func reportDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for real PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestReportSession_RefusalsWriteNothing_RealPostgres(t *testing.T) {
	db := reportDB(t)
	ctx := context.Background()
	wsID, agentID := uuid.New(), uuid.New()
	sfx := wsID.String()[:8]
	_, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'rep',$2,$3)`, wsID, "rep-ws-"+sfx, uuid.New())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		VALUES ($1,$2,'Rep Agent',$3,'not-a-real-hash','test')`, agentID, wsID, "rep-agent-"+sfx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agent_sessions WHERE agent_id=$1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agents WHERE id=$1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id=$1`, wsID)
	})

	agentSvc := service.NewAgentService(postgres.NewAgentRepo(db), nil, nil, nil)
	h := NewAgentHandlerWithEvents(agentSvc, nil, nil, nil, nil, postgres.NewSessionRepo(db))
	count := func() int {
		var n int
		require.NoError(t, db.Get(&n, `SELECT count(*) FROM agent_sessions WHERE agent_id=$1`, agentID))
		return n
	}

	t.Run("unknown agent answers 404", func(t *testing.T) {
		other := uuid.New()
		rec := postReport(t, h, echo.New(), &other, `{"tokens_in":5}`)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, 0, count())
	})
	t.Run("addition refused by the database answers 500 and changes nothing", func(t *testing.T) {
		body := `{"tokens_in":` + strconv.FormatInt(math.MaxInt64, 10) + `}`
		require.Equal(t, http.StatusOK, postReport(t, h, echo.New(), &agentID, body).Code)
		rec := postReport(t, h, echo.New(), &agentID, `{"tokens_in":1}`)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var in int64
		require.NoError(t, db.Get(&in, `SELECT tokens_in FROM agent_sessions WHERE agent_id=$1`, agentID))
		assert.EqualValues(t, math.MaxInt64, in)
		assert.Equal(t, 1, count())
	})
}
