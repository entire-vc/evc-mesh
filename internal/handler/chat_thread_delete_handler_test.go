package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestChatThreadDeleteHandler_RejectsMalformedIDs(t *testing.T) {
	h := NewChatThreadDeleteHandler(nil) // the id checks run before the repo is touched
	for _, tc := range []struct{ name, proj, thread string }{
		{"bad project", "nope", uuid.NewString()},
		{"nil project", uuid.Nil.String(), uuid.NewString()},
		{"bad thread", uuid.NewString(), "nope"},
		{"nil thread", uuid.NewString(), uuid.Nil.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), rec)
			c.SetParamNames("proj_id", "thread_id")
			c.SetParamValues(tc.proj, tc.thread)
			require.NoError(t, h.Delete(c))
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestChatThreadDeleteHandler_DeletesOwnThreadOnly(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the chat thread delete handler against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ws, project, agent, owner := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'chat delete handler',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	_, err = db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'p',$3)`, project, ws, "chat-"+project.String()[:8])
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'a',$3,'h','p')`, agent, ws, "chat-agent-"+agent.String()[:8])
	require.NoError(t, err)
	for _, u := range []uuid.UUID{owner} {
		_, err = db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','u')`, u, u.String()+"@chat.test", "chat-"+u.String()[:30])
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, u) })
	}
	thread := uuid.New()
	_, err = db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id) VALUES ($1,$2,$3,$4,$5)`, thread, ws, project, agent, owner)
	require.NoError(t, err)

	h := NewChatThreadDeleteHandler(postgres.NewChatThreadDeleteRepo(db, nil))
	call := func(user uuid.UUID) int {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", http.NoBody), rec)
		c.SetParamNames("proj_id", "thread_id")
		c.SetParamValues(project.String(), thread.String())
		c.Set(mw.ContextKeyUserID, user)
		require.NoError(t, h.Delete(c))
		return rec.Code
	}
	require.Equal(t, http.StatusNotFound, call(uuid.New()), "another user's thread is a 404")
	require.Equal(t, http.StatusNoContent, call(owner))
	require.Equal(t, http.StatusNotFound, call(owner), "a repeated DELETE is a 404")
}
