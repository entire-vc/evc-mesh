package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The real-DB tests (document_chunk_repo_db_test.go) prove the SQL against Postgres
// where one is reachable; these pin the Go-side logic (transaction shape, version
// guard, ranking, error paths) without one, so the diff-coverage gate sees them.

func newDocChunkMock(t *testing.T) (*DocumentChunkRepo, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return NewDocumentChunkRepo(sqlx.NewDb(raw, "postgres")), mock
}

func TestDocChunkRepo_ReplaceChunks_WritesInOneTransaction(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	doc, proj := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("max\\(doc_version\\)").WithArgs(doc).WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(1))
	mock.ExpectExec("DELETE FROM document_chunks").WithArgs(doc).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO document_chunks").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO document_chunks").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.ReplaceChunks(context.Background(), doc, proj, 2, []domain.DocumentChunk{
		{ChunkIdx: 0, Content: "a"}, {ChunkIdx: 1, Content: "b"},
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDocChunkRepo_ReplaceChunks_OlderVersionIsNoOp(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	doc := uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("max\\(doc_version\\)").WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(5))
	mock.ExpectRollback() // no DELETE, no INSERT: a stale async write must not clobber a newer set
	require.NoError(t, repo.ReplaceChunks(context.Background(), doc, uuid.New(), 3, []domain.DocumentChunk{{Content: "old"}}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDocChunkRepo_ReplaceChunks_ErrorPaths(t *testing.T) {
	steps := []struct {
		name  string
		build func(sqlmock.Sqlmock)
		want  string
	}{
		{"begin", func(m sqlmock.Sqlmock) { m.ExpectBegin().WillReturnError(errors.New("x")) }, "begin"},
		{"lock", func(m sqlmock.Sqlmock) {
			m.ExpectBegin()
			m.ExpectExec("pg_advisory_xact_lock").WillReturnError(errors.New("x"))
			m.ExpectRollback()
		}, "lock"},
		{"read version", func(m sqlmock.Sqlmock) {
			m.ExpectBegin()
			m.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
			m.ExpectQuery("max").WillReturnError(errors.New("x"))
			m.ExpectRollback()
		}, "read version"},
		{"delete", func(m sqlmock.Sqlmock) {
			m.ExpectBegin()
			m.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
			m.ExpectQuery("max").WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(0))
			m.ExpectExec("DELETE").WillReturnError(errors.New("x"))
			m.ExpectRollback()
		}, "delete"},
		{"insert", func(m sqlmock.Sqlmock) {
			m.ExpectBegin()
			m.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 0))
			m.ExpectQuery("max").WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(0))
			m.ExpectExec("DELETE").WillReturnResult(sqlmock.NewResult(0, 0))
			m.ExpectExec("INSERT").WillReturnError(errors.New("x"))
			m.ExpectRollback()
		}, "insert"},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			repo, mock := newDocChunkMock(t)
			s.build(mock)
			err := repo.ReplaceChunks(context.Background(), uuid.New(), uuid.New(), 1, []domain.DocumentChunk{{Content: "a"}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), s.want)
		})
	}
}

func TestDocChunkRepo_DeleteAndPurge(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	id := uuid.New()
	mock.ExpectExec("DELETE FROM document_chunks WHERE document_id").WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 2))
	require.NoError(t, repo.DeleteByDocument(context.Background(), id))
	mock.ExpectExec("DELETE FROM document_chunks c USING documents").WillReturnResult(sqlmock.NewResult(0, 4))
	n, err := repo.PurgeDeleted(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(4), n)
	mock.ExpectExec("DELETE FROM document_chunks c USING").WillReturnError(errors.New("x"))
	_, err = repo.PurgeDeleted(context.Background())
	require.Error(t, err)
}

var hitCols = []string{"id", "document_id", "project_id", "slug", "title", "heading", "content", "doc_version", "updated_at", "score"}

func TestDocChunkRepo_FullTextSearch_ShapesHitsAndFallsBackToOR(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	ws, proj := uuid.New(), uuid.New()
	cid, did := uuid.New(), uuid.New()
	now := time.Now()
	// AND query returns < minFTSHits rows, so the OR relaxation must run and replace it.
	mock.ExpectQuery("plainto_tsquery\\('english', \\$2\\)").WillReturnRows(sqlmock.NewRows(hitCols))
	mock.ExpectQuery("regexp_replace").WillReturnRows(sqlmock.NewRows(hitCols).
		AddRow(cid, did, proj, "audit", "Audit", "3.5 Gate", "text", 1, now, 0.4).
		AddRow(uuid.New(), did, proj, "audit", "Audit", "", "more", 1, now, 0.2))
	got, err := repo.FullTextSearch(context.Background(), ws, &proj, "fleet gate", domain.DocViewer{AllProjects: true}, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, domain.SourceDoc, got[0].SourceType)
	assert.Equal(t, "doc/audit#3.5 Gate", got[0].Key)
	assert.Equal(t, "doc/audit", got[1].Key, "no heading → no fragment")
	assert.Equal(t, did, *got[0].SourceDocID)
	assert.Equal(t, ws, got[0].WorkspaceID)
	assert.InDelta(t, 0.4, got[0].Score, 1e-9)
}

func TestDocChunkRepo_FullTextSearch_Error(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	mock.ExpectQuery("plainto_tsquery").WillReturnError(errors.New("boom"))
	_, err := repo.FullTextSearch(context.Background(), uuid.New(), nil, "q", domain.DocViewer{AllProjects: true}, 5)
	require.Error(t, err)
}

