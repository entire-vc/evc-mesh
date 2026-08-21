package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// R3's three repository methods, exercised through the repository itself
// against a live Postgres.
//
// The service-level tests run these against a mock, which can only prove the
// service calls them correctly. What a mock cannot prove is the half that
// actually protects the data: that a copy row satisfies chk_documents_source_shape,
// that a duplicate mount is refused by uq_documents_source in the DATABASE rather
// than only by the service's own GetBySourceInProject lookup (which races with
// itself), and that RefreshSyncedCopy's `source_kind <> 'own'` guard is real.
//
// Skips, not fails, when no Postgres is reachable — same convention as the
// neighbouring *_db_test.go files.

func mountRepoFixture(t *testing.T) (*DocumentRepo, sourceFixture, *sqlx.DB) {
	t.Helper()
	db := externalSourceTestDB(t)
	return NewDocumentRepo(db), newSourceFixture(t, db), db
}

func newCopyDoc(f sourceFixture, share, path, sha string, syncedAt time.Time) *domain.Document {
	id, slug, storageKey, createdBy := f.newDocRow()
	shareCopy, pathCopy, shaCopy, syncedCopy := share, path, sha, syncedAt
	return &domain.Document{
		ID: id, ProjectID: f.projectID, Slug: slug, Title: "Copy " + path,
		StorageKey: storageKey, Position: 0, Version: 1,
		CreatedBy: createdBy, CreatedByType: domain.ActorTypeUser,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		SourceKind:  domain.DocumentSourceTeamRelay,
		SourceShare: &shareCopy, SourcePath: &pathCopy,
		SourceSHA256: &shaCopy, SyncedAt: &syncedCopy,
	}
}

func TestCreateExternalCopy_RoundTripsEverySourceColumn(t *testing.T) {
	repo, f, _ := mountRepoFixture(t)
	ctx := context.Background()
	share, path, sha := uuid.New().String(), "Notes/Welcome.md", "sha-abc"
	syncedAt := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, repo.CreateExternalCopy(ctx, newCopyDoc(f, share, path, sha, syncedAt)))

	got, err := repo.GetBySourceInProject(ctx, f.projectID, share, path)
	require.NoError(t, err)
	require.NotNil(t, got, "the copy just written must be findable by its source coordinates")

	assert.Equal(t, domain.DocumentSourceTeamRelay, got.SourceKind)
	require.NotNil(t, got.SourceShare)
	require.NotNil(t, got.SourcePath)
	require.NotNil(t, got.SourceSHA256)
	require.NotNil(t, got.SyncedAt, "synced_at must survive the round trip — RefreshIfStale gates on it, and a NULL would make every open re-check")
	assert.Equal(t, share, *got.SourceShare)
	assert.Equal(t, path, *got.SourcePath)
	assert.Equal(t, sha, *got.SourceSHA256)
	assert.WithinDuration(t, syncedAt, *got.SyncedAt, time.Second)
}

// The service checks GetBySourceInProject before creating, but two concurrent
// mounts both pass that check before either writes. The database is what makes
// the invariant hold; this proves it is actually enforced there.
func TestCreateExternalCopy_DuplicateSourceIsRefusedByTheDatabase(t *testing.T) {
	repo, f, _ := mountRepoFixture(t)
	ctx := context.Background()
	share, path := uuid.New().String(), "Notes/Welcome.md"

	require.NoError(t, repo.CreateExternalCopy(ctx, newCopyDoc(f, share, path, "sha-1", time.Now().UTC())))

	err := repo.CreateExternalCopy(ctx, newCopyDoc(f, share, path, "sha-2", time.Now().UTC()))

	require.Error(t, err, "a second copy of the same source path in the same project must be refused")
	assert.True(t, isUniqueViolation(err, "uq_documents_source"),
		"expected uq_documents_source to be the thing that refused it, got: %v", err)
}

