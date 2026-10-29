package postgres

import (
	"context"
	"encoding/json"
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
	agents, err := d.repo.OperatorAgents(context.Background(), d.actor, d.owner)
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

	runners, err := d.repo.OperatorRunners(ctx, d.actor, d.owner)
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

func agentIDs(t *testing.T, repo *UserRuntimeRepo, user, ws uuid.UUID) []uuid.UUID {
	t.Helper()
	agents, err := repo.OperatorAgents(context.Background(), UserRuntimeActor{UserID: user}, ws)
	require.NoError(t, err)
	ids := make([]uuid.UUID, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.AgentID)
	}
	return ids
}

// The screen is open in one workspace: an agent the same user operates in
// another workspace is not listed there, nor grouped under its runner.
func TestOperatorAgents_OnlyCurrentWorkspace(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()
	d.admitted(true)
	foreign := seedGrantAgent(t, d.db, d.receiver)
	_, err := d.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=$2 WHERE id=$1`, foreign.ID, d.actor.UserID)
	require.NoError(t, err)
	_, err = d.db.ExecContext(ctx, `INSERT INTO agent_controllers(agent_id,runner_id,runner_name,host,version,lease_expires_at)
		VALUES($1,'foreign-runner','foreign-runner','h','v', now() + interval '1 minute')`, foreign.ID)
	require.NoError(t, err)

	require.Equal(t, []uuid.UUID{d.agent}, agentIDs(t, d.repo, d.actor.UserID, d.owner))
	runners, err := d.repo.OperatorRunners(ctx, d.actor, d.owner)
	require.NoError(t, err)
	require.Len(t, runners, 1)
	require.Equal(t, "runner-a", runners[0].RunnerID)
}

// An agent with no operator that the workspace's runner drives is listed for the
// workspace's owner or admin, once, and not for a plain member.
func TestOperatorAgents_RunnerDrivenAgentsForAdmins(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()
	_, err := d.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=NULL WHERE id=$1`, d.agent)
	require.NoError(t, err)
	admin, member := seedGrantUser(t, d.db), seedGrantUser(t, d.db)
	for user, role := range map[uuid.UUID]string{admin: "admin", member: "member"} {
		_, err = d.db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4)`, uuid.New(), d.owner, user, role)
		require.NoError(t, err)
	}

	d.admitted(false)
	require.Empty(t, agentIDs(t, d.repo, admin, d.owner), "disabled admission: not driven by the runner")

	d.admitted(true)
	require.Equal(t, []uuid.UUID{d.agent}, agentIDs(t, d.repo, admin, d.owner))
	require.Equal(t, []uuid.UUID{d.agent}, agentIDs(t, d.repo, d.actor.UserID, d.owner), "the workspace owner sees it too")
	require.Empty(t, agentIDs(t, d.repo, member, d.owner))

	// owner_id wins over a membership row that says "member".
	_, err = d.db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'member')
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role='member'`, uuid.New(), d.owner, d.actor.UserID)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{d.agent}, agentIDs(t, d.repo, d.actor.UserID, d.owner), "the owner keeps the owner's view")

	row := d.agentRow(t)
	require.Nil(t, row.OperatorUserID)
	require.False(t, row.RuntimeManager)

	// operated by the admin and driven by the runner: one row, not two.
	_, err = d.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=$2 WHERE id=$1`, d.agent, admin)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{d.agent}, agentIDs(t, d.repo, admin, d.owner))
	require.Empty(t, agentIDs(t, d.repo, member, d.owner), "the member neither operates it nor administers the workspace")
	runners, err := d.repo.OperatorRunners(ctx, UserRuntimeActor{UserID: admin}, d.owner)
	require.NoError(t, err)
	require.Len(t, runners, 1)
	require.Len(t, runners[0].Agents, 1)

	_, err = d.repo.OperatorAgents(ctx, UserRuntimeActor{UserID: member}, d.receiver)
	require.Error(t, err, "a caller outside the workspace is refused")
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

// The version on the screen is the one the runner reports about itself; the
// catalog's is only the fallback for a runner that does not send it.
func TestOperatorAgents_VersionFromReport(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()
	d.admitted(true)
	catalogVersion := d.catalog.Controllers["runner-a"].Version
	require.Equal(t, catalogVersion, d.agentRow(t).Controller.Version, "no reported version: the catalog's")

	snapshot, err := d.runtimeFixture.repo.Inventory(ctx, d.owner, d.ownerActor)
	require.NoError(t, err)
	report := domain.RuntimeReport{SchemaVersion: 2, Revision: snapshot.Revision, Digest: snapshot.Digest, Status: "applied", Capabilities: d.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{}, Version: "0.1.3"}
	require.NotEqual(t, catalogVersion, report.Version)
	require.NoError(t, d.runtimeFixture.repo.Report(ctx, d.owner, "runner-a", d.reporter, report))
	require.Equal(t, "0.1.3", d.agentRow(t).Controller.Version)
	runners, err := d.repo.OperatorRunners(ctx, d.actor, d.owner)
	require.NoError(t, err)
	require.Len(t, runners, 1)
	require.Equal(t, "0.1.3", runners[0].Version)
}

// A binding taken out of the catalog no longer leads its agent: Save disables
// its admission, and an admission still enabled for a binding the latest
// revision lacks (left by an older server) is not counted either.
func TestOperatorAgents_RemovedBindingNotLed(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()
	_, err := d.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=NULL WHERE id=$1`, d.agent)
	require.NoError(t, err)
	d.admitted(true)
	require.Equal(t, []uuid.UUID{d.agent}, agentIDs(t, d.repo, d.actor.UserID, d.owner))

	snapshot, err := d.runtimeFixture.repo.Inventory(ctx, d.owner, d.ownerActor)
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, json.Unmarshal(d.input.Config, &config))
	config["bindings"] = map[string]any{}
	data, err := json.Marshal(config)
	require.NoError(t, err)
	_, err = d.runtimeFixture.repo.Save(ctx, d.owner, d.ownerActor, RuntimeSave{IfRevision: snapshot.Revision, Config: data})
	require.NoError(t, err)

	var enabled bool
	require.NoError(t, d.db.GetContext(ctx, &enabled, `SELECT enabled FROM runtime_binding_admissions WHERE integration_id=$1 AND binding_ref='worker-b'`, d.integ))
	require.False(t, enabled, "Save disables the admission of a removed binding")
	require.Empty(t, agentIDs(t, d.repo, d.actor.UserID, d.owner))
	runners, err := d.repo.OperatorRunners(ctx, d.actor, d.owner)
	require.NoError(t, err)
	require.Empty(t, runners)

	d.admitted(true)
	require.Empty(t, agentIDs(t, d.repo, d.actor.UserID, d.owner), "an enabled admission of a removed binding is not counted")
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

