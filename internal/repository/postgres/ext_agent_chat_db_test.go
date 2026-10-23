//go:build integration

package postgres

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type agentChatFixture struct {
	db       *sqlx.DB
	dsn      string
	wsA, wsB uuid.UUID
	userA    uuid.UUID
	userB    uuid.UUID
	agentA   uuid.UUID
	agentB   uuid.UUID
	projA    uuid.UUID
	projB    uuid.UUID
}

func newAgentChatFixture(t *testing.T) *agentChatFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		dsn = "postgres://evc:evc@localhost:5437/evc_mesh_test?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	f := &agentChatFixture{db: db, dsn: dsn}
	mk := func(label string) (ws, user, agent, proj uuid.UUID) {
		w := &domain.Workspace{ID: uuid.New(), Name: "Chat " + label, Slug: "chat-" + uuid.NewString(), OwnerID: uuid.New()}
		require.NoError(t, NewWorkspaceRepo(db).Create(ctx, w))
		t.Cleanup(func() { _, err := db.Exec(`DELETE FROM workspaces WHERE id=$1`, w.ID); require.NoError(t, err) })
		p := &domain.Project{ID: uuid.New(), WorkspaceID: w.ID, Name: "Chat", Slug: "chat-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
		require.NoError(t, NewProjectRepo(db).Create(ctx, p))
		user, agent = uuid.New(), uuid.New()
		_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1, $2, $3, 'x', 'chat user')`, user, user.String()+"@chat.test", "chat-"+user.String()[:30])
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, user) })
		_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, 'chat agent', $3, 'h', 'p')`, agent, w.ID, "chat-agent-"+uuid.NewString()[:8])
		require.NoError(t, err)
		return w.ID, user, agent, p.ID
	}
	f.wsA, f.userA, f.agentA, f.projA = mk("A")
	f.wsB, f.userB, f.agentB, f.projB = mk("B")
	return f
}

func (f *agentChatFixture) insertThread(t *testing.T, db *sqlx.DB, ws, proj, agent, user uuid.UUID) (uuid.UUID, error) {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, title, last_message_at) VALUES ($1,$2,$3,$4,$5,'t', now())`, id, ws, proj, agent, user)
	return id, err
}

// workspaceRoleDB connects as a non-owner role pinned to one workspace, the
// only kind of connection RLS applies to (table owners bypass it).
func (f *agentChatFixture) workspaceRoleDB(t *testing.T, ws uuid.UUID) *sqlx.DB {
	t.Helper()
	role := "chat_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := uuid.NewString()
	q := pq.QuoteIdentifier(role)
	_, err := f.db.Exec(`CREATE ROLE ` + q + ` LOGIN PASSWORD ` + pq.QuoteLiteral(password) + `; GRANT USAGE ON SCHEMA public TO ` + q + `; GRANT SELECT, INSERT, UPDATE, DELETE ON ext_agent_chat_threads, ext_agent_chat_messages, ext_agent_chat_turns TO ` + q) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL quotes role identifier and password literal with lib/pq.
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := f.db.Exec(`DROP OWNED BY ` + q + `; DROP ROLE ` + q) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL quotes generated role identifier with lib/pq.
		require.NoError(t, cleanupErr)
	})
	dsn, err := url.Parse(f.dsn)
	require.NoError(t, err)
	dsn.User = url.UserPassword(role, password)
	opts := dsn.Query()
	opts.Set("options", "-c app.current_workspace_id="+ws.String())
	dsn.RawQuery = opts.Encode()
	db, err := sqlx.Connect("postgres", dsn.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAgentChatThreadsRLSIsolatesWorkspaces(t *testing.T) {
	f := newAgentChatFixture(t)
	threadA, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	_, err = f.insertThread(t, f.db, f.wsB, f.projB, f.agentB, f.userB)
	require.NoError(t, err)

	dbB := f.workspaceRoleDB(t, f.wsB)

	var seen int
	require.NoError(t, dbB.Get(&seen, `SELECT count(*) FROM ext_agent_chat_threads WHERE id=$1`, threadA))
	require.Zero(t, seen, "workspace B must not see workspace A thread")
	require.NoError(t, dbB.Get(&seen, `SELECT count(*) FROM ext_agent_chat_threads WHERE workspace_id IN ($1,$2)`, f.wsA, f.wsB))
	require.Equal(t, 1, seen, "workspace B sees only its own row")

	// A write into another workspace is rejected by WITH CHECK.
	_, err = f.insertThread(t, dbB, f.wsA, f.projA, f.agentA, f.userA)
	require.ErrorContains(t, err, "row-level security")

	// Red control: the same query on a connection RLS does not apply to (table
	// owner) sees both workspaces, so the zero above comes from the policy.
	require.NoError(t, f.db.Get(&seen, `SELECT count(*) FROM ext_agent_chat_threads WHERE workspace_id IN ($1,$2)`, f.wsA, f.wsB))
	require.Equal(t, 2, seen, "without RLS both rows are visible")
}

func TestAgentChatThreadListUsesIndex(t *testing.T) {
	f := newAgentChatFixture(t)
	for i := 0; i < 3; i++ {
		_, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
		require.NoError(t, err)
	}
	tx, err := f.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// The table is tiny, so the planner would pick a seq scan on cost alone;
	// forbidding it proves the list query is servable from the index.
	_, err = tx.Exec(`SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)
	var plan []string
	require.NoError(t, tx.Select(&plan, `EXPLAIN SELECT id FROM ext_agent_chat_threads WHERE project_id=$1 AND user_id=$2 AND agent_id=$3 AND deleted_at IS NULL ORDER BY last_message_at DESC NULLS LAST LIMIT 50`, f.projA, f.userA, f.agentA))
	require.Contains(t, strings.Join(plan, "\n"), "ext_agent_chat_threads_list")
}

