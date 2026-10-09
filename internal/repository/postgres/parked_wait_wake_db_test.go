package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A WAKE re-checks the assignee's pause and grant as well as blockers, gate and
// lease. A refused wake leaves the wait registered and untouched, and the same
// registration can be released once the condition is gone.
func TestParkedWaitWakeRechecksAssigneePauseAndGrant(t *testing.T) {
	f := newParkedFixture(t)
	f.register(t)
	_, err := f.repo.db.Exec(`UPDATE agents SET capabilities='{"fleet_paused": true}'::jsonb WHERE id=$1`, f.owner)
	require.NoError(t, err)
	_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	parked409(t, err)
	f.assertHeld(t)

	_, err = f.repo.db.Exec(`UPDATE agents SET capabilities='["fleet-paused"]'::jsonb WHERE id=$1`, f.owner)
	require.NoError(t, err)
	_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	parked409(t, err)
	f.assertHeld(t)

	// Resumed, but the identity lives in another workspace without a grant here.
	other := seedGrantWorkspace(t, f.repo.db)
	var home uuid.UUID
	require.NoError(t, f.repo.db.Get(&home, `SELECT workspace_id FROM agents WHERE id=$1`, f.owner))
	_, err = f.repo.db.Exec(`UPDATE agents SET capabilities='{}'::jsonb, workspace_id=$2 WHERE id=$1`, f.owner, other)
	require.NoError(t, err)
	_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	parked409(t, err)
	f.assertHeld(t)

	// A revoked grant is no grant.
	revokedAt := time.Now().Add(-time.Minute)
	insertGrant(t, f.repo.db, f.owner, home, "member", "wakerevoked", "$2a$12$revoked-hash", &revokedAt)
	_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	parked409(t, err)
	f.assertHeld(t)

	_, err = f.repo.db.Exec(`UPDATE agents SET workspace_id=$2 WHERE id=$1`, f.owner, home)
	require.NoError(t, err)
	_, err = f.repo.db.Exec(`UPDATE agents SET deleted_at=now() WHERE id=$1`, f.owner)
	require.NoError(t, err)
	_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	parked409(t, err)
	f.assertHeld(t)

	// Foreign agent with a live grant wakes normally.
	_, err = f.repo.db.Exec(`UPDATE agents SET deleted_at=NULL, workspace_id=$2 WHERE id=$1`, f.owner, other)
	require.NoError(t, err)
	_, err = f.repo.db.Exec(`UPDATE agent_workspace_grants SET revoked_at=NULL WHERE agent_id=$1 AND workspace_id=$2`, f.owner, home)
	require.NoError(t, err)
	result, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	require.NoError(t, err)
	require.True(t, result.Released)
}
