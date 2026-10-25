package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// derivedFixture is a saved catalog with a fresh controller report, one agent
// of the operator, and helpers to flip its admission and the report age.
type derivedFixture struct {
	runtimeFixture
	repo     *UserRuntimeRepo
	actor    UserRuntimeActor
	agent    uuid.UUID
	integ    uuid.UUID
	admitted func(enabled bool)
}

func seedDerived(t *testing.T) derivedFixture {
	t.Helper()
	f := seedRuntime(t)
	ctx := context.Background()
	snapshot, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	report := domain.RuntimeReport{SchemaVersion: 2, Revision: snapshot.Revision, Digest: snapshot.Digest, Status: "applied", Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{}}
	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report))

	var integ uuid.UUID
	require.NoError(t, f.db.GetContext(ctx, &integ, `SELECT id FROM integration_configs WHERE workspace_id=$1 AND provider IN ('runner','agent_runtime')`, f.owner))
	_, err = f.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=$2 WHERE id=$1`, f.worker.AgentID, f.ownerActor.UserID)
	require.NoError(t, err)

	d := derivedFixture{runtimeFixture: f, repo: NewUserRuntimeRepo(f.db), actor: UserRuntimeActor{UserID: f.ownerActor.UserID}, agent: f.worker.AgentID, integ: integ}
	d.admitted = func(enabled bool) {
		_, e := f.db.ExecContext(ctx, `INSERT INTO runtime_binding_admissions(integration_id,binding_ref,workspace_id,agent_id,grant_id,grant_fingerprint,revision,catalog_digest,enabled,permitted_profiles,updated_by)
			VALUES($1,'worker-b',$2,$3,$4,'fp',1,$5,$6,'[]',$7)
			ON CONFLICT (integration_id,binding_ref) DO UPDATE SET enabled=$6`, integ, f.receiver, f.worker.AgentID, f.grant, snapshot.Digest, enabled, f.ownerActor.UserID)
		require.NoError(t, e)
	}
	return d
}

func (d derivedFixture) agentRow(t *testing.T) OperatorAgent {
	t.Helper()
	agents, err := d.repo.OperatorAgents(context.Background(), d.actor)
	require.NoError(t, err)
	for _, a := range agents {
		if a.AgentID == d.agent {
			return a
		}
	}
	t.Fatalf("agent %s missing from OperatorAgents", d.agent)
	return OperatorAgent{}
}

func TestOperatorAgents_DerivedController(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()

	require.Nil(t, d.agentRow(t).Controller, "no admission: no controller")

	d.admitted(false)
	require.Nil(t, d.agentRow(t).Controller, "disabled admission: no controller")

	d.admitted(true)
	c := d.agentRow(t).Controller
	require.NotNil(t, c)
	require.Equal(t, "runner-a", c.RunnerID)
	require.True(t, c.Live)

	runners, err := d.repo.OperatorRunners(ctx, d.actor)
	require.NoError(t, err)
	require.Len(t, runners, 1)
	require.Equal(t, "runner-a", runners[0].RunnerID)
	require.True(t, runners[0].Live)
	require.Len(t, runners[0].Agents, 1)
	require.Equal(t, d.agent, runners[0].Agents[0].AgentID)

	_, err = d.db.ExecContext(ctx, `UPDATE runtime_controller_reports SET received_at = now() - interval '1 day' WHERE integration_id=$1`, d.integ)
	require.NoError(t, err)
	c = d.agentRow(t).Controller
	require.NotNil(t, c)
	require.False(t, c.Live, "stale report: controller shown, not live")
}

func TestOperatorAgents_ExplicitAnnounceWins(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()
	d.admitted(true)
	_, err := d.db.ExecContext(ctx, `INSERT INTO agent_controllers(agent_id,runner_id,runner_name,host,version,lease_expires_at)
		VALUES($1,'announced','announced-name','h1','v1', now() + interval '1 minute')`, d.agent)
	require.NoError(t, err)
	c := d.agentRow(t).Controller
	require.NotNil(t, c)
	require.Equal(t, "announced", c.RunnerID)

	_, err = d.db.ExecContext(ctx, `UPDATE agent_controllers SET lease_expires_at = now() - interval '1 minute' WHERE agent_id=$1`, d.agent)
	require.NoError(t, err)
	c = d.agentRow(t).Controller
	require.Equal(t, "runner-a", c.RunnerID, "lapsed announce yields to a live derived controller")
	require.True(t, c.Live)
}

func TestChatAgentPicker_DerivedController(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()
	d.admitted(true)
	var project uuid.UUID
	require.NoError(t, d.db.GetContext(ctx, &project, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(gen_random_uuid(),$1,'derived','derived-'||substr(md5(random()::text),1,8)) RETURNING id`, d.owner))
	_, err := d.db.ExecContext(ctx, `INSERT INTO project_members(id,project_id,agent_id,workspace_id,role) VALUES($1,$2,$3,$4,'member')`, uuid.New(), project, d.agent, d.owner)
	require.NoError(t, err)

	list, err := NewChatAgentPickerRepo(d.db).List(ctx, d.actor, project)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, ChatPresenceOnline, list[0].Presence)

	_, err = d.db.ExecContext(ctx, `UPDATE runtime_controller_reports SET received_at = now() - interval '1 day' WHERE integration_id=$1`, d.integ)
	require.NoError(t, err)
	list, err = NewChatAgentPickerRepo(d.db).List(ctx, d.actor, project)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, ChatPresenceOffline, list[0].Presence)
}
