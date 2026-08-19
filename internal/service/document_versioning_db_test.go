package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Document versioning against a real PostgreSQL.
//
// The sibling test file asserts the service's rules through the in-memory
// repository double. It cannot assert the claim those rules actually rest on:
// that `UPDATE ... SET version = version + 1` under `SELECT ... FOR UPDATE`
// serializes two concurrent writers on one row. The double enforces that with a
// Go mutex, so it would stay green even if the SQL were not doing it at all —
// exactly the failure mode the refresh-token concurrency test next door exists
// to close. This is the only place the claim is true or false.
//
// Untagged, matching the other *_db_test.go files: CI's plain `go test ./...`
// runs against a migrated DATABASE_URL, and this skips when no Postgres is
// reachable rather than failing a local run.
func documentVersionTestDB(t *testing.T) *sqlx.DB {
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

// documentDBFixture is one project in one workspace with the document service
// wired to the REAL repository. Object storage stays a double: S3 is a true
// external boundary, and nothing here is a claim about S3.
type documentDBFixture struct {
	svc       DocumentService
	db        *sqlx.DB
	storage   *MockStorageClient
	projectID uuid.UUID
	wsID      uuid.UUID
}

func newDocumentDBFixture(t *testing.T, db *sqlx.DB) *documentDBFixture {
	t.Helper()

	ownerID := uuid.New()
	handle := "docver-" + ownerID.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		 VALUES ($1, $2, 'not-a-real-hash', 'Doc Version Test', $3, true)`,
		ownerID, handle+"@example.test", handle,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, ownerID) })

	wsID := uuid.New()
	_, err = db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, $2, $3, $4)`,
		wsID, "Doc Version Test", "docver-ws-"+wsID.String()[:8], ownerID,
	)
	require.NoError(t, err)
	// documents.project_id cascades from projects, which cascades from
	// workspaces, so this one delete clears everything the test wrote.
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })

	projectID := uuid.New()
	_, err = db.Exec(
		`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, $3, $4)`,
		projectID, wsID, "Docs", "docver-p-"+projectID.String()[:8],
	)
	require.NoError(t, err)

	storage := NewMockStorageClient()
	svc := NewDocumentService(postgres.NewDocumentRepo(db), storage, postgres.NewProjectRepo(db))

	return &documentDBFixture{svc: svc, db: db, storage: storage, projectID: projectID, wsID: wsID}
}

func (f *documentDBFixture) create(t *testing.T, title, body string) *domain.Document {
	t.Helper()
	doc, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID:     f.projectID,
		Title:         title,
		Body:          body,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)
	return doc
}

func (f *documentDBFixture) storedBody(doc *domain.Document) string {
	f.storage.mu.RLock()
	defer f.storage.mu.RUnlock()
	return string(f.storage.objects[doc.StorageKey])
}

func (f *documentDBFixture) storedVersion(t *testing.T, doc *domain.Document) int64 {
	t.Helper()
	var v int64
	require.NoError(t, f.db.Get(&v, `SELECT version FROM documents WHERE id = $1`, doc.ID))
	return v
}

// AC4. Two — here, eight — simultaneous appends, and every one of their texts is
// in the final body.
//
// An append is a read-modify-write across Postgres and S3, which no statement
// can make atomic. Unserialized, the losers read the same body the winner did
// and overwrite it: with N appenders the expected survivor count is 1, not N.
// The row lock is the whole mechanism, and this is where it is proved.
func TestDocumentVersionDB_ConcurrentAppendsAllLand(t *testing.T) {
	const appenders = 8

	db := documentVersionTestDB(t)
	f := newDocumentDBFixture(t, db)
	doc := f.create(t, "Run log", "# Run log\n")

	// Every appender gets its own connection and they are released together, so
	// the contention is real row-level contention in Postgres rather than queuing
	// in the pool or in goroutine start-up.
	db.SetMaxOpenConns(appenders * 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, appenders)

	for i := 0; i < appenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.svc.AppendBody(context.Background(), doc.ID, f.wsID, AppendDocumentInput{
				Text:          fmt.Sprintf("- entry %d\n", i),
				UpdatedBy:     uuid.New(),
				UpdatedByType: domain.ActorTypeAgent,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "appender %d failed; an append has nothing to conflict with", i)
	}

	body := f.storedBody(doc)
	var missing []string
	for i := 0; i < appenders; i++ {
		if !strings.Contains(body, fmt.Sprintf("- entry %d\n", i)) {
			missing = append(missing, fmt.Sprintf("entry %d", i))
		}
	}
	assert.Empty(t, missing,
		"%d of %d concurrent appends were overwritten by another appender; body was:\n%s",
		len(missing), appenders, body)

	assert.True(t, strings.HasPrefix(body, "# Run log\n"), "the text that was already there survived")
	assert.Equal(t, int64(1+appenders), f.storedVersion(t, doc),
		"every append is a write, so each one moves the counter exactly once")
}

