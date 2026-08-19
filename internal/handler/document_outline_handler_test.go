package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/markdown"
)

// docSubRequest builds a request against /documents/:doc_id/<sub> with a query
// string, which is how the new routes are addressed.
func docSubRequest(e *echo.Echo, sub, docID string, wsID *uuid.UUID, query string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/?"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/documents/:doc_id/" + sub)
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

func TestDocumentHandler_TOC(t *testing.T) {
	docID, wsID := uuid.New(), uuid.New()
	var gotDoc, gotWS uuid.UUID

	mockSvc := &MockDocumentService{
		TOCFunc: func(_ context.Context, id, workspaceID uuid.UUID) (*service.DocumentTOC, error) {
			gotDoc, gotWS = id, workspaceID
			return &service.DocumentTOC{
				DocumentID: id,
				Title:      "Раннбук",
				Headings: []markdown.Heading{
					{Level: 1, Text: "Раннбук", Anchor: "раннбук", Path: []string{"раннбук"}},
					{Level: 2, Text: "Установка", Anchor: "установка", Path: []string{"раннбук", "установка"}},
				},
			}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, "toc", docID.String(), &wsID, "")
	require.NoError(t, h.TOC(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, docID, gotDoc)
	assert.Equal(t, wsID, gotWS, "the caller's workspace narrows the lookup")

	var body service.DocumentTOC
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Headings, 2)
	assert.Equal(t, "установка", body.Headings[1].Anchor,
		"the Cyrillic anchor survives JSON serialisation")
	assert.Equal(t, []string{"раннбук", "установка"}, body.Headings[1].Path)
}

func TestDocumentHandler_TOC_InvalidIDAndMissingWorkspace(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})
	wsID := uuid.New()

	c, rec := docSubRequest(e, "toc", "not-a-uuid", &wsID, "")
	require.NoError(t, h.TOC(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	c, rec = docSubRequest(e, "toc", uuid.New().String(), nil, "")
	require.NoError(t, h.TOC(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentHandler_Section(t *testing.T) {
	docID, wsID := uuid.New(), uuid.New()
	var gotRef string

	mockSvc := &MockDocumentService{
		SectionFunc: func(_ context.Context, id, _ uuid.UUID, ref string) (*service.DocumentSection, error) {
			gotRef = ref
			return &service.DocumentSection{
				DocumentID: id,
				Title:      "Раннбук",
				Heading:    markdown.Heading{Level: 3, Text: "Linux", Anchor: "linux-1"},
				Content:    "### Linux\n\nСтавим через apt.\n",
			}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, "section", docID.String(), &wsID, "ref=%D1%83%D1%81%D1%82%D0%B0%D0%BD%D0%BE%D0%B2%D0%BA%D0%B0%2Flinux")
	require.NoError(t, h.Section(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "установка/linux", gotRef,
		"a percent-encoded Cyrillic path reaches the service intact")

	var body service.DocumentSection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body.Content, "Ставим через apt.")
	assert.Equal(t, "linux-1", body.Heading.Anchor)
}

// An ambiguous reference must come back as a 400 carrying the references that
// resolve it — the handler is the layer a caller actually sees, so the guidance
// has to survive to here rather than stopping at the service.
func TestDocumentHandler_Section_AmbiguousRefIsA400WithGuidance(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentService{
		SectionFunc: func(context.Context, uuid.UUID, uuid.UUID, string) (*service.DocumentSection, error) {
			return nil, apierror.BadRequestWithDetails(
				`section reference "linux" matches 2 headings`,
				"address one of them directly: linux-1, linux-2")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, "section", uuid.New().String(), &wsID, "ref=linux")
	require.NoError(t, h.Section(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "linux-1")
	assert.Contains(t, rec.Body.String(), "linux-2")
}

func TestDocumentHandler_Section_MissingRef(t *testing.T) {
	wsID := uuid.New()
	called := false
	mockSvc := &MockDocumentService{
		SectionFunc: func(_ context.Context, _, _ uuid.UUID, ref string) (*service.DocumentSection, error) {
			called = true
			assert.Empty(t, ref)
			return nil, apierror.ValidationError(map[string]string{"ref": "a section reference is required"})
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, "section", uuid.New().String(), &wsID, "")
	require.NoError(t, h.Section(c))

	assert.True(t, called)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- by-path ---

func byPathRequest(e *echo.Echo, projID string, wsID *uuid.UUID, query string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/?"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/projects/:proj_id/documents/by-path")
	c.SetParamNames("proj_id")
	c.SetParamValues(projID)
	return c, rec
}

func TestDocumentHandler_GetByPath(t *testing.T) {
	projID, wsID, docID := uuid.New(), uuid.New(), uuid.New()
	var gotProject, gotWS uuid.UUID
	var gotPath string

	mockSvc := &MockDocumentService{
		GetByPathFunc: func(_ context.Context, p, w uuid.UUID, path string) (*domain.Document, error) {
			gotProject, gotWS, gotPath = p, w, path
			return &domain.Document{ID: docID, Slug: "adr-004", Title: "ADR-004", Body: "# ADR-004\n"}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := byPathRequest(e, projID.String(), &wsID, "path=architecture/adr/adr-004")
	require.NoError(t, h.GetByPath(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, projID, gotProject)
	assert.Equal(t, wsID, gotWS)
	assert.Equal(t, "architecture/adr/adr-004", gotPath)
	assert.Contains(t, rec.Body.String(), "adr-004")
	assert.Contains(t, rec.Body.String(), "# ADR-004", "without ?section the whole body comes back")
}

// The pairing that makes the route worth having: path and section in one call,
// so the caller never downloads the body it is not going to read.
func TestDocumentHandler_GetByPath_WithSection(t *testing.T) {
	projID, wsID, docID := uuid.New(), uuid.New(), uuid.New()
	var sectionDoc uuid.UUID
	var sectionRef string

	mockSvc := &MockDocumentService{
		GetByPathFunc: func(context.Context, uuid.UUID, uuid.UUID, string) (*domain.Document, error) {
			return &domain.Document{ID: docID, Title: "ADR-004", Body: "# ADR-004\n\n## Решение\n\nЧто решили.\n"}, nil
		},
		SectionFunc: func(_ context.Context, id, _ uuid.UUID, ref string) (*service.DocumentSection, error) {
			sectionDoc, sectionRef = id, ref
			return &service.DocumentSection{
				DocumentID: id,
				Heading:    markdown.Heading{Level: 2, Text: "Решение", Anchor: "решение"},
				Content:    "## Решение\n\nЧто решили.\n",
			}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := byPathRequest(e, projID.String(), &wsID,
		"path=architecture/adr/adr-004&section=%D1%80%D0%B5%D1%88%D0%B5%D0%BD%D0%B8%D0%B5")
	require.NoError(t, h.GetByPath(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, docID, sectionDoc, "the section is taken from the document the path resolved to")
	assert.Equal(t, "решение", sectionRef)

	var body service.DocumentSection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "## Решение\n\nЧто решили.\n", body.Content)
	assert.NotContains(t, rec.Body.String(), "# ADR-004\\n\\n## Решение",
		"the full body is not shipped alongside the section it was asked to narrow to")
}

func TestDocumentHandler_GetByPath_UnknownPathIsANotFoundThatSaysWhere(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentService{
		GetByPathFunc: func(context.Context, uuid.UUID, uuid.UUID, string) (*domain.Document, error) {
			return nil, apierror.NotFoundWithDetails("Document", `no document "adr-999" under "architecture/adr"`)
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := byPathRequest(e, uuid.New().String(), &wsID, "path=architecture/adr/adr-999")
	require.NoError(t, h.GetByPath(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "adr-999")
	assert.Contains(t, rec.Body.String(), "architecture/adr")
}

func TestDocumentHandler_GetByPath_InvalidProjectAndMissingWorkspace(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})
	wsID := uuid.New()

	c, rec := byPathRequest(e, "not-a-uuid", &wsID, "path=a/b")
	require.NoError(t, h.GetByPath(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	c, rec = byPathRequest(e, uuid.New().String(), nil, "path=a/b")
	require.NoError(t, h.GetByPath(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
