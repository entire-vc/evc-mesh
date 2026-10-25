package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

type agentChatEnv struct {
	db      *sqlx.DB
	h       *AgentChatHandler
	del     echo.HandlerFunc // DELETE lives in ChatThreadDeleteHandler (soft delete + purge)
	ws      uuid.UUID
	project uuid.UUID
	agent   uuid.UUID
	owner   uuid.UUID // workspace owner
	userA   uuid.UUID // plain project member
	userB   uuid.UUID // plain project member
}

func seedChatMember(t *testing.T, db *sqlx.DB, ws, project uuid.UUID, wsRole, projectRole string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	short := id.String()[:8]
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','chat user')`,
		id, "chat-"+short+"@example.com", "chat-"+short)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4)`, uuid.New(), ws, id, wsRole)
	require.NoError(t, err)
	if projectRole != "" {
		_, err = db.ExecContext(ctx, `INSERT INTO project_members (id, project_id, user_id, workspace_id, role) VALUES ($1,$2,$3,$4,$5)`,
			uuid.New(), project, id, ws, projectRole)
		require.NoError(t, err)
	}
	return id
}

// newAgentChatEnv builds a workspace with the chat switched on for one project,
// one runner-served agent, and two ordinary members.
func newAgentChatEnv(t *testing.T) *agentChatEnv {
	t.Helper()
	db := runtimeHTTPDB(t)
	ws, owner := seedRuntimeWorkspace(t, db)
	project, _ := seedChatProject(t, db, ws)
	_, err := db.ExecContext(context.Background(), `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'owner') ON CONFLICT (workspace_id, user_id) DO NOTHING`, uuid.New(), ws, owner)
	require.NoError(t, err)
	agent := seedOperatorAgent(t, db, ws, &owner)
	addChatMember(t, db, project, ws, agent)
	giveRunner(t, db, agent, `now() + interval '1 hour'`)
	env := &agentChatEnv{db: db, ws: ws, project: project, agent: agent, owner: owner,
		userA: seedChatMember(t, db, ws, project, "member", "member"),
		userB: seedChatMember(t, db, ws, project, "member", "member"),
		del:   NewChatThreadDeleteHandler(postgres.NewChatThreadDeleteRepo(db, nil)).Delete,
		h:     NewAgentChatHandler(postgres.NewAgentChatRepo(db), postgres.NewIntegrationRepo(db), postgres.NewProjectIntegrationRepo(db))}

	status, out := env.call(t, env.h.PutWorkspaceSettings, http.MethodPut, `{}`, env.owner, map[string]string{"ws_id": ws.String()})
	require.Equal(t, http.StatusOK, status, "%v", out)
	status, out = env.call(t, env.h.PutProjectSettings, http.MethodPut,
		fmt.Sprintf(`{"enabled":true,"agents":[%q]}`, agent), env.owner, map[string]string{"proj_id": project.String()})
	require.Equal(t, http.StatusOK, status, "%v", out)
	return env
}

func (e *agentChatEnv) call(t *testing.T, fn echo.HandlerFunc, method, body string, user uuid.UUID, params map[string]string) (status int, out map[string]any) {
	t.Helper()
	return callUserRuntime(t, fn, method, "/", body, user, uuid.Nil, params)
}

func (e *agentChatEnv) proj() map[string]string {
	return map[string]string{"proj_id": e.project.String()}
}

func (e *agentChatEnv) thr(thread string) map[string]string {
	return map[string]string{"proj_id": e.project.String(), "thread_id": thread}
}

func (e *agentChatEnv) createThread(t *testing.T, user uuid.UUID) string {
	t.Helper()
	status, out := e.call(t, e.h.CreateThread, http.MethodPost, fmt.Sprintf(`{"agent_id":%q}`, e.agent), user, e.proj())
	require.Equal(t, http.StatusCreated, status, "%v", out)
	return out["id"].(string)
}

func (e *agentChatEnv) send(t *testing.T, user uuid.UUID, thread, key, body string) (status int, out map[string]any) {
	t.Helper()
	return e.call(t, e.h.SendMessage, http.MethodPost, fmt.Sprintf(`{"client_message_id":%q,"body":%q}`, key, body), user, e.thr(thread))
}

// finishTurns plays the runner's part: every unfinished turn of the thread ends done.
func (e *agentChatEnv) finishTurns(t *testing.T, thread string) {
	t.Helper()
	_, err := e.db.ExecContext(context.Background(), `UPDATE ext_agent_chat_turns SET state='done', finished_at=now() WHERE thread_id=$1 AND state IN ('queued','claimed','streaming')`, thread)
	require.NoError(t, err)
}

func (e *agentChatEnv) threadExists(t *testing.T, thread string) bool {
	t.Helper()
	var n int
	require.NoError(t, e.db.GetContext(context.Background(), &n, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NULL`, thread))
	return n == 1
}