func TestAgentChatOneActiveTurnPerThread(t *testing.T) {
	f := newAgentChatFixture(t)
	thread, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	msg := uuid.New()
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body) VALUES ($1,$2,$3,'user','hi')`, msg, f.wsA, thread)
	require.NoError(t, err)
	ins := func(state string) error {
		_, err := f.db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, user_message_id, state, expires_at) VALUES ($1,$2,$3,$4,$5,$6, now() + interval '1 day')`, uuid.New(), f.wsA, thread, f.agentA, msg, state)
		return err
	}
	require.NoError(t, ins("queued"))
	require.ErrorContains(t, ins("streaming"), "ext_agent_chat_turns_one_active")
	require.NoError(t, ins("done"), "finished turns do not count as active")
}

func TestAgentChatChildCannotReferenceOtherWorkspaceParent(t *testing.T) {
	f := newAgentChatFixture(t)
	threadA, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)

	// workspace_id says B, thread_id belongs to A: passes a workspace_id-only
	// RLS check, must be stopped by the composite foreign key.
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body) VALUES ($1,$2,$3,'user','x')`, uuid.New(), f.wsB, threadA)
	require.ErrorContains(t, err, "fk_ext_agent_chat_messages_thread")

	// Control: the same row in the thread's own workspace is accepted.
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body) VALUES ($1,$2,$3,'user','x')`, uuid.New(), f.wsA, threadA)
	require.NoError(t, err)
}

// Every parent reference is workspace-aware: for each child table a row whose
// workspace_id differs from the parent's is rejected, the same row in the
// parent's workspace is accepted.
func TestAgentChatCompositeFKsRejectCrossWorkspace(t *testing.T) {
	f := newAgentChatFixture(t)
	threadA, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	msgA, msgA2, turnA := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{msgA, msgA2} {
		_, err = f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role) VALUES ($1,$2,$3,'user')`, id, f.wsA, threadA)
		require.NoError(t, err)
	}
	_, err = f.db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, user_message_id, state, expires_at) VALUES ($1,$2,$3,$4,$5,'done', now())`, turnA, f.wsA, threadA, f.agentA, msgA)
	require.NoError(t, err)

	cases := []struct {
		name, fk, sql string
		args          func(ws uuid.UUID) []any
	}{
		{"turn.thread", "fk_ext_agent_chat_turns_thread",
			`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, expires_at) VALUES ($1,$2,$3,$4, now())`,
			func(ws uuid.UUID) []any { return []any{uuid.New(), ws, threadA, f.agentA} }},
		{"turn.user_message", "fk_ext_agent_chat_turns_user_message",
			`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, user_message_id, state, expires_at) VALUES ($1,$2,$3,$4,$5,'done', now())`,
			func(ws uuid.UUID) []any { return []any{uuid.New(), ws, threadA, f.agentA, msgA2} }},
		{"turn.agent_message", "fk_ext_agent_chat_turns_agent_message",
			`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, agent_message_id, state, expires_at) VALUES ($1,$2,$3,$4,$5,'done', now())`,
			func(ws uuid.UUID) []any { return []any{uuid.New(), ws, threadA, f.agentA, msgA2} }},
		{"message.turn", "fk_ext_agent_chat_messages_turn",
			`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, turn_id) VALUES ($1,$2,$3,'agent',$4)`,
			func(ws uuid.UUID) []any { return []any{uuid.New(), ws, threadA, turnA} }},
		{"attachment.thread", "fk_ext_agent_chat_attachments_thread",
			`INSERT INTO ext_agent_chat_attachments (id, workspace_id, thread_id, s3_key, file_name, mime, size) VALUES ($1,$2,$3,'k','f','text/plain',1)`,
			func(ws uuid.UUID) []any { return []any{uuid.New(), ws, threadA} }},
		{"attachment.message", "fk_ext_agent_chat_attachments_message",
			`INSERT INTO ext_agent_chat_attachments (id, workspace_id, thread_id, message_id, s3_key, file_name, mime, size) VALUES ($1,$2,$3,$4,'k','f','text/plain',1)`,
			func(ws uuid.UUID) []any { return []any{uuid.New(), ws, threadA, msgA} }},
		{"tool_event.turn", "fk_ext_agent_chat_tool_events_turn",
			`INSERT INTO ext_agent_chat_tool_events (turn_id, seq, workspace_id, kind, label) VALUES ($1,1,$2,'read','x')`,
			func(ws uuid.UUID) []any { return []any{turnA, ws} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A row in workspace B pointing at workspace A's parent is refused.
			// The attachment.message and turn.thread cases also need a valid thread
			// in the same row, so ws B is reused for the whole insert.
			_, err := f.db.Exec(c.sql, c.args(f.wsB)...)
			require.Error(t, err)
			require.Contains(t, err.Error(), "violates foreign key constraint")
			// Control: the same statement in the parent's workspace is accepted
			// (the one-active-turn index does not apply to turn.thread: A has only a done turn).
			_, err = f.db.Exec(c.sql, c.args(f.wsA)...)
			require.NoError(t, err)
		})
	}
}

