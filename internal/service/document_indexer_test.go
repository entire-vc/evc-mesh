package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

func TestSplitDocument_ByH2(t *testing.T) {
	body := "intro line\n\n## Alpha\nalpha text\n\n### sub\nstays in alpha\n\n## Beta\nbeta text\n"
	got := splitDocument("Doc Title", body)
	require.Len(t, got, 3)
	assert.Equal(t, "Doc Title", got[0].heading)
	assert.Contains(t, got[0].text, "intro line")
	assert.Equal(t, "Alpha", got[1].heading)
	assert.Contains(t, got[1].text, "stays in alpha", "an H3 belongs to its H2 section")
	assert.Equal(t, "Beta", got[2].heading)
}

func TestSplitDocument_H2InsideCodeFenceIsNotASplit(t *testing.T) {
	body := "## Real\ntext\n```\n## not a heading\n```\n"
	got := splitDocument("T", body)
	require.Len(t, got, 1)
	assert.Equal(t, "Real", got[0].heading)
}

func TestSplitDocument_EmptyBodyStillYieldsTitleChunk(t *testing.T) {
	got := splitDocument("Only Title", "  \n")
	require.Len(t, got, 1, "every live document must be represented, or backfill completeness can never hold")
	assert.Equal(t, "Only Title", got[0].text)
}

func TestSplitDocument_LongSectionSplitAndCapped(t *testing.T) {
	long := "## Big\n" + strings.Repeat("слово ", 20000)
	got := splitDocument("T", long)
	assert.Greater(t, len(got), 1)
	assert.LessOrEqual(t, len(got), maxDocChunks)
	for _, p := range got {
		assert.Equal(t, "Big", p.heading)
	}
}

type fakeDocChunkRepo struct {
	repository.DocumentChunkRepository
	ftsCalls int
	hits     []domain.ScoredMemory
}

func (f *fakeDocChunkRepo) FullTextSearch(context.Context, uuid.UUID, *uuid.UUID, string, int) ([]domain.ScoredMemory, error) {
	f.ftsCalls++
	return f.hits, nil
}

func docHit(slug string, score float64) domain.ScoredMemory {
	id := uuid.New()
	return domain.ScoredMemory{Memory: domain.Memory{
		ID: uuid.New(), Key: "doc/" + slug, Content: "doc text", SourceType: domain.SourceDoc,
		ImportanceScore: 0.5, Scope: domain.ScopeWorkspace, Status: domain.MemoryStatusActive, SourceDocID: &id, DocSlug: slug,
	}, Score: score}
}

func recallWithDocs(t *testing.T, recall bool, docs *fakeDocChunkRepo, mems []domain.ScoredMemory, limit int) []domain.ScoredMemory {
	t.Helper()
	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(context.Context, uuid.UUID, *uuid.UUID, string, domain.MemorySearchFilter, int) ([]domain.ScoredMemory, error) {
			return mems, nil
		},
	}
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, nil, MemoryWithDocIndex(docs, recall))
	got, _, err := svc.Recall(context.Background(), domain.RecallOpts{Query: "audit", WorkspaceID: uuid.New(), Limit: limit})
	require.NoError(t, err)
	return got
}

func TestRecall_DocArm_OffByDefault_NeverTouchesDocRepo(t *testing.T) {
	docs := &fakeDocChunkRepo{hits: []domain.ScoredMemory{docHit("audit", 1)}}
	mems := []domain.ScoredMemory{{Memory: domain.Memory{ID: uuid.New(), Key: "m", Content: "c", ImportanceScore: 0.8}, Score: 1}}
	got := recallWithDocs(t, false, docs, mems, 10)
	assert.Zero(t, docs.ftsCalls, "flag off → the doc arm must not run at all")
	require.Len(t, got, 1)
	assert.NotEqual(t, domain.SourceDoc, got[0].SourceType)
}

func TestRecall_DocArm_OnReturnsDocsWithSourceMarker(t *testing.T) {
	docs := &fakeDocChunkRepo{hits: []domain.ScoredMemory{docHit("audit", 1)}}
	got := recallWithDocs(t, true, docs, nil, 10)
	require.Len(t, got, 1)
	assert.Equal(t, domain.SourceDoc, got[0].SourceType)
	assert.Equal(t, "audit", got[0].DocSlug)
}

func TestRecall_DocArm_ShareIsCapped(t *testing.T) {
	var hits []domain.ScoredMemory
	for i := 0; i < 20; i++ {
		hits = append(hits, docHit("d", float64(20-i)))
	}
	got := recallWithDocs(t, true, &fakeDocChunkRepo{hits: hits}, nil, 10)
	assert.Len(t, got, 4, "docs may fill at most 40%% of the requested page")
}

// --- indexer behaviour (fake repo/embedder/store) ---------------------------------

type recordingDocRepo struct {
	repository.DocumentChunkRepository
	mu         sync.Mutex
	replaced   []domain.DocumentChunk
	replacedID uuid.UUID
	deleted    []uuid.UUID
	stale      []domain.Document
	replaceErr error
	listErr    error
}

func (r *recordingDocRepo) ReplaceChunks(_ context.Context, id, _ uuid.UUID, _ int, c []domain.DocumentChunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replacedID, r.replaced = id, c
	return r.replaceErr
}
func (r *recordingDocRepo) DeleteByDocument(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, id)
	return nil
}
func (r *recordingDocRepo) PurgeDeleted(context.Context) (int64, error) { return 2, nil }
func (r *recordingDocRepo) ListStale(context.Context, uuid.UUID, *uuid.UUID, int) ([]domain.Document, error) {
	return r.stale, r.listErr
}
func (r *recordingDocRepo) Status(context.Context, uuid.UUID, *uuid.UUID) (domain.DocIndexStatus, error) {
	return domain.DocIndexStatus{LiveDocs: 1, IndexedDocs: 1}, nil
}

type fixedEmbedder struct{ fail bool }

func (f fixedEmbedder) Embed(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }
func (f fixedEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	if f.fail {
		return nil, errors.New("down")
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}
func (fixedEmbedder) Model() string   { return "m" }
func (fixedEmbedder) Dimensions() int { return 2 }

type mapStore struct {
	DocumentStore
	body string
	err  error
}

func (m mapStore) Download(context.Context, string) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(strings.NewReader(m.body)), nil
}

func TestIndexer_IndexStoresVectorsWhenEmbedderUp(t *testing.T) {
	repo := &recordingDocRepo{}
	x := NewDocumentIndexer(repo, fixedEmbedder{}, nil, true)
	doc := &domain.Document{ID: uuid.New(), ProjectID: uuid.New(), Title: "T", Version: 3}
	require.NoError(t, x.Index(context.Background(), doc, "## A\none\n## B\ntwo"))
	require.Len(t, repo.replaced, 2)
	for _, c := range repo.replaced {
		require.NotNil(t, c.Embedding)
		assert.Equal(t, "m", *c.EmbeddingModel)
	}
}

func TestIndexer_EmbedderDownStillIndexesBM25Only(t *testing.T) {
	repo := &recordingDocRepo{}
	x := NewDocumentIndexer(repo, fixedEmbedder{fail: true}, nil, true)
	doc := &domain.Document{ID: uuid.New(), Title: "T"}
	require.NoError(t, x.Index(context.Background(), doc, "## A\none"))
	require.NotEmpty(t, repo.replaced)
	assert.Nil(t, repo.replaced[0].Embedding, "no vector, but the chunk must still be written for BM25")
}

func TestIndexer_NilAndOffAreInert(t *testing.T) {
	var nilIdx *DocumentIndexer
	assert.False(t, nilIdx.WriteEnabled())
	nilIdx.IndexAsync(context.Background(), &domain.Document{}, "x")
	nilIdx.Remove(context.Background(), uuid.New())

	repo := &recordingDocRepo{}
	off := NewDocumentIndexer(repo, nil, nil, false)
	off.IndexAsync(context.Background(), &domain.Document{ID: uuid.New()}, "x")
	off.Remove(context.Background(), uuid.New())
	assert.Empty(t, repo.deleted, "write flag off: no writes at all")
	assert.Empty(t, repo.replaced)
}

func TestIndexer_RemoveDeletesChunks(t *testing.T) {
	repo := &recordingDocRepo{}
	x := NewDocumentIndexer(repo, nil, nil, true)
	id := uuid.New()
	x.Remove(context.Background(), id)
	assert.Equal(t, []uuid.UUID{id}, repo.deleted)
}

func TestIndexer_IndexAsyncWritesInBackground(t *testing.T) {
	repo := &recordingDocRepo{}
	x := NewDocumentIndexer(repo, nil, nil, true)
	x.IndexAsync(context.Background(), &domain.Document{ID: uuid.New(), Title: "T"}, "## A\nx")
	require.Eventually(t, func() bool { repo.mu.Lock(); defer repo.mu.Unlock(); return len(repo.replaced) > 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestIndexer_Backfill(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()
	doc := domain.Document{ID: uuid.New(), ProjectID: uuid.New(), Title: "T", StorageKey: "k", Version: 1}

	_, err := NewDocumentIndexer(&recordingDocRepo{}, nil, mapStore{}, false).Backfill(ctx, ws, nil, 10)
	require.Error(t, err, "refused while DOC_INDEX_WRITE is off")

	_, err = NewDocumentIndexer(&recordingDocRepo{}, nil, nil, true).Backfill(ctx, ws, nil, 10)
	require.Error(t, err, "no storage configured")

	res, err := NewDocumentIndexer(&recordingDocRepo{stale: []domain.Document{doc}}, nil, mapStore{body: "## A\nx"}, true).Backfill(ctx, ws, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Indexed)
	assert.Equal(t, int64(2), res.Purged)
	assert.Equal(t, 1, res.Status.IndexedDocs)

	res, err = NewDocumentIndexer(&recordingDocRepo{stale: []domain.Document{doc}}, nil, mapStore{err: errors.New("s3")}, true).Backfill(ctx, ws, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Failed, "a body that cannot be read is counted, not skipped silently")

	res, err = NewDocumentIndexer(&recordingDocRepo{stale: []domain.Document{doc}, replaceErr: errors.New("db")}, nil, mapStore{body: "x"}, true).Backfill(ctx, ws, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Failed)

	_, err = NewDocumentIndexer(&recordingDocRepo{listErr: errors.New("db")}, nil, mapStore{}, true).Backfill(ctx, ws, nil, 10)
	require.Error(t, err)

	st, err := NewDocumentIndexer(&recordingDocRepo{}, nil, nil, false).Status(ctx, ws, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, st.LiveDocs)
}
