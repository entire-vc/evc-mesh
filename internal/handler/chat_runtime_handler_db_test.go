package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// runtimeEnv is the chat env plus the controller handler that stores snapshots
// and the read routes switched on. giveRunner already made "r1" the agent's
// controller with a live lease.
type runtimeEnv struct {
	*agentChatEnv
	ctl *AgentControllerHandler
}

func newRuntimeEnv(t *testing.T) *runtimeEnv {
	t.Helper()
	env := newAgentChatEnv(t)
	repo := postgres.NewAgentControllerRepo(env.db)
	env.h.WithRunnerSnapshots(repo)
	return &runtimeEnv{agentChatEnv: env, ctl: NewAgentControllerHandler(repo).WithChatSnapshots(repo)}
}

// announceAs posts one controller announce as the env's agent; extra is
// spliced into the body after the identity fields ("" sends none).
func (e *runtimeEnv) announceAs(t *testing.T, runner, extra string) (status int, out map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"runner_id":%q,"name":"runner","host":"h","version":"1"%s}`, runner, extra)
	return callUserRuntime(t, e.ctl.Announce, http.MethodPost, "/", body, uuid.Nil, e.agent, nil)
}

func (e *runtimeEnv) runtime(t *testing.T, user uuid.UUID, thread string) (status int, out map[string]any) {
	t.Helper()
	return e.call(t, e.h.ThreadRuntime, http.MethodGet, "", user, e.thr(thread))
}

func (e *runtimeEnv) sessions(t *testing.T, user uuid.UUID, runner string) (status int, out map[string]any) {
	t.Helper()
	return e.call(t, e.h.RunnerSessions, http.MethodGet, "", user,
		map[string]string{"proj_id": e.project.String(), "runner_id": runner})
}

func (e *runtimeEnv) ageSnapshot(t *testing.T, interval string) {
	t.Helper()
	_, err := e.db.ExecContext(context.Background(),
		`UPDATE agent_controller_chat_snapshots SET received_at = now() - $2::interval WHERE agent_id = $1`, e.agent, interval)
	require.NoError(t, err)
}

func aliveSession(thread string) string {
	return fmt.Sprintf(`,"sessions":[{"thread_id":%q,"agent":"claude","process_alive":true,"slot_held":true,"busy":true,"last_end_at":null}],"slots":{"max_per_agent":3,"used":1},"reported_at":"2026-11-10T20:00:00Z"`, thread)
}

// AC1: an announce with a snapshot is what the runtime answers; an announce
// without the field neither fails nor touches the stored snapshot; [] says the
// slot is released and the process gone.
func TestChatRuntimeSnapshot_AnnounceStoresAndRuntimeReads(t *testing.T) {
	env := newRuntimeEnv(t)
	thread := env.createThread(t, env.userA)

	// Before any snapshot: unknown, not "gone".
	code, out := env.runtime(t, env.userA, thread)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, false, out["fresh"])
	require.Nil(t, out["runner_id"])
	require.Nil(t, out["process_alive"])
	require.Nil(t, out["slot_held"])

	code, out = env.announceAs(t, "r1", aliveSession(thread))
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, true, out["chat_snapshot_saved"])

	code, out = env.runtime(t, env.userA, thread)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, thread, out["thread_id"])
	require.Equal(t, env.agent.String(), out["agent_id"])
	require.Equal(t, "r1", out["runner_id"])
	require.Equal(t, true, out["fresh"])
	require.Equal(t, true, out["process_alive"])
	require.Equal(t, true, out["slot_held"])
	require.Equal(t, true, out["busy"])
	require.Equal(t, "2026-11-10T20:00:00Z", out["reported_at"])
	require.EqualValues(t, 120, out["fresh_for_seconds"])

	// An old runner without the field: 200, snapshot unchanged.
	code, out = env.announceAs(t, "r1", "")
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.NotContains(t, out, "chat_snapshot_saved")
	_, out = env.runtime(t, env.userA, thread)
	require.Equal(t, true, out["process_alive"], "an announce without sessions must not change the snapshot")
	require.Equal(t, true, out["slot_held"])

	// null is the same as absent.
	code, _ = env.announceAs(t, "r1", `,"sessions":null`)
	require.Equal(t, http.StatusOK, code)
	_, out = env.runtime(t, env.userA, thread)
	require.Equal(t, true, out["process_alive"])

	// A second runner without the lease is refused and stores nothing.
	code, _ = env.announceAs(t, "r2", `,"sessions":[]`)
	require.Equal(t, http.StatusConflict, code)
	_, out = env.runtime(t, env.userA, thread)
	require.Equal(t, "r1", out["runner_id"])
	require.Equal(t, true, out["slot_held"])

	// After reap: the runner reports no session for the thread.
	code, _ = env.announceAs(t, "r1", `,"sessions":[],"slots":{"max_per_agent":3,"used":0}`)
	require.Equal(t, http.StatusOK, code)
	_, out = env.runtime(t, env.userA, thread)
	require.Equal(t, true, out["fresh"])
	require.Equal(t, false, out["process_alive"])
	require.Equal(t, false, out["slot_held"])

	// Reaped sessions remain in the runner's diagnostic snapshot with a
	// released slot. Presence alone must not turn the slot back on.
	code, _ = env.announceAs(t, "r1", fmt.Sprintf(`,"sessions":[{"thread_id":%q,"agent":%q,"process_alive":false,"slot_held":false,"busy":false}],"slots":{"max_per_agent":3,"used":0}`, thread, env.agent.String()))
	require.Equal(t, http.StatusOK, code)
	_, out = env.runtime(t, env.userA, thread)
	require.Equal(t, true, out["fresh"])
	require.Equal(t, false, out["process_alive"])
	require.Equal(t, false, out["slot_held"])
	_, listing := env.sessions(t, env.userA, "r1")
	listed := listing["agents"].([]any)[0].(map[string]any)["sessions"].([]any)
	require.Len(t, listed, 1)
	require.Equal(t, false, listed[0].(map[string]any)["slot_held"])

	// A held slot whose process died is reported as such.
	code, _ = env.announceAs(t, "r1", fmt.Sprintf(`,"sessions":[{"thread_id":%q,"agent":"claude","process_alive":false,"busy":false,"last_end_at":"2026-11-10T19:59:00Z"}]`, thread))
	require.Equal(t, http.StatusOK, code)
	_, out = env.runtime(t, env.userA, thread)
	require.Equal(t, false, out["process_alive"])
	require.Equal(t, true, out["slot_held"])
	require.Equal(t, "2026-11-10T19:59:00Z", out["last_end_at"])

	// A malformed snapshot is a 400 and does not overwrite the stored one.
	for name, extra := range map[string]string{
		"bad thread id":  `,"sessions":[{"thread_id":"nope"}]`,
		"duplicate":      fmt.Sprintf(`,"sessions":[{"thread_id":%q},{"thread_id":%q}]`, thread, thread),
		"negative slots": `,"sessions":[],"slots":{"max_per_agent":-1}`,
	} {
		code, _ = env.announceAs(t, "r1", extra)
		require.Equal(t, http.StatusBadRequest, code, name)
	}
	_, out = env.runtime(t, env.userA, thread)
	require.Equal(t, true, out["slot_held"], "a refused announce must leave the snapshot")
}

// AC2: a snapshot older than two announce periods is unknown, never "gone".
// The 60 s case is the positive control: the same row inside the window is fresh.
func TestChatRuntimeSnapshot_StaleSnapshotIsUnknown(t *testing.T) {
	env := newRuntimeEnv(t)
	thread := env.createThread(t, env.userA)
	code, _ := env.announceAs(t, "r1", aliveSession(thread))
	require.Equal(t, http.StatusOK, code)

	env.ageSnapshot(t, "60 seconds")
	_, out := env.runtime(t, env.userA, thread)
	require.Equal(t, true, out["fresh"], "a snapshot inside the window is fresh: %v", out)
	require.Equal(t, true, out["process_alive"])
	_, sess := env.sessions(t, env.userA, "r1")
	require.Equal(t, true, sess["fresh"])
	agents := sess["agents"].([]any)
	require.Len(t, agents, 1)
	require.EqualValues(t, 1, agents[0].(map[string]any)["alive_total"])

	env.ageSnapshot(t, "3 minutes")
	code, out = env.runtime(t, env.userA, thread)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["fresh"], "a snapshot older than %s must not be fresh: %v", ChatSnapshotFreshFor, out)
	require.Nil(t, out["process_alive"], "stale is unknown, not process_alive=false")
	require.Nil(t, out["slot_held"])
	require.Equal(t, "r1", out["runner_id"], "the stale snapshot's origin is still named")
	require.GreaterOrEqual(t, out["age_seconds"].(float64), float64(170))

	_, sess = env.sessions(t, env.userA, "r1")
	require.Equal(t, false, sess["fresh"])
	a := sess["agents"].([]any)[0].(map[string]any)
	require.Equal(t, false, a["fresh"])
	require.Nil(t, a["alive_total"], "stale counts are unknown")
	require.Nil(t, a["sessions_total"])
}

// AC3: the runtime reads like the thread itself. The author gets 200 with
// content; another member, and a user of another workspace, get 404/403; the
// runner listing shows counts to the workspace but entries only to the author.
func TestChatRuntimeSnapshot_Access(t *testing.T) {
	env := newRuntimeEnv(t)
	thread := env.createThread(t, env.userA)
	code, _ := env.announceAs(t, "r1", aliveSession(thread))
	require.Equal(t, http.StatusOK, code)

	code, out := env.runtime(t, env.userA, thread)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["process_alive"])
	require.Equal(t, "r1", out["runner_id"])

	_, missing := env.runtime(t, env.userB, uuid.NewString())
	code, out = env.runtime(t, env.userB, thread)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, missing, out, "another member sees the thread as missing")
	require.NotContains(t, fmt.Sprint(out), "r1")

	// Another workspace: its own project with the foreign thread id, and the foreign project.
	other := newRuntimeEnv(t)
	code, out = other.call(t, other.h.ThreadRuntime, http.MethodGet, "", other.userA,
		map[string]string{"proj_id": other.project.String(), "thread_id": thread})
	require.Equal(t, http.StatusNotFound, code, "%v", out)
	code, out = other.call(t, other.h.ThreadRuntime, http.MethodGet, "", other.userA, env.thr(thread))
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, code, "%v", out)
	require.NotContains(t, fmt.Sprint(out), "process_alive")

	// Runner listing: the author sees their thread; another member only counts.
	code, out = env.sessions(t, env.userA, "r1")
	require.Equal(t, http.StatusOK, code, "%v", out)
	a := out["agents"].([]any)[0].(map[string]any)
	require.Equal(t, env.agent.String(), a["agent_id"])
	require.EqualValues(t, 1, a["sessions_total"])
	require.EqualValues(t, 1, a["alive_total"])
	require.EqualValues(t, 3, a["slots"].(map[string]any)["max_per_agent"])
	require.EqualValues(t, 1, a["slots"].(map[string]any)["used"])
	entries := a["sessions"].([]any)
	require.Len(t, entries, 1)
	require.Equal(t, thread, entries[0].(map[string]any)["thread_id"])
	require.Equal(t, true, entries[0].(map[string]any)["process_alive"])

	code, out = env.sessions(t, env.userB, "r1")
	require.Equal(t, http.StatusOK, code)
	b := out["agents"].([]any)[0].(map[string]any)
	require.EqualValues(t, 1, b["sessions_total"])
	require.Empty(t, b["sessions"], "another member's listing must not name the author's thread")

	// The other workspace has no snapshot of r1 for its agents.
	code, out = other.sessions(t, other.userA, "r1")
	require.Equal(t, http.StatusNotFound, code, "%v", out)
	require.NotContains(t, fmt.Sprint(out), thread)
	code, _ = env.sessions(t, env.userA, "unknown-runner")
	require.Equal(t, http.StatusNotFound, code)
}
