package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// execRepoFixture drives RuntimeRepo directly (no HTTP) so the repository
// statements are measured in their own package.
type execRepoFixture struct {
	runtimeFixture
	project uuid.UUID
}

func seedExecRepo(t *testing.T) execRepoFixture {
	t.Helper()
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	enabled := true
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: []string{"preferred", "reserve"}})
	require.NoError(t, err)
	project := uuid.New()
	_, err = f.db.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'exec',$3)`, project, f.receiver, "exec-"+project.String()[:8])
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `INSERT INTO project_members (id, project_id, agent_id, workspace_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), project, f.worker.AgentID, f.receiver)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `INSERT INTO project_integrations (id, project_id, type, extension_id, enabled, settings) VALUES ($1,$2,'agent-runner','agent-runner',true,'{}')`, uuid.New(), project)
	require.NoError(t, err)
	return execRepoFixture{runtimeFixture: f, project: project}
}

func (f execRepoFixture) report(instance string, generation, sequence int64, now time.Time, attempts ...domain.ExecutionAttemptReport) domain.ExecutionReport {
	return domain.ExecutionReport{SchemaVersion: 1, InstanceID: instance, Generation: generation, Sequence: sequence, ReportedAt: now.Add(-time.Second), Attempts: attempts}
}

func (f execRepoFixture) attempt(instance string, generation int64, id, turn, state string, model *string, now time.Time) domain.ExecutionAttemptReport {
	profile, harness, routeModel := "preferred", "prepared-harness", "model-a"
	return domain.ExecutionAttemptReport{
		WorkspaceID: f.receiver, ProjectID: f.project, AgentID: f.worker.AgentID, BindingRef: "worker-b",
		ControllerRef: "runner-a", InstanceID: instance, Generation: generation, AttemptID: id, TurnID: &turn, State: state,
		ResolvedRoute: domain.ExecutionResolvedRoute{Source: "runner_resolver", Revision: 1, ProfileRef: &profile, Harness: &harness, Model: &routeModel},
		Observed:      domain.ExecutionObserved{Harness: &harness, Model: model, Source: "adapter_handshake", ObservedAt: now.Add(-2 * time.Second)},
	}
}

func TestAgentExecutionRepoRoundTripAndFencing(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"

	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)
	again, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)
	require.Equal(t, inst.Generation, again.Generation, "re-registering the same instance keeps its generation")

	a1 := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	ack, err := f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, a1))
	require.NoError(t, err)
	require.True(t, ack.Recorded)
	require.False(t, ack.LaunchAuthorized)

	dup, err := f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, a1))
	require.NoError(t, err)
	require.True(t, dup.Duplicate)

	changed := f.attempt("boot-1", inst.Generation, "a1", "t2", "running", &model, now)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, changed))
	require.Error(t, err, "a changed replay of the same sequence conflicts")
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation+9, 2, now, a1))
	require.Error(t, err, "an unregistered generation is rejected")

	list, err := f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	item := list.Items[0]
	require.True(t, item.Managed)
	require.True(t, item.Enabled)
	require.Equal(t, domain.FreshnessCurrent, item.Freshness)
	require.Len(t, item.Attempts, 1)
	require.Equal(t, "model-a", *item.Attempts[0].Observed.Model)

	// A restart takes a newer generation; the old attempt is no longer current.
	next, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-2"})
	require.NoError(t, err)
	require.Greater(t, next.Generation, inst.Generation)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 2, now, a1))
	require.Error(t, err, "the old generation cannot report after a restart")
	list, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	require.Equal(t, domain.FreshnessStale, list.Items[0].Attempts[0].Freshness)

	// A resume on the new generation starts a new turn with an unknown model.
	a2 := f.attempt("boot-2", next.Generation, "a1", "t9", "running", nil, now)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-2", next.Generation, 1, now, a2))
	require.NoError(t, err)
	list, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project, AgentID: &f.worker.AgentID, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, domain.FreshnessUnknown, list.Items[0].Attempts[0].Freshness)
	require.Nil(t, list.Items[0].Attempts[0].Observed.Model)
}

func TestAgentExecutionRepoRefusesForeignAndFuture(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"
	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)

	_, err = f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.worker, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "x"})
	require.Error(t, err, "only the controller's reporter registers instances")

	foreign := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	foreign.ProjectID = uuid.New()
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, foreign))
	require.Error(t, err, "an attempt for a project outside the binding workspace is refused")

	future := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	future.Observed.ObservedAt = now.Add(time.Hour)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, future))
	require.Error(t, err, "a future observation time is refused")

	_, err = f.repo.AgentExecutions(ctx, f.receiver, RuntimeActor{UserID: uuid.New()}, ExecutionFilter{})
	require.Error(t, err, "a stranger cannot read the projection")
}

