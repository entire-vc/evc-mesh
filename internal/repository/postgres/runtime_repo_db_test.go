package postgres

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type runtimeFixture struct {
	db                                          *sqlx.DB
	repo                                        *RuntimeRepo
	owner, receiver                             uuid.UUID
	ownerActor, receiverActor, reporter, worker RuntimeActor
	catalog                                     *domain.RuntimeCatalog
	input                                       RuntimeSave
	grant                                       uuid.UUID
}

func seedRuntime(t *testing.T) runtimeFixture {
	t.Helper()
	db := agentDigestTestDB(t)
	ctx := context.Background()
	owner, receiver := seedGrantWorkspace(t, db), seedGrantWorkspace(t, db)
	var ownerID, receiverID uuid.UUID
	require.NoError(t, db.GetContext(ctx, &ownerID, `SELECT owner_id FROM workspaces WHERE id=$1`, owner))
	require.NoError(t, db.GetContext(ctx, &receiverID, `SELECT owner_id FROM workspaces WHERE id=$1`, receiver))
	reporter, worker := seedGrantAgent(t, db, owner), seedGrantAgent(t, db, owner)
	reporterGrant := insertGrant(t, db, reporter.ID, owner, "member", "runtime-reporter", "synthetic-reporter-hash", nil)
	workerGrant := insertGrant(t, db, worker.ID, receiver, "member", "runtime-worker", "synthetic-worker-hash", nil)
	data, err := os.ReadFile("../../../docs/api/agent-runtime.example.json")
	require.NoError(t, err)
	catalog, err := domain.ParseRuntimeCatalog(data)
	require.NoError(t, err)
	c := catalog.Controllers["runner-a"]
	c.ReporterGrantID = reporterGrant
	catalog.Controllers["runner-a"] = c
	b := catalog.Bindings["worker-b"]
	b.Binding = domain.RuntimeIdentity{AgentID: worker.ID, WorkspaceID: receiver, GrantID: workerGrant}
	catalog.Bindings["worker-b"] = b
	data, err = json.Marshal(catalog)
	require.NoError(t, err)
	enabled := true
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id IN ($1,$2)`, owner, receiver)
	})
	return runtimeFixture{db: db, repo: NewRuntimeRepo(db), owner: owner, receiver: receiver, ownerActor: RuntimeActor{UserID: ownerID}, receiverActor: RuntimeActor{UserID: receiverID}, reporter: RuntimeActor{AgentID: reporter.ID, AuthWorkspaceID: owner}, worker: RuntimeActor{AgentID: worker.ID, AuthWorkspaceID: receiver}, catalog: catalog, input: RuntimeSave{Enabled: &enabled, Config: data}, grant: workerGrant}
}

func TestRuntimeRepoCASAndApply(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	before, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.Equal(t, "direct", before.Mode)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, saveErr := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
			if saveErr == nil {
				wins.Add(1)
			} else {
				require.Contains(t, saveErr.Error(), "revision changed")
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, wins.Load(), "exactly one concurrent first save wins CAS")
	snapshot, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision)
	inventoryJSON, marshalErr := json.Marshal(snapshot)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(inventoryJSON), "cred:", "inventory exposes no credential references")
	report := domain.RuntimeReport{SchemaVersion: 2, Revision: 1, Digest: snapshot.Digest, Status: "applied", Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{}}
	wrong := report
	wrong.Digest = "incorrect"
	require.ErrorContains(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, wrong), "current revision and digest")
	wrong.Revision = 2
	require.Error(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, wrong))
	wrong = report
	wrong.Capabilities = []string{}
	require.ErrorContains(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, wrong), "capabilities")
	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report))
	snapshot, err = f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.True(t, snapshot.Controllers[0].Current)
	f.input.IfRevision = 1
	saved, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	savedJSON, marshalErr := json.Marshal(saved)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(savedJSON), "cred:", "save response exposes no credential references")
	require.ErrorContains(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report), "current revision and digest")
	snapshot, err = f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.False(t, snapshot.Controllers[0].Current)
	report.Revision = 2
	report.EmergencyPaused = true
	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report))
	snapshot, err = f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.True(t, snapshot.Controllers[0].Report.EmergencyPaused)
	require.False(t, snapshot.Controllers[0].Current)
}

func TestRuntimeRepoExactGrantReceiverAdmissionAndDrain(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.receiverActor, f.input)
	require.ErrorContains(t, err, "owner or admin")
	_, err = f.repo.Save(ctx, f.owner, f.worker, f.input)
	require.ErrorContains(t, err, "owner or admin")
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	view, err := f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", f.worker)
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.Empty(t, view.Profiles)
	enabled := true
	input := RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: []string{"preferred", "reserve"}}
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.ownerActor, input)
	require.ErrorContains(t, err, "owner or admin", "owner cannot consent for receiver")
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, input)
	require.NoError(t, err)
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, input)
	require.ErrorContains(t, err, "revision changed")
	view, err = f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", f.worker)
	require.NoError(t, err)
	require.True(t, view.Enabled)
	require.Len(t, view.Profiles, 2)
	homeKey := f.worker
	homeKey.AuthWorkspaceID = f.owner
	_, err = f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", homeKey)
	require.ErrorContains(t, err, "bound workspace agent key")
	_, err = f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", f.reporter)
	require.Error(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, f.grant)
	require.NoError(t, err)
	_, err = f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", f.worker)
	require.ErrorContains(t, err, "active exact workspace grant")
	_, err = f.db.ExecContext(ctx, `UPDATE agent_workspace_grants SET revoked_at=NULL,api_key_hash='synthetic-rotated-hash' WHERE id=$1`, f.grant)
	require.NoError(t, err)
	view, err = f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", f.worker)
	require.NoError(t, err)
	require.False(t, view.Enabled, "reinvited same grant ID must not inherit admission")
	input.IfRevision = 1
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, input)
	require.NoError(t, err)
	disabled := false
	f.input.IfRevision = 1
	f.input.Enabled = &disabled
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	view, err = f.repo.Binding(ctx, f.owner, f.receiver, "worker-b", f.worker)
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.True(t, view.DrainRequested)
	snapshot, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.Equal(t, "managed", snapshot.Mode)
	require.True(t, snapshot.DrainRequested)
	jsonData, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(jsonData), "synthetic-")
	require.NotContains(t, string(jsonData), "fingerprint")
	require.NotContains(t, string(jsonData), "credential_ref")
}

func TestRuntimeRepoReporterIsolationAndPoolAliases(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	snapshot, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	observation := domain.RuntimePoolObservation{State: "available", ObservedAt: time.Now().Add(-time.Second), Verified: true}
	report := domain.RuntimeReport{SchemaVersion: 2, Revision: 1, Digest: snapshot.Digest, Status: "applied", Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{"another-credential-view": observation}}
	require.ErrorContains(t, f.repo.Report(ctx, f.owner, "runner-a", f.ownerActor, report), "exact owner-workspace")
	connector := f.reporter
	connector.Connector = true
	require.Error(t, f.repo.Report(ctx, f.owner, "runner-a", connector, report))
	otherWS := f.reporter
	otherWS.AuthWorkspaceID = f.receiver
	require.Error(t, f.repo.Report(ctx, f.owner, "runner-a", otherWS, report))
	report.Pools["shared-window"] = observation
	require.ErrorContains(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report), "duplicate canonical pool")
	delete(report.Pools, "shared-window")
	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report))
	snapshot, err = f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.Contains(t, snapshot.Controllers[0].Report.Pools, "shared-window")
	require.NotContains(t, snapshot.Controllers[0].Report.Pools, "another-credential-view")
}

func TestRuntimeRepoPreviewIsAdvisoryAndBoundToExactGrant(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	request := domain.RuntimePreviewInput{Purpose: "new_launch", RequiredCapabilities: []string{}}

	before, err := f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.worker, request)
	require.NoError(t, err)
	require.False(t, before.LaunchAuthorized)
	require.Empty(t, before.SelectedProfileID, "a binding the receiver has not admitted selects nothing")

	enabled := true
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: []string{"preferred", "reserve"}})
	require.NoError(t, err)
	after, err := f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.worker, request)
	require.NoError(t, err)
	require.False(t, after.LaunchAuthorized, "preview is explanation only, never launch authority")
	require.NotEmpty(t, after.Trace)

	homeKey := f.worker
	homeKey.AuthWorkspaceID = f.owner
	_, err = f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", homeKey, request)
	require.ErrorContains(t, err, "bound workspace agent key", "no fallback to the home key")
	_, err = f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.reporter, request)
	require.Error(t, err, "another agent's key cannot explain someone else's binding")
	_, err = f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.worker, domain.RuntimePreviewInput{Purpose: "new_launch", RequiredCapabilities: []string{"bad capability!"}})
	require.Error(t, err)
}

func TestRuntimeRepoProvenanceAttestationDrivesExactSHAPreview(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	enabled := true
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: []string{"preferred", "reserve"}})
	require.NoError(t, err)
	artifact := uuid.New()
	request := domain.RuntimePreviewInput{Purpose: "new_launch", RequiredCapabilities: []string{}, ArtifactID: artifact, ArtifactRevision: "sha-1"}

	unknown, err := f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.worker, request)
	require.NoError(t, err)
	require.Equal(t, "exact_sha_provenance_unavailable", unknown.Reason)

	report := domain.RuntimeProvenanceReport{ControllerRef: "runner-a", ArtifactID: artifact, ArtifactRevision: "sha-1", Complete: true, Authors: []domain.RuntimeAuthor{{AgentID: uuid.New(), ModelDeveloper: "developer-a", ModelFamily: "family-a"}}}
	require.Error(t, f.repo.Attest(ctx, f.owner, "runner-a", f.worker, report), "a receiving worker cannot attest")
	require.Error(t, f.repo.Attest(ctx, f.owner, "runner-a", f.receiverActor, report), "a user cannot attest")
	bad := report
	bad.Authors = nil
	require.Error(t, f.repo.Attest(ctx, f.owner, "runner-a", f.reporter, bad), "attestation without authors is not provenance")
	require.NoError(t, f.repo.Attest(ctx, f.owner, "runner-a", f.reporter, report))
	known, err := f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.worker, request)
	require.NoError(t, err)
	require.NotEqual(t, "exact_sha_provenance_unavailable", known.Reason)
	require.False(t, known.LaunchAuthorized)

	// A later attestation can add an author but never drop the first one.
	second := report
	second.Authors = []domain.RuntimeAuthor{{AgentID: uuid.New(), ModelDeveloper: "developer-b", ModelFamily: "family-b"}}
	require.NoError(t, f.repo.Attest(ctx, f.owner, "runner-a", f.reporter, second))
	var stored int
	require.NoError(t, f.db.GetContext(ctx, &stored, `SELECT jsonb_array_length(authors) FROM runtime_artifact_provenance WHERE artifact_id=$1`, artifact))
	require.Equal(t, 2, stored)

	// Rotating the reporter grant voids the attestation back to unknown.
	_, err = f.db.ExecContext(ctx, `UPDATE agent_workspace_grants SET api_key_hash='synthetic-rotated-reporter' WHERE agent_id=$1 AND workspace_id=$2`, f.reporter.AgentID, f.owner)
	require.NoError(t, err)
	voided, err := f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.worker, request)
	require.NoError(t, err)
	require.Equal(t, "exact_sha_provenance_unavailable", voided.Reason)
}

func TestRuntimeRepoRefusesInvalidRequests(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	attestation := domain.RuntimeProvenanceReport{ControllerRef: "runner-a", ArtifactID: uuid.New(), ArtifactRevision: "r1", Complete: true, Authors: []domain.RuntimeAuthor{{AgentID: uuid.New(), ModelDeveloper: "developer-a", ModelFamily: "family-a"}}}

	// Nothing saved yet: controller writes and reads find no runtime connection.
	require.ErrorContains(t, f.repo.Attest(ctx, f.owner, "runner-a", f.reporter, attestation), "Runtime connection")
	require.ErrorContains(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, domain.RuntimeReport{SchemaVersion: 2, Revision: 1, Status: "applied"}), "Runtime connection")

	// Save refuses malformed input without creating a revision.
	enabled := true
	bad := []RuntimeSave{
		{IfRevision: -1, Enabled: &enabled, Config: f.input.Config},
		{IfRevision: 0, Enabled: nil, Config: f.input.Config},
		{IfRevision: 0, Enabled: &enabled, Config: json.RawMessage(`{"schema_version":2}`)},
	}
	for _, input := range bad {
		_, err := f.repo.Save(ctx, f.owner, f.ownerActor, input)
		require.Error(t, err)
	}
	_, err := f.repo.Save(ctx, f.owner, RuntimeActor{UserID: f.ownerActor.UserID, Connector: true}, f.input)
	require.Error(t, err, "an OAuth connector cannot administer the catalog")

	// A reporter grant from the receiving workspace is not owner-workspace authority.
	foreign := *f.catalog
	foreign.Controllers = map[string]domain.RuntimeController{}
	for ref, c := range f.catalog.Controllers {
		c.ReporterGrantID = f.grant
		foreign.Controllers[ref] = c
	}
	data, err := json.Marshal(foreign)
	require.NoError(t, err)
	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, RuntimeSave{Enabled: &enabled, Config: data})
	require.ErrorContains(t, err, "resource owner workspace")

	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	snapshot, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	_, err = f.repo.Inventory(ctx, f.owner, f.receiverActor)
	require.Error(t, err, "inventory is for the resource owner's admins")

	good := domain.RuntimeReport{SchemaVersion: 2, Revision: 1, Digest: snapshot.Digest, Status: "applied", Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{}}
	mutate := func(change func(*domain.RuntimeReport)) domain.RuntimeReport {
		r := good
		r.Pools = map[string]domain.RuntimePoolObservation{}
		change(&r)
		return r
	}
	observed := time.Now().Add(-time.Minute)
	reports := map[string]domain.RuntimeReport{
		"schema": mutate(func(r *domain.RuntimeReport) { r.SchemaVersion = 1 }),
		"status": mutate(func(r *domain.RuntimeReport) { r.Status = "mystery" }),
		"unrelated pool": mutate(func(r *domain.RuntimeReport) {
			r.Pools["not-in-catalog"] = domain.RuntimePoolObservation{State: "available", ObservedAt: observed}
		}),
		"future pool": mutate(func(r *domain.RuntimeReport) {
			for name := range f.catalog.Pools {
				r.Pools[name] = domain.RuntimePoolObservation{State: "available", ObservedAt: time.Now().Add(time.Hour)}
				break
			}
		}),
		"unrelated profile": mutate(func(r *domain.RuntimeReport) {
			r.Profiles = map[string]domain.RuntimeModelObservation{"not-a-profile": {Model: "m", ModelDeveloper: "d", ModelFamily: "f", ObservedAt: observed}}
		}),
	}
	for name, report := range reports {
		require.Error(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report), name)
	}
	require.ErrorContains(t, f.repo.Report(ctx, f.owner, "no-such-controller", f.reporter, good), "Runtime controller")
	require.Error(t, f.repo.Report(ctx, f.owner, "runner-a", RuntimeActor{AgentID: f.reporter.AgentID, AuthWorkspaceID: f.owner, Connector: true}, good))
	require.Error(t, f.repo.Attest(ctx, f.owner, "no-such-controller", f.reporter, attestation))
	require.Error(t, f.repo.Attest(ctx, f.owner, "runner-a", RuntimeActor{AgentID: f.reporter.AgentID, AuthWorkspaceID: f.owner, Connector: true}, attestation))

	// Admission and binding reads for refs the catalog does not know.
	input := RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: []string{"preferred"}}
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "no-such-binding", f.receiverActor, input)
	require.ErrorContains(t, err, "Runtime binding")
	_, err = f.repo.Binding(ctx, f.owner, f.receiver, "no-such-binding", f.worker)
	require.ErrorContains(t, err, "Runtime binding")
	input.PermittedProfiles = []string{"not-a-profile"}
	_, err = f.repo.Admit(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, input)
	require.Error(t, err, "cannot admit a profile outside the binding's policy")
}

func TestIntegrationRepoRefusesAgentRuntimeBypass(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	integrations := NewIntegrationRepo(f.db)
	connection, err := integrations.GetByProvider(ctx, f.owner, domain.IntegrationProviderAgentRuntime)
	require.NoError(t, err)
	require.NotNil(t, connection)
	off := false
	require.ErrorContains(t, integrations.Upsert(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: f.owner, Provider: domain.IntegrationProviderAgentRuntime, Config: json.RawMessage(`{}`), IsActive: true}), "versioned runtime API")
	_, err = integrations.Update(ctx, connection.ID, domain.UpdateIntegrationInput{IsActive: &off})
	require.ErrorContains(t, err, "versioned runtime API")
	require.ErrorContains(t, integrations.Delete(ctx, connection.ID), "versioned runtime API")
	still, err := integrations.GetByID(ctx, connection.ID)
	require.NoError(t, err)
	require.True(t, still.IsActive)
}
