package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ownBinding rebinds worker-b to an agent grant of the owner workspace and
// returns the save input for it.
func (f runtimeFixture) ownBinding(t *testing.T) RuntimeSave {
	t.Helper()
	agent := seedGrantAgent(t, f.db, f.owner)
	grant := insertGrant(t, f.db, agent.ID, f.owner, "member", "runtime-own-worker", "synthetic-own-worker-hash", nil)
	var catalog domain.RuntimeCatalog
	require.NoError(t, json.Unmarshal(f.input.Config, &catalog))
	b := catalog.Bindings["worker-b"]
	b.Binding = domain.RuntimeIdentity{AgentID: agent.ID, WorkspaceID: f.owner, GrantID: grant}
	catalog.Bindings["worker-b"] = b
	data, err := json.Marshal(catalog)
	require.NoError(t, err)
	input := f.input
	input.Config = data
	return input
}

func (f runtimeFixture) profileReport(digest string, revision int64) domain.RuntimeReport {
	observed := time.Now().Add(-time.Second)
	return domain.RuntimeReport{SchemaVersion: 2, Revision: revision, Digest: digest, Status: "applied", Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{},
		Profiles: map[string]domain.RuntimeModelObservation{"preferred": {Model: "model-a", ModelDeveloper: "developer-a", ModelFamily: "family-a", ObservedAt: observed}},
		// A controller cannot write the exclusions itself.
		Excluded: []domain.RuntimeExclusion{{Ref: "forged", Reason: "forged"}}}
}

func TestRuntimeRepoStrictIsolationRefusesForeignBindingOnSave(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	f.repo.WithStrictWorkspaceIsolation(true)
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "a binding of another workspace is refused: %v", err)
	require.Equal(t, http.StatusUnprocessableEntity, apiErr.Code)
	require.Equal(t, map[string]string{"config.bindings.worker-b.binding.workspace_id": "foreign_workspace"}, apiErr.Validation)

	saved, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.ownBinding(t))
	require.NoError(t, err, "a binding of the owner workspace saves")
	require.EqualValues(t, 1, saved.Revision)
}

// A binding that names the owner workspace but carries a grant of another
// workspace is refused with the same field error, not a bare 403.
func TestRuntimeRepoStrictIsolationRefusesForeignGrantOnSave(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	f.repo.WithStrictWorkspaceIsolation(true)
	foreignGrant := f.catalog.Bindings["worker-b"].Binding.GrantID
	input := f.ownBinding(t)
	var catalog domain.RuntimeCatalog
	require.NoError(t, json.Unmarshal(input.Config, &catalog))
	b := catalog.Bindings["worker-b"]
	b.Binding.GrantID = foreignGrant
	catalog.Bindings["worker-b"] = b
	data, err := json.Marshal(catalog)
	require.NoError(t, err)
	input.Config = data

	_, err = f.repo.Save(ctx, f.owner, f.ownerActor, input)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "a grant of another workspace is refused: %v", err)
	require.Equal(t, http.StatusUnprocessableEntity, apiErr.Code)
	require.Equal(t, map[string]string{"config.bindings.worker-b.binding.workspace_id": "foreign_workspace"}, apiErr.Validation)
}

// Off, a cross-workspace binding saves, is handed to the controller and its
// report is stored exactly as before the flag existed.
func TestRuntimeRepoIsolationFlagOffKeepsCrossWorkspaceBehaviour(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	snapshot, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	view, err := f.repo.Desired(ctx, f.owner, "runner-a", f.reporter)
	require.NoError(t, err)
	require.Contains(t, view.Catalog.Bindings, "worker-b")
	require.Nil(t, view.Excluded)
	data, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(data), "excluded")

	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, f.profileReport(snapshot.Digest, 1)))
	inventory, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.Contains(t, inventory.Controllers[0].Report.Profiles, "preferred")
	require.Nil(t, inventory.Controllers[0].Report.Excluded)
	var stored []byte
	require.NoError(t, f.db.GetContext(ctx, &stored, `SELECT r.report FROM runtime_controller_reports r JOIN integration_configs c ON c.id=r.integration_id WHERE c.workspace_id=$1`, f.owner))
	require.NotContains(t, string(stored), "excluded")
}

// A catalog saved before the flag (as on prod) keeps its foreign binding; on,
// the controller is not given it and its profile observations are not applied.
func TestRuntimeRepoStrictIsolationExcludesForeignBinding(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	snapshot, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	f.repo.WithStrictWorkspaceIsolation(true)

	view, err := f.repo.Desired(ctx, f.owner, "runner-a", f.reporter)
	require.NoError(t, err)
	require.NotContains(t, view.Catalog.Bindings, "worker-b")
	require.Equal(t, []domain.RuntimeExclusion{{Ref: "worker-b", Reason: "foreign_workspace"}}, view.Excluded)

	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, f.profileReport(snapshot.Digest, 1)))
	inventory, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.NotContains(t, inventory.Controllers[0].Report.Profiles, "preferred")
	require.Equal(t, []domain.RuntimeExclusion{{Ref: "worker-b", Reason: "foreign_workspace"}}, inventory.Controllers[0].Report.Excluded)
}

// An own binding runs while the Runner is on and is left out, with the
// reason, once it is switched off.
func TestRuntimeRepoStrictIsolationExcludesRunnerOff(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	f.repo.WithStrictWorkspaceIsolation(true)
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.ownBinding(t))
	require.NoError(t, err)
	view, err := f.repo.Desired(ctx, f.owner, "runner-a", f.reporter)
	require.NoError(t, err)
	require.Contains(t, view.Catalog.Bindings, "worker-b")
	require.Empty(t, view.Excluded)

	f.runnerSwitch(t, false)
	view, err = f.repo.Desired(ctx, f.owner, "runner-a", f.reporter)
	require.NoError(t, err)
	require.NotContains(t, view.Catalog.Bindings, "worker-b")
	require.Equal(t, []domain.RuntimeExclusion{{Ref: "worker-b", Reason: "runner_off"}}, view.Excluded)

	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, f.profileReport(view.Digest, view.Revision)))
	inventory, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.NotContains(t, inventory.Controllers[0].Report.Profiles, "preferred")
	require.Equal(t, []domain.RuntimeExclusion{{Ref: "worker-b", Reason: "runner_off"}}, inventory.Controllers[0].Report.Excluded)
}

// A Runner the instance allow-list refuses is a program that is not connected.
func TestRuntimeRepoStrictIsolationExcludesProgramNotConnected(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	f.repo.WithStrictWorkspaceIsolation(true)
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.ownBinding(t))
	require.NoError(t, err)
	f.repo.WithAllowList(denyAllRuntime{})
	view, err := f.repo.Desired(ctx, f.owner, "runner-a", f.reporter)
	require.NoError(t, err)
	require.NotContains(t, view.Catalog.Bindings, "worker-b")
	require.Equal(t, []domain.RuntimeExclusion{{Ref: "worker-b", Reason: "program_not_connected"}}, view.Excluded)
}

type denyAllRuntime struct{}

func (denyAllRuntime) Allowed(context.Context, string) (bool, error) { return false, nil }
