package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

const (
	auditSecretBody  = "SECRET-BODY-must-not-leave"
	auditSecretLabel = "SECRET-LABEL-must-not-leave"
	auditSecretFile  = "SECRET-FILE-must-not-leave.png"
	auditSecretKey   = "SECRET-S3-KEY-must-not-leave"
	auditSecretMem   = "SECRET-MEMORY-CONTENT-must-not-leave"
)

type chatAuditWorld struct {
	db                              *sqlx.DB
	ws, project, agent              uuid.UUID
	admin, owner, member, viewer    uuid.UUID
	foreignWS, foreignAdmin         uuid.UUID
	thread, turn, session, otherThr uuid.UUID
	foreignThread, foreignAgent     uuid.UUID
}

func auditSetMember(t *testing.T, db *sqlx.DB, ws, user uuid.UUID, role string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO workspace_members (id, workspace_id, user_id, role)
		VALUES ($1,$2,$3,$4::workspace_role)
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role`, uuid.New(), ws, user, role)
	require.NoError(t, err)
}

func auditUser(t *testing.T, db *sqlx.DB, label string) uuid.UUID {
	t.Helper()
	s := uuid.New().String()[:8]
	u := &domain.User{ID: uuid.New(), Email: "audit-" + label + "-" + s + "@example.com", PasswordHash: "x", Name: label, Username: "audit-" + label + "-" + s, IsActive: true}
	require.NoError(t, postgres.NewUserRepo(db).Create(context.Background(), u))
	return u.ID
}

func auditThread(t *testing.T, db *sqlx.DB, ws, project, agent, user uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(), `INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, execution_epoch, runtime)
		VALUES ($1,$2,$3,$4,$5,3,$6::jsonb)`, id, ws, project, agent, user,
		`{"runner_id":"r-1","harness":"claude","profile":"p","session_id":"sess-1","prompt":"`+auditSecretBody+`"}`)
	require.NoError(t, err)
	return id
}

func newChatAuditWorld(t *testing.T) *chatAuditWorld {
	t.Helper()
	db := runtimeHTTPDB(t)
	ctx := context.Background()
	w := &chatAuditWorld{db: db}
	var owner uuid.UUID
	w.ws, owner = seedRuntimeWorkspace(t, db)
	w.owner = owner
	w.admin, w.member, w.viewer = auditUser(t, db, "admin"), auditUser(t, db, "member"), auditUser(t, db, "viewer")
	auditSetMember(t, db, w.ws, w.owner, "owner")
	auditSetMember(t, db, w.ws, w.admin, "admin")
	auditSetMember(t, db, w.ws, w.member, "member")
	auditSetMember(t, db, w.ws, w.viewer, "viewer")
	w.project, _ = seedChatProject(t, db, w.ws)
	w.agent = seedOperatorAgent(t, db, w.ws, &w.owner)
	addChatMember(t, db, w.project, w.ws, w.agent)

	w.thread = auditThread(t, db, w.ws, w.project, w.agent, w.member)
	w.otherThr = auditThread(t, db, w.ws, w.project, w.agent, w.member)
	w.turn = uuid.New()
	_, err := db.ExecContext(ctx, `INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, state, runner_id, attempt,
			execution_epoch, session_id, lease_until, expires_at, first_claimed_at, started_at, last_seq, model)
		VALUES ($1,$2,$3,$4,'streaming',$5,1,3,'sess-1', now() + interval '1 minute', now() + interval '1 hour',
			now() - interval '5 minutes', now() - interval '5 minutes', 2, 'm-1')`, w.turn, w.ws, w.thread, w.agent, uuid.New())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO ext_agent_chat_messages (workspace_id, thread_id, role, body, status, turn_id)
		VALUES ($1,$2,'user',$3,'done',$4), ($1,$2,'agent','partial','streaming',$4)`, w.ws, w.thread, auditSecretBody, w.turn)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO ext_agent_chat_tool_events (turn_id, seq, workspace_id, kind, label)
		VALUES ($1,1,$2,'tool_use',$3), ($1,2,$2,'tool_use',$3), ($1,3,$2,'thinking',$3)`, w.turn, w.ws, auditSecretLabel)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO ext_agent_chat_attachments (workspace_id, thread_id, s3_key, file_name, mime, size, uploaded_at)
		VALUES ($1,$2,$3,$4,'image/png',1234, now())`, w.ws, w.thread, auditSecretKey, auditSecretFile)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO ext_agent_chat_commands (workspace_id, agent_id, kind, thread_id, session_id, state, attempts, expires_at)
		VALUES ($1,$2,'stop_session',$3,'sess-1','done',1, now() + interval '1 hour')`, w.ws, w.agent, w.thread)
	require.NoError(t, err)

	w.session = uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO chat_session_tokens (token_hash, user_id, agent_id, workspace_id, project_id,
			chat_thread_id, chat_session_id, runner_id, execution_epoch, issued_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'r-1',3, now() - interval '10 minutes', now() + interval '10 minutes')`,
		"hash-"+w.session.String(), w.member, w.agent, w.ws, w.project, w.thread, w.session)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO chat_session_stops (chat_session_id, stopped_at, reason) VALUES ($1, now(), 'stop')`, w.session)
	require.NoError(t, err)

	// A second workspace with its own admin, thread and agent: the tenant the
	// first workspace's admin must not reach.
	var foreignOwner uuid.UUID
	w.foreignWS, foreignOwner = seedRuntimeWorkspace(t, db)
	w.foreignAdmin = foreignOwner
	auditSetMember(t, db, w.foreignWS, w.foreignAdmin, "admin")
	fproject, _ := seedChatProject(t, db, w.foreignWS)
	w.foreignAgent = seedOperatorAgent(t, db, w.foreignWS, &foreignOwner)
	addChatMember(t, db, fproject, w.foreignWS, w.foreignAgent)
	w.foreignThread = auditThread(t, db, w.foreignWS, fproject, w.foreignAgent, foreignOwner)
	return w
}

func auditCall(t *testing.T, h *ChatAuditHandler, which, uri string, user, agent uuid.UUID, params map[string]string) (status int, out map[string]any) {
	t.Helper()
	switch which {
	case "thread":
		return callUserRuntime(t, h.Thread, http.MethodGet, uri, "", user, agent, params)
	case "events":
		return callUserRuntime(t, h.ThreadEvents, http.MethodGet, uri, "", user, agent, params)
	case "sessions":
		return callUserRuntime(t, h.AgentSessions, http.MethodGet, uri, "", user, agent, params)
	case "memory":
		return callUserRuntime(t, h.MemoryWrites, http.MethodGet, uri, "", user, agent, params)
	}
	t.Fatalf("unknown endpoint %q", which)
	return 0, nil
}

func requireNoSecrets(t *testing.T, out map[string]any) {
	t.Helper()
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	for _, s := range []string{"k-in-session", auditSecretBody, auditSecretLabel, auditSecretFile, auditSecretKey, auditSecretMem} {
		require.NotContains(t, string(raw), s, "audit output leaked content")
	}
}

// Who may call: every endpoint, every principal. Red control: the workspace's
// own admin gets 200 on the same request that a member, a viewer, an agent key
// and another workspace's admin get 403 for.
func TestChatAudit_AccessMatrix(t *testing.T) {
	w := newChatAuditWorld(t)
	h := NewChatAuditHandler(postgres.NewChatAuditRepo(w.db))
	wsp := map[string]string{"ws_id": w.ws.String()}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{"ws_id": w.ws.String()}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name, which, uri string
		params           map[string]string
	}{
		{"thread", "thread", "/", with(map[string]string{"thread_id": w.thread.String()})},
		{"events", "events", "/", with(map[string]string{"thread_id": w.thread.String()})},
		{"sessions", "sessions", "/", with(map[string]string{"agent_id": w.agent.String()})},
		{"memory-writes", "memory", "/", wsp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := auditCall(t, h, tc.which, tc.uri, w.admin, uuid.Nil, tc.params)
			require.Equal(t, http.StatusOK, status, "positive control: workspace admin")
			requireNoSecrets(t, out)
			status, _ = auditCall(t, h, tc.which, tc.uri, w.owner, uuid.Nil, tc.params)
			require.Equal(t, http.StatusOK, status, "workspace owner")

			for who, user := range map[string]uuid.UUID{"member": w.member, "viewer": w.viewer, "foreign-admin": w.foreignAdmin} {
				status, _ = auditCall(t, h, tc.which, tc.uri, user, uuid.Nil, tc.params)
				require.Equal(t, http.StatusForbidden, status, who)
			}
			status, _ = auditCall(t, h, tc.which, tc.uri, uuid.Nil, w.agent, tc.params)
			require.Equal(t, http.StatusForbidden, status, "agent key")
			status, _ = auditCall(t, h, tc.which, tc.uri, uuid.Nil, uuid.Nil, tc.params)
			require.Equal(t, http.StatusForbidden, status, "no identity")
		})
	}

	// The other workspace's ids through the own workspace: not found, never data.
	status, _ := auditCall(t, h, "thread", "/", w.admin, uuid.Nil, with(map[string]string{"thread_id": w.foreignThread.String()}))
	require.Equal(t, http.StatusNotFound, status, "foreign thread via own workspace")
	status, _ = auditCall(t, h, "events", "/", w.admin, uuid.Nil, with(map[string]string{"thread_id": w.foreignThread.String()}))
	require.Equal(t, http.StatusNotFound, status)
	status, _ = auditCall(t, h, "sessions", "/", w.admin, uuid.Nil, with(map[string]string{"agent_id": w.foreignAgent.String()}))
	require.Equal(t, http.StatusNotFound, status, "foreign agent via own workspace")
	// And the foreign admin asking for the first workspace's thread by its own
	// workspace id gets the same 404; by the first workspace's id, 403 (above).
	status, _ = auditCall(t, h, "thread", "/", w.foreignAdmin, uuid.Nil, map[string]string{"ws_id": w.foreignWS.String(), "thread_id": w.thread.String()})
	require.Equal(t, http.StatusNotFound, status)

	// The workspace owner counts as owner without a workspace_members row.
	_, err := w.db.ExecContext(context.Background(), `DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, w.ws, w.owner)
	require.NoError(t, err)
	status, _ = auditCall(t, h, "thread", "/", w.owner, uuid.Nil, with(map[string]string{"thread_id": w.thread.String()}))
	require.Equal(t, http.StatusOK, status, "owner_id fallback")

	status, _ = auditCall(t, h, "thread", "/", w.admin, uuid.Nil, with(map[string]string{"thread_id": "not-a-uuid"}))
	require.Equal(t, http.StatusBadRequest, status)
}

