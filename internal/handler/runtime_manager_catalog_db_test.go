package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// "Can manage My Runner": an agent key holding its operator's
// runtime_manager grant reads and writes the workspace catalog wherever the
// operator owns or administers the workspace and the agent is connected to it,
// and reads the operator's own runtime; without the grant, after revoke, or in
// any other workspace it gets 403 with the reason.
func TestRuntimeCatalog_RuntimeManagerGrant(t *testing.T) {
	f := newRuntimeHTTP(t)
	ctx := context.Background()
	users := postgres.NewUserRuntimeRepo(f.db)
	uh := NewUserRuntimeHandler(users)
	operator := postgres.UserRuntimeActor{UserID: f.ownerUser}
	manager := seedOperatorAgent(t, f.db, f.owner, &f.ownerUser)
	as := map[string]string{"X-Test-Agent": manager.String(), "X-Test-Agent-Workspace": f.owner.String()}
	base := "/workspaces/" + f.owner.String() + "/runtime"
	save := `{"if_revision":0,"config":` + string(f.catalog) + `}`

	// Without the grant: every catalog path and the operator's runtime are 403.
	for _, req := range [][2]string{{http.MethodGet, base}, {http.MethodGet, base + "/revisions"}, {http.MethodPut, base}} {
		code, _, body := f.do(req[0], req[1], save, as)
		require.Equal(t, http.StatusForbidden, code, req)
		require.Contains(t, body, "Can manage My Runner is off", req)
	}
	code, _ := callUserRuntime(t, uh.GetAsManager, http.MethodGet, "/", "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)

	require.NoError(t, users.GrantManager(ctx, operator, manager))

	// With the grant: read and write the catalog of the operator's workspace.
	code, _, body := f.do(http.MethodGet, base, "", as)
	require.Equal(t, http.StatusOK, code, body)
	code, _, body = f.do(http.MethodPut, base, save, as)
	require.Equal(t, http.StatusOK, code, body)
	code, revisions, body := f.do(http.MethodGet, base+"/revisions", "", as)
	require.Equal(t, http.StatusOK, code, body)
	list, _ := revisions["revisions"].([]any)
	require.Len(t, list, 1)
	rev, _ := list[0].(map[string]any)
	require.Equal(t, "agent", rev["author_type"], "a save by the key is authored by the agent")
	require.Equal(t, manager.String(), rev["author_agent_id"])
	require.Equal(t, f.ownerUser.String(), rev["author_id"], "for its operator")

	// ... and the operator's own runtime.
	code, out := callUserRuntime(t, uh.GetAsManager, http.MethodGet, "/", "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, f.ownerUser.String(), out["user_id"])
	code, out = callUserRuntime(t, uh.HistoryAsManager, http.MethodGet, "/", "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusOK, code, out)
	code, out = callUserRuntime(t, uh.OperatorAgentsAsManager, http.MethodGet, "/?workspace_id="+f.owner.String(), "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusOK, code, out)
	code, out = callUserRuntime(t, uh.OperatorRunnersAsManager, http.MethodGet, "/?workspace_id="+f.owner.String(), "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusOK, code, out)

	// A foreign workspace: the operator is no admin there and the agent is not connected.
	foreign := "/workspaces/" + f.receiver.String() + "/runtime"
	code, _, _ = f.do(http.MethodGet, foreign, "", as)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = callUserRuntime(t, uh.OperatorAgentsAsManager, http.MethodGet, "/?workspace_id="+f.receiver.String(), "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = callUserRuntime(t, uh.OperatorRunnersAsManager, http.MethodGet, "/?workspace_id="+f.receiver.String(), "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = callUserRuntime(t, uh.OperatorRunnersAsManager, http.MethodGet, "/", "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusBadRequest, code, "workspace_id is required")

	// Connected to it (live workspace grant) is not enough while the operator is no admin there.
	_, err := f.db.ExecContext(ctx, `INSERT INTO agent_workspace_grants (id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, created_at)
		VALUES ($1,$2,$3,'member',$4,$5,NOW())`, uuid.New(), manager, f.receiver, "m"+manager.String()[:8], "synthetic-m"+manager.String()[:8])
	require.NoError(t, err)
	code, _, body = f.do(http.MethodGet, foreign, "", as)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, body, "operator is not an admin of this workspace")

	// The operator administers a third workspace the agent is not connected to.
	// In the API this request is refused earlier, by RequireWorkspaceMemberScoped
	// (the key's workspace is not :ws_id → 403 "workspace access denied"); this
	// harness has no global guard, so it shows that RuntimeRepo refuses it too.
	third, _ := seedRuntimeWorkspace(t, f.db)
	t.Cleanup(func() { _, _ = f.db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id=$1`, third) })
	_, err = f.db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'admin')`, uuid.New(), third, f.ownerUser)
	require.NoError(t, err)
	code, _, body = f.do(http.MethodGet, "/workspaces/"+third.String()+"/runtime", "", as)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, body, "agent is not connected to this workspace")

	// Revoked: back to 403 everywhere.
	require.NoError(t, users.RevokeManager(ctx, operator, manager))
	code, _, body = f.do(http.MethodGet, base, "", as)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, body, "Can manage My Runner is off")
	code, _ = callUserRuntime(t, uh.GetAsManager, http.MethodGet, "/", "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = callUserRuntime(t, uh.HistoryAsManager, http.MethodGet, "/", "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)

	// The person's /me/runtime paths point an agent key to its own route.
	code, out = callUserRuntime(t, uh.Get, http.MethodGet, "/", "", uuid.Nil, manager, nil)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, out["message"], "/agents/me/operator-runtime")
}
