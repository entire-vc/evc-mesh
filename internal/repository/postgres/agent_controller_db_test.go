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

func TestAgentControllerRepo_LeaseAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run PostgreSQL lease controls")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ws, agent := uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'controller fixture',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM workspaces WHERE id=$1", ws) })
	_, err = db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'fixture',$3,'fixture','test')`, agent, ws, agent.String())
	require.NoError(t, err)

	repo := NewAgentControllerRepo(db)
	ctx := context.Background()
	mk := func(id, host string) repository.AgentController {
		return repository.AgentController{AgentID: agent, RunnerID: id, RunnerName: "runner-" + id, Host: host}
	}

	_, held, err := repo.Claim(ctx, mk("a", "h1"), time.Minute)
	require.NoError(t, err)
	require.True(t, held)
	_, held, err = repo.Claim(ctx, mk("a", "h1"), time.Minute)
	require.NoError(t, err)
	require.True(t, held, "same runner renews")

	cur, held, err := repo.Claim(ctx, mk("b", "h2"), time.Minute)
	require.NoError(t, err)
	require.False(t, held)
	require.Equal(t, "h1", cur.Host)

	// A foreign heartbeat must not extend a's lease; a's own must.
	require.NoError(t, repo.Renew(ctx, agent, "b", time.Hour))
	cur, _, _ = repo.Claim(ctx, mk("b", "h2"), time.Minute)
	require.Equal(t, "a", cur.RunnerID)
	require.True(t, cur.LeaseExpiresAt.Before(time.Now().Add(5*time.Minute)))

	// Expire a's lease; b takes over.
	_, err = db.Exec(`UPDATE agent_controllers SET lease_expires_at = now() - interval '1 second' WHERE agent_id=$1`, agent)
	require.NoError(t, err)
	cur, held, err = repo.Claim(ctx, mk("b", "h2"), time.Minute)
	require.NoError(t, err)
	require.True(t, held)
	require.Equal(t, "b", cur.RunnerID)

	// Agent with no controller: Renew is a no-op, not an error.
	require.NoError(t, repo.Renew(ctx, uuid.New(), "", time.Minute))
}
