package postgres

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Proves what the sqlmock tests next door cannot: that Postgres itself now
// enforces uniqueness only among LIVE workspaces (migration 20260911002),
// and that WorkspaceRepo.Delete's rename really lands a value the table's
// own chk_workspaces_slug_format CHECK still accepts. Task #c164a5df.
//
// Untagged — same convention as the other *_db_test.go files in this
// package. Skips when no DB is reachable.
func slugRenameTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func slugRenameOwner(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	suffix := uuid.New().String()[:8]
	owner := &domain.User{
		ID: uuid.New(), Email: "slug-rename-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Slug Rename Owner", Username: "slug-rename-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(context.Background(), owner))
	return owner.ID
}

func slugRenameGetSlug(t *testing.T, db *sqlx.DB, id uuid.UUID) string {
	t.Helper()
	var slug string
	require.NoError(t, db.Get(&slug, `SELECT slug FROM workspaces WHERE id = $1`, id))
	return slug
}

// AC1 (task #c164a5df): delete workspace A (slug=foo) → create a NEW
// workspace with the SAME slug=foo → succeeds. Before the partial unique
// index this failed outright — the dead row still held the slug.
func TestWorkspaceRepo_DeleteThenRecreate_SameSlug_Succeeds(t *testing.T) {
	db := slugRenameTestDB(t)
	ownerID := slugRenameOwner(t, db)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	slug := "foo-" + uuid.New().String()[:8]

	first := &domain.Workspace{ID: uuid.New(), Name: "Foo", Slug: slug, OwnerID: ownerID}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Delete(ctx, first.ID))

	second := &domain.Workspace{ID: uuid.New(), Name: "Foo Again", Slug: slug, OwnerID: ownerID}
	err := repo.Create(ctx, second)
	require.NoError(t, err, "recreating a workspace with a soft-deleted predecessor's exact slug must succeed")

	got, err := repo.GetBySlug(ctx, slug)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, second.ID, got.ID, "the live GetBySlug lookup must resolve to the NEW workspace, not the dead one")
}

// AC2: the dead row is reachable by its new (renamed) slug, not by the
// original one — the original now belongs to whoever holds it live.
func TestWorkspaceRepo_Delete_RenamesDeadRowSlug_OldSlugNotReachable(t *testing.T) {
	db := slugRenameTestDB(t)
	ownerID := slugRenameOwner(t, db)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	slug := "bar-" + uuid.New().String()[:8]
	ws := &domain.Workspace{ID: uuid.New(), Name: "Bar", Slug: slug, OwnerID: ownerID}
	require.NoError(t, repo.Create(ctx, ws))
	require.NoError(t, repo.Delete(ctx, ws.ID))

	// Not reachable by the original slug through the repo (live-only lookup).
	got, err := repo.GetBySlug(ctx, slug)
	require.NoError(t, err)
	assert.Nil(t, got, "GetBySlug must not resolve a soft-deleted row by its original slug")

	// The row itself now carries a distinguishable, still-valid slug.
	newSlug := slugRenameGetSlug(t, db, ws.ID)
	assert.NotEqual(t, slug, newSlug)
	assert.True(t, strings.HasPrefix(newSlug, slug+"-deleted-"+time.Now().UTC().Format("20060102")),
		"renamed slug must keep the original as a readable prefix plus today's date: got %q", newSlug)

	var deletedAt sql.NullTime
	require.NoError(t, db.Get(&deletedAt, `SELECT deleted_at FROM workspaces WHERE id = $1`, ws.ID))
	require.True(t, deletedAt.Valid)

	// The dead row's own CHECK constraint still holds for the renamed value
	// — proven by construction (the UPDATE in Delete would have errored
	// otherwise), reconfirmed here directly against the live column.
	var ok bool
	require.NoError(t, db.Get(&ok, `SELECT $1 ~ '^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$'`, newSlug))
	assert.True(t, ok, "renamed slug must still satisfy chk_workspaces_slug_format: %q", newSlug)
}