func TestAgentExecutionRepoRefusesBadSequenceTurnAndRevivedAttempt(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"
	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)
	send := func(seq int64, a domain.ExecutionAttemptReport) error {
		_, rerr := f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, seq, now, a))
		return rerr
	}

	running := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	require.NoError(t, send(5, running))
	other := f.attempt("boot-1", inst.Generation, "a2", "t1", "running", &model, now)
	require.Error(t, send(3, other), "a lower sequence with a different body is refused")

	newTurn := f.attempt("boot-1", inst.Generation, "a1", "t2", "running", &model, now)
	newTurn.Observed.ObservedAt = now.Add(-30 * time.Second)
	require.Error(t, send(6, newTurn), "a new turn cannot reuse an observation older than the previous one")

	done := f.attempt("boot-1", inst.Generation, "a1", "t1", "completed", &model, now)
	require.NoError(t, send(7, done))
	revived := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	require.Error(t, send(8, revived), "a finished attempt cannot run again")
}

func TestAgentExecutionRepoReadRefusalsAndRevokedReporter(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()

	_, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "no-such-controller", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.Error(t, err, "an unknown controller ref has no instances")
	_, err = f.repo.RegisterExecutionInstance(ctx, f.receiver, "runner-a", RuntimeActor{AgentID: f.worker.AgentID, AuthWorkspaceID: f.receiver}, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.Error(t, err, "a workspace without a runtime has no controller")

	_, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{Limit: 500})
	require.Error(t, err, "limit above 200 is refused")
	_, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{Cursor: "!!not-a-cursor"})
	require.Error(t, err, "a malformed cursor is refused")
	foreign := uuid.New()
	_, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &foreign})
	require.Error(t, err, "a project outside the workspace is not found")

	grant := f.catalog.Controllers["runner-a"].ReporterGrantID
	_, err = f.db.ExecContext(ctx, `UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, grant)
	require.NoError(t, err)
	_, err = f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.Error(t, err, "a revoked reporter grant cannot register")
	now := time.Now()
	model := "model-a"
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", 1, 1, now, f.attempt("boot-1", 1, "a1", "t1", "running", &model, now)))
	require.Error(t, err, "a revoked reporter grant cannot report")
}

func TestAgentExecutionRepoDelayedReplayAndForeignAgent(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"
	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)

	foreign := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	foreign.AgentID = uuid.New()
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, foreign))
	require.Error(t, err, "an agent the binding does not name is refused")

	// A report delivered long after it was made may be stored, but it can
	// never present itself as current.
	old := now.Add(-10 * time.Minute)
	late := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, old)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, old, late))
	if err == nil {
		list, lerr := f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
		require.NoError(t, lerr)
		require.Len(t, list.Items[0].Attempts, 1)
		require.NotEqual(t, domain.FreshnessCurrent, list.Items[0].Attempts[0].Freshness, "a delayed replay is not current")
	}
}

func TestAgentExecutionRepoTruncatesAndCarriesNoUsage(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"
	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)

	list, err := f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	require.False(t, list.Items[0].AttemptsTruncated)

	var batch []domain.ExecutionAttemptReport
	for i := 0; i < executionMaxAttemptsRead+1; i++ {
		batch = append(batch, f.attempt("boot-1", inst.Generation, fmt.Sprintf("a%03d", i), "t1", "running", &model, now))
	}
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, batch...))
	require.NoError(t, err)

	list, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	require.Len(t, list.Items[0].Attempts, executionMaxAttemptsRead)
	require.True(t, list.Items[0].AttemptsTruncated, "a cut list says so")

	raw, err := json.Marshal(list)
	require.NoError(t, err)
	for _, forbidden := range []string{"usage", "tokens", "cost", "quota"} {
		require.NotContains(t, string(raw), forbidden, "the projection carries no usage data")
	}
}

func TestAgentExecutionRepoAgentReaderSeesOnlyItself(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"
	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)))
	require.NoError(t, err)

	// A second agent of the same project with its own stored attempt.
	other := seedGrantAgent(t, f.db, f.receiver)
	_, err = f.db.ExecContext(ctx, `INSERT INTO project_members (id, project_id, agent_id, workspace_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.project, other.ID, f.receiver)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `INSERT INTO runtime_execution_attempts
		(integration_id,controller_ref,project_id,agent_id,attempt_id,workspace_id,binding_ref,instance_id,generation,sequence,turn_id,state,route_source,route_revision,observed_source,observed_at,reporter_grant_id,grant_fingerprint,heartbeat_at)
		SELECT integration_id,controller_ref,project_id,$2,attempt_id,workspace_id,binding_ref,instance_id,generation,sequence,turn_id,state,route_source,route_revision,observed_source,observed_at,reporter_grant_id,grant_fingerprint,heartbeat_at
		FROM runtime_execution_attempts WHERE project_id=$1 AND agent_id=$3`, f.project, other.ID, f.worker.AgentID)
	require.NoError(t, err)

	asAgent := RuntimeActor{AgentID: f.worker.AgentID, AuthWorkspaceID: f.receiver}
	list, err := f.repo.AgentExecutions(ctx, f.receiver, asAgent, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	for _, item := range list.Items {
		require.Equal(t, f.worker.AgentID, item.AgentID, "an agent reader never sees another agent's telemetry")
	}
	require.NotEmpty(t, list.Items)

	list, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	seen := map[uuid.UUID]bool{}
	for _, item := range list.Items {
		seen[item.AgentID] = true
	}
	require.True(t, seen[f.worker.AgentID] && seen[other.ID], "the workspace owner sees both agents")
}

