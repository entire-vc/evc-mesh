package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// badEntry cannot be written: action is VARCHAR(32). A real database error in
// the journal insert is what the mutation must not survive.
func badEntry(extID string) *domain.InstanceExtensionJournalEntry {
	return &domain.InstanceExtensionJournalEntry{ExtensionID: extID, Action: strings.Repeat("x", 40)}
}

func goodEntry(extID, action string) *domain.InstanceExtensionJournalEntry {
	return &domain.InstanceExtensionJournalEntry{ExtensionID: extID, Action: action, Details: json.RawMessage(`{"k":"v"}`)}
}

func journalRows(t *testing.T, repo *ThemeRepo, extID string) int {
	t.Helper()
	var n int
	require.NoError(t, repo.db.Get(&n, `SELECT count(*) FROM instance_extension_journal WHERE extension_id = $1`, extID))
	return n
}

func TestThemeRepo_JournalIsAtomicWithTheMutation(t *testing.T) {
	repo, db, ws := themeFixture(t)
	ctx, by := context.Background(), uuid.New()
	id := "t7j-" + uuid.NewString()[:8]
	_, err := db.Exec(`INSERT INTO installed_extensions(extension_id, version, manifest, source, trust_level) VALUES($1,'1.0.0','{"kind":"theme"}','repo','custom')`, id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id)
		_, _ = db.Exec(`UPDATE instance_theme SET theme_id = NULL, settings = '{}'::jsonb`)
	})
	settings := json.RawMessage(`{"accent":"1 2% 3%"}`)

	t.Run("select instance", func(t *testing.T) {
		before, gerr := repo.GetInstance(ctx)
		require.NoError(t, gerr)
		_, err := repo.SetInstance(ctx, &id, settings, by, badEntry(id))
		require.Error(t, err)
		after, gerr := repo.GetInstance(ctx)
		require.NoError(t, gerr)
		require.Equal(t, before.Version, after.Version, "a failed journal write leaves the choice and its version alone")
		require.Nil(t, after.ThemeID)

		stored, err := repo.SetInstance(ctx, &id, settings, by, goodEntry(id, "theme_selected"))
		require.NoError(t, err, "control: the same call with a writable entry succeeds")
		require.Equal(t, 1, journalRows(t, repo, id))
		var d map[string]any
		var raw []byte
		require.NoError(t, db.Get(&raw, `SELECT details FROM instance_extension_journal WHERE extension_id = $1`, id))
		require.NoError(t, json.Unmarshal(raw, &d))
		require.EqualValues(t, stored.Version, d["version"], "the entry carries the stored version")
		require.Equal(t, "v", d["k"])
	})
	t.Run("select workspace", func(t *testing.T) {
		_, err := repo.SetWorkspace(ctx, ws, &id, settings, by, badEntry(id))
		require.Error(t, err)
		got, gerr := repo.GetWorkspace(ctx, ws)
		require.NoError(t, gerr)
		require.Nil(t, got, "no choice row appears")
		_, err = repo.SetWorkspace(ctx, ws, &id, settings, by, goodEntry(id, "theme_selected"))
		require.NoError(t, err, "control")
	})
	t.Run("delete", func(t *testing.T) {
		ok, err := repo.DeleteTheme(ctx, id, badEntry(id))
		require.Error(t, err)
		require.False(t, ok)
		row, gerr := NewInstalledExtensionRepo(db).Get(ctx, id)
		require.NoError(t, gerr)
		require.NotNil(t, row, "the theme is still installed")
		inst, gerr := repo.GetInstance(ctx)
		require.NoError(t, gerr)
		require.NotNil(t, inst.ThemeID, "and the selections still point at it")
		ws2, gerr := repo.GetWorkspace(ctx, ws)
		require.NoError(t, gerr)
		require.NotNil(t, ws2.ThemeID)

		before := journalRows(t, repo, id)
		ok, err = repo.DeleteTheme(ctx, id, goodEntry(id, "theme_deleted"))
		require.NoError(t, err, "the retry deletes, not a 404")
		require.True(t, ok)
		require.Equal(t, before+1, journalRows(t, repo, id))

		ok, err = repo.DeleteTheme(ctx, id, goodEntry(id, "theme_deleted"))
		require.NoError(t, err)
		require.False(t, ok, "already gone")
		require.Equal(t, before+1, journalRows(t, repo, id), "nothing was deleted, nothing is recorded")
	})
	t.Run("install builtin", func(t *testing.T) {
		bid := "t7j-" + uuid.NewString()[:8]
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, bid)
			_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, bid)
		})
		ext := &domain.InstalledExtension{ExtensionID: bid, Version: "1.0.0", Manifest: []byte(`{}`), Source: domain.ExtensionSourceBuiltin, TrustLevel: domain.ExtensionTrustBuiltin}
		require.Error(t, repo.InstallBuiltin(ctx, ext, badEntry(bid)))
		row, gerr := NewInstalledExtensionRepo(db).Get(ctx, bid)
		require.NoError(t, gerr)
		require.Nil(t, row, "nothing is installed")
		require.NoError(t, repo.InstallBuiltin(ctx, ext, goodEntry(bid, "installed")), "the retry installs")
		require.Equal(t, 1, journalRows(t, repo, bid))
	})
	t.Run("install from repo", func(t *testing.T) {
		rid := "t7j-" + uuid.NewString()[:8]
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, rid)
			_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, rid)
		})
		ext := &domain.InstalledExtension{ExtensionID: rid, Version: "1.0.0", Manifest: []byte(`{}`), RepoURL: "https://example.test/r.git", RepoRef: "main", CommitSHA: "abc", TrustLevel: domain.ExtensionTrustCustom}
		rr := NewRepoExtensionRepo(db)
		require.Error(t, rr.InsertRepo(ctx, ext, "", badEntry(rid)))
		row, gerr := NewInstalledExtensionRepo(db).Get(ctx, rid)
		require.NoError(t, gerr)
		require.Nil(t, row, "the failed install leaves no row, so a retry is not a 409")
		require.NoError(t, rr.InsertRepo(ctx, ext, "", goodEntry(rid, "installed")))
		require.Equal(t, 1, journalRows(t, repo, rid))
	})
}

// An id that no longer names a theme (another extension took it after the
// service's check) is left alone, token included, and nothing is journaled.
func TestThemeRepo_DeleteSkipsANonTheme(t *testing.T) {
	repo, db, _ := themeFixture(t)
	ctx := context.Background()
	id := "t7k-" + uuid.NewString()[:8]
	_, err := db.Exec(`INSERT INTO installed_extensions(extension_id, version, manifest, source, trust_level) VALUES($1,'1.0.0','{"kind":"plugin"}','repo','custom')`, id)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO extension_repo_credentials(extension_id, token_enc) VALUES($1, 'enc')`, id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM extension_repo_credentials WHERE extension_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id)
	})

	ok, err := repo.DeleteTheme(ctx, id, goodEntry(id, "theme_deleted"))
	require.NoError(t, err)
	require.False(t, ok)
	row, err := NewInstalledExtensionRepo(db).Get(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, row, "the plugin stays installed")
	var creds int
	require.NoError(t, db.Get(&creds, `SELECT count(*) FROM extension_repo_credentials WHERE extension_id = $1`, id))
	require.Equal(t, 1, creds, "and keeps its token")
	require.Zero(t, journalRows(t, repo, id))
}
