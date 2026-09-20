package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type fakeDocChunks struct {
	repository.DocumentChunkRepository
	stale []domain.Document
}

func (f *fakeDocChunks) PurgeDeleted(context.Context) (int64, error) { return 0, nil }
func (f *fakeDocChunks) ListStale(context.Context, uuid.UUID, *uuid.UUID, int) ([]domain.Document, error) {
	return f.stale, nil
}
func (f *fakeDocChunks) Status(context.Context, uuid.UUID, *uuid.UUID) (domain.DocIndexStatus, error) {
	return domain.DocIndexStatus{LiveDocs: 3, IndexedDocs: 2}, nil
}
func (f *fakeDocChunks) ReplaceChunks(context.Context, uuid.UUID, uuid.UUID, int, []domain.DocumentChunk) error {
	return nil
}

type fakeDocStore struct{ service.DocumentStore }

func (fakeDocStore) Download(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("## A\ntext")), nil
}

func docIndexCall(t *testing.T, h *MemoryHandler, fn func(echo.Context) error, method, query string, ws uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, "/api/v1/memories/x"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ws)
	require.NoError(t, fn(c))
	return rec
}

func TestDocIndex_NotConfiguredIs503(t *testing.T) {
	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})
	ws := uuid.New()
	assert.Equal(t, http.StatusServiceUnavailable, docIndexCall(t, h, h.BackfillDocIndex, http.MethodPost, "", ws).Code)
	assert.Equal(t, http.StatusServiceUnavailable, docIndexCall(t, h, h.DocIndexStatus, http.MethodGet, "", ws).Code)
}

func TestDocIndex_ForeignWorkspaceRejected(t *testing.T) {
	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})
	h.SetDocumentIndexer(service.NewDocumentIndexer(&fakeDocChunks{}, nil, fakeDocStore{}, true))
	q := "?workspace_id=" + uuid.New().String()
	assert.Equal(t, http.StatusForbidden, docIndexCall(t, h, h.DocIndexStatus, http.MethodGet, q, uuid.New()).Code)
	assert.Equal(t, http.StatusForbidden, docIndexCall(t, h, h.BackfillDocIndex, http.MethodPost, q, uuid.New()).Code)
}

func TestDocIndex_BadProjectIDIs400(t *testing.T) {
	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})
	h.SetDocumentIndexer(service.NewDocumentIndexer(&fakeDocChunks{}, nil, fakeDocStore{}, true))
	assert.Equal(t, http.StatusBadRequest, docIndexCall(t, h, h.DocIndexStatus, http.MethodGet, "?project_id=nope", uuid.New()).Code)
}

func TestDocIndex_StatusReturnsCounts(t *testing.T) {
	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})
	h.SetDocumentIndexer(service.NewDocumentIndexer(&fakeDocChunks{}, nil, fakeDocStore{}, false))
	rec := docIndexCall(t, h, h.DocIndexStatus, http.MethodGet, "?project_id="+uuid.New().String(), uuid.New())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"live_docs":3,"indexed_docs":2}`, rec.Body.String())
}

func TestDocIndex_BackfillRefusedWhileWriteFlagOff(t *testing.T) {
	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})
	h.SetDocumentIndexer(service.NewDocumentIndexer(&fakeDocChunks{}, nil, fakeDocStore{}, false))
	assert.Equal(t, http.StatusConflict, docIndexCall(t, h, h.BackfillDocIndex, http.MethodPost, "", uuid.New()).Code)
}

func TestDocIndex_BackfillIndexesStaleDocuments(t *testing.T) {
	doc := domain.Document{ID: uuid.New(), ProjectID: uuid.New(), Title: "T", StorageKey: "k", Version: 1}
	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})
	h.SetDocumentIndexer(service.NewDocumentIndexer(&fakeDocChunks{stale: []domain.Document{doc}}, nil, fakeDocStore{}, true))
	rec := docIndexCall(t, h, h.BackfillDocIndex, http.MethodPost, "?limit=abc", uuid.New())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"indexed":1`)
	assert.Contains(t, rec.Body.String(), `"failed":0`)
}