func ids(out map[string]any) []string {
	var got []string
	for _, it := range out["items"].([]any) {
		got = append(got, it.(map[string]any)["id"].(string))
	}
	return got
}

// Privacy rule: a thread belongs to its author. Another member, and a workspace
// owner, cannot see, read, write to, stop, rename or delete it, and every such
// attempt is the same 404 as for an id that never existed.
//
// Red control: replacing `t.user_id = $3` in chatOwnThread (repository/postgres/agent_chat_repo.go)
// with a predicate that ignores the caller makes this test and
// TestAgentChatRepo_ThreadsAreOwnedByTheirUser fail on the "Should be empty" assertions.
func TestAgentChatPrivacy_OtherUserCannotSeeOrDeleteThread(t *testing.T) {
	env := newAgentChatEnv(t)
	thread := env.createThread(t, env.userA)
	status, _ := env.send(t, env.userA, thread, "key-aaaaaaaa", "secret plan")
	require.Equal(t, http.StatusAccepted, status)

	// The author sees the thread and its message.
	status, out := env.call(t, env.h.ListThreads, http.MethodGet, "", env.userA, env.proj())
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []string{thread}, ids(out))
	status, out = env.call(t, env.h.ListMessages, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	require.Len(t, out["items"], 1)

	missing := uuid.NewString()
	_, wantBody := env.call(t, env.h.GetThread, http.MethodGet, "", env.userB, env.thr(missing))
	require.Equal(t, "thread_not_found", wantBody["error_code"])

	for _, stranger := range []struct {
		name string
		user uuid.UUID
	}{{"another member", env.userB}, {"workspace owner", env.owner}} {
		t.Run(stranger.name, func(t *testing.T) {
			code, listed := env.call(t, env.h.ListThreads, http.MethodGet, "", stranger.user, env.proj())
			require.Equal(t, http.StatusOK, code)
			require.Empty(t, ids(listed), "list must not contain someone else's thread")

			for name, fn := range map[string]struct {
				h      echo.HandlerFunc
				method string
				body   string
			}{
				"get":      {env.h.GetThread, http.MethodGet, ""},
				"rename":   {env.h.RenameThread, http.MethodPatch, `{"title":"mine now"}`},
				"messages": {env.h.ListMessages, http.MethodGet, ""},
				"send":     {env.h.SendMessage, http.MethodPost, `{"client_message_id":"key-stranger","body":"hi"}`},
				"stop":     {env.h.StopTurn, http.MethodPost, ""},
			} {
				got, body := env.call(t, fn.h, fn.method, fn.body, stranger.user, env.thr(thread))
				require.Equal(t, http.StatusNotFound, got, name)
				require.Equal(t, wantBody, body, "%s: a stranger's thread must look exactly like a missing one", name)
			}
			// DELETE is served by the retention handler: same rule, its own error shape.
			code, body := env.call(t, env.del, http.MethodDelete, "", stranger.user, env.thr(thread))
			_, missingBody := env.call(t, env.del, http.MethodDelete, "", stranger.user, env.thr(missing))
			require.Equal(t, http.StatusNotFound, code)
			require.Equal(t, missingBody, body, "delete: a stranger's thread must look exactly like a missing one")
		})
	}

	require.True(t, env.threadExists(t, thread), "the stranger's delete must not have touched the thread")
	var msgs int
	require.NoError(t, env.db.GetContext(context.Background(), &msgs, `SELECT count(*) FROM ext_agent_chat_messages WHERE thread_id=$1`, thread))
	require.Equal(t, 1, msgs, "the stranger's send must not have written a message")
	status, out = env.call(t, env.h.GetThread, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status, "%v", out)
	require.Equal(t, "secret plan", out["title"])
}