// A binding ref given to another agent disables the old admission, so
// restoring the earlier catalog does not revive it without a new admission.
func TestOperatorAgents_ReassignedBindingNotRevived(t *testing.T) {
	d := seedDerived(t)
	ctx := context.Background()
	_, err := d.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=NULL WHERE id=$1`, d.agent)
	require.NoError(t, err)
	d.admitted(true)
	require.Equal(t, []uuid.UUID{d.agent}, agentIDs(t, d.repo, d.actor.UserID, d.owner))

	other := seedGrantAgent(t, d.db, d.owner)
	otherGrant := insertGrant(t, d.db, other.ID, d.receiver, "member", "runtime-other", "synthetic-other-hash", nil)
	catalog := *d.catalog
	catalog.Bindings = map[string]domain.RuntimeBinding{}
	for ref, b := range d.catalog.Bindings {
		catalog.Bindings[ref] = b
	}
	b := catalog.Bindings["worker-b"]
	b.Binding = domain.RuntimeIdentity{AgentID: other.ID, WorkspaceID: d.receiver, GrantID: otherGrant}
	catalog.Bindings["worker-b"] = b
	data, err := json.Marshal(catalog)
	require.NoError(t, err)
	snapshot, err := d.runtimeFixture.repo.Inventory(ctx, d.owner, d.ownerActor)
	require.NoError(t, err)
	_, err = d.runtimeFixture.repo.Save(ctx, d.owner, d.ownerActor, RuntimeSave{IfRevision: snapshot.Revision, Config: data})
	require.NoError(t, err)

	var enabled bool
	require.NoError(t, d.db.GetContext(ctx, &enabled, `SELECT enabled FROM runtime_binding_admissions WHERE integration_id=$1 AND binding_ref='worker-b'`, d.integ))
	require.False(t, enabled, "Save disables the admission of a reassigned binding")
	require.Empty(t, agentIDs(t, d.repo, d.actor.UserID, d.owner))

	_, err = d.runtimeFixture.repo.Restore(ctx, d.owner, d.ownerActor, RuntimeRestore{IfRevision: snapshot.Revision + 1, Revision: snapshot.Revision})
	require.NoError(t, err)
	require.Empty(t, agentIDs(t, d.repo, d.actor.UserID, d.owner), "restoring the old catalog does not revive the old admission")
}
