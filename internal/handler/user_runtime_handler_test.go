package handler

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// DB-free coverage of the operator-runtime handlers: routing of the actor, body
// and parameter validation, and the status each repository outcome maps to. The
// end-to-end flow with a real database is user_runtime_handler_db_test.go.

func newUserRuntimeHandlerMock(t *testing.T) (*UserRuntimeHandler, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return NewUserRuntimeHandler(postgres.NewUserRuntimeRepo(sqlx.NewDb(raw, "postgres"))), mock
}

var handlerCfgCols = []string{"user_id", "revision", "config", "updated_at"}

func TestUserRuntimeHandler_RefusesTheWrongCaller(t *testing.T) {
	h, _ := newUserRuntimeHandlerMock(t)
	user, agent := uuid.New(), uuid.New()
	body := `{"if_revision":0,"config":{}}`
	p := map[string]string{"agent_id": agent.String()}

	// agent key on the operator-only surface
	for name, fn := range map[string]func() int{
		"get":     func() int { c, _ := callUserRuntime(t, h.Get, http.MethodGet, "/", "", user, agent, nil); return c },
		"save":    func() int { c, _ := callUserRuntime(t, h.Save, http.MethodPut, "/", body, user, agent, nil); return c },
		"history": func() int { c, _ := callUserRuntime(t, h.History, http.MethodGet, "/", "", user, agent, nil); return c },
		"managers": func() int {
			c, _ := callUserRuntime(t, h.Managers, http.MethodGet, "/", "", user, agent, nil)
			return c
		},
		"limits": func() int {
			c, _ := callUserRuntime(t, h.SetLimits, http.MethodPut, "/", `{"max_concurrent_tasks":1}`, user, agent, p)
			return c
		},
		"grant": func() int {
			c, _ := callUserRuntime(t, h.GrantManager, http.MethodPost, "/", `{"agent_id":"`+agent.String()+`"}`, user, agent, nil)
			return c
		},
		"revoke": func() int {
			c, _ := callUserRuntime(t, h.RevokeManager, http.MethodPost, "/", `{"agent_id":"`+agent.String()+`"}`, user, agent, nil)
			return c
		},
	} {
		require.Equal(t, http.StatusForbidden, fn(), name)
	}

	// user session on the agent-key surface
	for name, fn := range map[string]func() int{
		"runtime": func() int {
			c, _ := callUserRuntime(t, h.AgentRuntime, http.MethodGet, "/", "", user, uuid.Nil, nil)
			return c
		},
		"manager save": func() int {
			c, _ := callUserRuntime(t, h.SaveAsManager, http.MethodPut, "/", body, user, uuid.Nil, nil)
			return c
		},
		"manager limits": func() int {
			c, _ := callUserRuntime(t, h.SetLimitsAsManager, http.MethodPut, "/", `{"max_concurrent_tasks":1}`, user, uuid.Nil, p)
			return c
		},
	} {
		require.Equal(t, http.StatusForbidden, fn(), name)
	}
}

func TestUserRuntimeHandler_ValidatesInput(t *testing.T) {
	h, _ := newUserRuntimeHandlerMock(t)
	user, agent := uuid.New(), uuid.New()
	good := map[string]string{"agent_id": agent.String()}

	for name, body := range map[string]string{
		"missing config":      `{"if_revision":0}`,
		"missing if_revision": `{"config":{}}`,
		"unknown field":       `{"if_revision":0,"config":{},"extra":1}`,
		"not json":            `nope`,
	} {
		c, _ := callUserRuntime(t, h.Save, http.MethodPut, "/", body, user, uuid.Nil, nil)
		require.Equal(t, http.StatusBadRequest, c, name)
		c, _ = callUserRuntime(t, h.SaveAsManager, http.MethodPut, "/", body, uuid.Nil, agent, nil)
		require.Equal(t, http.StatusBadRequest, c, name)
	}
	for name, tc := range map[string]struct {
		body   string
		params map[string]string
	}{
		"limit missing":     {`{}`, good},
		"limit not numeric": {`{"max_concurrent_tasks":"x"}`, good},
		"bad agent id":      {`{"max_concurrent_tasks":1}`, map[string]string{"agent_id": "nope"}},
		"nil agent id":      {`{"max_concurrent_tasks":1}`, map[string]string{"agent_id": uuid.Nil.String()}},
	} {
		c, _ := callUserRuntime(t, h.SetLimits, http.MethodPut, "/", tc.body, user, uuid.Nil, tc.params)
		require.Equal(t, http.StatusBadRequest, c, name)
		c, _ = callUserRuntime(t, h.SetLimitsAsManager, http.MethodPut, "/", tc.body, uuid.Nil, agent, tc.params)
		require.Equal(t, http.StatusBadRequest, c, name)
	}
	for name, body := range map[string]string{
		"no agent_id":  `{}`,
		"nil agent_id": `{"agent_id":"` + uuid.Nil.String() + `"}`,
		"bad agent_id": `{"agent_id":"x"}`,
	} {
		c, _ := callUserRuntime(t, h.GrantManager, http.MethodPost, "/", body, user, uuid.Nil, nil)
		require.Equal(t, http.StatusBadRequest, c, name)
		c, _ = callUserRuntime(t, h.RevokeManager, http.MethodPost, "/", body, user, uuid.Nil, nil)
		require.Equal(t, http.StatusBadRequest, c, name)
	}
}

