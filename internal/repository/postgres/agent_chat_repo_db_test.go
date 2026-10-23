package postgres

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type chatFixture struct {
	db      *sqlx.DB
	repo    *AgentChatRepo
	ws      uuid.UUID
	project uuid.UUID
	agent   uuid.UUID
	userA   uuid.UUID
	userB   uuid.UUID
	owner   uuid.UUID
}

func chatExec(t *testing.T, db *sqlx.DB, q string, args ...any) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), q, args...)
	require.NoError(t, err, q)
}

func chatUser(t *testing.T, db *sqlx.DB, ws, project uuid.UUID, wsRole, projectRole string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	short := id.String()[:8]
	chatExec(t, db, `INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','chat user')`,
		id, "repo-chat-"+short+"@example.com", "repo-chat-"+short)
	chatExec(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4)`, uuid.New(), ws, id, wsRole)
	if projectRole != "" {
		chatExec(t, db, `INSERT INTO project_members (id, project_id, user_id, workspace_id, role) VALUES ($1,$2,$3,$4,$5)`,
			uuid.New(), project, id, ws, projectRole)
	}
	return id
}

func newChatFixture(t *testing.T) *chatFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the agent-chat repository tests")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	f := &chatFixture{db: db, repo: NewAgentChatRepo(db), ws: uuid.New(), project: uuid.New(), agent: uuid.New()}
	short := f.ws.String()[:8]
	f.owner = uuid.New()
	chatExec(t, db, `INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','owner')`,
		f.owner, "repo-chat-own-"+short+"@example.com", "repo-chat-own-"+short)
	chatExec(t, db, `INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'chat repo',$2,$3)`, f.ws, "chat-repo-"+short, f.owner)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, f.ws) })
	chatExec(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'owner')`, uuid.New(), f.ws, f.owner)
	chatExec(t, db, `INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'chat',$3)`, f.project, f.ws, "chat-"+short)
	chatExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix, max_concurrent_tasks)
		VALUES ($1,$2,$3,$3,'x',$4,5)`, f.agent, f.ws, "ag-"+short, short)
	chatExec(t, db, `INSERT INTO project_members (id, project_id, agent_id, workspace_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.project, f.agent, f.ws)
	chatExec(t, db, `INSERT INTO integration_configs (id, workspace_id, provider, config, is_active) VALUES ($1,$2,'agent-chat','{}',true)`, uuid.New(), f.ws)
	chatExec(t, db, `INSERT INTO project_integrations (id, project_id, type, extension_id, enabled, settings) VALUES ($1,$2,'agent-chat','agent-chat',true,$3)`,
		uuid.New(), f.project, `{"agents":["`+f.agent.String()+`"],"allowed_roles":["admin","member"]}`)
	f.userA = chatUser(t, db, f.ws, f.project, "member", "member")
	f.userB = chatUser(t, db, f.ws, f.project, "member", "member")
	return f
}

func (f *chatFixture) gate(t *testing.T, user uuid.UUID) *ChatGate {
	t.Helper()
	g, err := f.repo.Gate(context.Background(), UserRuntimeActor{UserID: user}, f.project)
	require.NoError(t, err)
	return g
}

func requireChatErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, status, ae.Code)
	require.Equal(t, code, ae.ErrorCode)
}

func TestAgentChatRepo_Gate(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()

	g := f.gate(t, f.userA)
	require.Equal(t, f.ws, g.WorkspaceID)
	require.Equal(t, "member", g.Role)
	require.True(t, g.Project.HasAgent(f.agent))

	// A workspace owner has no project_members row and is treated as admin.
	require.Equal(t, "admin", f.gate(t, f.owner).Role)

	_, err := f.repo.Gate(ctx, UserRuntimeActor{AgentID: f.agent}, f.project)
	requireChatErr(t, err, http.StatusForbidden, ChatCodeUserOnly)
	_, err = f.repo.Gate(ctx, UserRuntimeActor{UserID: f.userA, Connector: true}, f.project)
	requireChatErr(t, err, http.StatusForbidden, ChatCodeUserOnly)

	_, err = f.repo.Gate(ctx, UserRuntimeActor{UserID: f.userA}, uuid.New())
	require.Error(t, err)

	viewer := chatUser(t, f.db, f.ws, f.project, "member", "viewer")
	_, err = f.repo.Gate(ctx, UserRuntimeActor{UserID: viewer}, f.project)
	requireChatErr(t, err, http.StatusForbidden, ChatCodeNotAllowed)

	outsider := chatUser(t, f.db, f.ws, f.project, "member", "")
	_, err = f.repo.Gate(ctx, UserRuntimeActor{UserID: outsider}, f.project)
	requireChatErr(t, err, http.StatusForbidden, ChatCodeNotAllowed)

	chatExec(t, f.db, `UPDATE project_integrations SET settings = '{"agents":[]}' WHERE project_id = $1`, f.project)
	_, err = f.repo.Gate(ctx, UserRuntimeActor{UserID: f.userA}, f.project)
	requireChatErr(t, err, http.StatusNotFound, ChatCodeNotEnabled)

	chatExec(t, f.db, `UPDATE project_integrations SET enabled = false WHERE project_id = $1`, f.project)
	_, err = f.repo.Gate(ctx, UserRuntimeActor{UserID: f.userA}, f.project)
	requireChatErr(t, err, http.StatusNotFound, ChatCodeNotEnabled)
}

func TestAgentChatRepo_ThreadsAreOwnedByTheirUser(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	g := f.gate(t, f.userA)

	_, err := f.repo.CreateThread(ctx, g, f.project, f.userA, uuid.New(), "")
	requireChatErr(t, err, http.StatusUnprocessableEntity, ChatCodeAgentNA)
	_, err = f.repo.CreateThread(ctx, g, f.project, f.userA, f.agent, strings.Repeat("x", ChatTitleMax+1))
	requireChatErr(t, err, http.StatusUnprocessableEntity, ChatCodeValidationError)

	named, err := f.repo.CreateThread(ctx, g, f.project, f.userA, f.agent, "  Mine  ")
	require.NoError(t, err)
	require.Equal(t, "Mine", named.Title)
	require.True(t, named.TitleIsCustom)
	untitled, err := f.repo.CreateThread(ctx, g, f.project, f.userA, f.agent, "")
	require.NoError(t, err)
	require.False(t, untitled.TitleIsCustom)

	got, err := f.repo.GetThread(ctx, f.ws, f.project, f.userA, named.ID)
	require.NoError(t, err)
	require.Equal(t, named.ID, got.ID)

	// The same answer for someone else's thread and one that never existed.
	_, errOther := f.repo.GetThread(ctx, f.ws, f.project, f.userB, named.ID)
	_, errMissing := f.repo.GetThread(ctx, f.ws, f.project, f.userA, uuid.New())
	requireChatErr(t, errOther, http.StatusNotFound, ChatCodeThreadNotFound)
	require.Equal(t, errMissing, errOther)
	_, err = f.repo.RenameThread(ctx, f.ws, f.project, f.userB, named.ID, "stolen")
	requireChatErr(t, err, http.StatusNotFound, ChatCodeThreadNotFound)
	_, err = f.repo.StopTurn(ctx, f.ws, f.project, f.userB, named.ID)
	requireChatErr(t, err, http.StatusNotFound, ChatCodeThreadNotFound)
	_, err = f.repo.ListMessages(ctx, f.ws, f.project, f.userB, named.ID, 10, "")
	requireChatErr(t, err, http.StatusNotFound, ChatCodeThreadNotFound)

	mine, err := f.repo.ListThreads(ctx, f.ws, f.project, f.userA, uuid.Nil, 10, "")
	require.NoError(t, err)
	require.Len(t, mine.Items, 2)
	theirs, err := f.repo.ListThreads(ctx, f.ws, f.project, f.userB, uuid.Nil, 10, "")
	require.NoError(t, err)
	require.Empty(t, theirs.Items)

	// Paging walks every thread once; the agent filter narrows only.
	first, err := f.repo.ListThreads(ctx, f.ws, f.project, f.userA, uuid.Nil, 1, "")
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.True(t, first.HasMore)
	require.NotNil(t, first.NextCursor)
	second, err := f.repo.ListThreads(ctx, f.ws, f.project, f.userA, f.agent, 1, *first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
	none, err := f.repo.ListThreads(ctx, f.ws, f.project, f.userA, uuid.New(), 10, "")
	require.NoError(t, err)
	require.Empty(t, none.Items)
	_, err = f.repo.ListThreads(ctx, f.ws, f.project, f.userA, uuid.Nil, 10, "not-a-cursor")
	requireChatErr(t, err, http.StatusBadRequest, ChatCodeInvalidCursor)

	renamed, err := f.repo.RenameThread(ctx, f.ws, f.project, f.userA, untitled.ID, " Plan ")
	require.NoError(t, err)
	require.Equal(t, "Plan", renamed.Title)
	require.True(t, renamed.TitleIsCustom)
	_, err = f.repo.RenameThread(ctx, f.ws, f.project, f.userA, untitled.ID, strings.Repeat("x", ChatTitleMax+1))
	requireChatErr(t, err, http.StatusUnprocessableEntity, ChatCodeValidationError)

	chatExec(t, f.db, `UPDATE ext_agent_chat_threads SET deleted_at = now() WHERE id = $1`, named.ID)
	_, err = f.repo.GetThread(ctx, f.ws, f.project, f.userA, named.ID)
	requireChatErr(t, err, http.StatusNotFound, ChatCodeThreadNotFound)
}

func TestAgentChatRepo_TurnsWaitInQueueAndStop(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	g := f.gate(t, f.userA)
	th, err := f.repo.CreateThread(ctx, g, f.project, f.userA, f.agent, "")
	require.NoError(t, err)

	send := func(key, body string) (*ChatAccepted, error) {
		return f.repo.SendMessage(ctx, g, f.project, f.userA, th.ID, SendInput{ClientMessageID: key, Body: body})
	}

	_, err = send("short", "hi")
	requireChatErr(t, err, http.StatusUnprocessableEntity, ChatCodeValidationError)
	_, err = send("key-empty-1", "   ")
	requireChatErr(t, err, http.StatusUnprocessableEntity, ChatCodeEmptyMessage)
	_, err = send("key-long-01", strings.Repeat("x", ChatBodyMax+1))
	requireChatErr(t, err, http.StatusUnprocessableEntity, ChatCodeValidationError)
	_, err = f.repo.SendMessage(ctx, g, f.project, f.userB, th.ID, SendInput{ClientMessageID: "key-other-1", Body: "hi"})
	requireChatErr(t, err, http.StatusNotFound, ChatCodeThreadNotFound)

	// No runner is holding the agent: the turn waits and says so.
	first, err := send("key-aaaaaaaa", "  A question that is long enough to be shortened into a thread title for the sidebar list  ")
	require.NoError(t, err)
	require.Equal(t, "queued", first.Turn.State)
	require.True(t, first.Turn.AgentOffline)
	require.Equal(t, "queued", first.Message.Status)

	again, err := send("key-aaaaaaaa", "the retry body is ignored")
	require.NoError(t, err)
	require.Equal(t, first.Message.ID, again.Message.ID)
	require.Equal(t, first.Turn.ID, again.Turn.ID)

	_, err = send("key-bbbbbbbb", "second while the first is unfinished")
	requireChatErr(t, err, http.StatusConflict, ChatCodeTurnInProgress)

	got, err := f.repo.GetThread(ctx, f.ws, f.project, f.userA, th.ID)
	require.NoError(t, err)
	require.NotEmpty(t, got.Title)
	require.LessOrEqual(t, len([]rune(got.Title)), ChatAutoTitleRunes)
	require.False(t, got.TitleIsCustom)
	require.NotNil(t, got.ActiveTurn)
	require.Equal(t, first.Turn.ID, got.ActiveTurn.ID)
	require.NotNil(t, got.LastMessageAt)

	page, err := f.repo.ListMessages(ctx, f.ws, f.project, f.userA, th.ID, 10, "")
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.ActiveTurn)

	// A live runner lease turns "offline" off and reports the place in the queue.
	chatExec(t, f.db, `INSERT INTO agent_controllers (agent_id, runner_id, runner_name, host, lease_expires_at) VALUES ($1,'r1','runner','h', now() + interval '1 hour')`, f.agent)
	live, err := f.repo.GetThread(ctx, f.ws, f.project, f.userA, th.ID)
	require.NoError(t, err)
	require.NotNil(t, live.ActiveTurn)
	require.False(t, live.ActiveTurn.AgentOffline)
	served, err := f.repo.ServedAgents(ctx, f.project, []uuid.UUID{f.agent, uuid.New()})
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]bool{f.agent: true}, served)

	stopped, err := f.repo.StopTurn(ctx, f.ws, f.project, f.userA, th.ID)
	require.NoError(t, err)
	require.True(t, stopped.Stopped)
	stopped, err = f.repo.StopTurn(ctx, f.ws, f.project, f.userA, th.ID)
	require.NoError(t, err)
	require.False(t, stopped.Stopped)

	next, err := send("key-bbbbbbbb", "now it can go")
	require.NoError(t, err)
	require.NotEqual(t, first.Turn.ID, next.Turn.ID)

	// Stopping hands the waiting turn back as cancelled.
	stopped, err = f.repo.StopTurn(ctx, f.ws, f.project, f.userA, th.ID)
	require.NoError(t, err)
	require.True(t, stopped.Stopped)
	var state string
	require.NoError(t, f.db.Get(&state, `SELECT state FROM ext_agent_chat_turns WHERE id = $1`, next.Turn.ID))
	require.Equal(t, "cancelled", state)
}

func TestAgentChatRepo_MessagePaging(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	g := f.gate(t, f.userA)
	th, err := f.repo.CreateThread(ctx, g, f.project, f.userA, f.agent, "")
	require.NoError(t, err)
	for _, key := range []string{"key-page-01", "key-page-02", "key-page-03"} {
		acc, sendErr := f.repo.SendMessage(ctx, g, f.project, f.userA, th.ID, SendInput{ClientMessageID: key, Body: key})
		require.NoError(t, sendErr)
		_, stopErr := f.repo.StopTurn(ctx, f.ws, f.project, f.userA, th.ID)
		require.NoError(t, stopErr)
		require.NotEqual(t, uuid.Nil, acc.Message.ID)
	}
	newest, err := f.repo.ListMessages(ctx, f.ws, f.project, f.userA, th.ID, 2, "")
	require.NoError(t, err)
	require.Len(t, newest.Items, 2)
	require.True(t, newest.HasMore)
	require.NotNil(t, newest.NextCursor)
	older, err := f.repo.ListMessages(ctx, f.ws, f.project, f.userA, th.ID, 2, *newest.NextCursor)
	require.NoError(t, err)
	require.Len(t, older.Items, 1)
	require.False(t, older.HasMore)
	_, err = f.repo.ListMessages(ctx, f.ws, f.project, f.userA, th.ID, 2, "garbage")
	requireChatErr(t, err, http.StatusBadRequest, ChatCodeInvalidCursor)
}

// A database failure must surface as an error, never as an empty answer that
// looks like "no threads".
func TestAgentChatRepo_DatabaseErrorsPropagate(t *testing.T) {
	f := newChatFixture(t)
	g := f.gate(t, f.userA)
	th, err := f.repo.CreateThread(context.Background(), g, f.project, f.userA, f.agent, "t")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := th.ID
	calls := map[string]func() error{
		"gate":   func() error { _, e := f.repo.Gate(ctx, UserRuntimeActor{UserID: f.userA}, f.project); return e },
		"create": func() error { _, e := f.repo.CreateThread(ctx, g, f.project, f.userA, f.agent, ""); return e },
		"get":    func() error { _, e := f.repo.GetThread(ctx, f.ws, f.project, f.userA, id); return e },
		"list":   func() error { _, e := f.repo.ListThreads(ctx, f.ws, f.project, f.userA, f.agent, 5, ""); return e },
		"rename": func() error { _, e := f.repo.RenameThread(ctx, f.ws, f.project, f.userA, id, "x"); return e },
		"stop":   func() error { _, e := f.repo.StopTurn(ctx, f.ws, f.project, f.userA, id); return e },
		"msgs":   func() error { _, e := f.repo.ListMessages(ctx, f.ws, f.project, f.userA, id, 5, ""); return e },
		"send": func() error {
			_, e := f.repo.SendMessage(ctx, g, f.project, f.userA, id, SendInput{ClientMessageID: "key-cancel-1", Body: "hi"})
			return e
		},
		"served": func() error { _, e := f.repo.ServedAgents(ctx, f.project, []uuid.UUID{f.agent}); return e },
	}
	for name, call := range calls {
		require.Error(t, call(), name)
	}
}
