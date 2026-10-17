// No //go:build integration tag — same convention as the other *_db_test.go
// files here; the coverage job has Postgres, a plain `go test` without one skips.

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestInstanceExtensionPolicyRepo_AllowListFlagAndJournal(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	repo := NewInstanceExtensionPolicyRepo(db)
	const community, official = "policy-test-community", "policy-test-official"
	cleanup := func() {
		db.Exec(`DELETE FROM instance_extension_policy WHERE extension_id IN ($1,$2)`, community, official)
		db.Exec(`DELETE FROM installed_extensions WHERE extension_id IN ($1,$2)`, community, official)
		db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id IN ($1,$2)`, community, official)
	}
	cleanup()
	t.Cleanup(cleanup)
	for id, trust := range map[string]string{community: "community", official: "official"} {
		_, err := db.Exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source, trust_level) VALUES ($1,'1.0.0','{}','catalog',$2)`, id, trust)
		require.NoError(t, err)
	}

	// trust-level defaults, an unknown id (no row anywhere) fails closed
	ok, err := repo.Allowed(ctx, official)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.Allowed(ctx, community)
	require.NoError(t, err)
	require.False(t, ok, "community needs an explicit allow")
	ok, err = repo.Allowed(ctx, "policy-test-unknown")
	require.NoError(t, err)
	require.False(t, ok, "an extension with no policy and no installed record is not allowed")

	// an explicit row beats the default, in both directions
	by := uuid.New()
	require.NoError(t, repo.SetAllowed(ctx, community, true, &by))
	require.NoError(t, repo.SetAllowed(ctx, official, false, &by))
	ok, _ = repo.Allowed(ctx, community)
	require.True(t, ok)
	ok, _ = repo.Allowed(ctx, official)
	require.False(t, ok)
	require.NoError(t, repo.SetAllowed(ctx, community, false, &by)) // idempotent upsert
	pol, err := repo.ListPolicies(ctx)
	require.NoError(t, err)
	require.False(t, pol[community].Allowed)

	// the auto-update flag is a singleton
	cfg, err := repo.GetConfig(ctx)
	require.NoError(t, err)
	prev := cfg.AutoUpdate
	t.Cleanup(func() { _ = repo.SetAutoUpdate(ctx, prev, nil) })
	require.NoError(t, repo.SetAutoUpdate(ctx, !prev, &by))
	cfg, _ = repo.GetConfig(ctx)
	require.Equal(t, !prev, cfg.AutoUpdate)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM instance_extension_config`))
	require.Equal(t, 1, n)

	// journal: newest first
	for _, a := range []string{domain.InstanceJournalAllowChanged, domain.InstanceJournalSettingsChanged} {
		require.NoError(t, repo.AppendJournal(ctx, &domain.InstanceExtensionJournalEntry{ActorID: &by, ExtensionID: community, Action: a, Details: json.RawMessage(`{"k":1}`)}))
	}
	rows, err := repo.ListJournal(ctx, 500)
	require.NoError(t, err)
	var mine []string
	for _, r := range rows {
		if r.ExtensionID == community {
			mine = append(mine, r.Action)
		}
	}
	require.Equal(t, []string{domain.InstanceJournalSettingsChanged, domain.InstanceJournalAllowChanged}, mine)
}
