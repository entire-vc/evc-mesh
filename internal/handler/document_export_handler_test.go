package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func setupExportTest(mockSvc *MockDocumentExportService) (*DocumentExportHandler, *echo.Echo) {
	return NewDocumentExportHandler(mockSvc), echo.New()
}

// exportRequest builds a request against GET /documents/:doc_id/export.
func exportRequest(e *echo.Echo, docID string, wsID *uuid.UUID, target string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/documents/:doc_id/export")
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

func TestDocumentExportHandler_Export(t *testing.T) {
	t.Run("scope=self serves the bytes with the filename and content type the service returned", func(t *testing.T) {
		docID, wsID := uuid.New(), uuid.New()
		var gotRoot, gotWS uuid.UUID
		var gotScope service.ExportScope
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(_ context.Context, rootID, workspaceID uuid.UUID, scope service.ExportScope) ([]byte, string, string, error) {
				gotRoot, gotWS, gotScope = rootID, workspaceID, scope
				return []byte("# Runbook"), "runbook-2026-09-02.md", "text/markdown", nil
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, docID.String(), &wsID, "/?format=md&scope=self")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "# Runbook", rec.Body.String())
		assert.Equal(t, `attachment; filename="runbook-2026-09-02.md"`, rec.Header().Get(echo.HeaderContentDisposition))
		assert.Equal(t, "text/markdown", rec.Header().Get(echo.HeaderContentType))
		assert.Equal(t, docID, gotRoot, "the path's doc_id must reach the service as rootID")
		assert.Equal(t, wsID, gotWS, "the caller's workspace must reach the service — this is what makes scope=tree tenant-safe at the endpoint, not just in WalkExportTree")
		assert.Equal(t, service.ExportScopeSelf, gotScope)
	})

	t.Run("scope=tree serves a zip with its own content type", func(t *testing.T) {
		docID, wsID := uuid.New(), uuid.New()
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(_ context.Context, _, _ uuid.UUID, scope service.ExportScope) ([]byte, string, string, error) {
				require.Equal(t, service.ExportScopeTree, scope)
				return []byte("PK\x03\x04fake-zip"), "guide-2026-09-02.zip", "application/zip", nil
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, docID.String(), &wsID, "/?format=md&scope=tree")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/zip", rec.Header().Get(echo.HeaderContentType))
		assert.Equal(t, `attachment; filename="guide-2026-09-02.zip"`, rec.Header().Get(echo.HeaderContentDisposition))
	})

	t.Run("format=pdf routes to ExportPDF, not ExportMarkdown", func(t *testing.T) {
		docID, wsID := uuid.New(), uuid.New()
		var gotRoot, gotWS uuid.UUID
		var gotScope service.ExportScope
		mdCalled := false
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ExportScope) ([]byte, string, string, error) {
				mdCalled = true
				return nil, "", "", nil
			},
			ExportPDFFunc: func(_ context.Context, rootID, workspaceID uuid.UUID, scope service.ExportScope) ([]byte, string, string, error) {
				gotRoot, gotWS, gotScope = rootID, workspaceID, scope
				return []byte("%PDF-fake"), "guide-2026-09-02.pdf", "application/pdf", nil
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, docID.String(), &wsID, "/?format=pdf&scope=tree")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/pdf", rec.Header().Get(echo.HeaderContentType))
		assert.Equal(t, `attachment; filename="guide-2026-09-02.pdf"`, rec.Header().Get(echo.HeaderContentDisposition))
		assert.Equal(t, docID, gotRoot)
		assert.Equal(t, wsID, gotWS)
		assert.Equal(t, service.ExportScopeTree, gotScope)
		assert.False(t, mdCalled, "format=pdf must not call through to ExportMarkdown")
	})

	t.Run("format=docx routes to ExportDOCX, not ExportMarkdown", func(t *testing.T) {
		docID, wsID := uuid.New(), uuid.New()
		var gotScope service.ExportScope
		mdCalled := false
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ExportScope) ([]byte, string, string, error) {
				mdCalled = true
				return nil, "", "", nil
			},
			ExportDOCXFunc: func(_ context.Context, _, _ uuid.UUID, scope service.ExportScope) ([]byte, string, string, error) {
				gotScope = scope
				return []byte("PK\x03\x04fake-docx"), "guide-2026-09-02.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, docID.String(), &wsID, "/?format=docx&scope=self")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, `attachment; filename="guide-2026-09-02.docx"`, rec.Header().Get(echo.HeaderContentDisposition))
		assert.Equal(t, service.ExportScopeSelf, gotScope)
		assert.False(t, mdCalled, "format=docx must not call through to ExportMarkdown")
	})

	t.Run("an unrecognized format is refused before any service method is called", func(t *testing.T) {
		called := false
		markCalled := func(context.Context, uuid.UUID, uuid.UUID, service.ExportScope) ([]byte, string, string, error) {
			called = true
			return nil, "", "", nil
		}
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: markCalled,
			ExportPDFFunc:      markCalled,
			ExportDOCXFunc:     markCalled,
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, uuid.New().String(), uuidPtr(uuid.New()), "/?format=doc&scope=self")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.False(t, called, "an unrecognized format must not call through to any export service method")
	})

	t.Run("a missing or unrecognized scope is refused before the service is called", func(t *testing.T) {
		called := false
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ExportScope) ([]byte, string, string, error) {
				called = true
				return nil, "", "", nil
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, uuid.New().String(), uuidPtr(uuid.New()), "/?format=md")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.False(t, called)
	})

	// This is the endpoint-level half of the negative test WalkExportTree
	// already proves at the service layer (Export 1/7): that test shows the
	// WALK itself refuses a stranger. This test shows the ENDPOINT actually
	// reaches that walk with the real caller's workspace rather than, say, a
	// hardcoded or forgotten value — the class of bug a purely service-level
	// test cannot catch, because it never goes through routing/wsAccess/the
	// handler's own argument-passing at all.
	t.Run("a caller from another workspace gets the service's refusal, not a 200", func(t *testing.T) {
		docID, strangerWS := uuid.New(), uuid.New()
		var gotWS uuid.UUID
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(_ context.Context, _ uuid.UUID, workspaceID uuid.UUID, _ service.ExportScope) ([]byte, string, string, error) {
				gotWS = workspaceID
				// Mirrors what the REAL DocumentExportService returns for a
				// cross-workspace root, via GetByIDInWorkspace/WalkExportTree
				// (Export 1/7): apierror.NotFound, no bytes.
				return nil, "", "", apierror.NotFound("Document")
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, docID.String(), &strangerWS, "/?format=md&scope=tree")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, strangerWS, gotWS, "the endpoint must forward the ACTUAL caller's workspace, not silently substitute a different one")
		assert.NotContains(t, rec.Body.String(), "PK", "no archive bytes may reach the response body on a refusal")
	})

	t.Run("a tree past the size ceiling is reported as 413 with the actual/limit named", func(t *testing.T) {
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ExportScope) ([]byte, string, string, error) {
				return nil, "", "", &service.ExportTreeTooLargeError{Kind: "documents", Actual: 501, Limit: 500}
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, uuid.New().String(), uuidPtr(uuid.New()), "/?format=md&scope=tree")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), `"kind":"documents"`)
		assert.Contains(t, rec.Body.String(), `"actual":501`)
		assert.Contains(t, rec.Body.String(), `"limit":500`)
	})

	t.Run("invalid doc_id is a 400 before the service is reached", func(t *testing.T) {
		called := false
		mockSvc := &MockDocumentExportService{
			ExportMarkdownFunc: func(context.Context, uuid.UUID, uuid.UUID, service.ExportScope) ([]byte, string, string, error) {
				called = true
				return nil, "", "", nil
			},
		}
		h, e := setupExportTest(mockSvc)

		c, rec := exportRequest(e, "not-a-uuid", uuidPtr(uuid.New()), "/?format=md&scope=self")
		require.NoError(t, h.Export(c))

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.False(t, called)
	})
}

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }
