package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func themeFixture(t *testing.T) (*ThemeRepo, *sqlx.DB, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run PostgreSQL theme controls")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err, "configured PostgreSQL controls must not silently skip")
	t.Cleanup(func() { _ = db.Close() })
	ws := uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'theme fixture',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM workspaces WHERE id=$1", ws) })
	return NewThemeRepo(db), db, ws
}

func TestThemeRepo_Workspace(t *testing.T) {
	repo, db, ws := themeFixture(t)
	ctx := context.Background()
	by := uuid.New()

	got, err := repo.GetWorkspace(ctx, ws)
	require.NoError(t, err)
	require.Nil(t, got, "no choice yet")

	_, err = db.Exec(`INSERT INTO installed_extensions(extension_id,version,manifest,source,trust_level) VALUES('themerepo-test','1.0.0','{}','repo','custom') ON CONFLICT DO NOTHING`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id='themerepo-test'`) })

	id := "themerepo-test"
	first, err := repo.SetWorkspace(ctx, ws, &id, json.RawMessage(`{"dense":true}`), by, nil)
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)
	second, err := repo.SetWorkspace(ctx, ws, &id, nil, by, nil)
	require.NoError(t, err)
	require.Equal(t, 2, second.Version, "every write bumps the version")
	require.JSONEq(t, `{}`, string(second.Settings))

	// Removing the theme row keeps the choice row; theme_id goes NULL.
	_, err = db.Exec(`DELETE FROM installed_extensions WHERE extension_id='themerepo-test'`)
	require.NoError(t, err)
	got, err = repo.GetWorkspace(ctx, ws)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Nil(t, got.ThemeID)
}

func TestThemeRepo_InstanceSingleton(t *testing.T) {
	repo, db, _ := themeFixture(t)
	ctx := context.Background()
	before, err := repo.GetInstance(ctx)
	require.NoError(t, err)
	require.NotNil(t, before, "the migration seeds the row")
	after, err := repo.SetInstance(ctx, nil, json.RawMessage(`{}`), uuid.New(), nil)
	require.NoError(t, err)
	require.Equal(t, before.Version+1, after.Version)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM instance_theme`))
	require.Equal(t, 1, n)
	_, err = db.Exec(`INSERT INTO instance_theme(id) VALUES(false)`)
	require.Error(t, err, "only one row can exist")
}

