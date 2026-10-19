package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func seedOperatorAgent(t *testing.T, db *sqlx.DB, ws uuid.UUID, operator *uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	short := id.String()[:8]
	_, err := db.ExecContext(context.Background(), `INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix, operator_user_id, max_concurrent_tasks)
		VALUES ($1,$2,$3,$3,'x',$4,$5,5)`, id, ws, "ag-"+short, short, operator)
	require.NoError(t, err)
	return id
}

// callUserRuntime runs a handler as a user (agent==Nil) or as an agent key.
func callUserRuntime(t *testing.T, fn echo.HandlerFunc, method, path, body string, user, agent uuid.UUID, params map[string]string) (status int, out map[string]any) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != uuid.Nil {
		c.Set(mw.ContextKeyUserID, user)
	}
	if agent != uuid.Nil {
		c.Set(mw.ContextKeyAgentID, agent)
	}
	var names, values []string
	for k, v := range params {
		names, values = append(names, k), append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if err := fn(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestUserRuntime_OperatorManagerFlow(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, operator := seedRuntimeWorkspace(t, db)
	h := NewUserRuntimeHandler(postgres.NewUserRuntimeRepo(db))
	manager := seedOperatorAgent(t, db, ws, &operator)
	worker := seedOperatorAgent(t, db, ws, &operator)
	stranger := seedOperatorAgent(t, db, ws, nil)

	cfg := func(rev int, model string) string {
		return `{"if_revision":` + string(rune('0'+rev)) + `,"config":{"accounts":{"a1":{"credential":"cred:acct-1"},"a2":{"credential":"cred:acct-2"}},"pools":{"p1":{"accounts":["a1"]},"p2":{"accounts":["a2"]}},` +
			`"profiles":{"fast":{"harness":"claude","model":"` + model + `","effort":"low","pool":"p1"},"other":{"model":"x","pool":"p2"}},` +
			`"agents":{"` + worker.String() + `":{"profile":"fast"}}}}`
	}

	// 3a. an agent without the assignment cannot write the operator's resources.
	code, _ := callUserRuntime(t, h.SaveAsManager, http.MethodPut, "/", cfg(0, "m1"), uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)

	// the operator saves; a stale revision conflicts; a secret is refused.
	code, out := callUserRuntime(t, h.Save, http.MethodPut, "/", cfg(0, "m1"), operator, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, code, out)
	require.EqualValues(t, 1, out["revision"])
	code, _ = callUserRuntime(t, h.Save, http.MethodPut, "/", cfg(0, "m1"), operator, uuid.Nil, nil)
	require.Equal(t, http.StatusConflict, code)
	code, _ = callUserRuntime(t, h.Save, http.MethodPut, "/", `{"if_revision":1,"config":{"accounts":{"a":{"api_key":"sk-1"}}}}`, operator, uuid.Nil, nil)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = callUserRuntime(t, h.Save, http.MethodPut, "/", `{"if_revision":1,"config":{"accounts":{"a":{"credential":"plain-text"}}}}`, operator, uuid.Nil, nil)
	require.Equal(t, http.StatusBadRequest, code)

	// 2. GET /agents/me/runtime: profile, referenced resources, limits.
	code, out = callUserRuntime(t, h.AgentRuntime, http.MethodGet, "/", "", uuid.Nil, worker, nil)
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 1, out["revision"])
	require.Equal(t, "fast", out["profile"].(map[string]any)["profile"])
	profiles := out["resources"].(map[string]any)["profiles"].(map[string]any)
	require.Contains(t, profiles, "fast")
	require.NotContains(t, profiles, "other", "unreferenced profiles are not leaked")
	res := out["resources"].(map[string]any)
	require.Contains(t, res["pools"], "p1")
	require.NotContains(t, res["pools"], "p2", "unreferenced pools are not leaked")
	require.Contains(t, res["accounts"], "a1")
	require.NotContains(t, res["accounts"], "a2", "unreferenced accounts are not leaked")
	require.EqualValues(t, 5, out["limits"].(map[string]any)["max_concurrent_tasks"])
	// an agent with no operator still gets a valid, empty answer
	code, out = callUserRuntime(t, h.AgentRuntime, http.MethodGet, "/", "", uuid.Nil, stranger, nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["operator_resolved"])
	// a user (no agent key) is refused
	code, _ = callUserRuntime(t, h.AgentRuntime, http.MethodGet, "/", "", operator, uuid.Nil, nil)
	require.Equal(t, http.StatusForbidden, code)

	// the operator can only assign agents it operates.
	code, _ = callUserRuntime(t, h.GrantManager, http.MethodPost, "/", `{"agent_id":"`+stranger.String()+`"}`, operator, uuid.Nil, nil)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = callUserRuntime(t, h.GrantManager, http.MethodPost, "/", `{"agent_id":"`+manager.String()+`"}`, operator, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, code)

	// 3b. with the assignment: 200 and a revision authored by the agent.
	code, out = callUserRuntime(t, h.SaveAsManager, http.MethodPut, "/", cfg(1, "m2"), uuid.Nil, manager, nil)
	require.Equal(t, http.StatusOK, code, out)
	require.EqualValues(t, 2, out["revision"])
	code, out = callUserRuntime(t, h.History, http.MethodGet, "/", "", operator, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, code)
	var authors []string
	for _, e := range out["history"].([]any) {
		m := e.(map[string]any)
		authors = append(authors, m["kind"].(string)+":"+m["author_type"].(string))
		if m["kind"] == "config" && m["revision"].(float64) == 2 {
			require.Equal(t, manager.String(), m["author_id"])
		}
	}
	require.Contains(t, authors, "config:agent")
	require.Contains(t, authors, "config:user")
	require.Contains(t, authors, "manager_granted:user")

	// limits: operator and manager both; a non-operated agent is a 404.
	body := `{"max_concurrent_tasks":3}`
	code, out = callUserRuntime(t, h.SetLimits, http.MethodPut, "/", body, operator, uuid.Nil, map[string]string{"agent_id": worker.String()})
	require.Equal(t, http.StatusOK, code, out)
	code, _ = callUserRuntime(t, h.SetLimitsAsManager, http.MethodPut, "/", `{"max_concurrent_tasks":4}`, uuid.Nil, manager, map[string]string{"agent_id": worker.String()})
	require.Equal(t, http.StatusOK, code)
	code, _ = callUserRuntime(t, h.SetLimitsAsManager, http.MethodPut, "/", body, uuid.Nil, manager, map[string]string{"agent_id": stranger.String()})
	require.Equal(t, http.StatusNotFound, code)
	code, out = callUserRuntime(t, h.AgentRuntime, http.MethodGet, "/", "", uuid.Nil, worker, nil)
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 4, out["limits"].(map[string]any)["max_concurrent_tasks"])

	// revocation takes effect at once.
	code, _ = callUserRuntime(t, h.RevokeManager, http.MethodPost, "/", `{"agent_id":"`+manager.String()+`"}`, operator, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, code)
	code, _ = callUserRuntime(t, h.SaveAsManager, http.MethodPut, "/", cfg(2, "m3"), uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)

	// an agent key cannot use the operator-only surface.
	code, _ = callUserRuntime(t, h.Save, http.MethodPut, "/", cfg(2, "m3"), operator, manager, nil)
	require.Equal(t, http.StatusForbidden, code)
}