func TestUserRuntimeHandler_ReadsAndWrites(t *testing.T) {
	h, mock := newUserRuntimeHandlerMock(t)
	user, agent := uuid.New(), uuid.New()

	// GET /me/runtime on a never-saved user
	mock.ExpectQuery("FROM user_runtime_configs").WithArgs(user).WillReturnError(sql.ErrNoRows)
	c, out := callUserRuntime(t, h.Get, http.MethodGet, "/", "", user, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, c)
	require.EqualValues(t, 0, out["revision"])

	// PUT /me/runtime
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO user_runtime_configs").WithArgs(user).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FOR UPDATE").WithArgs(user).WillReturnRows(sqlmock.NewRows(handlerCfgCols).AddRow(user, 0, []byte(`{}`), time.Now()))
	mock.ExpectQuery("UPDATE user_runtime_configs").WithArgs(user, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(handlerCfgCols).AddRow(user, 1, []byte(`{}`), time.Now()))
	mock.ExpectExec("INSERT INTO user_runtime_history").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	c, out = callUserRuntime(t, h.Save, http.MethodPut, "/", `{"if_revision":0,"config":{"accounts":{}}}`, user, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, c, out)
	require.EqualValues(t, 1, out["revision"])

	// stale revision maps to 409
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO user_runtime_configs").WithArgs(user).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FOR UPDATE").WithArgs(user).WillReturnRows(sqlmock.NewRows(handlerCfgCols).AddRow(user, 5, []byte(`{}`), time.Now()))
	mock.ExpectRollback()
	c, _ = callUserRuntime(t, h.Save, http.MethodPut, "/", `{"if_revision":1,"config":{}}`, user, uuid.Nil, nil)
	require.Equal(t, http.StatusConflict, c)

	// history and managers
	mock.ExpectQuery("FROM user_runtime_history").WithArgs(user, 7).WillReturnRows(
		sqlmock.NewRows([]string{"id", "revision", "kind", "detail", "author_type", "author_id", "created_at"}))
	c, out = callUserRuntime(t, h.History, http.MethodGet, "/?limit=7", "", user, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, c)
	require.Empty(t, out["history"])
	mock.ExpectQuery("FROM user_runtime_managers m JOIN agents").WithArgs(user).WillReturnRows(
		sqlmock.NewRows([]string{"agent_id", "agent_name", "granted_at"}).AddRow(agent, "robin", time.Now()))
	c, out = callUserRuntime(t, h.Managers, http.MethodGet, "/", "", user, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, c)
	require.Len(t, out["managers"], 1)

	// limits: ok, then an agent that is not the operator's → 404
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE agents SET max_concurrent_tasks").WithArgs(agent, user, 2).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FROM user_runtime_configs").WithArgs(user).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO user_runtime_history").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	c, out = callUserRuntime(t, h.SetLimits, http.MethodPut, "/", `{"max_concurrent_tasks":2}`, user, uuid.Nil, map[string]string{"agent_id": agent.String()})
	require.Equal(t, http.StatusOK, c, out)
	require.EqualValues(t, 2, out["max_concurrent_tasks"])
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE agents SET max_concurrent_tasks").WithArgs(agent, user, 2).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	c, _ = callUserRuntime(t, h.SetLimits, http.MethodPut, "/", `{"max_concurrent_tasks":2}`, user, uuid.Nil, map[string]string{"agent_id": agent.String()})
	require.Equal(t, http.StatusNotFound, c)

	// grant then revoke
	for _, fn := range []echoHandler{h.GrantManager, h.RevokeManager} {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM agents").WithArgs(agent, user).WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(true))
		mock.ExpectExec("user_runtime_managers").WithArgs(user, agent).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("FROM user_runtime_configs").WithArgs(user).WillReturnError(sql.ErrNoRows)
		mock.ExpectExec("INSERT INTO user_runtime_history").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		c, out = callUserRuntime(t, fn, http.MethodPost, "/", `{"agent_id":"`+agent.String()+`"}`, user, uuid.Nil, nil)
		require.Equal(t, http.StatusOK, c, out)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserRuntimeHandler_AgentRuntimeAndManagerWrites(t *testing.T) {
	h, mock := newUserRuntimeHandlerMock(t)
	agent, operator := uuid.New(), uuid.New()

	mock.ExpectQuery("FROM agents WHERE id=\\$1").WithArgs(agent).WillReturnRows(sqlmock.NewRows([]string{"operator_user_id", "max_concurrent_tasks"}).AddRow(nil, 3))
	c, out := callUserRuntime(t, h.AgentRuntime, http.MethodGet, "/", "", uuid.Nil, agent, nil)
	require.Equal(t, http.StatusOK, c)
	require.Equal(t, false, out["operator_resolved"])

	// a manager writes the operator's document
	mock.ExpectQuery("FROM user_runtime_managers m").WithArgs(agent).WillReturnRows(sqlmock.NewRows([]string{"operator_user_id"}).AddRow(operator))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO user_runtime_configs").WithArgs(operator).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FOR UPDATE").WithArgs(operator).WillReturnRows(sqlmock.NewRows(handlerCfgCols).AddRow(operator, 0, []byte(`{}`), time.Now()))
	mock.ExpectQuery("UPDATE user_runtime_configs").WithArgs(operator, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(handlerCfgCols).AddRow(operator, 1, []byte(`{}`), time.Now()))
	mock.ExpectExec("INSERT INTO user_runtime_history").WithArgs(operator, 1, sqlmock.AnyArg(), "agent", agent).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	c, out = callUserRuntime(t, h.SaveAsManager, http.MethodPut, "/", `{"if_revision":0,"config":{}}`, uuid.Nil, agent, nil)
	require.Equal(t, http.StatusOK, c, out)

	// without the assignment: 403
	mock.ExpectQuery("FROM user_runtime_managers m").WithArgs(agent).WillReturnError(sql.ErrNoRows)
	c, _ = callUserRuntime(t, h.SaveAsManager, http.MethodPut, "/", `{"if_revision":1,"config":{}}`, uuid.Nil, agent, nil)
	require.Equal(t, http.StatusForbidden, c)

	// limits as manager
	target := uuid.New()
	mock.ExpectQuery("FROM user_runtime_managers m").WithArgs(agent).WillReturnRows(sqlmock.NewRows([]string{"operator_user_id"}).AddRow(operator))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE agents SET max_concurrent_tasks").WithArgs(target, operator, 6).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FROM user_runtime_configs").WithArgs(operator).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO user_runtime_history").WithArgs(operator, 0, sqlmock.AnyArg(), "agent", agent).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	c, out = callUserRuntime(t, h.SetLimitsAsManager, http.MethodPut, "/", `{"max_concurrent_tasks":6}`, uuid.Nil, agent, map[string]string{"agent_id": target.String()})
	require.Equal(t, http.StatusOK, c, out)
	require.NoError(t, mock.ExpectationsWereMet())
}