// Same path, different project — legitimate. Two projects may each mount the
// same share; an over-broad index would break the second one.
func TestCreateExternalCopy_SamePathInAnotherProjectIsAllowed(t *testing.T) {
	repo, f1, db := mountRepoFixture(t)
	f2 := newSourceFixture(t, db)
	ctx := context.Background()
	share, path := uuid.New().String(), "Notes/Welcome.md"

	require.NoError(t, repo.CreateExternalCopy(ctx, newCopyDoc(f1, share, path, "sha-1", time.Now().UTC())))
	require.NoError(t, repo.CreateExternalCopy(ctx, newCopyDoc(f2, share, path, "sha-1", time.Now().UTC())),
		"the uniqueness is per project — two projects mounting one share must both work")
}

func TestGetBySourceInProject_UnknownSourceIsNotAnError(t *testing.T) {
	repo, f, _ := mountRepoFixture(t)

	got, err := repo.GetBySourceInProject(context.Background(), f.projectID, uuid.New().String(), "Nope.md")

	require.NoError(t, err, "'no copy yet' is the ordinary first-mount state, not a failure")
	assert.Nil(t, got)
}

// bumpVersion is the whole point of the split: a same-hash check advances
// synced_at only, and must NOT create a new version — otherwise every TTL
// expiry would manufacture a version bump for a document nobody edited.
func TestRefreshSyncedCopy_VersionBumpsOnlyWhenTheBodyWasRewritten(t *testing.T) {
	repo, f, _ := mountRepoFixture(t)
	ctx := context.Background()
	share, path := uuid.New().String(), "Notes/Welcome.md"
	doc := newCopyDoc(f, share, path, "sha-old", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, repo.CreateExternalCopy(ctx, doc))

	// Unchanged source: stamp only.
	stampedAt := time.Now().UTC().Truncate(time.Millisecond)
	v, err := repo.RefreshSyncedCopy(ctx, doc.ID, "sha-old", stampedAt, false)
	require.NoError(t, err)
	assert.Equal(t, 1, v, "an unchanged source must not manufacture a new version")

	after, err := repo.GetBySourceInProject(ctx, f.projectID, share, path)
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.WithinDuration(t, stampedAt, *after.SyncedAt, time.Second, "synced_at must advance even when the body did not")

	// Changed source: stamp AND bump.
	v2, err := repo.RefreshSyncedCopy(ctx, doc.ID, "sha-new", time.Now().UTC(), true)
	require.NoError(t, err)
	assert.Equal(t, 2, v2, "a rewritten body is a new version")

	after2, err := repo.GetBySourceInProject(ctx, f.projectID, share, path)
	require.NoError(t, err)
	require.NotNil(t, after2)
	assert.Equal(t, "sha-new", *after2.SourceSHA256)
}

// The `source_kind <> 'own'` guard. An own document must never receive sync
// metadata — chk_documents_source_shape forbids that row shape, so without the
// guard this UPDATE would fail with a constraint violation instead of a clean
// not-found, and a bug upstream would surface as a 500.
func TestRefreshSyncedCopy_RefusesToStampAnOwnDocument(t *testing.T) {
	repo, f, db := mountRepoFixture(t)
	ctx := context.Background()

	id, slug, storageKey, createdBy := f.newDocRow()
	_, err := db.ExecContext(ctx, `
		INSERT INTO documents (id, project_id, slug, title, storage_key, position,
			created_by, created_by_type, created_at, updated_at, version, source_kind)
		VALUES ($1,$2,$3,'Own doc',$4,0,$5,'user',now(),now(),1,'own')`,
		id, f.projectID, slug, storageKey, createdBy)
	require.NoError(t, err)

	_, err = repo.RefreshSyncedCopy(ctx, id, "sha-whatever", time.Now().UTC(), false)

	require.Error(t, err, "an own document must never be stamped with sync metadata")
	assert.NotContains(t, err.Error(), "chk_documents_source_shape",
		"the guard must reject this cleanly, not let it reach the CHECK constraint as a 500")
}

func TestRefreshSyncedCopy_UnknownDocumentIsNotFound(t *testing.T) {
	repo, _, _ := mountRepoFixture(t)

	_, err := repo.RefreshSyncedCopy(context.Background(), uuid.New(), "sha", time.Now().UTC(), false)

	require.Error(t, err)
}
