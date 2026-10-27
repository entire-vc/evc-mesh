package postgres

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Denied, a save into a Runner that is on is refused and changes nothing,
// whatever the request says about enabled; switched off by the extension
// switch, it saves; allowed again and switched on, the catalog is back.
func TestRuntimeRepoSave_AllowListDB(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	allow := &exttest.Allow{}
	f.repo.WithAllowList(allow)
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)

	allow.Deny(runner.ID)
	off := false
	for _, enabled := range []*bool{f.input.Enabled, &off, nil} {
		input := f.input
		input.IfRevision, input.Enabled = 1, enabled
		_, err = f.repo.Save(ctx, f.owner, f.ownerActor, input)
		var ae *apierror.Error
		require.ErrorAs(t, err, &ae)
		require.Equal(t, http.StatusForbidden, ae.Code)
		require.Equal(t, "extension_not_allowed", ae.ErrorCode)
	}
	snapshot, err := f.repo.Inventory(ctx, f.owner, f.ownerActor)
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Revision, "a refused save writes nothing")
	_, err = f.repo.Restore(ctx, f.owner, f.ownerActor, RuntimeRestore{IfRevision: 1, Revision: 1})
	require.Error(t, err, "a restore is a save and is refused the same way")

	f.runnerSwitch(t, false) // revision 2: the switch
	f.input.IfRevision = 2
	saved, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err, "a denied Runner that is off can still be edited")
	require.False(t, saved.Enabled)

	allow.Permit(runner.ID)
	f.runnerSwitch(t, true) // revision 4
	f.input.IfRevision = 4
	saved, err = f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	require.EqualValues(t, 5, saved.Revision)
	require.True(t, saved.Enabled)
	require.NotEmpty(t, saved.Catalog.Controllers, "the catalog is back without a migration")
}
