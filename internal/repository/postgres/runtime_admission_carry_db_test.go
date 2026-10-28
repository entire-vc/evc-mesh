package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// twoReceivers splits the fixture into two bindings of the receiver workspace
// that share no profile, account or pool: worker-b permits only "preferred",
// worker-c only "reserve". It returns worker-c's agent key.
func (f *runtimeFixture) twoReceivers(t *testing.T) RuntimeActor {
	t.Helper()
	agent := seedGrantAgent(t, f.db, f.owner)
	grant := insertGrant(t, f.db, agent.ID, f.receiver, "member", "runtime-worker-c", "synthetic-worker-c-hash", nil)
	b := f.catalog.Bindings["worker-b"]
	b.PermittedProfiles = []string{"preferred"}
	b.Policy.PrimaryProfiles = []string{"preferred"}
	b.Policy.QuotaEdges = map[string][]string{}
	b.Policy.PreferredAccounts = map[string]string{"provider-a": "preferred"}
	f.catalog.Bindings["worker-b"] = b
	c := b
	c.Binding = domain.RuntimeIdentity{AgentID: agent.ID, WorkspaceID: f.receiver, GrantID: grant}
	c.PermittedProfiles = []string{"reserve"}
	c.Policy.PrimaryProfiles = []string{"reserve"}
	c.Policy.PreferredAccounts = map[string]string{"provider-a": "reserve"}
	f.catalog.Bindings["worker-c"] = c
	return RuntimeActor{AgentID: agent.ID, AuthWorkspaceID: f.receiver}
}

// save PUTs the fixture's current catalog, credential references included.
func (f *runtimeFixture) save(t *testing.T, ifRevision int64) *domain.RuntimeSnapshot {
	t.Helper()
	data, err := json.Marshal(f.catalog)
	require.NoError(t, err)
	snapshot, err := f.repo.Save(context.Background(), f.owner, f.ownerActor, RuntimeSave{IfRevision: ifRevision, Config: data})
	require.NoError(t, err)
	return snapshot
}

func (f *runtimeFixture) admit(t *testing.T, ref string, profiles ...string) {
	t.Helper()
	enabled := true
	_, err := f.repo.Admit(context.Background(), f.owner, f.receiver, ref, f.receiverActor, RuntimeAdmissionInput{Enabled: &enabled, PermittedProfiles: profiles})
	require.NoError(t, err)
}

func (f *runtimeFixture) bindingEnabled(t *testing.T, ref string) bool {
	t.Helper()
	view, err := f.repo.Binding(context.Background(), f.owner, f.receiver, ref, f.receiverActor)
	require.NoError(t, err)
	return view.Enabled
}

func (f *runtimeFixture) admissionDigest(t *testing.T, ref string) string {
	t.Helper()
	var digest string
	require.NoError(t, f.db.GetContext(context.Background(), &digest, `SELECT a.catalog_digest FROM runtime_binding_admissions a JOIN integration_configs c ON c.id=a.integration_id WHERE c.workspace_id=$1 AND a.binding_ref=$2`, f.owner, ref))
	return digest
}

func (f *runtimeFixture) setProfileModel(profile, model string) {
	p := f.catalog.Profiles[profile]
	p.Model = model
	f.catalog.Profiles[profile] = p
}

// A catalog PUT that changes only what worker-c approved keeps worker-b
// admitted; before the fix every PUT read every admission as disabled until the
// receiver granted it again, and the router answered binding_denied meanwhile.
func TestRuntimeSaveCarriesAdmissionOfUnchangedBinding(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	workerC := f.twoReceivers(t)
	f.save(t, 0)
	f.admit(t, "worker-b", "preferred")
	f.admit(t, "worker-c", "reserve")
	require.True(t, f.bindingEnabled(t, "worker-b"))
	require.True(t, f.bindingEnabled(t, "worker-c"))

	f.setProfileModel("reserve", "model-b")
	saved := f.save(t, 1)

	require.True(t, f.bindingEnabled(t, "worker-b"), "unchanged binding stays admitted")
	require.Equal(t, saved.Digest, f.admissionDigest(t, "worker-b"))
	request := domain.RuntimePreviewInput{Purpose: "new_launch", RequiredCapabilities: []string{}}
	preview, err := f.repo.Preview(ctx, f.owner, f.receiver, "worker-b", f.worker, request)
	require.NoError(t, err)
	require.NotEqual(t, "integration_or_admission_disabled", preview.Reason)

	require.False(t, f.bindingEnabled(t, "worker-c"), "a changed profile needs renewed approval")
	require.NotEqual(t, saved.Digest, f.admissionDigest(t, "worker-c"))
	preview, err = f.repo.Preview(ctx, f.owner, f.receiver, "worker-c", workerC, request)
	require.NoError(t, err)
	require.Equal(t, "integration_or_admission_disabled", preview.Reason)
}

// Restore saves through Save, so it carries an unchanged binding too. Restoring
// revision 2 lands on a digest worker-b's admission was never stored under.
func TestRuntimeRestoreCarriesAdmissionOfUnchangedBinding(t *testing.T) {
	f := seedRuntime(t)
	f.twoReceivers(t)
	f.save(t, 0)
	f.admit(t, "worker-b", "preferred")
	f.setProfileModel("reserve", "model-b")
	f.save(t, 1)
	f.setProfileModel("reserve", "model-c")
	f.save(t, 2)

	restored, err := f.repo.Restore(context.Background(), f.owner, f.ownerActor, RuntimeRestore{IfRevision: 3, Revision: 2})
	require.NoError(t, err)
	require.EqualValues(t, 4, restored.Revision)
	require.True(t, f.bindingEnabled(t, "worker-b"))
	require.Equal(t, restored.Digest, f.admissionDigest(t, "worker-b"))
}

// An unchanged projection is not enough: an admission bound to another grant
// key never moves to the new digest.
func TestRuntimeSaveDoesNotCarryAdmissionOfRotatedGrant(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	f.save(t, 0)
	f.admit(t, "worker-b", "preferred", "reserve")

	// Positive control: a controller version is outside the projection.
	controller := f.catalog.Controllers["runner-a"]
	controller.Version = "v3"
	f.catalog.Controllers["runner-a"] = controller
	second := f.save(t, 1)
	require.Equal(t, second.Digest, f.admissionDigest(t, "worker-b"))
	require.True(t, f.bindingEnabled(t, "worker-b"))

	_, err := f.db.ExecContext(ctx, `UPDATE agent_workspace_grants SET api_key_hash=$2 WHERE id=$1`, f.grant, "synthetic-rotated-"+uuid.NewString()[:8])
	require.NoError(t, err)
	controller.Version = "v4"
	f.catalog.Controllers["runner-a"] = controller
	third := f.save(t, 2)
	require.NotEqual(t, second.Digest, third.Digest)
	require.Equal(t, second.Digest, f.admissionDigest(t, "worker-b"), "rotated key keeps the admission on the old digest")
	require.False(t, f.bindingEnabled(t, "worker-b"))
}