func TestChatAudit_ThreadSessionStopExpiry(t *testing.T) {
	w := newChatAuditWorld(t)
	h := NewChatAuditHandler(postgres.NewChatAuditRepo(w.db))
	status, out := auditCall(t, h, "thread", "/", w.admin, uuid.Nil, map[string]string{"ws_id": w.ws.String(), "thread_id": w.thread.String()})
	require.Equal(t, http.StatusOK, status)
	requireNoSecrets(t, out)

	require.Equal(t, float64(3), out["execution_epoch"])
	require.Equal(t, map[string]any{"runner_id": "r-1", "harness": "claude", "profile": "p", "session_id": "sess-1"}, out["runtime"],
		"only the addressing keys of runtime leave the server")
	require.Equal(t, float64(1), out["agent_live_sessions"])

	turns := out["turns"].([]any)
	require.Len(t, turns, 1)
	turn := turns[0].(map[string]any)
	require.Equal(t, w.turn.String(), turn["id"])
	require.Equal(t, "streaming", turn["state"])
	require.Equal(t, true, turn["lease_live"])
	require.NotNil(t, turn["lease_until"])
	require.NotNil(t, turn["expires_at"])

	// turn.state, message.status and event kind are three different fields.
	statuses := map[string]bool{}
	for _, m := range out["messages"].([]any) {
		mm := m.(map[string]any)
		statuses[mm["status"].(string)] = true
		require.Greater(t, mm["body_length"].(float64), float64(0))
		require.NotContains(t, mm, "body")
	}
	require.Equal(t, map[string]bool{"done": true, "streaming": true}, statuses)
	kinds := map[string]float64{}
	for _, k := range out["event_kinds"].([]any) {
		kk := k.(map[string]any)
		kinds[kk["kind"].(string)] = kk["count"].(float64)
	}
	require.Equal(t, map[string]float64{"tool_use": 2, "thinking": 1}, kinds)

	cmds := out["commands"].([]any)
	require.Len(t, cmds, 1)
	require.Equal(t, "stop_session", cmds[0].(map[string]any)["kind"])
	require.Equal(t, "done", cmds[0].(map[string]any)["state"])
	toks := out["session_tokens"].([]any)
	require.Len(t, toks, 1)
	require.Equal(t, true, toks[0].(map[string]any)["live"])
	require.NotContains(t, toks[0], "token_hash")
	// A Stop before the first token: a tombstone keyed by the turn id, no token row.
	_, err := w.db.ExecContext(context.Background(), `INSERT INTO chat_session_stops (chat_session_id, stopped_at, reason) VALUES ($1, now() - interval '1 minute', 'stop')`, w.turn)
	require.NoError(t, err)
	_, out = auditCall(t, h, "thread", "/", w.admin, uuid.Nil, map[string]string{"ws_id": w.ws.String(), "thread_id": w.thread.String()})
	require.ElementsMatch(t, []any{w.session.String(), w.turn.String()}, out["stopped_sessions"])
	// Red control: another thread's tombstones are not listed.
	other := uuid.New()
	_, err = w.db.ExecContext(context.Background(), `INSERT INTO chat_session_stops (chat_session_id, stopped_at, reason) VALUES ($1, now(), 'stop')`, other)
	require.NoError(t, err)
	_, out = auditCall(t, h, "thread", "/", w.admin, uuid.Nil, map[string]string{"ws_id": w.ws.String(), "thread_id": w.thread.String()})
	require.NotContains(t, out["stopped_sessions"], other.String())

	// Lapsed lease: the same turn reads lease_live=false and counts as lapsed.
	_, err = w.db.ExecContext(context.Background(), `UPDATE ext_agent_chat_turns SET lease_until = now() - interval '1 minute' WHERE id = $1`, w.turn)
	require.NoError(t, err)
	_, out = auditCall(t, h, "thread", "/", w.admin, uuid.Nil, map[string]string{"ws_id": w.ws.String(), "thread_id": w.thread.String()})
	require.Equal(t, false, out["turns"].([]any)[0].(map[string]any)["lease_live"])
	status, out = auditCall(t, h, "sessions", "/", w.admin, uuid.Nil, map[string]string{"ws_id": w.ws.String(), "agent_id": w.agent.String()})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, float64(1), out["streaming_turns"])
	require.Equal(t, float64(1), out["lease_lapsed_turns"])
	require.Equal(t, float64(1), out["live_session_tokens"])
	require.Len(t, out["active_turns"], 1)
}

