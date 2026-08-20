package postgres

import (
	"context"
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

// These pin what makes source_kind a real discriminator rather than a second
// inference layered on top of NULL: the database refuses the row shapes that
// would make "own" and "copy" ambiguous, and the query R3 needs — every copy
// of a share — resolves through the index this migration adds rather than a
// table scan. Reuses the skip-if-unreachable convention from
// document_comment_reanchor_db_test.go; no build tag, for the same reason.

func externalSourceTestDB(t *testing.T) *sqlx.DB {
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

type sourceFixture struct {
	projectID uuid.UUID
}

func newSourceFixture(t *testing.T, db *sqlx.DB) sourceFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{ID: uuid.New(), Name: "source-ws", Slug: "source-ws-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))
	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "source-proj",
		Slug: "source-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM documents WHERE project_id = $1`, proj.ID)
		_, _ = db.Exec(`DELETE FROM projects WHERE id = $1`, proj.ID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws.ID)
	})
	return sourceFixture{projectID: proj.ID}
}

// newDocRow returns the five columns every INSERT below needs regardless of
// which source_* columns it is proving — one document id/slug/storage_key/
// creator per row, so distinct tests never collide on the sibling-slug index.
func (f sourceFixture) newDocRow() (id uuid.UUID, slug, storageKey string, createdBy uuid.UUID) {
	id = uuid.New()
	return id, "doc-" + id.String()[:8], "documents/" + id.String() + ".md", uuid.New()
}

func isCheckViolation(err error, constraint string) bool {
	pqErr, ok := err.(*pq.Error)
	if !ok {
		return false
	}
	return pqErr.Code == "23514" && pqErr.Constraint == constraint
}

func isUniqueViolation(err error, constraint string) bool {
	pqErr, ok := err.(*pq.Error)
	if !ok {
		return false
	}
	return pqErr.Code == "23505" && pqErr.Constraint == constraint
}

// TestChkDocumentsSourceShape_OwnDocumentWithASourceFieldIsRejected proves the
// own-arm of the shape check: a document that claims to be 'own' (the default,
// and the only kind left unspecified) cannot also carry a source hash. Without
// this half of the constraint, an own document could silently pick up a stray
// source_sha256 a caller set by mistake, and nothing downstream — the index,
// R3's freshness check — would notice it was never supposed to be there.
func TestChkDocumentsSourceShape_OwnDocumentWithASourceFieldIsRejected(t *testing.T) {
	db := externalSourceTestDB(t)
	f := newSourceFixture(t, db)
	id, slug, storageKey, createdBy := f.newDocRow()

	_, err := db.ExecContext(context.Background(), `
		INSERT INTO documents (id, project_id, title, slug, storage_key, created_by, created_by_type, source_sha256)
		VALUES ($1, $2, 'doc', $3, $4, $5, 'agent', $6)`,
		id, f.projectID, slug, storageKey, createdBy, strings.Repeat("a", 64))

	require.Error(t, err)
	assert.True(t, isCheckViolation(err, "chk_documents_source_shape"),
		"expected chk_documents_source_shape violation, got: %v", err)
}

// TestChkDocumentsSourceShape_CopyWithoutSyncedAtIsRejected proves the copy
// arm: a row cannot claim to be a Team Relay copy while omitting when it was
// synced. synced_at is what R3's staleness check reads, so a copy without one
// would be un-stale-able — indistinguishable from "just synced" and "never
// synced" at once.
func TestChkDocumentsSourceShape_CopyWithoutSyncedAtIsRejected(t *testing.T) {
	db := externalSourceTestDB(t)
	f := newSourceFixture(t, db)
	id, slug, storageKey, createdBy := f.newDocRow()

	_, err := db.ExecContext(context.Background(), `
		INSERT INTO documents (id, project_id, title, slug, storage_key, created_by, created_by_type,
		                        source_kind, source_share, source_path, source_sha256)
		VALUES ($1, $2, 'doc', $3, $4, $5, 'agent', 'team_relay', $6, $7, $8)`,
		id, f.projectID, slug, storageKey, createdBy, "share-x", "a/b.md", strings.Repeat("b", 64))

	require.Error(t, err)
	assert.True(t, isCheckViolation(err, "chk_documents_source_shape"),
		"expected chk_documents_source_shape violation, got: %v", err)
}

