package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func apiCode(err error) int {
	if e, ok := err.(*apierror.Error); ok {
		return e.Code
	}
	return 0
}

// SetBindingPolicy edits one binding inside the stored catalog; the client
// never resends it, so credential references must survive untouched.
func TestRuntimeRepoSetBindingPolicy(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)

	policy := f.catalog.Bindings["worker-b"].Policy
	policy.MaxAttempts += 2
	input := RuntimePolicySave{IfRevision: 1, Policy: policy}

	// Red controls: each refusal must leave the revision at 1.
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.receiverActor, input)
	require.ErrorContains(t, err, "owner or admin")
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.worker, input)
	require.ErrorContains(t, err, "owner or admin")
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "no-such", f.ownerActor, input)
	require.Equal(t, http.StatusNotFound, apiCode(err))
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.owner, "worker-b", f.ownerActor, input)
	require.Equal(t, http.StatusNotFound, apiCode(err), "binding belongs to another receiver workspace")
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.ownerActor, RuntimePolicySave{IfRevision: -1, Policy: policy})
	require.Equal(t, http.StatusBadRequest, apiCode(err))
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.ownerActor, RuntimePolicySave{IfRevision: 1, PermittedProfiles: make([]string, 257), Policy: policy})
	require.Equal(t, http.StatusBadRequest, apiCode(err))
	bad := policy
	bad.PrimaryProfiles = []string{"not-permitted"}
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.ownerActor, RuntimePolicySave{IfRevision: 1, Policy: bad})
	require.Equal(t, http.StatusBadRequest, apiCode(err))
	_, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.ownerActor, RuntimePolicySave{IfRevision: 0, Policy: policy})
	require.Equal(t, http.StatusConflict, apiCode(err), "stale if_revision")

	snapshot, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision, "refused writes created no revision")

	view, err := f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.ownerActor, input)
	require.NoError(t, err)
	require.EqualValues(t, 2, view.Revision)
	require.Equal(t, policy.MaxAttempts, view.Binding.Policy.MaxAttempts)

	var stored []byte
	require.NoError(t, f.db.GetContext(ctx, &stored, `SELECT r.config FROM runtime_catalog_revisions r JOIN integration_configs c ON c.id=r.integration_id WHERE c.workspace_id=$1 AND c.provider='agent_runtime' AND r.revision=2`, f.owner))
	var now domain.RuntimeCatalog
	require.NoError(t, json.Unmarshal(stored, &now))
	for name, account := range f.catalog.Accounts {
		require.Equal(t, account.CredentialRef, now.Accounts[name].CredentialRef, "credential_ref untouched")
		require.NotEmpty(t, now.Accounts[name].CredentialRef)
	}

	// permitted_profiles is optional; when given it replaces the list.
	input = RuntimePolicySave{IfRevision: 2, PermittedProfiles: f.catalog.Bindings["worker-b"].PermittedProfiles, Policy: policy}
	view, err = f.repo.SetBindingPolicy(ctx, f.owner, f.receiver, "worker-b", f.ownerActor, input)
	require.NoError(t, err)
	require.EqualValues(t, 3, view.Revision)
}