// The other half of the same mechanism: N writers all holding the SAME base
// version, and exactly one of them may win. A conditional write that let two
// through would be no guard at all, and one that let none through would satisfy
// a naive "not more than one" check while making the document unwritable.
func TestDocumentVersionDB_ConcurrentConditionalWritesExactlyOneWins(t *testing.T) {
	const racers = 8

	db := documentVersionTestDB(t)
	f := newDocumentDBFixture(t, db)
	doc := f.create(t, "Runbook", "original\n")
	base := doc.Version

	db.SetMaxOpenConns(racers * 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, racers)

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf("written by racer %d\n", i)
			<-start
			_, errs[i] = f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{
				Body:          &body,
				BaseVersion:   &base,
				UpdatedBy:     uuid.New(),
				UpdatedByType: domain.ActorTypeAgent,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			continue
		}
		var conflict *DocumentVersionConflictError
		require.ErrorAs(t, err, &conflict,
			"racer %d lost with something other than a version conflict: %v", i, err)
	}
	assert.Equal(t, 1, winners,
		"%d of %d writers built on version %d were allowed to write; exactly 1 may win",
		winners, racers, base)

	// The stored body must be the winner's, whole — not a mix, and not a loser's.
	body := f.storedBody(doc)
	assert.Regexp(t, `^written by racer \d+\n$`, body,
		"the body is not exactly one writer's text: %q", body)

	assert.Equal(t, base+1, f.storedVersion(t, doc),
		"only the winning write moved the counter; a loser that bumped it would push every "+
			"other reader into a conflict over a write that never happened")
}

// The migration has to be safe to apply ahead of the code, which means rows
// written before the column existed have to read as something usable. This
// inserts a row the way code that predates the column does — naming every
// column except version — and reads the default back.
func TestDocumentVersionDB_PreexistingRowsDefaultToOne(t *testing.T) {
	db := documentVersionTestDB(t)
	f := newDocumentDBFixture(t, db)

	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO documents (id, project_id, slug, title, storage_key, position, created_by, created_by_type)
		 VALUES ($1, $2, $3, $4, $5, 0, $6, 'user')`,
		id, f.projectID, "legacy-"+id.String()[:8], "Legacy", "documents/legacy/"+id.String()+".md", uuid.New(),
	)
	require.NoError(t, err)

	var version int64
	require.NoError(t, db.Get(&version, `SELECT version FROM documents WHERE id = $1`, id))
	assert.Equal(t, int64(1), version,
		"a row written without a version must read as 1, or every pre-migration document "+
			"is unwritable until somebody back-fills it")
}

// A conflict is refused before anything is uploaded, not after. Asserting on the
// stored object rather than on the error is the point: a 409 that wrote anyway
// is indistinguishable from a working guard by status code, and it is the exact
// bug this feature exists to remove.
func TestDocumentVersionDB_StaleWriteTouchesNeitherStorageNorVersion(t *testing.T) {
	db := documentVersionTestDB(t)
	f := newDocumentDBFixture(t, db)
	doc := f.create(t, "Runbook", "original\n")
	base := doc.Version

	first := "first writer\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{
		Body: &first, BaseVersion: &base, UpdatedBy: uuid.New(), UpdatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	second := "second writer\n"
	_, err = f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{
		Body: &second, BaseVersion: &base, UpdatedBy: uuid.New(), UpdatedByType: domain.ActorTypeAgent,
	})

	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, base+1, conflict.CurrentVersion)

	assert.Equal(t, first, f.storedBody(doc), "the refused write reached object storage")
	assert.Equal(t, base+1, f.storedVersion(t, doc), "the refused write bumped the counter")
}