// TestChkDocumentsSourceShape_CopyIsDistinguishableWithoutExternalAuthor is the
// mutation this migration exists to survive. Team Relay names no per-file
// author (see the migration's comment), so external_author is NULL on every
// copy that exists or will exist until that changes upstream — a discriminator
// that inferred "is this a copy?" from external_author being non-NULL would
// answer "no" for every single copy in production. This inserts a legitimate
// copy — external_author NULL, every other source_* column populated — and
// asserts source_kind alone says "copy", which is exactly the case the naive
// discriminator gets wrong: run with `got.SourceKind` replaced by
// `got.ExternalAuthor != nil` this assertion fails, which is the point.
func TestChkDocumentsSourceShape_CopyIsDistinguishableWithoutExternalAuthor(t *testing.T) {
	db := externalSourceTestDB(t)
	f := newSourceFixture(t, db)
	id, slug, storageKey, createdBy := f.newDocRow()

	_, err := db.ExecContext(context.Background(), `
		INSERT INTO documents (id, project_id, title, slug, storage_key, created_by, created_by_type,
		                        source_kind, source_share, source_path, source_sha256, synced_at)
		VALUES ($1, $2, 'doc', $3, $4, $5, 'agent', 'team_relay', $6, $7, $8, $9)`,
		id, f.projectID, slug, storageKey, createdBy,
		"share-y", "c/d.md", strings.Repeat("c", 64), time.Now().UTC())
	require.NoError(t, err)

	var got struct {
		SourceKind     domain.DocumentSource `db:"source_kind"`
		ExternalAuthor *string               `db:"external_author"`
	}
	require.NoError(t, db.Get(&got, `SELECT source_kind, external_author FROM documents WHERE id = $1`, id))

	require.Nil(t, got.ExternalAuthor, "the honest value today — Team Relay names no per-file author")
	assert.Equal(t, domain.DocumentSourceTeamRelay, got.SourceKind,
		"source_kind must say 'copy' on its own — inferring it from "+
			"external_author != nil would misclassify this exact row, since "+
			"external_author is nil here just as it is on every real copy")
}

// TestUqDocumentsSource_RejectsASecondCopyOfTheSamePath proves the partial
// unique index is load-bearing, not decorative: a second sync of a page whose
// earlier copy was not found is refused, rather than producing a sibling row
// nothing in the tree can tell apart from the original.
func TestUqDocumentsSource_RejectsASecondCopyOfTheSamePath(t *testing.T) {
	db := externalSourceTestDB(t)
	f := newSourceFixture(t, db)

	insertCopy := func(sha string) error {
		id, slug, storageKey, createdBy := f.newDocRow()
		_, err := db.ExecContext(context.Background(), `
			INSERT INTO documents (id, project_id, title, slug, storage_key, created_by, created_by_type,
			                        source_kind, source_share, source_path, source_sha256, synced_at)
			VALUES ($1, $2, 'doc', $3, $4, $5, 'agent', 'team_relay', $6, $7, $8, $9)`,
			id, f.projectID, slug, storageKey, createdBy,
			"share-z", "e/f.md", sha, time.Now().UTC())
		return err
	}

	require.NoError(t, insertCopy(strings.Repeat("d", 64)))
	err := insertCopy(strings.Repeat("e", 64))

	require.Error(t, err)
	assert.True(t, isUniqueViolation(err, "uq_documents_source"),
		"expected uq_documents_source violation, got: %v", err)
}

// TestUqDocumentsSource_IndexServesTheListAllCopiesOfAShareQuery is AC-2: the
// query R3 will run on every open of a Team Relay-backed project tree — "every
// copy belonging to this share" — must be answerable through the partial
// index this migration creates, not only by a sequential scan.
//
// enable_seqscan is turned off for the EXPLAIN rather than left to the
// planner's own choice: this fixture seeds three rows, and Postgres correctly
// prefers a sequential scan over an index on a table that small regardless of
// which indexes exist — that would make the test flip on row count, not on
// whether uq_documents_source actually matches this query's shape. Forcing
// seqscan off asks the real question — can the planner reach the index at all
// for (project_id, source_kind <> 'own', source_share) — independent of the
// cost heuristic a three-row test table trips.
func TestUqDocumentsSource_IndexServesTheListAllCopiesOfAShareQuery(t *testing.T) {
	db := externalSourceTestDB(t)
	f := newSourceFixture(t, db)

	for i := 0; i < 3; i++ {
		id, slug, storageKey, createdBy := f.newDocRow()
		_, err := db.ExecContext(context.Background(), `
			INSERT INTO documents (id, project_id, title, slug, storage_key, created_by, created_by_type,
			                        source_kind, source_share, source_path, source_sha256, synced_at)
			VALUES ($1, $2, 'doc', $3, $4, $5, 'agent', 'team_relay', 'share-list', $6, $7, now())`,
			id, f.projectID, slug, storageKey, createdBy,
			"path/"+id.String()+".md", strings.Repeat("f", 64))
		require.NoError(t, err)
	}

	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec(`SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)

	rows, err := tx.Query(`EXPLAIN (FORMAT TEXT)
		SELECT id FROM documents
		 WHERE project_id = $1 AND source_kind <> 'own' AND source_share = $2 AND deleted_at IS NULL`,
		f.projectID, "share-list")
	require.NoError(t, err)
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan = append(plan, line)
	}
	require.NoError(t, rows.Err())

	planText := strings.Join(plan, "\n")
	assert.Contains(t, planText, "uq_documents_source",
		"expected the planner to be able to reach uq_documents_source, got plan:\n%s", planText)
}