func TestThemeRepo_DeleteTheme(t *testing.T) {
	repo, db, wsA := themeFixture(t)
	ctx, by := context.Background(), uuid.New()
	id, other := "t7b-"+uuid.NewString()[:8], "t7b-"+uuid.NewString()[:8]
	for _, ext := range []string{id, other} {
		_, err := db.Exec(`INSERT INTO installed_extensions(extension_id, version, manifest, source, trust_level) VALUES($1,'1.0.0','{"kind":"theme"}','repo','custom')`, ext)
		require.NoError(t, err)
	}
	wsB, wsStale := uuid.New(), uuid.New()
	for _, ws := range []uuid.UUID{wsB, wsStale} {
		_, err := db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'theme fixture',$2,$3)`, ws, ws.String(), uuid.New())
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = ANY($1)`, []string{wsB.String(), wsStale.String()})
		_, _ = db.Exec(`DELETE FROM extension_repo_credentials WHERE extension_id = ANY($1)`, []string{id, other})
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = ANY($1)`, []string{id, other})
		_, _ = db.Exec(`UPDATE instance_theme SET theme_id = NULL, settings = '{}'::jsonb`)
	})

	settings := json.RawMessage(`{"accent":"1 2% 3%"}`)
	_, err := repo.SetInstance(ctx, &id, settings, by, nil)
	require.NoError(t, err)
	_, err = repo.SetWorkspace(ctx, wsA, &id, settings, by, nil)
	require.NoError(t, err)
	_, err = repo.SetWorkspace(ctx, wsB, &other, settings, by, nil)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO extension_repo_credentials(extension_id, token_enc) VALUES($1, 'enc'), ($2, 'enc')`, id, other)
	require.NoError(t, err)
	// What the foreign key leaves behind when a selection races the delete: no
	// theme any more, settings still there.
	_, err = db.Exec(`INSERT INTO workspace_theme(workspace_id, theme_id, settings) VALUES($1, NULL, '{"stale":1}')`, wsStale)
	require.NoError(t, err)

	ok, err := repo.DeleteTheme(ctx, id, nil)
	require.NoError(t, err)
	require.True(t, ok)

	inst, err := repo.GetInstance(ctx)
	require.NoError(t, err)
	require.Nil(t, inst.ThemeID)
	require.JSONEq(t, `{}`, string(inst.Settings))
	for _, ws := range []uuid.UUID{wsA, wsStale} {
		sel, gerr := repo.GetWorkspace(ctx, ws)
		require.NoError(t, gerr)
		require.Nil(t, sel.ThemeID)
		require.JSONEq(t, `{}`, string(sel.Settings))
	}
	kept, err := repo.GetWorkspace(ctx, wsB)
	require.NoError(t, err)
	require.Equal(t, other, *kept.ThemeID, "control: a workspace on another theme is untouched")
	require.JSONEq(t, string(settings), string(kept.Settings))

	var creds int
	require.NoError(t, db.Get(&creds, `SELECT count(*) FROM extension_repo_credentials WHERE extension_id = $1`, id))
	require.Zero(t, creds, "the repository token goes with the theme")
	require.NoError(t, db.Get(&creds, `SELECT count(*) FROM extension_repo_credentials WHERE extension_id = $1`, other))
	require.Equal(t, 1, creds, "control: another theme's token stays")

	ok, err = repo.DeleteTheme(ctx, id, nil)
	require.NoError(t, err)
	require.False(t, ok, "already gone")
}

// A theme in effect in a workspace counts as used there, whether the workspace
// chose it or inherits the instance default; an explicit choice beats the default
// and a soft-deleted workspace is not counted.
func TestInstalledExtensionRepo_CountActiveConnectionsThemes(t *testing.T) {
	repo, db, wsA := themeFixture(t)
	counter := NewInstalledExtensionRepo(db)
	ctx, by := context.Background(), uuid.New()
	chosen, inherited := "tcount-"+uuid.NewString()[:8], "tcount-"+uuid.NewString()[:8]
	for _, id := range []string{chosen, inherited} {
		_, err := db.Exec(`INSERT INTO installed_extensions(extension_id,version,manifest,source,trust_level) VALUES($1,'1.0.0','{"kind":"theme"}','repo','custom')`, id)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id=$1`, id) })
	}
	before, err := repo.GetInstance(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = repo.SetInstance(ctx, before.ThemeID, before.Settings, by, nil) })

	gone := uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id,deleted_at) VALUES($1,'theme fixture gone',$2,$3,now())`, gone, gone.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM workspaces WHERE id=$1", gone) })
	for _, ws := range []uuid.UUID{wsA, gone} {
		_, err = repo.SetWorkspace(ctx, ws, &chosen, nil, by, nil)
		require.NoError(t, err)
	}

	_, err = repo.SetInstance(ctx, nil, nil, by, nil)
	require.NoError(t, err)
	got, err := counter.CountActiveConnections(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, got[chosen], "an explicit choice counts; the soft-deleted workspace does not")
	require.Zero(t, got[inherited])

	_, err = repo.SetInstance(ctx, &inherited, nil, by, nil)
	require.NoError(t, err)
	withDefault, err := counter.CountActiveConnections(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, withDefault[chosen], "the explicit choice beats the instance default")
	require.Positive(t, withDefault[inherited], "workspaces with no choice inherit the instance default")

	_, err = repo.SetWorkspace(ctx, wsA, nil, nil, by, nil)
	require.NoError(t, err)
	asInstance, err := counter.CountActiveConnections(ctx)
	require.NoError(t, err)
	require.Zero(t, asInstance[chosen])
	require.Equal(t, withDefault[inherited]+1, asInstance[inherited], "\"as the instance\" moves the workspace to the default")
}