func TestChatAudit_ThreadEventsMetadataOnly(t *testing.T) {
	w := newChatAuditWorld(t)
	h := NewChatAuditHandler(postgres.NewChatAuditRepo(w.db))
	status, out := auditCall(t, h, "events", "/", w.admin, uuid.Nil, map[string]string{"ws_id": w.ws.String(), "thread_id": w.thread.String()})
	require.Equal(t, http.StatusOK, status)
	requireNoSecrets(t, out)
	events := out["events"].([]any)
	require.Len(t, events, 3, "positive control: the seeded events are listed")
	require.Equal(t, float64(len(auditSecretLabel)), events[0].(map[string]any)["label_length"])
	att := out["attachments"].([]any)
	require.Len(t, att, 1)
	require.Equal(t, "image/png", att[0].(map[string]any)["mime"])
	require.Equal(t, true, att[0].(map[string]any)["uploaded"])
}

// Memory writes of a chat session carry the thread, the turn and the user they
// were written for. Controls: a write by the same agent outside the session
// window, a write by another agent and a write filtered out by thread_id must
// not appear.
func TestChatAudit_MemoryWritesAttribution(t *testing.T) {
	w := newChatAuditWorld(t)
	ctx := context.Background()
	h := NewChatAuditHandler(postgres.NewChatAuditRepo(w.db))

	mem := func(agent uuid.UUID, key string) uuid.UUID {
		id := uuid.New()
		_, err := w.db.ExecContext(ctx, `INSERT INTO memories (id, workspace_id, agent_id, key, content, scope)
			VALUES ($1,$2,$3,$4,$5,'workspace')`, id, w.ws, agent, key, auditSecretMem)
		require.NoError(t, err)
		return id
	}
	rev := func(m, agent uuid.UUID, at string) {
		_, err := w.db.ExecContext(ctx, `INSERT INTO memory_revisions (memory_id, version, content, action, actor_agent_id, created_at, workspace_id, reason)
			VALUES ($1,1,$2,'created',$3, `+at+`, $4, 'because')`, m, auditSecretMem, agent, w.ws)
		require.NoError(t, err)
	}
	inSession := mem(w.agent, "k-in-session")
	rev(inSession, w.agent, "now() - interval '2 minutes'")
	outside := mem(w.agent, "k-outside-window")
	rev(outside, w.agent, "now() - interval '3 hours'")
	otherAgent := seedOperatorAgent(t, w.db, w.ws, &w.owner)
	foreign := mem(otherAgent, "k-other-agent")
	rev(foreign, otherAgent, "now() - interval '2 minutes'")

	// Red control: a token that expired at -1m and was revoked only later (-30s)
	// must not stretch the window: a write at -45s is outside it.
	_, err := w.db.ExecContext(ctx, `UPDATE chat_session_tokens SET issued_at = now() - interval '10 minutes',
		expires_at = now() - interval '1 minute', revoked_at = now() - interval '30 seconds' WHERE chat_session_id = $1`, w.session)
	require.NoError(t, err)
	late := mem(w.agent, "k-after-expiry")
	rev(late, w.agent, "now() - interval '45 seconds'")
	// The in-session write at -2m predates the expiry, so it stays attributed.
	params := map[string]string{"ws_id": w.ws.String()}
	status, out := auditCall(t, h, "memory", "/", w.admin, uuid.Nil, params)
	require.Equal(t, http.StatusOK, status)
	requireNoSecrets(t, out)
	items := out["items"].([]any)
	require.Len(t, items, 1, "only the in-session write is attributed to a chat")
	it := items[0].(map[string]any)
	require.Equal(t, inSession.String(), it["memory_id"])
	require.NotContains(t, it, "key")
	require.Equal(t, w.thread.String(), it["chat_thread_id"])
	require.Equal(t, w.session.String(), it["chat_session_id"])
	require.Equal(t, w.member.String(), it["initiated_by_user_id"])
	require.Equal(t, w.turn.String(), it["turn_id"])
	require.Equal(t, w.agent.String(), it["actor_agent_id"])
	require.Equal(t, postgres.ChatAttributionDerived, it["attribution"])
	require.Equal(t, true, it["has_reason"])

	// thread filter: the other thread has no session, so no rows; its own id matches.
	_, out = auditCall(t, h, "memory", "/?thread_id="+w.otherThr.String(), w.admin, uuid.Nil, params)
	require.Empty(t, out["items"])
	_, out = auditCall(t, h, "memory", "/?thread_id="+w.thread.String(), w.admin, uuid.Nil, params)
	require.Len(t, out["items"], 1)

	// A thread filter naming another workspace's thread, or none, is a 404, not an empty 200.
	status, _ = auditCall(t, h, "memory", "/?thread_id="+w.foreignThread.String(), w.admin, uuid.Nil, params)
	require.Equal(t, http.StatusNotFound, status, "foreign thread filter")
	status, _ = auditCall(t, h, "memory", "/?thread_id="+uuid.NewString(), w.admin, uuid.Nil, params)
	require.Equal(t, http.StatusNotFound, status, "unknown thread filter")

	// The foreign workspace's admin sees none of it through its own workspace.
	_, out = auditCall(t, h, "memory", "/", w.foreignAdmin, uuid.Nil, map[string]string{"ws_id": w.foreignWS.String()})
	require.Empty(t, out["items"])

	status, _ = auditCall(t, h, "memory", "/?limit=0", w.admin, uuid.Nil, params)
	require.Equal(t, http.StatusBadRequest, status)
}