func TestUserRuntime_OperatorDefaultsToIssuer(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, issuer := seedRuntimeWorkspace(t, db)
	_, other := seedRuntimeWorkspace(t, db)
	repo := postgres.NewWorkspaceExportRepo(db)
	ctx := context.Background()
	operatorOf := func(id uuid.UUID) *uuid.UUID {
		var op *uuid.UUID
		require.NoError(t, db.GetContext(ctx, &op, `SELECT operator_user_id FROM agents WHERE id=$1`, id))
		return op
	}

	agent := seedOperatorAgent(t, db, ws, nil)
	require.NoError(t, repo.SetAgentSponsor(ctx, agent, issuer))
	require.Equal(t, &issuer, operatorOf(agent), "no operator named: the issuer runs the agent")

	// a later key issued by someone else does not take the agent over...
	require.NoError(t, repo.SetAgentSponsor(ctx, agent, other))
	require.Equal(t, &issuer, operatorOf(agent))
	// ...only an explicit operator does, and only a workspace member qualifies.
	ok, err := repo.UserCanOperate(ctx, ws, other)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = repo.UserCanOperate(ctx, ws, issuer)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.SetAgentOperator(ctx, agent, issuer))
}

func TestUserRuntime_OperatorReadAPIIsolation(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, opA := seedRuntimeWorkspace(t, db)
	_, opB := seedRuntimeWorkspace(t, db)
	h := NewUserRuntimeHandler(postgres.NewUserRuntimeRepo(db))
	a1 := seedOperatorAgent(t, db, ws, &opA)
	a2 := seedOperatorAgent(t, db, ws, &opA)
	b1 := seedOperatorAgent(t, db, ws, &opB)
	lease := func(agent uuid.UUID, runner, interval string) {
		_, err := db.Exec(`INSERT INTO agent_controllers(agent_id,runner_id,runner_name,host,version,lease_expires_at)
			VALUES($1,$2,'n-'||$2,'host-'||$2,'v1',now()+$3::interval)`, agent, runner, interval)
		require.NoError(t, err)
	}
	ra, rb := "ra-"+a1.String()[:8], "rb-"+b1.String()[:8]
	lease(a1, ra, "1 minute")
	lease(a2, ra, "1 minute")
	lease(b1, rb, "-1 minute")
	_, err := db.Exec(`INSERT INTO user_runtime_managers(operator_user_id,agent_id) VALUES($1,$2)`, opA, a1)
	require.NoError(t, err)

	code, out := callUserRuntime(t, h.OperatorAgents, http.MethodGet, "/", "", opA, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, code, out)
	agents := out["agents"].([]any)
	require.Len(t, agents, 2)
	for _, e := range agents {
		m := e.(map[string]any)
		require.NotEqual(t, b1.String(), m["agent_id"], "foreign agent leaked")
		require.Equal(t, opA.String(), m["operator_user_id"])
		require.Equal(t, true, m["controller"].(map[string]any)["live"])
		require.Equal(t, m["agent_id"] == a1.String(), m["runtime_manager"])
	}

	code, out = callUserRuntime(t, h.OperatorRunners, http.MethodGet, "/", "", opA, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, code)
	runners := out["runners"].([]any)
	require.Len(t, runners, 1, "foreign runner leaked")
	r0 := runners[0].(map[string]any)
	require.Equal(t, ra, r0["runner_id"])
	require.Len(t, r0["agents"], 2)
	require.Equal(t, true, r0["live"])

	// B sees only its own, lapsed lease.
	code, out = callUserRuntime(t, h.OperatorRunners, http.MethodGet, "/", "", opB, uuid.Nil, nil)
	require.Equal(t, http.StatusOK, code)
	rs := out["runners"].([]any)
	require.Len(t, rs, 1)
	require.Equal(t, rb, rs[0].(map[string]any)["runner_id"])
	require.Equal(t, false, rs[0].(map[string]any)["live"])

	// an agent key is refused on both routes.
	for _, fn := range []echo.HandlerFunc{h.OperatorAgents, h.OperatorRunners} {
		code, _ = callUserRuntime(t, fn, http.MethodGet, "/", "", uuid.Nil, a1, nil)
		require.Equal(t, http.StatusForbidden, code)
	}

	// the repo itself refuses an OAuth connector and a caller with no user.
	repo := postgres.NewUserRuntimeRepo(db)
	for name, actor := range map[string]postgres.UserRuntimeActor{
		"oauth":   {UserID: opA, Connector: true},
		"no user": {},
	} {
		_, err := repo.OperatorAgents(context.Background(), actor)
		require.Error(t, err, name)
		_, err = repo.OperatorRunners(context.Background(), actor)
		require.Error(t, err, name)
	}
}
