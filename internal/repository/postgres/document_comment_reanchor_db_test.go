package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// The re-anchoring pass reads and writes through two statements a sqlmock test
// can only prove we SENT. Both are hand-written and neither is exercised
// anywhere else: ListAnchorsByDocument's filter decides which comments get
// re-anchored at all, and UpdateAnchorPositions is an UPDATE ... FROM (VALUES)
// with explicit casts and a quoted "end" column that either binds or does not.
//
// It matters more here than it usually would, because the pass is best-effort:
// a statement that failed to parse would surface as one log line on a path
// nobody watches, and the symptom would be indistinguishable from the defect
// this whole change removes.
//
// No build tag — same convention as comment_cursor_tiebreak_db_test.go: skip
// when no Postgres is reachable rather than needing a separate CI invocation.

func reanchorTestDB(t *testing.T) *sqlx.DB {
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

// reanchorFixture is a workspace + project + document to hang comments on.
type reanchorFixture struct {
	documentID uuid.UUID
	repo       *DocumentCommentRepo
}

func newReanchorFixture(t *testing.T, db *sqlx.DB) reanchorFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{ID: uuid.New(), Name: "reanchor-ws", Slug: "reanchor-ws-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))
	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "reanchor-proj",
		Slug: "reanchor-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	docID := uuid.New()
	_, err := db.ExecContext(ctx, `
		INSERT INTO documents (id, project_id, title, slug, storage_key, created_by, created_by_type)
		VALUES ($1, $2, 'Reanchor', $3, $4, $5, 'agent')`,
		docID, proj.ID, "reanchor-"+suffix, "documents/"+proj.ID.String()+"/"+docID.String()+".md", uuid.New())
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM document_comments WHERE document_id = $1`, docID)
		_, _ = db.Exec(`DELETE FROM documents WHERE id = $1`, docID)
		_, _ = db.Exec(`DELETE FROM projects WHERE id = $1`, proj.ID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, ws.ID)
	})
	return reanchorFixture{documentID: docID, repo: NewDocumentCommentRepo(db)}
}

func (f reanchorFixture) seed(t *testing.T, exact string, start, end *int, at time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	c := &domain.DocumentComment{
		ID: id, DocumentID: f.documentID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "комментарий", CreatedAt: at, UpdatedAt: at,
	}
	if exact != "" {
		c.Anchor = domain.NewDocumentCommentAnchor(exact, "before", "after", start, end)
	}
	require.NoError(t, f.repo.Create(context.Background(), c))
	return id
}

func intp(v int) *int { return &v }

func TestDocumentCommentRepo_ListAnchorsByDocument_TakesEveryLiveQuote(t *testing.T) {
	db := reanchorTestDB(t)
	f := newReanchorFixture(t, db)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)

	anchored := f.seed(t, "первая цитата", intp(10), intp(20), base)
	orphaned := f.seed(t, "вторая цитата", nil, nil, base.Add(time.Second))
	// Resolved: resolving a conversation says nothing about whether its offsets
	// still describe the text, so it must be re-anchored like any other.
	resolved := f.seed(t, "третья цитата", intp(30), intp(40), base.Add(2*time.Second))
	at, by, byType := time.Now(), uuid.New(), domain.ActorTypeUser
	rc, err := f.repo.GetByID(ctx, resolved)
	require.NoError(t, err)
	rc.ResolvedAt, rc.ResolvedBy, rc.ResolvedByType = &at, &by, &byType
	require.NoError(t, f.repo.Update(ctx, rc))

	// Excluded: no quote at all (a comment on the whole document, or a reply).
	f.seed(t, "", nil, nil, base.Add(3*time.Second))
	// Excluded: soft-deleted.
	deleted := f.seed(t, "удалённая цитата", intp(50), intp(60), base.Add(4*time.Second))
	require.NoError(t, f.repo.SoftDelete(ctx, deleted, time.Now()))

	rows, err := f.repo.ListAnchorsByDocument(ctx, f.documentID)
	require.NoError(t, err)

	got := make(map[uuid.UUID]repository.DocumentCommentAnchorRow, len(rows))
	for _, r := range rows {
		got[r.ID] = r
	}
	assert.Len(t, rows, 3, "anchored + orphaned + resolved; the quoteless and the deleted are not anchors")
	assert.Contains(t, got, anchored)
	assert.Contains(t, got, orphaned, "an orphan keeps its quote, so an edit restoring the text re-adopts it")
	assert.Contains(t, got, resolved, "a resolved thread's offsets can go stale exactly like anyone else's")

	assert.Equal(t, "первая цитата", got[anchored].Exact)
	assert.Equal(t, intp(10), got[anchored].Start)
	assert.Nil(t, got[orphaned].Start, "orphaned is a NULL position, and it survives the round trip")
}

func TestDocumentCommentRepo_UpdateAnchorPositions_MovesAndOrphansInOneStatement(t *testing.T) {
	db := reanchorTestDB(t)
	f := newReanchorFixture(t, db)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)

	moving := f.seed(t, "поехавшая цитата", intp(10), intp(20), base)
	losing := f.seed(t, "исчезнувшая цитата", intp(30), intp(40), base.Add(time.Second))

	require.NoError(t, f.repo.UpdateAnchorPositions(ctx, []repository.DocumentCommentAnchorPosition{
		{ID: moving, Prefix: "новое слева", Suffix: "новое справа", Start: intp(100), End: intp(120)},
		{ID: losing, Prefix: "before", Suffix: "after"}, // nil offsets == orphaned
	}))

	got, err := f.repo.GetByID(ctx, moving)
	require.NoError(t, err)
	require.NotNil(t, got.Anchor)
	assert.False(t, got.Anchor.IsOrphaned())
	assert.Equal(t, intp(100), got.Anchor.Start)
	assert.Equal(t, intp(120), got.Anchor.End)
	assert.Equal(t, "новое слева", got.Anchor.Prefix, "the neighbourhood is retaken from the new body")
	assert.Equal(t, "поехавшая цитата", got.Anchor.Exact, "the quote is never rewritten — it is what the comment is ABOUT")

	got, err = f.repo.GetByID(ctx, losing)
	require.NoError(t, err)
	require.NotNil(t, got.Anchor)
	assert.True(t, got.Anchor.IsOrphaned(), "nulling the position IS the orphaning in this schema")
	assert.Nil(t, got.Anchor.End, "chk_document_comments_anchor_position: both or neither")
	assert.Equal(t, "исчезнувшая цитата", got.Anchor.Exact)
}

func TestDocumentCommentRepo_UpdateAnchorPositions_LeavesOtherDocumentsAlone(t *testing.T) {
	// The VALUES list names ids, but the statement is the only thing standing
	// between one document's re-anchor and every other document's anchors.
	db := reanchorTestDB(t)
	edited := newReanchorFixture(t, db)
	bystander := newReanchorFixture(t, db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	target := edited.seed(t, "цитата", intp(10), intp(20), now)
	untouched := bystander.seed(t, "цитата", intp(10), intp(20), now)

	require.NoError(t, edited.repo.UpdateAnchorPositions(ctx, []repository.DocumentCommentAnchorPosition{
		{ID: target, Start: intp(999), End: intp(1010)},
	}))

	other, err := bystander.repo.GetByID(ctx, untouched)
	require.NoError(t, err)
	assert.Equal(t, intp(10), other.Anchor.Start, "a re-anchor on one document must not reach another")
}

func TestDocumentCommentRepo_UpdateAnchorPositions_EmptyIsANoOp(t *testing.T) {
	// A document with no anchored comments is the common case on every autosave.
	// It must cost nothing and must not send `... FROM (VALUES ) ...`, which is
	// a syntax error rather than a no-op.
	db := reanchorTestDB(t)
	f := newReanchorFixture(t, db)
	require.NoError(t, f.repo.UpdateAnchorPositions(context.Background(), nil))
}
