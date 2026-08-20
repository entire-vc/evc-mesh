package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupAttachmentTest(mockSvc *MockDocumentAttachmentService) (*DocumentAttachmentHandler, *echo.Echo) {
	return NewDocumentAttachmentHandler(mockSvc), echo.New()
}

// docAttRequest builds a request against one of the /documents/:doc_id/attachments
// routes.
func docAttRequest(e *echo.Echo, method, docID string, wsID *uuid.UUID, target string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/documents/:doc_id/attachments")
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

// attRequest builds a request against one of the /document-attachments/:att_id
// routes.
func attRequest(e *echo.Echo, method, attID string, wsID *uuid.UUID, target string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/document-attachments/:att_id")
	c.SetParamNames("att_id")
	c.SetParamValues(attID)
	return c, rec
}

// uploadRequest builds the multipart POST the upload route takes.
func uploadRequest(e *echo.Echo, docID string, wsID *uuid.UUID, filename, contentType, nameField string, content []byte) (echo.Context, *httptest.ResponseRecorder) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if nameField != "" {
		_ = w.WriteField("name", nameField)
	}
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
	if contentType != "" {
		h["Content-Type"] = []string{contentType}
	}
	part, _ := w.CreatePart(h)
	_, _ = part.Write(content)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if wsID != nil {
		c.Set("workspace_id", *wsID)
	}
	c.SetPath("/documents/:doc_id/attachments")
	c.SetParamNames("doc_id")
	c.SetParamValues(docID)
	return c, rec
}

// --- List ---

