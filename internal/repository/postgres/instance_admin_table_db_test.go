// No //go:build integration tag — same convention as the other *_db_test.go
// files here; the coverage job has Postgres, a plain `go test` without one skips.

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestInstanceExtensionPolicyRepo_SearchJournalAndFacets(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	repo := NewInstanceExtensionPolicyRepo(db)
	const extA, extB = "journal-filter-a", "journal-filter-b"
	actor := uuid.New()
	cleanup := func() {
		db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id IN ($1,$2)`, extA, extB)
		db.Exec(`DELETE FROM users WHERE id = $1`, actor)
	}
	cleanup()
	t.Cleanup(cleanup)
	_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','Journal Admin')`, actor, actor.String()+"@journal.test", "ja-"+actor.String()[:30])
	require.NoError(t, err)
	other := uuid.New()
	for _, e := range []domain.InstanceExtensionJournalEntry{
		{ActorID: &actor, ExtensionID: extA, Action: domain.InstanceJournalAllowChanged},
		{ActorID: &other, ExtensionID: extA, Action: domain.InstanceJournalThemeSelected},
		{ActorID: &actor, ExtensionID: extB, Action: domain.InstanceJournalThemeSelected},
	} {
		require.NoError(t, repo.AppendJournal(ctx, &e))
	}

	got := func(f domain.InstanceJournalFilter) []string {
		f.Limit = 50
		rows, serr := repo.SearchJournal(ctx, f)
		require.NoError(t, serr)
		var out []string
		for _, r := range rows {
			if r.ExtensionID == extA || r.ExtensionID == extB {
				out = append(out, r.ExtensionID+"/"+r.Action)
			}
		}
		return out
	}
	require.Len(t, got(domain.InstanceJournalFilter{}), 3, "no filter: everything")
	require.Equal(t, []string{extA + "/theme_selected", extA + "/allow_changed"}, got(domain.InstanceJournalFilter{ExtensionID: extA}))
	require.Equal(t, []string{extB + "/theme_selected"}, got(domain.InstanceJournalFilter{ActorID: &actor, Action: domain.InstanceJournalThemeSelected}))
	future := time.Now().Add(time.Hour)
	require.Empty(t, got(domain.InstanceJournalFilter{Since: &future}), "since is a lower bound")
	require.Len(t, got(domain.InstanceJournalFilter{Until: &future}), 3, "until is an upper bound")

	facets, err := repo.JournalFacets(ctx)
	require.NoError(t, err)
	require.Contains(t, facets.Extensions, extA)
	require.Contains(t, facets.Actions, domain.InstanceJournalThemeSelected)
	require.Contains(t, facets.Actors, domain.InstanceJournalActor{ID: actor, Name: "Journal Admin"})
}

func TestInstalledExtensionRepo_CountActiveConnections(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	const ext = "usage-count-test"
	owner := uuid.New()
	live1, live2, gone := uuid.New(), uuid.New(), uuid.New()
	cleanup := func() {
		db.Exec(`DELETE FROM integration_configs WHERE provider = $1`, ext)
		db.Exec(`DELETE FROM workspaces WHERE id IN ($1,$2,$3)`, live1, live2, gone)
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, ws := range []uuid.UUID{live1, live2, gone} {
		_, err := db.Exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'usage',$2,$3)`, ws, ws.String(), owner)
		require.NoError(t, err)
	}
	_, err := db.Exec(`UPDATE workspaces SET deleted_at = now() WHERE id = $1`, gone)
	require.NoError(t, err)
	for ws, active := range map[uuid.UUID]bool{live1: true, live2: false, gone: true} {
		_, err = db.Exec(`INSERT INTO integration_configs (id, workspace_id, provider, config, is_active) VALUES ($1,$2,$3,'{}',$4)`, uuid.New(), ws, ext, active)
		require.NoError(t, err)
	}

	counts, err := NewInstalledExtensionRepo(db).CountActiveConnections(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, counts[ext], "inactive connections and deleted workspaces are not counted")
}