func TestAgentChatThreads_AutoTitleRenameAndPaging(t *testing.T) {
	env := newAgentChatEnv(t)
	thread := env.createThread(t, env.userA)
	status, out := env.call(t, env.h.GetThread, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", out["title"], "empty until the first message")
	require.Equal(t, "stopped", out["session_state"])

	long := strings.Repeat("ёж ", 40)
	status, _ = env.send(t, env.userA, thread, "key-title-01", long)
	require.Equal(t, http.StatusAccepted, status)
	_, out = env.call(t, env.h.GetThread, http.MethodGet, "", env.userA, env.thr(thread))
	title := out["title"].(string)
	require.Equal(t, 60, len([]rune(title)), "auto-title is the first 60 characters")
	require.Equal(t, false, out["title_is_custom"])

	env.finishTurns(t, thread)
	status, _ = env.send(t, env.userA, thread, "key-title-02", "second message")
	require.Equal(t, http.StatusAccepted, status)
	_, out = env.call(t, env.h.GetThread, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, title, out["title"], "later messages never retitle")

	status, out = env.call(t, env.h.RenameThread, http.MethodPatch, `{"title":"Release notes"}`, env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "Release notes", out["title"])
	require.Equal(t, true, out["title_is_custom"])
	status, _ = env.call(t, env.h.RenameThread, http.MethodPatch, `{"title":"   "}`, env.userA, env.thr(thread))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = env.call(t, env.h.RenameThread, http.MethodPatch, fmt.Sprintf(`{"title":%q}`, strings.Repeat("x", 121)), env.userA, env.thr(thread))
	require.Equal(t, http.StatusUnprocessableEntity, status)

	// A thread created with a title keeps it through the first message.
	status, out = env.call(t, env.h.CreateThread, http.MethodPost, fmt.Sprintf(`{"agent_id":%q,"title":"Mine"}`, env.agent), env.userA, env.proj())
	require.Equal(t, http.StatusCreated, status)
	named := out["id"].(string)
	env.send(t, env.userA, named, "key-title-03", "something else")
	_, out = env.call(t, env.h.GetThread, http.MethodGet, "", env.userA, env.thr(named))
	require.Equal(t, "Mine", out["title"])

	// Paging: two threads, page size one, the cursor leads to the other.
	status, out = callUserRuntimeQuery(t, env.h.ListThreads, "/?limit=1", env.userA, env.proj())
	require.Equal(t, http.StatusOK, status)
	require.Len(t, out["items"], 1)
	require.Equal(t, true, out["has_more"])
	first := ids(out)[0]
	status, out = callUserRuntimeQuery(t, env.h.ListThreads, "/?limit=1&cursor="+out["next_cursor"].(string), env.userA, env.proj())
	require.Equal(t, http.StatusOK, status)
	require.Len(t, out["items"], 1)
	require.NotEqual(t, first, ids(out)[0])
	require.Equal(t, false, out["has_more"])
}

// A turn without an executor waits in queued, and the API says so.
func TestAgentChatTurns_QueuedWithoutRunnerStopAndIdempotency(t *testing.T) {
	env := newAgentChatEnv(t)
	_, err := env.db.ExecContext(context.Background(), `UPDATE agent_controllers SET lease_expires_at = now() - interval '1 minute' WHERE agent_id=$1`, env.agent)
	require.NoError(t, err)
	thread := env.createThread(t, env.userA)

	status, out := env.send(t, env.userA, thread, "key-queued-1", "anyone there?")
	require.Equal(t, http.StatusAccepted, status, "%v", out)
	turn := out["turn"].(map[string]any)
	require.Equal(t, "queued", turn["state"])
	require.Equal(t, true, turn["agent_offline"])
	require.EqualValues(t, 1, turn["queue_position"])
	require.Equal(t, "queued", out["message"].(map[string]any)["status"])
	messageID := out["message"].(map[string]any)["id"]

	_, out = env.call(t, env.h.GetThread, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, "queued", out["active_turn"].(map[string]any)["state"])
	_, out = env.call(t, env.h.ListMessages, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, "queued", out["active_turn"].(map[string]any)["state"])

	// Retry with the same key: the original answer, no second turn.
	status, out = env.send(t, env.userA, thread, "key-queued-1", "anyone there?")
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, messageID, out["message"].(map[string]any)["id"])
	var turns int
	require.NoError(t, env.db.GetContext(context.Background(), &turns, `SELECT count(*) FROM ext_agent_chat_turns WHERE thread_id=$1`, thread))
	require.Equal(t, 1, turns)

	// A new message while a turn is unfinished is refused.
	status, out = env.send(t, env.userA, thread, "key-queued-2", "hello?")
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "turn_in_progress", out["error_code"])

	status, out = env.call(t, env.h.StopTurn, http.MethodPost, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, true, out["stopped"])
	require.Equal(t, "cancelled", out["turn"].(map[string]any)["state"])
	status, out = env.call(t, env.h.StopTurn, http.MethodPost, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, false, out["stopped"], "stop is idempotent")

	status, out = env.send(t, env.userA, thread, "key-queued-3", "now?")
	require.Equal(t, http.StatusAccepted, status, "a stopped thread takes the next message: %v", out)

	status, _ = env.send(t, env.userA, thread, "key-empty-001", "   ")
	require.Equal(t, http.StatusUnprocessableEntity, status)
	env.finishTurns(t, thread) // the attachment is checked against an idle thread
	status, out = env.call(t, env.h.SendMessage, http.MethodPost,
		fmt.Sprintf(`{"client_message_id":"key-att-0001","body":"x","attachment_ids":[%q]}`, uuid.New()), env.userA, env.thr(thread))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "attachment_not_found", out["error_code"])
}

