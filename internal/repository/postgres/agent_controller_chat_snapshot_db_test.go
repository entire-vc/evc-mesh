package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// The snapshot store against PostgreSQL: written only by the live lease
// holder, overwritten (not appended), newest-first per agent, listed per
// runner only for agents of the asked workspace.
func TestChatRuntimeSnapshot_RepoAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the chat snapshot store against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ws, otherWS, agent, foreign := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, w := range []uuid.UUID{ws, otherWS} {
		_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'snapshot fixture',$2,$3)`, w, w.String(), uuid.New())
		require.NoError(t, err)
		w := w
		t.Cleanup(func() { _, _ = db.Exec("DELETE FROM workspaces WHERE id=$1", w) })
	}
	for a, w := range map[uuid.UUID]uuid.UUID{agent: ws, foreign: otherWS} {
		_, err = db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'fixture',$3,'fixture','test')`, a, w, a.String())
		require.NoError(t, err)
	}

	repo := NewAgentControllerRepo(db)
	ctx := context.Background()
	runner := "snap-" + agent.String()[:8]
	thread := uuid.New()
	used, maxSlots := 1, 3
	snap := func(a uuid.UUID, r string, alive bool) repository.ChatRunnerSnapshot {
		return repository.ChatRunnerSnapshot{AgentID: a, RunnerID: r, SlotsMax: &maxSlots, SlotsUsed: &used,
			ReportedAt: time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC),
			Sessions:   []repository.ChatSessionEntry{{ThreadID: thread, Agent: "claude", ProcessAlive: alive}}}
	}

	got, err := repo.LatestChatSnapshot(ctx, agent)
	require.NoError(t, err)
	require.Nil(t, got, "no snapshot yet")

	// Without the lease nothing is written.
	saved, err := repo.SaveChatSnapshot(ctx, snap(agent, runner, true))
	require.NoError(t, err)
	require.False(t, saved, "a runner without the controller lease must not write")

	_, held, err := repo.Claim(ctx, repository.AgentController{AgentID: agent, RunnerID: runner, RunnerName: "r", Host: "h"}, time.Minute)
	require.NoError(t, err)
	require.True(t, held)
	saved, err = repo.SaveChatSnapshot(ctx, snap(agent, runner, true))
	require.NoError(t, err)
	require.True(t, saved)

	got, err = repo.LatestChatSnapshot(ctx, agent)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, runner, got.RunnerID)
	require.Len(t, got.Sessions, 1)
	require.Equal(t, thread, got.Sessions[0].ThreadID)
	require.True(t, got.Sessions[0].ProcessAlive)
	require.Equal(t, 3, *got.SlotsMax)
	require.Equal(t, 1, *got.SlotsUsed)
	require.Equal(t, time.UTC, got.ReportedAt.Location())
	require.Equal(t, time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC), got.ReportedAt)
	require.Less(t, got.Age, 30*time.Second)

	// Overwrite, not history: a nil session list is stored as [].
	empty := snap(agent, runner, false)
	empty.Sessions = nil
	saved, err = repo.SaveChatSnapshot(ctx, empty)
	require.NoError(t, err)
	require.True(t, saved)
	var rows int
	require.NoError(t, db.Get(&rows, `SELECT count(*) FROM agent_controller_chat_snapshots WHERE agent_id=$1`, agent))
	require.Equal(t, 1, rows)
	got, err = repo.LatestChatSnapshot(ctx, agent)
	require.NoError(t, err)
	require.Empty(t, got.Sessions)
	require.NotNil(t, got.Sessions)

	// The age is measured on the database clock.
	_, err = db.Exec(`UPDATE agent_controller_chat_snapshots SET received_at = now() - interval '3 minutes' WHERE agent_id=$1`, agent)
	require.NoError(t, err)
	got, err = repo.LatestChatSnapshot(ctx, agent)
	require.NoError(t, err)
	require.GreaterOrEqual(t, got.Age, 170*time.Second)

	// A lapsed lease stops further writes.
	_, err = db.Exec(`UPDATE agent_controllers SET lease_expires_at = now() - interval '1 second' WHERE agent_id=$1`, agent)
	require.NoError(t, err)
	saved, err = repo.SaveChatSnapshot(ctx, snap(agent, runner, true))
	require.NoError(t, err)
	require.False(t, saved)

	// Runner listing is confined to the asked workspace.
	_, held, err = repo.Claim(ctx, repository.AgentController{AgentID: foreign, RunnerID: runner, RunnerName: "r", Host: "h"}, time.Minute)
	require.NoError(t, err)
	require.True(t, held)
	saved, err = repo.SaveChatSnapshot(ctx, snap(foreign, runner, true))
	require.NoError(t, err)
	require.True(t, saved)
	list, err := repo.RunnerChatSnapshots(ctx, ws, runner)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, agent, list[0].AgentID)
	list, err = repo.RunnerChatSnapshots(ctx, otherWS, runner)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, foreign, list[0].AgentID)
	list, err = repo.RunnerChatSnapshots(ctx, ws, "no-such-runner")
	require.NoError(t, err)
	require.Empty(t, list)
}

// OwnThreadIDs keeps only the caller's live threads of the project: the
// visibility the runner listing relies on to hide other users' threads.
func TestChatRuntimeSnapshot_OwnThreadIDs(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run OwnThreadIDs against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ws, project, otherProject, agent, user, other := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'own threads',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM workspaces WHERE id=$1", ws) })
	for _, p := range []uuid.UUID{project, otherProject} {
		_, err = db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'p',$3)`, p, ws, "own-"+p.String()[:8])
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'fixture',$3,'fixture','test')`, agent, ws, agent.String())
	require.NoError(t, err)
	for _, u := range []uuid.UUID{user, other} {
		_, err = db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','u')`, u, u.String()+"@own.test", "own-"+u.String()[:30])
		require.NoError(t, err)
		u := u
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, u) })
	}
	thread := func(p, u uuid.UUID, deleted bool) uuid.UUID {
		id := uuid.New()
		_, insErr := db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, deleted_at) VALUES ($1,$2,$3,$4,$5, CASE WHEN $6 THEN now() END)`,
			id, ws, p, agent, u, deleted)
		require.NoError(t, insErr)
		return id
	}
	mine := thread(project, user, false)
	theirs := thread(project, other, false)
	elsewhere := thread(otherProject, user, false)
	gone := thread(project, user, true)

	repo := NewAgentChatRepo(db)
	ctx := context.Background()
	own, err := repo.OwnThreadIDs(ctx, ws, project, user, nil)
	require.NoError(t, err)
	require.Empty(t, own)
	own, err = repo.OwnThreadIDs(ctx, ws, project, user, []uuid.UUID{mine, theirs, elsewhere, gone, uuid.New()})
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]bool{mine: true}, own)
}
