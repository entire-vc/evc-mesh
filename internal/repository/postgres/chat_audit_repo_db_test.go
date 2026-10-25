package postgres

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The handler package drives ChatAuditRepo through HTTP; this keeps the
// repository's own package covering it: who may ask, what each query returns
// for a thread of the workspace, and that a thread of another workspace is a 404.
func TestChatAuditRepo_AccessAndQueries(t *testing.T) {
	db := chatRepoDB(t)
	ctx := context.Background()
	ws, project, agent := uuid.New(), uuid.New(), uuid.New()
	owner, admin, member := uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{owner, admin, member} {
		_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','audit user')`, u, u.String()+"@audit.test", "au-"+u.String()[:30])
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, u) })
	}
	_, err := db.Exec(`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'chat audit',$2,$3)`, ws, ws.String(), owner)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	_, err = db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($4,$1,$2,'admin'), ($5,$1,$3,'member')`, ws, admin, member, uuid.New(), uuid.New())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'p',$3)`, project, ws, project.String())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'a',$3,'h','p')`, agent, ws, agent.String())
	require.NoError(t, err)
	thread, turn := uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, execution_epoch, runtime)
		VALUES ($1,$2,$3,$4,$5,1,'{"runner_id":"r-1","harness":"claude","prompt":"never returned"}'::jsonb)`, thread, ws, project, agent, member)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, state, runner_id, attempt, execution_epoch,
			session_id, lease_until, expires_at, first_claimed_at, started_at, last_seq)
		VALUES ($1,$2,$3,$4,'streaming',$5,1,1,'sess-1', now() + interval '1 minute', now() + interval '1 hour',
			now() - interval '5 minutes', now() - interval '5 minutes', 1)`, turn, ws, thread, agent, uuid.New())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ext_agent_chat_messages (workspace_id, thread_id, role, body, status, turn_id) VALUES ($1,$2,'agent','secret body','streaming',$3)`, ws, thread, turn)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ext_agent_chat_tool_events (turn_id, seq, workspace_id, kind, label) VALUES ($1,1,$2,'tool_use','secret label')`, turn, ws)
	require.NoError(t, err)
	session := uuid.New()
	_, err = db.Exec(`INSERT INTO chat_session_tokens (token_hash, user_id, agent_id, workspace_id, project_id, chat_thread_id, chat_session_id,
			runner_id, execution_epoch, issued_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'r-1',1, now() - interval '10 minutes', now() + interval '10 minutes')`,
		"hash-"+session.String(), member, agent, ws, project, thread, session)
	require.NoError(t, err)
	mem := uuid.New()
	_, err = db.Exec(`INSERT INTO memories (id, workspace_id, agent_id, key, content, scope) VALUES ($1,$2,$3,'secret key','secret content','workspace')`, mem, ws, agent)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO memory_revisions (memory_id, version, content, action, actor_agent_id, created_at, workspace_id, reason)
		VALUES ($1,1,'secret content','created',$2, now() - interval '2 minutes', $3, 'because')`, mem, agent, ws)
	require.NoError(t, err)

	repo := NewChatAuditRepo(db)
	adminActor := UserRuntimeActor{UserID: admin}

	// Positive control: the admin and the owner (by owner_id, no member row) read.
	got, err := repo.ThreadAudit(ctx, adminActor, ws, thread)
	require.NoError(t, err)
	require.NotNil(t, got)
	_, err = repo.ThreadAudit(ctx, UserRuntimeActor{UserID: owner}, ws, thread)
	require.NoError(t, err)
	ev, err := repo.ThreadEvents(ctx, adminActor, ws, thread)
	require.NoError(t, err)
	require.NotNil(t, ev)
	sessions, err := repo.AgentSessions(ctx, adminActor, ws, agent)
	require.NoError(t, err)
	require.NotNil(t, sessions)
	writes, err := repo.MemoryWrites(ctx, adminActor, ws, &thread, 10)
	require.NoError(t, err)
	require.Len(t, writes, 1)
	require.Equal(t, ChatAttributionDerived, writes[0].Attribution)
	require.Equal(t, turn, *writes[0].TurnID)
	all, err := repo.MemoryWrites(ctx, adminActor, ws, nil, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)

	// Red controls: anyone else gets 403, a thread or agent of another workspace 404.
	var e *apierror.Error
	for who, actor := range map[string]UserRuntimeActor{
		"member": {UserID: member}, "agent": {AgentID: agent}, "connector": {UserID: admin, Connector: true}, "nobody": {},
	} {
		_, err = repo.ThreadAudit(ctx, actor, ws, thread)
		require.ErrorAs(t, err, &e, who)
		require.Equal(t, http.StatusForbidden, e.Code, who)
		_, err = repo.ThreadEvents(ctx, actor, ws, thread)
		require.ErrorAs(t, err, &e, who)
		_, err = repo.AgentSessions(ctx, actor, ws, agent)
		require.ErrorAs(t, err, &e, who)
		_, err = repo.MemoryWrites(ctx, actor, ws, nil, 10)
		require.ErrorAs(t, err, &e, who)
	}
	_, err = repo.ThreadAudit(ctx, adminActor, ws, uuid.New())
	require.ErrorAs(t, err, &e)
	require.Equal(t, http.StatusNotFound, e.Code)
	_, err = repo.ThreadEvents(ctx, adminActor, ws, uuid.New())
	require.ErrorAs(t, err, &e)
	require.Equal(t, http.StatusNotFound, e.Code)
	_, err = repo.AgentSessions(ctx, adminActor, ws, uuid.New())
	require.ErrorAs(t, err, &e)
	require.Equal(t, http.StatusNotFound, e.Code)
	unknown := uuid.New()
	_, err = repo.MemoryWrites(ctx, adminActor, ws, &unknown, 10)
	require.ErrorAs(t, err, &e)
	require.Equal(t, http.StatusNotFound, e.Code)
	// A workspace the admin does not belong to is 403 before any lookup.
	_, err = repo.ThreadAudit(ctx, adminActor, uuid.New(), thread)
	require.ErrorAs(t, err, &e)
	require.Equal(t, http.StatusForbidden, e.Code)
}
