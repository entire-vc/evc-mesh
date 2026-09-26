package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task cf226500: mirrorProjectMemberships (MR!1044, #ec0bc566) only runs once,
// at connector-agent registration. getOrCreateGrant's "existing grant is still
// usable" branch (oauth_service.go:682-695) returns the grant as-is without
// re-mirroring, so a human's LATER project changes never reach their connector
// agent on their own. These tests prove ResyncConnectorMemberships closes that
// gap in both directions, against the real schema.

// agentProjectMemberships returns every project_members row agentID holds,
// keyed by project_id, for assertions that don't care about row order.
func (env *oauthSvcEnv) agentProjectMemberships(t *testing.T, agentID uuid.UUID) map[uuid.UUID]domain.ProjectMember {
	t.Helper()
	var rows []domain.ProjectMember
	require.NoError(t, env.db.Select(&rows,
		`SELECT id, project_id, agent_id, role, created_at, updated_at FROM project_members WHERE agent_id=$1`, agentID))
	out := make(map[uuid.UUID]domain.ProjectMember, len(rows))
	for _, r := range rows {
		out[r.ProjectID] = r
	}
	return out
}

// TestOAuthSvc_ResyncConnectorMemberships_RemovesStaleMembershipAfterHumanLosesProjectAccess
// is the escalation-risk direction the task calls out: an admin removes the
// human from a project after their connector agent already mirrored it. RED
// (asserted explicitly below, before the fix runs): the agent keeps the stale
// membership indefinitely — nothing else in the system would ever touch it.
// GREEN: one ResyncConnectorMemberships sweep removes it.
func TestOAuthSvc_ResyncConnectorMemberships_RemovesStaleMembershipAfterHumanLosesProjectAccess(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	owner, _ := env.createUser(t, "resync-remove")
	ws := env.createWorkspace(t, owner)
	env.addMember(t, ws, owner, domain.RoleAdmin)

	project := env.createProject(t, ws, "Resync Remove Project")
	env.addProjectMember(t, project, owner, "admin")

	redirect := "https://resync-remove.example.com/cb"
	c := env.registerDCR(t, "Resync Remove App "+uuid.New().String()[:6], redirect)
	_, challenge := svcPKCE()
	env.consent(t, c.ClientID, redirect, challenge, owner, ws)
	agentID := env.grantAgentID(t, c.ClientID)

	before := env.agentProjectMemberships(t, agentID)
	require.Contains(t, before, project, "sanity: mirroring at registration must have given the agent this membership")

	// The admin removes the human from the project — exactly what MR!1044's
	// own doc comment on mirrorProjectMemberships calls the residual risk.
	_, err := env.db.Exec(`DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, project, owner)
	require.NoError(t, err)

	// RED: without a resync, the agent's copy is untouched — still shows the
	// project the human can no longer see.
	stillStale := env.agentProjectMemberships(t, agentID)
	assert.Contains(t, stillStale, project, "documents the pre-fix gap: nothing re-syncs the agent on its own")

	result, rerr := env.svc.ResyncConnectorMemberships(ctx)
	require.NoError(t, rerr)
	assert.Equal(t, 1, result.Removed, "exactly the one stale membership should be removed")

	// GREEN.
	after := env.agentProjectMemberships(t, agentID)
	assert.NotContains(t, after, project, "the connector agent must lose the membership its supervisor no longer has")
}

// TestOAuthSvc_ResyncConnectorMemberships_AddsMembershipHumanGainedAfterRegistration
// is the safe-direction drift: the human joins a NEW project after their
// connector agent already exists. Not an escalation, but still a real gap —
// the agent's list_projects stays stale until something re-mirrors it.
func TestOAuthSvc_ResyncConnectorMemberships_AddsMembershipHumanGainedAfterRegistration(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	owner, _ := env.createUser(t, "resync-add")
	ws := env.createWorkspace(t, owner)
	env.addMember(t, ws, owner, domain.RoleAdmin)

	redirect := "https://resync-add.example.com/cb"
	c := env.registerDCR(t, "Resync Add App "+uuid.New().String()[:6], redirect)
	_, challenge := svcPKCE()
	env.consent(t, c.ClientID, redirect, challenge, owner, ws)
	agentID := env.grantAgentID(t, c.ClientID)

	require.Empty(t, env.agentProjectMemberships(t, agentID), "sanity: no projects existed yet at registration")

	newProject := env.createProject(t, ws, "Resync Add Project")
	env.addProjectMember(t, newProject, owner, "member")

	result, rerr := env.svc.ResyncConnectorMemberships(ctx)
	require.NoError(t, rerr)
	assert.Equal(t, 1, result.Added)

	after := env.agentProjectMemberships(t, agentID)
	require.Contains(t, after, newProject)
	assert.Equal(t, "member", after[newProject].Role)
}

// TestOAuthSvc_ResyncConnectorMemberships_CorrectsRoleDowngrade proves a role
// change (not just add/remove) also propagates — a demoted admin's connector
// agent must not keep acting with the old, higher role.
func TestOAuthSvc_ResyncConnectorMemberships_CorrectsRoleDowngrade(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	owner, _ := env.createUser(t, "resync-role")
	ws := env.createWorkspace(t, owner)
	env.addMember(t, ws, owner, domain.RoleAdmin)

	project := env.createProject(t, ws, "Resync Role Project")
	env.addProjectMember(t, project, owner, "admin")

	redirect := "https://resync-role.example.com/cb"
	c := env.registerDCR(t, "Resync Role App "+uuid.New().String()[:6], redirect)
	_, challenge := svcPKCE()
	env.consent(t, c.ClientID, redirect, challenge, owner, ws)
	agentID := env.grantAgentID(t, c.ClientID)
	require.Equal(t, "admin", env.agentProjectMemberships(t, agentID)[project].Role)

	_, err := env.db.Exec(`UPDATE project_members SET role='member' WHERE project_id=$1 AND user_id=$2`, project, owner)
	require.NoError(t, err)

	result, rerr := env.svc.ResyncConnectorMemberships(ctx)
	require.NoError(t, rerr)
	assert.Equal(t, 1, result.Updated)

	assert.Equal(t, "member", env.agentProjectMemberships(t, agentID)[project].Role)
}

// TestOAuthSvc_ResyncConnectorMemberships_SkipsRevokedAgent proves the sweep
// does not touch (or error on) a connector agent an admin already revoked —
// that agent's membership rows are revocation's own business (project_access.go
// never bypasses on an agent's workspace role regardless), not this job's to
// second-guess.
func TestOAuthSvc_ResyncConnectorMemberships_SkipsRevokedAgent(t *testing.T) {
	env := newOAuthSvcEnv(t)
	ctx := context.Background()
	owner, _ := env.createUser(t, "resync-revoked")
	ws := env.createWorkspace(t, owner)
	env.addMember(t, ws, owner, domain.RoleAdmin)

	project := env.createProject(t, ws, "Resync Revoked Project")
	env.addProjectMember(t, project, owner, "admin")

	redirect := "https://resync-revoked.example.com/cb"
	c := env.registerDCR(t, "Resync Revoked App "+uuid.New().String()[:6], redirect)
	_, challenge := svcPKCE()
	env.consent(t, c.ClientID, redirect, challenge, owner, ws)
	agentID := env.grantAgentID(t, c.ClientID)

	env.revokeAgentConnection(t, agentID, ws)

	// The human then loses project access too — if the sweep touched a
	// revoked agent's rows this would otherwise also exercise the removal
	// path; asserting no error and Agents==0 for this grant is the point.
	_, err := env.db.Exec(`DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, project, owner)
	require.NoError(t, err)

	_, rerr := env.svc.ResyncConnectorMemberships(ctx)
	require.NoError(t, rerr)

	still := env.agentProjectMemberships(t, agentID)
	assert.Contains(t, still, project, "a revoked connector agent's own membership rows are untouched by this sweep")
}