func TestDocumentAttachmentHandler_List(t *testing.T) {
	docID, wsID := uuid.New(), uuid.New()
	var gotDoc, gotWS uuid.UUID
	var gotPageSize int

	mockSvc := &MockDocumentAttachmentService{
		ListByDocumentFunc: func(_ context.Context, d, w uuid.UUID, pg pagination.Params) (*pagination.Page[domain.DocumentAttachment], error) {
			gotDoc, gotWS, gotPageSize = d, w, pg.PageSize
			return pagination.NewPage([]domain.DocumentAttachment{{ID: uuid.New(), Name: "screenshot.png"}}, 1, pg), nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := docAttRequest(e, http.MethodGet, docID.String(), &wsID, "/?page_size=2")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, docID, gotDoc)
	assert.Equal(t, wsID, gotWS, "the caller's workspace reached the service")
	assert.Equal(t, 2, gotPageSize)
	assert.Contains(t, rec.Body.String(), "screenshot.png")
}

func TestDocumentAttachmentHandler_List_InvalidDocID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupAttachmentTest(&MockDocumentAttachmentService{})

	c, rec := docAttRequest(e, http.MethodGet, "not-a-uuid", &wsID, "/")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// No workspace in context means wsAccess did not run or refused. The handler must
// refuse rather than fall back to an unscoped read.
func TestDocumentAttachmentHandler_List_NoWorkspaceIsForbidden(t *testing.T) {
	called := false
	mockSvc := &MockDocumentAttachmentService{
		ListByDocumentFunc: func(_ context.Context, _, _ uuid.UUID, pg pagination.Params) (*pagination.Page[domain.DocumentAttachment], error) {
			called = true
			return pagination.NewPage([]domain.DocumentAttachment{}, 0, pg), nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := docAttRequest(e, http.MethodGet, uuid.New().String(), nil, "/")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "the service was reached without a resolved workspace")
}

func TestDocumentAttachmentHandler_List_ServiceError(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentAttachmentService{
		ListByDocumentFunc: func(_ context.Context, _, _ uuid.UUID, _ pagination.Params) (*pagination.Page[domain.DocumentAttachment], error) {
			return nil, apierror.NotFound("Document")
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := docAttRequest(e, http.MethodGet, uuid.New().String(), &wsID, "/")
	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- Upload ---

func TestDocumentAttachmentHandler_Upload(t *testing.T) {
	docID, wsID, callerID := uuid.New(), uuid.New(), uuid.New()
	var got service.UploadDocumentAttachmentInput

	mockSvc := &MockDocumentAttachmentService{
		UploadFunc: func(_ context.Context, in service.UploadDocumentAttachmentInput) (*domain.DocumentAttachment, error) {
			got = in
			return &domain.DocumentAttachment{ID: uuid.New(), DocumentID: in.DocumentID, Name: in.Name}, nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := uploadRequest(e, docID.String(), &wsID, "screenshot.png", "image/png", "", []byte("PNGDATA"))
	c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), callerID, domain.ActorTypeUser)))
	require.NoError(t, h.Upload(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, docID, got.DocumentID)
	assert.Equal(t, wsID, got.WorkspaceID, "the workspace comes from the route, not the body")
	assert.Equal(t, "screenshot.png", got.Name, "an absent name field falls back to the filename")
	assert.Equal(t, "image/png", got.MimeType)
	assert.Equal(t, int64(len("PNGDATA")), got.Size)
	assert.Equal(t, callerID, got.UploadedBy)
	assert.Equal(t, domain.ActorTypeUser, got.UploadedByType)
}

func TestDocumentAttachmentHandler_Upload_NameFieldOverridesTheFilename(t *testing.T) {
	wsID := uuid.New()
	var got service.UploadDocumentAttachmentInput
	mockSvc := &MockDocumentAttachmentService{
		UploadFunc: func(_ context.Context, in service.UploadDocumentAttachmentInput) (*domain.DocumentAttachment, error) {
			got = in
			return &domain.DocumentAttachment{ID: uuid.New()}, nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := uploadRequest(e, uuid.New().String(), &wsID, "IMG_4821.HEIC", "image/heic", "architecture diagram.heic", []byte("x"))
	require.NoError(t, h.Upload(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "architecture diagram.heic", got.Name)
}

// A generic octet-stream from the browser is replaced by the extension's type:
// an image served as application/octet-stream downloads instead of rendering,
// however the disposition is set.
func TestDocumentAttachmentHandler_Upload_InfersMimeFromTheExtension(t *testing.T) {
	wsID := uuid.New()
	var got service.UploadDocumentAttachmentInput
	mockSvc := &MockDocumentAttachmentService{
		UploadFunc: func(_ context.Context, in service.UploadDocumentAttachmentInput) (*domain.DocumentAttachment, error) {
			got = in
			return &domain.DocumentAttachment{ID: uuid.New()}, nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := uploadRequest(e, uuid.New().String(), &wsID, "diagram.png", "application/octet-stream", "", []byte("x"))
	require.NoError(t, h.Upload(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, got.MimeType, "image/png")
}

func TestDocumentAttachmentHandler_Upload_MissingFile(t *testing.T) {
	wsID := uuid.New()
	h, e := setupAttachmentTest(&MockDocumentAttachmentService{})

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("name", "orphan.png")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("workspace_id", wsID)
	c.SetPath("/documents/:doc_id/attachments")
	c.SetParamNames("doc_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentAttachmentHandler_Upload_NoWorkspaceIsForbidden(t *testing.T) {
	called := false
	mockSvc := &MockDocumentAttachmentService{
		UploadFunc: func(_ context.Context, _ service.UploadDocumentAttachmentInput) (*domain.DocumentAttachment, error) {
			called = true
			return &domain.DocumentAttachment{ID: uuid.New()}, nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := uploadRequest(e, uuid.New().String(), nil, "x.png", "image/png", "", []byte("x"))
	require.NoError(t, h.Upload(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called)
}

func TestDocumentAttachmentHandler_Upload_ServiceRefusal(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentAttachmentService{
		UploadFunc: func(_ context.Context, _ service.UploadDocumentAttachmentInput) (*domain.DocumentAttachment, error) {
			return nil, apierror.NotFound("Document")
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := uploadRequest(e, uuid.New().String(), &wsID, "x.png", "image/png", "", []byte("x"))
	require.NoError(t, h.Upload(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- Download ---

func TestDocumentAttachmentHandler_Download(t *testing.T) {
	attID, wsID := uuid.New(), uuid.New()
	var gotID, gotWS uuid.UUID
	var gotInline bool

	mockSvc := &MockDocumentAttachmentService{
		GetDownloadURLFunc: func(_ context.Context, id, ws uuid.UUID, inline bool) (string, error) {
			gotID, gotWS, gotInline = id, ws, inline
			return "https://s3.example.com/signed", nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := attRequest(e, http.MethodGet, attID.String(), &wsID, "/?disposition=inline")
	require.NoError(t, h.Download(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, attID, gotID)
	assert.Equal(t, wsID, gotWS)
	assert.True(t, gotInline)

	// The response shape is the artifact endpoint's, exactly: the frontend
	// resolver is shared, so a different envelope would need a second resolver.
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, map[string]string{"url": "https://s3.example.com/signed"}, body)
}

func TestDocumentAttachmentHandler_Download_DefaultsToAttachmentDisposition(t *testing.T) {
	wsID := uuid.New()
	var gotInline bool
	mockSvc := &MockDocumentAttachmentService{
		GetDownloadURLFunc: func(_ context.Context, _, _ uuid.UUID, inline bool) (string, error) {
			gotInline = inline
			return "https://s3.example.com/signed", nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := attRequest(e, http.MethodGet, uuid.New().String(), &wsID, "/")
	require.NoError(t, h.Download(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, gotInline, "only ?disposition=inline asks for an inline render")
}

func TestDocumentAttachmentHandler_Download_InvalidAttID(t *testing.T) {
	wsID := uuid.New()
	h, e := setupAttachmentTest(&MockDocumentAttachmentService{})

	c, rec := attRequest(e, http.MethodGet, "nope", &wsID, "/")
	require.NoError(t, h.Download(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDocumentAttachmentHandler_Download_NoWorkspaceIsForbidden(t *testing.T) {
	called := false
	mockSvc := &MockDocumentAttachmentService{
		GetDownloadURLFunc: func(_ context.Context, _, _ uuid.UUID, _ bool) (string, error) {
			called = true
			return "https://s3.example.com/leaked", nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := attRequest(e, http.MethodGet, uuid.New().String(), nil, "/?disposition=inline")
	require.NoError(t, h.Download(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "a presigned URL was minted without a resolved workspace")
	assert.NotContains(t, rec.Body.String(), "s3.example.com")
}

func TestDocumentAttachmentHandler_Download_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentAttachmentService{
		GetDownloadURLFunc: func(_ context.Context, _, _ uuid.UUID, _ bool) (string, error) {
			return "", apierror.NotFound("Attachment")
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := attRequest(e, http.MethodGet, uuid.New().String(), &wsID, "/")
	require.NoError(t, h.Download(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- Delete ---

func TestDocumentAttachmentHandler_Delete(t *testing.T) {
	attID, wsID := uuid.New(), uuid.New()
	var gotID, gotWS uuid.UUID

	mockSvc := &MockDocumentAttachmentService{
		DeleteFunc: func(_ context.Context, id, ws uuid.UUID) error {
			gotID, gotWS = id, ws
			return nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := attRequest(e, http.MethodDelete, attID.String(), &wsID, "/")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, attID, gotID)
	assert.Equal(t, wsID, gotWS)
}

func TestDocumentAttachmentHandler_Delete_NoWorkspaceIsForbidden(t *testing.T) {
	called := false
	mockSvc := &MockDocumentAttachmentService{
		DeleteFunc: func(_ context.Context, _, _ uuid.UUID) error {
			called = true
			return nil
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := attRequest(e, http.MethodDelete, uuid.New().String(), nil, "/")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "a delete reached the service without a resolved workspace")
}

func TestDocumentAttachmentHandler_Delete_NotFound(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockDocumentAttachmentService{
		DeleteFunc: func(_ context.Context, _, _ uuid.UUID) error {
			return apierror.NotFound("Attachment")
		},
	}
	h, e := setupAttachmentTest(mockSvc)

	c, rec := attRequest(e, http.MethodDelete, uuid.New().String(), &wsID, "/")
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// attachmentScope returns an *apierror.Error rather than an echo response for a
// reason worth stating in a test: c.JSON returns nil on success, so a refusal
// handed back as a plain error would be indistinguishable from "no error" at the
// call site and the request would proceed.
func TestAttachmentScope_RefusalIsTypedNotNil(t *testing.T) {
	e := echo.New()
	c, _ := attRequest(e, http.MethodGet, "not-a-uuid", nil, "/")

	_, _, apiErr := attachmentScope(c)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
}
