package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// An agent key holding its operator's runtime_manager grant sees exactly the
// operator's runner: the operator's document and history, the operator's view
// of a workspace the agent is connected to, and the catalog of a workspace the
// operator administers. Without the grant, after revoke, in a workspace the
// operator does not administer, or one the agent is not connected to: 403.
func TestUserRuntime_RuntimeManagerGrantScope(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	users := NewUserRuntimeRepo(f.db)
	operator := UserRuntimeActor{UserID: f.ownerActor.UserID}
	agent := f.worker.AgentID
	_, err := f.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=$2 WHERE id=$1`, agent, operator.UserID)
	require.NoError(t, err)
	asAgent := UserRuntimeActor{AgentID: agent}
	catalogAgent := RuntimeActor{AgentID: agent, AuthWorkspaceID: f.owner}

	forbidden := func(err error, reason string) {
		t.Helper()
		require.Error(t, err)
		require.Equal(t, 403, statusOf(t, err), err)
		require.Contains(t, err.Error(), reason)
	}
	const off = "Can manage My Runner is off"

	_, err = users.GetAsManager(ctx, asAgent)
	forbidden(err, off)
	_, err = users.HistoryAsManager(ctx, asAgent, 10)
	forbidden(err, off)
	_, err = users.OperatorAgentsAsManager(ctx, asAgent, f.owner)
	forbidden(err, off)
	_, err = f.repo.Inventory(ctx, f.owner, catalogAgent)
	forbidden(err, off)

	require.NoError(t, users.GrantManager(ctx, operator, agent))

	doc, err := users.GetAsManager(ctx, asAgent)
	require.NoError(t, err)
	require.Equal(t, operator.UserID, doc.UserID)
	_, err = users.HistoryAsManager(ctx, asAgent, 10)
	require.NoError(t, err)
	agents, err := users.OperatorAgentsAsManager(ctx, asAgent, f.owner)
	require.NoError(t, err)
	var listed bool
	for _, a := range agents {
		listed = listed || a.AgentID == agent
	}
	require.True(t, listed, "the operator's view lists the agent")
	_, err = users.OperatorRunnersAsManager(ctx, asAgent, f.owner)
	require.NoError(t, err)

	// Strict workspace isolation applies to the agent's save exactly as to its
	// operator's: the fixture binds worker-b in the receiver workspace, so the
	// save is refused as foreign after the grant check passes, and reads still work.
	strict := NewRuntimeRepo(f.db).WithStrictWorkspaceIsolation(true)
	_, err = strict.Save(ctx, f.owner, catalogAgent, f.input)
	require.Error(t, err)
	require.Equal(t, 422, statusOf(t, err), err)
	require.Contains(t, err.Error(), "runtime bindings must belong to this workspace")
	_, err = strict.Save(ctx, f.owner, f.ownerActor, f.input)
	require.Equal(t, 422, statusOf(t, err), "the operator gets the same refusal")

	_, err = f.repo.Save(ctx, f.owner, catalogAgent, f.input)
	require.NoError(t, err)
	_, err = f.repo.Inventory(ctx, f.owner, catalogAgent)
	require.NoError(t, err)
	_, err = strict.Inventory(ctx, f.owner, catalogAgent)
	require.NoError(t, err)

	// receiver: the agent is connected (live workspace grant), the operator is no admin there.
	_, err = f.repo.Inventory(ctx, f.receiver, RuntimeActor{AgentID: agent, AuthWorkspaceID: f.owner})
	forbidden(err, "operator is not an admin of this workspace")

	// third: the operator administers it, the agent is not connected.
	third := seedGrantWorkspace(t, f.db)
	t.Cleanup(func() { _, _ = f.db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id=$1`, third) })
	_, err = f.db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'admin')`, uuid.New(), third, operator.UserID)
	require.NoError(t, err)
	_, err = f.repo.Inventory(ctx, third, catalogAgent)
	forbidden(err, "agent is not connected to this workspace")
	_, err = users.OperatorAgentsAsManager(ctx, asAgent, third)
	forbidden(err, "agent is not connected to this workspace")
	_, err = users.OperatorRunnersAsManager(ctx, asAgent, third)
	forbidden(err, "agent is not connected to this workspace")

	require.NoError(t, users.RevokeManager(ctx, operator, agent))
	_, err = users.GetAsManager(ctx, asAgent)
	forbidden(err, off)
	_, err = users.OperatorRunnersAsManager(ctx, asAgent, f.owner)
	forbidden(err, off)
	_, err = f.repo.Inventory(ctx, f.owner, catalogAgent)
	forbidden(err, off)
}