// Thread and turn references to projects and agents are workspace-aware too:
// workspace_id says A, the project or agent belongs to B. Each reference is
// isolated (the other one is valid), and the same-workspace row is accepted.
func TestAgentChatThreadCannotReferenceOtherWorkspaceProjectOrAgent(t *testing.T) {
	f := newAgentChatFixture(t)
	_, err := f.insertThread(t, f.db, f.wsA, f.projB, f.agentA, f.userA)
	require.ErrorContains(t, err, "fk_ext_agent_chat_threads_project")
	_, err = f.insertThread(t, f.db, f.wsA, f.projA, f.agentB, f.userA)
	require.ErrorContains(t, err, "fk_ext_agent_chat_threads_agent")
	_, err = f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err, "control: same-workspace project and agent")

	thread, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	ins := func(agent uuid.UUID) error {
		_, err := f.db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, state, expires_at) VALUES ($1,$2,$3,$4,'done', now())`, uuid.New(), f.wsA, thread, agent)
		return err
	}
	require.ErrorContains(t, ins(f.agentB), "fk_ext_agent_chat_turns_agent")
	require.NoError(t, ins(f.agentA))
}

func TestAgentChatSendIdempotencyKeyIsUniquePerThread(t *testing.T) {
	f := newAgentChatFixture(t)
	thread, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	ins := func() error {
		_, err := f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, client_message_id) VALUES ($1,$2,$3,'user','key-12345')`, uuid.New(), f.wsA, thread)
		return err
	}
	require.NoError(t, ins())
	require.ErrorContains(t, ins(), "ext_agent_chat_messages_idem")
}