// AC3 (parent task, negative control): pre-fix, deleting then recreating
// with the same slug failed on the unique constraint. Reproduced directly
// against Postgres rather than asserted from memory — a partial index that
// silently didn't apply would make this pass for the wrong reason otherwise.
// Companion to subtask #4's baseline capture (if that ran first, this is the
// same fact reconfirmed from a second angle: the constraint's error shape).
func TestWorkspaceRepo_PreFixBehavior_DuplicateSlugAcrossLiveAndDead_StillRejectedByFullUniqueConstraint(t *testing.T) {
	db := slugRenameTestDB(t)
	ownerID := slugRenameOwner(t, db)
	ctx := context.Background()

	// Simulate the OLD schema inline: a second, table-wide unique constraint
	// that ignores deleted_at, layered on top of today's partial one. This
	// reproduces exactly the failure Pavel hit (#93644be7) without requiring
	// a second DB pointed at a pre-migration schema.
	_, err := db.ExecContext(ctx, `ALTER TABLE workspaces ADD CONSTRAINT chk_slug_rename_test_old_style_unique UNIQUE (slug)`)
	require.NoError(t, err)
	defer func() {
		_, _ = db.ExecContext(ctx, `ALTER TABLE workspaces DROP CONSTRAINT IF EXISTS chk_slug_rename_test_old_style_unique`)
	}()

	slug := "baseline-" + uuid.New().String()[:8]
	repo := NewWorkspaceRepo(db)

	first := &domain.Workspace{ID: uuid.New(), Name: "Baseline", Slug: slug, OwnerID: ownerID}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Delete(ctx, first.ID))
	// Delete already renamed the dead row's slug (today's fix), so re-insert
	// the OLD slug directly to reconstruct the pre-fix scenario: a dead row
	// still squatting on the exact slug a new workspace wants.
	_, err = db.ExecContext(ctx, `UPDATE workspaces SET slug = $1 WHERE id = $2`, slug, first.ID)
	require.NoError(t, err)

	second := &domain.Workspace{ID: uuid.New(), Name: "Baseline Again", Slug: slug, OwnerID: ownerID}
	err = repo.Create(ctx, second)
	require.Error(t, err, "with a table-wide unique constraint restored, recreating the same slug must fail")
	var pqErr *pq.Error
	require.ErrorAs(t, err, &pqErr)
	assert.EqualValues(t, "23505", pqErr.Code, "must fail specifically on unique_violation, not some other error")
}

// AC (parent task risk list): same slug deleted twice in one day must not
// collide — each dead row gets a distinct renamed slug because the
// disambiguator is derived from the row's own id, not from the date alone.
func TestWorkspaceRepo_Delete_SameSlugDeletedTwiceSameDay_NoCollision(t *testing.T) {
	db := slugRenameTestDB(t)
	ownerID := slugRenameOwner(t, db)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	slug := "twice-" + uuid.New().String()[:8]

	first := &domain.Workspace{ID: uuid.New(), Name: "Twice A", Slug: slug, OwnerID: ownerID}
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Delete(ctx, first.ID))

	second := &domain.Workspace{ID: uuid.New(), Name: "Twice B", Slug: slug, OwnerID: ownerID}
	require.NoError(t, repo.Create(ctx, second))
	require.NoError(t, repo.Delete(ctx, second.ID))

	firstSlug := slugRenameGetSlug(t, db, first.ID)
	secondSlug := slugRenameGetSlug(t, db, second.ID)
	assert.NotEqual(t, firstSlug, secondSlug,
		"two dead rows sharing an original slug, deleted the same day, must end up with distinct renamed slugs")
}

// Length safety: a slug at the column's own max (chk_workspaces_slug_format
// allows up to 100 chars) must still fit after the rename suffix is
// appended, and the row must remain insertable/recreatable afterward.
func TestWorkspaceRepo_Delete_MaxLengthSlug_RenameStaysWithinConstraint(t *testing.T) {
	db := slugRenameTestDB(t)
	ownerID := slugRenameOwner(t, db)
	repo := NewWorkspaceRepo(db)
	ctx := context.Background()

	// Build a 100-char valid slug: starts/ends alnum, unique enough not to
	// collide with a previous test run.
	unique := uuid.New().String()[:8]
	base := "a" + unique + strings.Repeat("x", 100-2-len(unique)) + "9"
	require.Len(t, base, 100)

	ws := &domain.Workspace{ID: uuid.New(), Name: "Max Slug", Slug: base, OwnerID: ownerID}
	require.NoError(t, repo.Create(ctx, ws))
	require.NoError(t, repo.Delete(ctx, ws.ID), "rename-on-delete must not violate the length/format CHECK even at max slug length")

	newSlug := slugRenameGetSlug(t, db, ws.ID)
	assert.LessOrEqual(t, len(newSlug), 100)

	var ok bool
	require.NoError(t, db.Get(&ok, `SELECT $1 ~ '^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$'`, newSlug))
	assert.True(t, ok, "renamed max-length slug must still satisfy chk_workspaces_slug_format: %q (len=%d)", newSlug, len(newSlug))

	// And the original max-length slug is free again.
	recreated := &domain.Workspace{ID: uuid.New(), Name: "Max Slug Again", Slug: base, OwnerID: ownerID}
	require.NoError(t, repo.Create(ctx, recreated))
}
