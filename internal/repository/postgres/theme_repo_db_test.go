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
	first, err := repo.SetWorkspace(ctx, ws, &id, json.RawMessage(`{"dense":true}`), by)
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)
	second, err := repo.SetWorkspace(ctx, ws, &id, nil, by)
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
	after, err := repo.SetInstance(ctx, nil, json.RawMessage(`{}`), uuid.New())
	require.NoError(t, err)
	require.Equal(t, before.Version+1, after.Version)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM instance_theme`))
	require.Equal(t, 1, n)
	_, err = db.Exec(`INSERT INTO instance_theme(id) VALUES(false)`)
	require.Error(t, err, "only one row can exist")
}