// The three nullable references clear on delete (agent-chat-openapi §4 v6) and
// keep workspace_id, which is NOT NULL and part of the composite key.
func TestAgentChatNullableRefsSetNullOnDelete(t *testing.T) {
	f := newAgentChatFixture(t)
	thread, err := f.insertThread(t, f.db, f.wsA, f.projA, f.agentA, f.userA)
	require.NoError(t, err)
	newMsg := func(turn *uuid.UUID) uuid.UUID {
		id := uuid.New()
		_, err := f.db.Exec(`INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, turn_id) VALUES ($1,$2,$3,'user',$4)`, id, f.wsA, thread, turn)
		require.NoError(t, err)
		return id
	}
	newTurn := func(userMsg, agentMsg uuid.UUID) uuid.UUID {
		id := uuid.New()
		_, err := f.db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, user_message_id, agent_message_id, state, expires_at) VALUES ($1,$2,$3,$4,$5,$6,'done', now())`, id, f.wsA, thread, f.agentA, userMsg, agentMsg)
		require.NoError(t, err)
		return id
	}
	userMsg, agentMsg := newMsg(nil), newMsg(nil)
	turn := newTurn(userMsg, agentMsg)
	linked := newMsg(&turn)

	// Deleting the turn clears messages.turn_id and leaves the message.
	_, err = f.db.Exec(`DELETE FROM ext_agent_chat_turns WHERE id=$1`, turn)
	require.NoError(t, err)
	var turnRef *uuid.UUID
	var ws uuid.UUID
	require.NoError(t, f.db.QueryRow(`SELECT turn_id, workspace_id FROM ext_agent_chat_messages WHERE id=$1`, linked).Scan(&turnRef, &ws))
	require.Nil(t, turnRef)
	require.Equal(t, f.wsA, ws, "workspace_id survives SET NULL")

	// Deleting a message clears the turn's reference and leaves the turn.
	turn = newTurn(userMsg, agentMsg)
	_, err = f.db.Exec(`DELETE FROM ext_agent_chat_messages WHERE id IN ($1,$2)`, userMsg, agentMsg)
	require.NoError(t, err)
	var u, a *uuid.UUID
	require.NoError(t, f.db.QueryRow(`SELECT user_message_id, agent_message_id, workspace_id FROM ext_agent_chat_turns WHERE id=$1`, turn).Scan(&u, &a, &ws))
	require.Nil(t, u)
	require.Nil(t, a)
	require.Equal(t, f.wsA, ws)
}

func (f *agentChatFixture) insertCommand(db *sqlx.DB, ws, agent uuid.UUID, kind, state string, thread uuid.UUID) error {
	_, err := db.Exec(`INSERT INTO ext_agent_chat_commands (workspace_id, agent_id, kind, thread_id, state, expires_at) VALUES ($1,$2,$3,$4,$5, now() + interval '1 day')`, ws, agent, kind, thread, state)
	return err
}

func TestAgentChatCommandsRLSIsolatesWorkspaces(t *testing.T) {
	f := newAgentChatFixture(t)
	require.NoError(t, f.insertCommand(f.db, f.wsA, f.agentA, "stop_session", "pending", uuid.New()))
	require.NoError(t, f.insertCommand(f.db, f.wsB, f.agentB, "stop_session", "pending", uuid.New()))

	// workspaceRoleDB grants only the older tables; commands needs its own grant.
	dbB := f.workspaceRoleDB(t, f.wsB)
	var role string
	require.NoError(t, dbB.Get(&role, `SELECT current_user`))
	_, err := f.db.Exec(`GRANT SELECT, INSERT, UPDATE, DELETE ON ext_agent_chat_commands TO ` + pq.QuoteIdentifier(role)) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL quotes the generated role identifier with lib/pq.
	require.NoError(t, err)

	var seen int
	require.NoError(t, dbB.Get(&seen, `SELECT count(*) FROM ext_agent_chat_commands WHERE workspace_id IN ($1,$2)`, f.wsA, f.wsB))
	require.Equal(t, 1, seen, "workspace B sees only its own command")
	require.NoError(t, dbB.Get(&seen, `SELECT count(*) FROM ext_agent_chat_commands WHERE agent_id=$1`, f.agentA))
	require.Zero(t, seen, "workspace B must not see workspace A command")

	// A write into another workspace is rejected by WITH CHECK.
	require.ErrorContains(t, f.insertCommand(dbB, f.wsA, f.agentA, "stop_session", "pending", uuid.New()), "row-level security")

	// Red control: the owner connection is not subject to RLS and sees both.
	require.NoError(t, f.db.Get(&seen, `SELECT count(*) FROM ext_agent_chat_commands WHERE workspace_id IN ($1,$2)`, f.wsA, f.wsB))
	require.Equal(t, 2, seen, "without RLS both rows are visible")
}

func TestAgentChatCommandsChecksAndNoThreadFK(t *testing.T) {
	f := newAgentChatFixture(t)

	require.ErrorContains(t, f.insertCommand(f.db, f.wsA, f.agentA, "reboot", "pending", uuid.New()), "ext_agent_chat_commands_kind_check")
	require.ErrorContains(t, f.insertCommand(f.db, f.wsA, f.agentA, "stop_session", "stuck", uuid.New()), "ext_agent_chat_commands_state_check")

	// An agent of another workspace cannot be ordered through this workspace.
	require.ErrorContains(t, f.insertCommand(f.db, f.wsA, f.agentB, "stop_session", "pending", uuid.New()), "fk_ext_agent_chat_commands_agent")

	// Control: every allowed kind and state is accepted, and thread_id may point
	// at a thread that does not exist (no FK: commands outlive the thread).
	for _, kind := range []string{"stop_session", "delete_transcript"} {
		for _, state := range []string{"pending", "done", "failed", "dead"} {
			require.NoError(t, f.insertCommand(f.db, f.wsA, f.agentA, kind, state, uuid.New()), "%s/%s", kind, state)
		}
	}
}

func TestAgentChatCommandsPendingUsesIndex(t *testing.T) {
	f := newAgentChatFixture(t)
	require.NoError(t, f.insertCommand(f.db, f.wsA, f.agentA, "stop_session", "pending", uuid.New()))
	tx, err := f.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)
	var plan []string
	require.NoError(t, tx.Select(&plan, `EXPLAIN SELECT id FROM ext_agent_chat_commands WHERE agent_id=$1 AND state='pending'`, f.agentA))
	require.Contains(t, strings.Join(plan, "\n"), "ext_agent_chat_commands_pending")
}
