package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func seedChatProject(t *testing.T, db *sqlx.DB, ws uuid.UUID) (project, inProgress uuid.UUID) {
	t.Helper()
	project, inProgress = uuid.New(), uuid.New()
	short := project.String()[:8]
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,$3,$3)`, project, ws, "chat-"+short)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO task_statuses (id, project_id, name, slug, category) VALUES ($1,$2,'In progress','in-progress','in_progress')`, inProgress, project)
	require.NoError(t, err)
	return project, inProgress
}

func addChatMember(t *testing.T, db *sqlx.DB, project, ws, agent uuid.UUID) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO project_members (id, project_id, agent_id, workspace_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), project, agent, ws)
	require.NoError(t, err)
}

func giveRunner(t *testing.T, db *sqlx.DB, agent uuid.UUID, leaseSQL string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO agent_controllers (agent_id, runner_id, runner_name, host, lease_expires_at) VALUES ($1,'r1','runner','h',`+leaseSQL+`)`, agent)
	require.NoError(t, err)
}

func checkOut(t *testing.T, db *sqlx.DB, project, status, agent uuid.UUID, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := db.ExecContext(context.Background(), `INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by, checked_out_by) VALUES ($1,$2,$3,'t',(SELECT COALESCE(MAX(task_number),0)+1 FROM tasks WHERE project_id=$2),$4,$4)`, uuid.New(), project, status, agent)
		require.NoError(t, err)
	}
}

// chatTurns opens one thread per turn (a thread holds one unfinished turn) and
// puts each turn into the given state.
func chatTurns(t *testing.T, db *sqlx.DB, ws, project, agent, user uuid.UUID, state string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		thread := uuid.New()
		_, err := db.ExecContext(context.Background(), `INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id) VALUES ($1,$2,$3,$4,$5)`, thread, ws, project, agent, user)
		require.NoError(t, err)
		_, err = db.ExecContext(context.Background(), `INSERT INTO ext_agent_chat_turns (workspace_id, thread_id, agent_id, state, expires_at) VALUES ($1,$2,$3,$4, now() + interval '1 hour')`, ws, thread, agent, state)
		require.NoError(t, err)
	}
}

func chatInts(out map[string]any, field string) map[string]int {
	got := map[string]int{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		got[m["name"].(string)] = int(m[field].(float64))
	}
	return got
}

func chatNames(out map[string]any) map[string]string {
	got := map[string]string{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		got[m["name"].(string)] = m["presence"].(string)
	}
	return got
}

func TestChatAgentPicker_ListsRunnerServedProjectAgents(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, user := seedRuntimeWorkspace(t, db)
	project, inProgress := seedChatProject(t, db, ws)
	otherProject, _ := seedChatProject(t, db, ws)

	mk := func(name string) uuid.UUID {
		// seedOperatorAgent gives every agent a limit of five concurrent tasks.
		id := seedOperatorAgent(t, db, ws, &user)
		_, err := db.ExecContext(context.Background(), `UPDATE agents SET name=$2 WHERE id=$1`, id, name)
		require.NoError(t, err)
		return id
	}
	online, offline, busy := mk("a-online"), mk("b-offline"), mk("c-busy")
	noRunner, foreign := mk("d-no-runner"), mk("e-other-project")
	for _, a := range []uuid.UUID{online, offline, busy, noRunner} {
		addChatMember(t, db, project, ws, a)
	}
	addChatMember(t, db, otherProject, ws, foreign)
	giveRunner(t, db, online, `now() + interval '1 hour'`)
	giveRunner(t, db, offline, `now() - interval '1 minute'`)
	giveRunner(t, db, busy, `now() + interval '1 hour'`)
	giveRunner(t, db, foreign, `now() + interval '1 hour'`)
	// Load is chat sessions (claimed|streaming turns), not cards. Red control:
	// "online" holds five checked-out cards and must stay online; "busy" holds
	// five sessions and no cards and must be busy.
	checkOut(t, db, project, inProgress, online, 5)
	chatTurns(t, db, ws, project, busy, user, "streaming", 3)
	chatTurns(t, db, ws, project, busy, user, "claimed", 2)
	chatTurns(t, db, ws, project, busy, user, "queued", 2)
	chatTurns(t, db, ws, otherProject, busy, user, "queued", 1) // another project's queue
	chatTurns(t, db, ws, project, busy, user, "done", 4)        // finished turns do not count
	chatTurns(t, db, ws, project, online, user, "streaming", 4) // below the limit of 5

	h := NewChatAgentPickerHandler(postgres.NewChatAgentPickerRepo(db))
	params := map[string]string{"proj_id": project.String()}
	status, out := callUserRuntime(t, h.List, http.MethodGet, "/", "", user, uuid.Nil, params)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string]string{"a-online": "online", "b-offline": "offline", "c-busy": "busy"}, chatNames(out),
		"runner-less agent and the other project's agent must not appear")

	require.Equal(t, map[string]int{"a-online": 4, "b-offline": 0, "c-busy": 5}, chatInts(out, "active_sessions"))
	require.Equal(t, map[string]int{"a-online": 0, "b-offline": 0, "c-busy": 2}, chatInts(out, "queue_length"),
		"queue is the project's queued turns only")

	// Red control: the other project's picker shows only its own agent.
	status, out = callUserRuntime(t, h.List, http.MethodGet, "/", "", user, uuid.Nil, map[string]string{"proj_id": otherProject.String()})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string]string{"e-other-project": "online"}, chatNames(out))

	// Agent keys are refused.
	status, _ = callUserRuntime(t, h.List, http.MethodGet, "/", "", uuid.Nil, online, params)
	require.Equal(t, http.StatusForbidden, status)
}

// Guest agents are out of the picker on purpose: an agent whose home workspace
// differs from the project's is served by its home workspace's runner, even with
// an active grant and a controller row. Red control: the same wiring for an agent
// homed in the project's workspace lists it.
func TestChatAgentPicker_ExcludesGuestAgentWithActiveGrant(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, user := seedRuntimeWorkspace(t, db)
	homeWS, homeUser := seedRuntimeWorkspace(t, db)
	project, _ := seedChatProject(t, db, ws)

	guest := seedOperatorAgent(t, db, homeWS, &homeUser)
	local := seedOperatorAgent(t, db, ws, &user)
	// A project membership needs the grant first (DB trigger).
	_, err := db.ExecContext(context.Background(), `INSERT INTO agent_workspace_grants (agent_id, workspace_id, api_key_prefix, api_key_hash) VALUES ($1,$2,'gst','x')`, guest, ws)
	require.NoError(t, err)
	for a, name := range map[uuid.UUID]string{guest: "guest", local: "local"} {
		_, err := db.ExecContext(context.Background(), `UPDATE agents SET name=$2 WHERE id=$1`, a, name)
		require.NoError(t, err)
		addChatMember(t, db, project, ws, a)
		giveRunner(t, db, a, `now() + interval '1 hour'`)
	}

	h := NewChatAgentPickerHandler(postgres.NewChatAgentPickerRepo(db))
	status, out := callUserRuntime(t, h.List, http.MethodGet, "/", "", user, uuid.Nil, map[string]string{"proj_id": project.String()})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, map[string]string{"local": "online"}, chatNames(out), "guest agent with an active grant must stay out")
}