func TestAgentExecutionRepoRotatedGrantIsNotCurrent(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"
	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)))
	require.NoError(t, err)

	list, err := f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	require.Equal(t, domain.FreshnessCurrent, list.Items[0].Attempts[0].Freshness, "control: written under the live grant")

	// The grant behind the stored observation is no longer the one the catalog names.
	_, err = f.db.ExecContext(ctx, `UPDATE runtime_execution_attempts SET grant_fingerprint='rotated-away' WHERE project_id=$1`, f.project)
	require.NoError(t, err)
	list, err = f.repo.AgentExecutions(ctx, f.receiver, f.receiverActor, ExecutionFilter{ProjectID: &f.project})
	require.NoError(t, err)
	require.Equal(t, domain.FreshnessOffline, list.Items[0].Attempts[0].Freshness, "telemetry from a superseded grant is not current")
}

func TestAgentExecutionRepoRefusesAttemptWithForeignWorkspaceID(t *testing.T) {
	f := seedExecRepo(t)
	ctx := context.Background()
	now := time.Now()
	model := "model-a"
	inst, err := f.repo.RegisterExecutionInstance(ctx, f.owner, "runner-a", f.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)

	// Another fixture legitimately stores the same attempt id: attempt ids are only unique per
	// project, so an unscoped count over the table would see this row and report a false leak.
	other := seedExecRepo(t)
	otherInst, err := other.repo.RegisterExecutionInstance(ctx, other.owner, "runner-a", other.reporter, domain.ExecutionInstanceRequest{SchemaVersion: 1, InstanceID: "boot-1"})
	require.NoError(t, err)
	_, err = other.repo.ReportExecutions(ctx, other.owner, "runner-a", other.reporter, other.report("boot-1", otherInst.Generation, 1, now, other.attempt("boot-1", otherInst.Generation, "a1", "t1", "running", &model, now)))
	require.NoError(t, err)

	// Rows of THIS fixture only: its project is unique to the test, whichever workspace a row claims.
	storedForFixture := func() int {
		var n int
		require.NoError(t, f.db.GetContext(ctx, &n, `SELECT count(*) FROM runtime_execution_attempts WHERE project_id=$1 AND attempt_id='a1'`, f.project))
		return n
	}

	// Project, agent and binding are the valid ones; only the workspace id is another tenant's.
	foreign := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	foreign.WorkspaceID = uuid.New()
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, foreign))
	require.Error(t, err, "an attempt carrying a foreign workspace id is refused")
	require.Zero(t, storedForFixture(), "nothing is stored for a refused attempt")

	// Control: the same attempt with the binding's workspace is accepted and counted.
	ok := f.attempt("boot-1", inst.Generation, "a1", "t1", "running", &model, now)
	_, err = f.repo.ReportExecutions(ctx, f.owner, "runner-a", f.reporter, f.report("boot-1", inst.Generation, 1, now, ok))
	require.NoError(t, err)
	require.Equal(t, 1, storedForFixture(), "control: the scoped count sees this fixture's own write")
}