func TestDocChunkRepo_VectorSearch_RanksByCosineAndCapsToLimit(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	ws, proj := uuid.New(), uuid.New()
	near, far, badDim := uuid.New(), uuid.New(), uuid.New()
	doc := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT c.id, c.embedding").WillReturnRows(sqlmock.NewRows([]string{"id", "embedding"}).
		AddRow(far, domain.EncodeEmbedding([]float32{0, 1})).
		AddRow(near, domain.EncodeEmbedding([]float32{1, 0})).
		AddRow(badDim, domain.EncodeEmbedding([]float32{1, 0, 0})).
		AddRow(uuid.New(), "not-base64!!"))
	mock.ExpectQuery("FROM document_chunks c").WillReturnRows(sqlmock.NewRows(hitCols).
		AddRow(near, doc, proj, "s", "T", "H", "c1", 1, now, 0).
		AddRow(far, doc, proj, "s", "T", "H2", "c2", 1, now, 0))
	got, err := repo.VectorSearch(context.Background(), []float32{1, 0}, ws, &proj, domain.DocViewer{AllProjects: true}, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, near, got[0].ID, "the parallel vector must rank first")
	assert.Greater(t, got[0].Score, got[1].Score)
}

func TestDocChunkRepo_VectorSearch_EmptyAndErrors(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	mock.ExpectQuery("SELECT c.id, c.embedding").WillReturnRows(sqlmock.NewRows([]string{"id", "embedding"}))
	got, err := repo.VectorSearch(context.Background(), []float32{1}, uuid.New(), nil, domain.DocViewer{AllProjects: true}, 0)
	require.NoError(t, err)
	assert.Empty(t, got)

	mock.ExpectQuery("SELECT c.id, c.embedding").WillReturnError(errors.New("x"))
	_, err = repo.VectorSearch(context.Background(), []float32{1}, uuid.New(), nil, domain.DocViewer{AllProjects: true}, 3)
	require.Error(t, err)

	mock.ExpectQuery("SELECT c.id, c.embedding").WillReturnRows(sqlmock.NewRows([]string{"id", "embedding"}).
		AddRow(uuid.New(), domain.EncodeEmbedding([]float32{1})))
	mock.ExpectQuery("FROM document_chunks c").WillReturnError(errors.New("hydrate"))
	_, err = repo.VectorSearch(context.Background(), []float32{1}, uuid.New(), nil, domain.DocViewer{AllProjects: true}, 3)
	require.Error(t, err)
}

func TestDocChunkRepo_ListStaleAndStatus(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	ws, proj := uuid.New(), uuid.New()
	mock.ExpectQuery("NOT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "slug", "title", "storage_key", "version"}).
		AddRow(uuid.New(), proj, "s", "T", "k", 1))
	docs, err := repo.ListStale(context.Background(), ws, &proj, 0)
	require.NoError(t, err)
	assert.Len(t, docs, 1)

	mock.ExpectQuery("NOT EXISTS").WillReturnError(errors.New("x"))
	_, err = repo.ListStale(context.Background(), ws, nil, 5)
	require.Error(t, err)

	mock.ExpectQuery("AS excluded_docs").WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c"}).AddRow(201, 197, 259))
	st, err := repo.Status(context.Background(), ws, &proj)
	require.NoError(t, err)
	assert.Equal(t, domain.DocIndexStatus{LiveDocs: 201, IndexedDocs: 197, ExcludedDocs: 259}, st)
}

func TestDocChunkRepo_ViewerCondition(t *testing.T) {
	agent, user := uuid.New(), uuid.New()

	sql, args := docViewerCond(domain.DocViewer{AllProjects: true}, nil)
	assert.Empty(t, sql, "an owner/admin is not filtered by membership")
	assert.Empty(t, args)

	sql, args = docViewerCond(domain.DocViewer{}, nil)
	assert.Equal(t, "FALSE", sql, "no identity sees nothing")
	assert.Empty(t, args)

	sql, args = docViewerCond(domain.DocViewer{AgentID: &agent}, []interface{}{"ws"})
	assert.Contains(t, sql, "pm.agent_id = $2")
	assert.Contains(t, sql, "project_members")
	assert.Equal(t, []interface{}{"ws", agent}, args)

	sql, args = docViewerCond(domain.DocViewer{UserID: &user, AgentID: &agent}, nil)
	assert.Contains(t, sql, "pm.agent_id = $1 OR pm.user_id = $2")
	assert.Len(t, args, 2)
}

func TestDocChunkRepo_ProjectIndexable(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	id := uuid.New()
	mock.ExpectQuery("FROM projects p WHERE p.id").WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"ok"}).AddRow(true))
	ok, err := repo.ProjectIndexable(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, ok)

	mock.ExpectQuery("FROM projects p WHERE p.id").WillReturnRows(sqlmock.NewRows([]string{"ok"}))
	ok, err = repo.ProjectIndexable(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, ok, "an unknown project has nothing to index into")

	mock.ExpectQuery("FROM projects p WHERE p.id").WillReturnError(errors.New("x"))
	_, err = repo.ProjectIndexable(context.Background(), id)
	require.Error(t, err)
}

func TestDocChunkRepo_SearchAppliesViewerPredicateInSQL(t *testing.T) {
	repo, mock := newDocChunkMock(t)
	agent := uuid.New()
	// The membership test must be part of the arm's own query, not a filter applied
	// after truncation.
	mock.ExpectQuery("EXISTS \\(SELECT 1 FROM project_members pm").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), agent, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows(hitCols))
	mock.ExpectQuery("EXISTS \\(SELECT 1 FROM project_members pm").WillReturnRows(sqlmock.NewRows(hitCols))
	_, err := repo.FullTextSearch(context.Background(), uuid.New(), nil, "q", domain.DocViewer{AgentID: &agent}, 5)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
