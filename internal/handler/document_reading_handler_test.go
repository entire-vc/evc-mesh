package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// docSubRequest builds a request against one of the /documents/:doc_id/… routes.
// target carries the query string, which is where the section reference lives.
func docSubRequest(e *echo.Echo, method, routePath, docID, target, body string) (echo.Context, *httptest.ResponseRecorder) {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, http.NoBody)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("workspace_id", uuid.New())
	c.SetPath(routePath)
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

// --- outline ---------------------------------------------------------------

func TestDocumentHandler_Outline(t *testing.T) {
	docID := uuid.New()
	mockSvc := &MockDocumentService{
		OutlineFunc: func(_ context.Context, id, _ uuid.UUID) (*service.DocumentOutline, error) {
			return &service.DocumentOutline{
				DocumentID: id,
				Title:      "Runbook",
				Version:    3,
				Outline:    []mdoc.Heading{{Level: 2, Text: "Deploy", Anchor: "deploy", Line: 5, Start: 40, End: 90}},
			}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/outline", docID.String(), "/", "")
	require.NoError(t, h.Outline(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	var got service.DocumentOutline
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, docID, got.DocumentID)
	assert.Equal(t, 3, got.Version)
	require.Len(t, got.Outline, 1)
	assert.Equal(t, "deploy", got.Outline[0].Anchor)
}

func TestDocumentHandler_Outline_InvalidDocID(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/outline", "not-a-uuid", "/", "")
	require.NoError(t, h.Outline(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Outline_NoWorkspaceIsForbidden(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docRequest(e, http.MethodGet, uuid.New().String(), nil, "")
	require.NoError(t, h.Outline(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDocumentHandler_Outline_ServiceError(t *testing.T) {
	mockSvc := &MockDocumentService{
		OutlineFunc: func(context.Context, uuid.UUID, uuid.UUID) (*service.DocumentOutline, error) {
			return nil, apierror.NotFound("Document")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/outline", uuid.New().String(), "/", "")
	require.NoError(t, h.Outline(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- section ---------------------------------------------------------------

func TestDocumentHandler_Section(t *testing.T) {
	var gotRef string
	mockSvc := &MockDocumentService{
		SectionFunc: func(_ context.Context, id, _ uuid.UUID, ref string) (*service.DocumentSection, error) {
			gotRef = ref
			return &service.DocumentSection{
				DocumentID: id,
				Version:    2,
				Heading:    mdoc.Heading{Level: 2, Text: "Deploy", Anchor: "deploy"},
				Content:    "## Deploy\n\nPush the tag.\n",
			}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/section", uuid.New().String(),
		"/?heading=deploy", "")
	require.NoError(t, h.Section(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "deploy", gotRef)
	assert.Contains(t, rec.Body.String(), "Push the tag")
}

// A heading with spaces and punctuation is exactly why the reference is a query
// parameter and not a path segment.
func TestDocumentHandler_Section_HeadingWithSpacesAndSlashes(t *testing.T) {
	var gotRef string
	mockSvc := &MockDocumentService{
		SectionFunc: func(_ context.Context, id, _ uuid.UUID, ref string) (*service.DocumentSection, error) {
			gotRef = ref
			return &service.DocumentSection{DocumentID: id}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/section", uuid.New().String(),
		"/?heading=Rollback%2Frestore%2C+step+by+step", "")
	require.NoError(t, h.Section(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Rollback/restore, step by step", gotRef)
}

func TestDocumentHandler_Section_MissingHeadingParam(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/section", uuid.New().String(), "/?heading=%20", "")
	require.NoError(t, h.Section(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "outline")
}

func TestDocumentHandler_Section_InvalidDocID(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/section", "nope", "/?heading=deploy", "")
	require.NoError(t, h.Section(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_Section_ServiceError(t *testing.T) {
	mockSvc := &MockDocumentService{
		SectionFunc: func(context.Context, uuid.UUID, uuid.UUID, string) (*service.DocumentSection, error) {
			return nil, apierror.NotFoundWithDetails("Heading", "available anchors: deploy, monitoring")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodGet, "/documents/:doc_id/section", uuid.New().String(),
		"/?heading=nope", "")
	require.NoError(t, h.Section(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "monitoring")
}

// --- by-path ---------------------------------------------------------------

func pathRequest(e *echo.Echo, projID, wildcard string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/documents/by-path/*")
	c.SetParamNames("proj_id", "*")
	c.SetParamValues(projID, wildcard)
	return c, rec
}

func TestDocumentHandler_GetByPath(t *testing.T) {
	projID, docID := uuid.New(), uuid.New()
	var gotProject, gotPath string

	mockSvc := &MockDocumentService{
		GetByPathFunc: func(_ context.Context, p uuid.UUID, path string) (*domain.Document, error) {
			gotProject, gotPath = p.String(), path
			return &domain.Document{ID: docID, Title: "ADR-004", Version: 2, Body: "# ADR-004\n"}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := pathRequest(e, projID.String(), "architecture/adr/adr-004")
	require.NoError(t, h.GetByPath(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, projID.String(), gotProject)
	assert.Equal(t, "architecture/adr/adr-004", gotPath, "the whole wildcard reaches the service")
	assert.Contains(t, rec.Body.String(), "ADR-004")
	assert.Contains(t, rec.Body.String(), `"version":2`, "so the caller can write back conditionally")
}

func TestDocumentHandler_GetByPath_InvalidProjectID(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := pathRequest(e, "not-a-uuid", "architecture")
	require.NoError(t, h.GetByPath(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_GetByPath_ServiceError(t *testing.T) {
	mockSvc := &MockDocumentService{
		GetByPathFunc: func(context.Context, uuid.UUID, string) (*domain.Document, error) {
			return nil, apierror.NotFoundWithDetails("Document", `path resolves as far as "architecture"`)
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := pathRequest(e, uuid.New().String(), "architecture/nope")
	require.NoError(t, h.GetByPath(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "architecture")
}

// --- resolve-anchor --------------------------------------------------------

func TestDocumentHandler_ResolveAnchor(t *testing.T) {
	var got service.ResolveAnchorInput
	mockSvc := &MockDocumentService{
		ResolveAnchorFunc: func(_ context.Context, _, _ uuid.UUID, in service.ResolveAnchorInput) (*mdoc.Anchor, error) {
			got = in
			return &mdoc.Anchor{Exact: in.Quote, Prefix: "before ", Suffix: " after", Start: 853, End: 900}, nil
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodPost, "/documents/:doc_id/resolve-anchor", uuid.New().String(), "/",
		`{"quote":"поднять эскалацию","prefix":"дежурный обязан немедленно ","suffix":" и позвать"}`)
	require.NoError(t, h.ResolveAnchor(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "поднять эскалацию", got.Quote)
	assert.Equal(t, "дежурный обязан немедленно ", got.Prefix)
	assert.Equal(t, " и позвать", got.Suffix)

	var anchor mdoc.Anchor
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &anchor))
	assert.Equal(t, 853, anchor.Start)
	assert.Equal(t, 900, anchor.End)
}

func TestDocumentHandler_ResolveAnchor_InvalidBody(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docSubRequest(e, http.MethodPost, "/documents/:doc_id/resolve-anchor", uuid.New().String(), "/",
		`{"quote":`)
	require.NoError(t, h.ResolveAnchor(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_ResolveAnchor_InvalidDocID(t *testing.T) {
	h, e := setupDocumentTest(&MockDocumentService{})

	c, rec := docSubRequest(e, http.MethodPost, "/documents/:doc_id/resolve-anchor", "nope", "/", `{"quote":"x"}`)
	require.NoError(t, h.ResolveAnchor(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentHandler_ResolveAnchor_MissingQuoteIs400(t *testing.T) {
	mockSvc := &MockDocumentService{
		ResolveAnchorFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ResolveAnchorInput) (*mdoc.Anchor, error) {
			return nil, apierror.BadRequestWithDetails("No such quote in this document", "the text was not found")
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodPost, "/documents/:doc_id/resolve-anchor", uuid.New().String(), "/",
		`{"quote":"not in the document"}`)
	require.NoError(t, h.ResolveAnchor(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "No such quote")
}

// The match count has to survive as a number: it is what tells the caller
// whether to add context or to quote something else.
func TestDocumentHandler_ResolveAnchor_AmbiguousCarriesTheCount(t *testing.T) {
	mockSvc := &MockDocumentService{
		ResolveAnchorFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ResolveAnchorInput) (*mdoc.Anchor, error) {
			return nil, &mdoc.AmbiguousQuoteError{Quote: "the API", Matches: 11}
		},
	}
	h, e := setupDocumentTest(mockSvc)

	c, rec := docSubRequest(e, http.MethodPost, "/documents/:doc_id/resolve-anchor", uuid.New().String(), "/",
		`{"quote":"the API"}`)
	require.NoError(t, h.ResolveAnchor(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ambiguous_quote", body["code"])
	assert.Equal(t, float64(11), body["matches"])
	assert.Contains(t, body["message"], "prefix")
}