type echoHandler = echo.HandlerFunc

// stubKeyService records the context Issue receives.
type stubKeyService struct{ ctx context.Context }

func (s *stubKeyService) Issue(ctx context.Context, agentID uuid.UUID, _ string, _ time.Duration) (*service.IssuedAgentKey, error) {
	s.ctx = ctx
	return &service.IssuedAgentKey{AgentID: agentID, KeyID: uuid.New(), APIKey: "k"}, nil
}
func (s *stubKeyService) Revoke(context.Context, uuid.UUID, string) error { return nil }
func (s *stubKeyService) List(context.Context, uuid.UUID) ([]service.AgentKeyInfo, error) {
	return nil, nil
}

func TestAgentKeyIssue_PassesTheNamedOperatorOn(t *testing.T) {
	stub := &stubKeyService{}
	h := NewAgentKeyHandler(stub)
	agent, operator := uuid.New(), uuid.New()
	p := map[string]string{"agent_id": agent.String()}

	c, _ := callUserRuntime(t, h.Issue, http.MethodPost, "/", `{"operator_user_id":"`+operator.String()+`"}`, uuid.New(), uuid.Nil, p)
	require.Equal(t, http.StatusCreated, c)
	require.Equal(t, operator, service.OperatorUserFromContext(stub.ctx), "an explicit operator reaches the service")

	c, _ = callUserRuntime(t, h.Issue, http.MethodPost, "/", `{}`, uuid.New(), uuid.Nil, p)
	require.Equal(t, http.StatusCreated, c)
	require.Equal(t, uuid.Nil, service.OperatorUserFromContext(stub.ctx), "no operator named: the service defaults to the issuer")
}