func TestAgentChatSend_BindsUploadedAttachment(t *testing.T) {
	env := newAgentChatEnv(t)
	thread := env.createThread(t, env.userA)
	ctx := context.Background()
	insert := func(uploaded bool) uuid.UUID {
		id := uuid.New()
		var at any
		if uploaded {
			at = time.Now()
		}
		_, err := env.db.ExecContext(ctx, `INSERT INTO ext_agent_chat_attachments
			(id, workspace_id, thread_id, s3_key, file_name, mime, size, uploaded_at) VALUES ($1,$2,$3,$4,'a.png','image/png',10,$5)`,
			id, env.ws, thread, "k/"+id.String(), at)
		require.NoError(t, err)
		return id
	}
	send := func(key string, ids ...uuid.UUID) (int, map[string]any) {
		list, _ := json.Marshal(ids)
		return env.call(t, env.h.SendMessage, http.MethodPost,
			fmt.Sprintf(`{"client_message_id":%q,"body":"","attachment_ids":%s}`, key, list), env.userA, env.thr(thread))
	}

	pending := insert(false)
	status, out := send("key-att-pend1", pending)
	require.Equal(t, http.StatusUnprocessableEntity, status, "an attachment that never reached storage is not sendable")
	require.Equal(t, "attachment_not_found", out["error_code"])

	ok := insert(true)
	status, out = send("key-att-ok-01", ok)
	require.Equal(t, http.StatusAccepted, status, "an image-only message is a message: %v", out)
	atts := out["message"].(map[string]any)["attachments"].([]any)
	require.Len(t, atts, 1)
	require.Equal(t, ok.String(), atts[0].(map[string]any)["id"])

	env.finishTurns(t, thread)
	status, _ = send("key-att-reuse1", ok)
	require.Equal(t, http.StatusUnprocessableEntity, status, "an attachment is bound once")

	status, out = env.call(t, env.h.ListMessages, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	bound := 0
	for _, it := range out["items"].([]any) {
		bound += len(it.(map[string]any)["attachments"].([]any))
	}
	require.Equal(t, 1, bound, "history carries the attachment")
}

func TestAgentChatThreads_DeleteIsSoftAndCancelsTheTurn(t *testing.T) {
	env := newAgentChatEnv(t)
	thread := env.createThread(t, env.userA)
	env.send(t, env.userA, thread, "key-delete-1", "to be deleted")

	status, _ := env.call(t, env.del, http.MethodDelete, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusNoContent, status)
	status, out := env.call(t, env.h.GetThread, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "thread_not_found", out["error_code"])
	status, _ = env.call(t, env.del, http.MethodDelete, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusNotFound, status, "a repeat delete is the same 404")

	var deleted, cancelled int
	require.NoError(t, env.db.GetContext(context.Background(), &deleted, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1 AND deleted_at IS NOT NULL`, thread))
	require.Equal(t, 1, deleted, "the row stays for the 30-day purge")
	require.NoError(t, env.db.GetContext(context.Background(), &cancelled, `SELECT count(*) FROM ext_agent_chat_turns WHERE thread_id=$1 AND state='cancelled'`, thread))
	require.Equal(t, 1, cancelled)
	_, out = env.call(t, env.h.ListThreads, http.MethodGet, "", env.userA, env.proj())
	require.Empty(t, ids(out))
}

func TestAgentChatGate_RolesEnabledAndAgents(t *testing.T) {
	env := newAgentChatEnv(t)
	viewer := seedChatMember(t, env.db, env.ws, env.project, "member", "viewer")
	outsider := seedChatMember(t, env.db, env.ws, env.project, "member", "")

	status, out := env.call(t, env.h.ListThreads, http.MethodGet, "", viewer, env.proj())
	require.Equal(t, http.StatusForbidden, status, "viewer is not in allowed_roles")
	require.Equal(t, "chat_not_allowed", out["error_code"])
	status, _ = env.call(t, env.h.ListThreads, http.MethodGet, "", outsider, env.proj())
	require.Equal(t, http.StatusForbidden, status, "not a project member")

	status, out = callUserRuntime(t, env.h.ListThreads, http.MethodGet, "/", "", uuid.Nil, env.agent, env.proj())
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "user_only", out["error_code"])

	// Another agent of the project that is not in the chat list.
	other := seedOperatorAgent(t, env.db, env.ws, &env.owner)
	addChatMember(t, env.db, env.project, env.ws, other)
	giveRunner(t, env.db, other, `now() + interval '1 hour'`)
	status, out = env.call(t, env.h.CreateThread, http.MethodPost, fmt.Sprintf(`{"agent_id":%q}`, other), env.userA, env.proj())
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "agent_not_available", out["error_code"])

	// Roles can be widened by the project settings.
	status, _ = env.call(t, env.h.PutProjectSettings, http.MethodPut,
		fmt.Sprintf(`{"enabled":true,"agents":[%q],"allowed_roles":["admin","member","viewer"]}`, env.agent), env.owner, env.proj())
	require.Equal(t, http.StatusOK, status)
	status, _ = env.call(t, env.h.ListThreads, http.MethodGet, "", viewer, env.proj())
	require.Equal(t, http.StatusOK, status)

	// Off in the project: 404 chat_not_enabled, for everyone.
	status, _ = env.call(t, env.h.PutProjectSettings, http.MethodPut,
		fmt.Sprintf(`{"enabled":false,"agents":[%q]}`, env.agent), env.owner, env.proj())
	require.Equal(t, http.StatusOK, status)
	status, out = env.call(t, env.h.ListThreads, http.MethodGet, "", env.userA, env.proj())
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "chat_not_enabled", out["error_code"])
}

func TestAgentChatSettings_Validation(t *testing.T) {
	env := newAgentChatEnv(t)
	wsParams := map[string]string{"ws_id": env.ws.String()}

	status, out := env.call(t, env.h.GetWorkspaceSettings, http.MethodGet, "", env.owner, wsParams)
	require.Equal(t, http.StatusOK, status)
	require.EqualValues(t, 25, out["max_attachment_mb"])
	require.EqualValues(t, 10, out["session_idle_minutes"])
	require.EqualValues(t, 24, out["offline_expiry_hours"])

	status, _ = env.call(t, env.h.PutWorkspaceSettings, http.MethodPut, `{"max_attachment_mb":26}`, env.owner, wsParams)
	require.Equal(t, http.StatusUnprocessableEntity, status, "the Docs limit is a ceiling")
	status, _ = env.call(t, env.h.PutWorkspaceSettings, http.MethodPut, `{"session_idle_minute":5}`, env.owner, wsParams)
	require.Equal(t, http.StatusUnprocessableEntity, status, "unknown workspace key is rejected, not dropped")
	status, _ = env.call(t, env.h.PutProjectSettings, http.MethodPut, `{"enabled":true,"agent":[]}`, env.owner, env.proj())
	require.Equal(t, http.StatusUnprocessableEntity, status, "unknown project key is rejected")
	status, out = env.call(t, env.h.PutWorkspaceSettings, http.MethodPut, `{"max_attachment_mb":5,"history_retention_days":30}`, env.owner, wsParams)
	require.Equal(t, http.StatusOK, status)
	require.EqualValues(t, 5, out["max_attachment_mb"])
	require.EqualValues(t, 10, out["session_idle_minutes"], "omitted fields take their defaults")
	_, out = env.call(t, env.h.GetWorkspaceSettings, http.MethodGet, "", env.owner, wsParams)
	require.EqualValues(t, 30, out["history_retention_days"])

	// An agent without a runner cannot be chosen; neither can one outside the project.
	loner := seedOperatorAgent(t, env.db, env.ws, &env.owner)
	addChatMember(t, env.db, env.project, env.ws, loner)
	status, out = env.call(t, env.h.PutProjectSettings, http.MethodPut,
		fmt.Sprintf(`{"enabled":true,"agents":[%q,%q]}`, env.agent, loner), env.owner, env.proj())
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "agent_not_available", out["error_code"])
	status, _ = env.call(t, env.h.PutProjectSettings, http.MethodPut, `{"enabled":true,"agents":[]}`, env.owner, env.proj())
	require.Equal(t, http.StatusUnprocessableEntity, status, "enabling needs an agent")
	status, _ = env.call(t, env.h.PutProjectSettings, http.MethodPut,
		fmt.Sprintf(`{"enabled":true,"agents":[%q],"allowed_roles":["owner"]}`, env.agent), env.owner, env.proj())
	require.Equal(t, http.StatusUnprocessableEntity, status)
}

// callUserRuntimeQuery is callUserRuntime with a request URI that carries a query string.
func callUserRuntimeQuery(t *testing.T, fn echo.HandlerFunc, uri string, user uuid.UUID, params map[string]string) (status int, out map[string]any) {
	t.Helper()
	return callUserRuntime(t, fn, http.MethodGet, uri, "", user, uuid.Nil, params)
}

// A presigned upload is confirmed at send time: the client PUTs straight to S3
// and never calls the API again, so the object itself is the proof.
func TestAgentChat_SendConfirmsPresignedUpload(t *testing.T) {
	f := newAgentChatEnv(t)
	store := &fakeChatStore{objects: map[string]int64{}}
	atts := postgres.NewChatAttachmentRepo(f.db)
	f.h.WithAttachments(atts, store)
	ah := NewChatAttachmentHandler(atts, store)
	thread := f.createThread(t, f.userA)

	presign := func(size int64) uuid.UUID {
		status, out := callUserRuntime(t, ah.Presign, http.MethodPost, "/", presignBody(uuid.MustParse(thread), size), f.userA, uuid.Nil, f.proj())
		require.Equal(t, http.StatusCreated, status, "%v", out)
		return uuid.MustParse(out["attachment_id"].(string))
	}
	sendWith := func(key string, id uuid.UUID) (int, map[string]any) {
		return f.call(t, f.h.SendMessage, http.MethodPost,
			fmt.Sprintf(`{"client_message_id":%q,"body":"look","attachment_ids":[%q]}`, key, id), f.userA, f.thr(thread))
	}

	// No object in storage: refused, and the row stays unconfirmed.
	missing := presign(1024)
	status, out := sendWith("att-missing", missing)
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	require.Contains(t, fmt.Sprint(out), "attachment_not_found")

	// Object of another size than declared: refused.
	wrong := presign(1024)
	store.objects[chatAttachmentKey(f.ws, wrong)] = 1
	status, out = sendWith("att-wrong-size", wrong)
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", out)
	require.Contains(t, fmt.Sprint(out), "attachment_not_found")

	// Object present with the declared size: accepted, attachment in the answer.
	ok := presign(1024)
	store.objects[chatAttachmentKey(f.ws, ok)] = 1024
	status, out = sendWith("att-ok-send", ok)
	require.Equal(t, http.StatusAccepted, status, "%v", out)
	msg := out["message"].(map[string]any)
	require.Len(t, msg["attachments"], 1)
}

// History contract: the agent's reply comes back from
// GET .../threads/:thread_id/messages with role "assistant", which the web
// feed and the agent-chat e2e harness wait for. The table stores 'agent'.
//
// Red control: on main before ChatWireRole the reply role is "agent".
func TestAgentChatHistory_AgentReplyRoleIsAssistant(t *testing.T) {
	env := newAgentChatEnv(t)
	thread := env.createThread(t, env.userA)
	status, _ := env.send(t, env.userA, thread, "key-role0001", "ping")
	require.Equal(t, http.StatusAccepted, status)
	_, err := env.db.ExecContext(context.Background(), `INSERT INTO ext_agent_chat_messages (workspace_id, thread_id, role, body, status)
		VALUES ($1, $2, 'agent', 'pong', 'done')`, env.ws, thread)
	require.NoError(t, err)

	status, out := env.call(t, env.h.ListMessages, http.MethodGet, "", env.userA, env.thr(thread))
	require.Equal(t, http.StatusOK, status)
	roles := map[string]string{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		roles[m["body"].(string)] = m["role"].(string)
	}
	require.Equal(t, map[string]string{"ping": "user", "pong": "assistant"}, roles)
}
