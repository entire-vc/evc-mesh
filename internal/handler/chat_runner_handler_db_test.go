package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// runnerCall drives one runner handler as the given agent, against a real repo.
func runnerCall(t *testing.T, fn echo.HandlerFunc, method, target, body string, agent uuid.UUID, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(mw.ContextKeyAgentID, agent)
	var names, values []string
	for k, v := range params {
		names, values = append(names, k), append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if err := fn(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

// Through the HTTP handlers: a deleted thread orders two commands, the ack closes
// them for good, history serves the last N to the runner holding the turn, and
// another agent's runner is refused everywhere.
func TestChatRunnerHandler_CommandsAckAndHistory(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the chat runner handler against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ws, project, agent, other, owner := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'chat runner handler',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	_, err = db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'p',$3)`, project, ws, "chat-"+project.String()[:8])
	require.NoError(t, err)
	for _, a := range []uuid.UUID{agent, other} {
		_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'a',$3,'h','p')`, a, ws, "chat-agent-"+a.String()[:8])
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','u')`, owner, owner.String()+"@chat.test", "chat-"+owner.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, owner) })

	// a thread with two finished messages and one claimed turn holding a live lease
	thread := uuid.New()
	_, err = db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id) VALUES ($1,$2,$3,$4,$5)`, thread, ws, project, agent, owner)
	require.NoError(t, err)
	msg := func(role, body, status, at string) uuid.UUID {
		id := uuid.New()
		_, insErr := db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body, status, created_at) VALUES ($1,$2,$3,$4,$5,$6, now() - $7::interval)`,
			id, ws, thread, role, body, status, at)
		require.NoError(t, insErr)
		return id
	}
	msg("user", "first", "done", "10 minutes")
	second := msg("agent", "second", "done", "9 minutes")
	current := msg("user", "current", "done", "1 minute")
	_, err = db.Exec(`INSERT INTO ext_agent_chat_turns (workspace_id, thread_id, agent_id, user_message_id, state, lease_until, expires_at)
		VALUES ($1,$2,$3,$4,'claimed', now() + interval '1 minute', now() + interval '1 day')`, ws, thread, agent, current)
	require.NoError(t, err)

	h := NewChatRunnerHandler(postgres.NewChatRunnerRepo(db))
	hist := runnerCall(t, h.History, http.MethodGet, "/?limit=1", "", agent, map[string]string{"thread_id": thread.String()})
	require.Equal(t, http.StatusOK, hist.Code, hist.Body.String())
	var page struct {
		Messages []struct {
			MessageID uuid.UUID `json:"message_id"`
		} `json:"messages"`
		HasMore bool `json:"has_more"`
	}
	require.NoError(t, json.Unmarshal(hist.Body.Bytes(), &page))
	require.Len(t, page.Messages, 1)
	require.Equal(t, second, page.Messages[0].MessageID, "the current user message is left out; the last one before it is returned")
	require.True(t, page.HasMore)

	older := runnerCall(t, h.History, http.MethodGet, "/?before="+second.String(), "", agent, map[string]string{"thread_id": thread.String()})
	require.Equal(t, http.StatusOK, older.Code, older.Body.String())
	require.Equal(t, http.StatusForbidden,
		runnerCall(t, h.History, http.MethodGet, "/", "", other, map[string]string{"thread_id": thread.String()}).Code)

	// deleting the thread orders both commands
	del := NewChatThreadDeleteHandler(postgres.NewChatThreadDeleteRepo(db, nil))
	e := echo.New()
	delRec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), delRec)
	c.SetParamNames("proj_id", "thread_id")
	c.SetParamValues(project.String(), thread.String())
	c.Set(mw.ContextKeyUserID, owner)
	require.NoError(t, del.Delete(c))
	require.Equal(t, http.StatusNoContent, delRec.Code)
	// the runtime's runner pin is dropped so a runner that names no id may take them
	_, err = db.Exec(`UPDATE ext_agent_chat_commands SET runner_id = NULL WHERE thread_id = $1`, thread)
	require.NoError(t, err)

	list := func(as uuid.UUID, target string) (int, []struct {
		ID   uuid.UUID `json:"command_id"`
		Kind string    `json:"kind"`
	}) {
		rec := runnerCall(t, h.Commands, http.MethodGet, target, "", as, nil)
		var out struct {
			Commands []struct {
				ID   uuid.UUID `json:"command_id"`
				Kind string    `json:"kind"`
			} `json:"commands"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Commands
	}
	code, cmds := list(agent, "/?agents="+agent.String())
	require.Equal(t, http.StatusOK, code)
	require.Len(t, cmds, 2)
	foreign, _ := list(other, "/?agents="+agent.String())
	require.Equal(t, http.StatusForbidden, foreign)

	ack := func(as, id uuid.UUID, body string) *httptest.ResponseRecorder {
		return runnerCall(t, h.AckCommand, http.MethodPost, "/", body, as, map[string]string{"command_id": id.String()})
	}
	require.Equal(t, http.StatusForbidden, ack(other, cmds[0].ID, `{"result":"done"}`).Code)
	require.Equal(t, http.StatusBadRequest, ack(agent, cmds[0].ID, `{"result":"maybe"}`).Code)
	require.Equal(t, http.StatusBadRequest, ack(agent, cmds[0].ID, `{`).Code)
	require.Equal(t, http.StatusForbidden, ack(agent, uuid.New(), `{"result":"done"}`).Code)
	for _, cmd := range cmds {
		rec := ack(agent, cmd.ID, `{"result":"done","detail":"ok"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, http.StatusOK, ack(agent, cmd.ID, `{"result":"done"}`).Code, "a repeated ack is idempotent")
	}
	_, left := list(agent, "/")
	require.Empty(t, left, "acked commands are not issued again")
}

// Through the HTTP handlers: runners register under their agent, runner_id comes
// from the bearer, a sibling runner and a foreign agent are refused.
func TestChatRunnerHandler_RunnerCredentials(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the chat runner handler against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ws, agent, other := uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'chat runner creds',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	for _, a := range []uuid.UUID{agent, other} {
		_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'a',$3,'h','p')`, a, ws, "chat-agent-"+a.String()[:8])
		require.NoError(t, err)
	}
	h := NewChatRunnerHandler(postgres.NewChatRunnerRepo(db))

	call := func(fn echo.HandlerFunc, method, target, body, token string, as uuid.UUID, params map[string]string) *httptest.ResponseRecorder {
		e := echo.New()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if token != "" {
			req.Header.Set(ChatRunnerTokenHeader, token)
		}
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(mw.ContextKeyAgentID, as)
		var names, values []string
		for k, v := range params {
			names, values = append(names, k), append(values, v)
		}
		c.SetParamNames(names...)
		c.SetParamValues(values...)
		if herr := fn(c); herr != nil {
			e.HTTPErrorHandler(herr, c)
		}
		return rec
	}
	register := func(as uuid.UUID, name string) (uuid.UUID, string) {
		rec := call(h.RegisterRunner, http.MethodPost, "/", `{"name":"`+name+`"}`, "", as, nil)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		var out struct {
			RunnerID uuid.UUID `json:"runner_id"`
			Token    string    `json:"token"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.True(t, strings.HasPrefix(out.Token, postgres.ChatRunnerTokenPrefix))
		return out.RunnerID, out.Token
	}
	require.Equal(t, http.StatusBadRequest, call(h.RegisterRunner, http.MethodPost, "/", `{`, "", agent, nil).Code)
	runnerA, tokA := register(agent, "a")
	runnerB, tokB := register(agent, "b")

	// one order addressed to runner B
	cmd := uuid.New()
	_, err = db.Exec(`INSERT INTO ext_agent_chat_commands (id, workspace_id, agent_id, runner_id, kind, thread_id, expires_at)
		VALUES ($1,$2,$3,$4,'stop_session',$5, now() + interval '1 day')`, cmd, ws, agent, runnerB, uuid.New())
	require.NoError(t, err)

	count := func(rec *httptest.ResponseRecorder) int {
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			Commands []json.RawMessage `json:"commands"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return len(out.Commands)
	}
	require.Equal(t, 1, count(call(h.Commands, http.MethodGet, "/", "", tokB, agent, nil)))
	require.Equal(t, 1, count(call(h.Commands, http.MethodGet, "/?runner_id="+runnerB.String(), "", tokB, agent, nil)))
	require.Equal(t, 0, count(call(h.Commands, http.MethodGet, "/", "", tokA, agent, nil)), "runner A does not see B's order")
	require.Equal(t, 0, count(call(h.Commands, http.MethodGet, "/", "", "", agent, nil)))
	// naming B with A's bearer, B's bearer under another agent, a made-up bearer
	require.Equal(t, http.StatusForbidden, call(h.Commands, http.MethodGet, "/?runner_id="+runnerB.String(), "", tokA, agent, nil).Code)
	require.Equal(t, http.StatusForbidden, call(h.Commands, http.MethodGet, "/", "", tokB, other, nil).Code)
	require.Equal(t, http.StatusForbidden, call(h.Commands, http.MethodGet, "/", "", "crt_nope", agent, nil).Code)

	ack := func(token string, as uuid.UUID) *httptest.ResponseRecorder {
		return call(h.AckCommand, http.MethodPost, "/", `{"result":"done"}`, token, as, map[string]string{"command_id": cmd.String()})
	}
	require.Equal(t, http.StatusForbidden, ack(tokA, agent).Code, "sibling runner cannot ack")
	require.Equal(t, http.StatusForbidden, ack("", agent).Code, "no runner credential cannot ack")
	require.Equal(t, http.StatusOK, ack(tokB, agent).Code)

	// revoke: foreign agent 404, bad id 400, owner 204, then the bearer is dead
	revoke := func(as uuid.UUID, id string) int {
		return call(h.RevokeRunner, http.MethodDelete, "/", "", "", as, map[string]string{"runner_id": id}).Code
	}
	require.Equal(t, http.StatusBadRequest, revoke(agent, "nope"))
	require.Equal(t, http.StatusNotFound, revoke(other, runnerA.String()))
	require.Equal(t, http.StatusNoContent, revoke(agent, runnerA.String()))
	require.Equal(t, http.StatusForbidden, call(h.Commands, http.MethodGet, "/", "", tokA, agent, nil).Code)
}
