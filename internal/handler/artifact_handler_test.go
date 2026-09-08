package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
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
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupArtifactTest(mockSvc *MockArtifactService) (*ArtifactHandler, *echo.Echo) {
	e := echo.New()
	h := NewArtifactHandler(mockSvc, nil)
	return h, e
}

// Redaction unit tests now live on the type itself:
// internal/domain/artifact_test.go (domain.Artifact.MarshalJSON).

// --- GetByID handler tests ---

func TestArtifactHandler_GetByID_StripsTrAgentKey(t *testing.T) {
	artifactID := uuid.New()
	wsID := uuid.New()

	mockSvc := &MockArtifactService{
		GetByIDInWorkspaceFunc: func(_ context.Context, id, _ uuid.UUID) (*domain.Artifact, error) {
			return &domain.Artifact{
				ID:       id,
				Name:     "report.md",
				Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/f","tr_agent_key":"tr_agent_secret"}`),
			}, nil
		},
	}

	h, e := setupArtifactTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("workspace_id", wsID)
	c.SetPath("/artifacts/:artifact_id")
	c.SetParamNames("artifact_id")
	c.SetParamValues(artifactID.String())

	require.NoError(t, h.GetByID(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var result map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))

	rawMeta, ok := result["metadata"].(map[string]any)
	require.True(t, ok, "metadata should be a JSON object")
	assert.NotContains(t, rawMeta, "tr_agent_key", "tr_agent_key must not appear in API response")
	assert.Equal(t, "https://relay.example.com/f", rawMeta["tr_public_url"])
}

// TestArtifactHandler_GetByID_CarriesDownloadPath proves the response carries
// the stable machine-readable download path alongside tr_public_url, and
// that tr_public_url itself is unchanged by this addition (task #97c60be9:
// tr_public_url must stay exactly as-is on shares where it already works).
func TestArtifactHandler_GetByID_CarriesDownloadPath(t *testing.T) {
	artifactID := uuid.New()
	wsID := uuid.New()

	mockSvc := &MockArtifactService{
		GetByIDInWorkspaceFunc: func(_ context.Context, id, _ uuid.UUID) (*domain.Artifact, error) {
			return &domain.Artifact{
				ID:       id,
				Name:     "report.md",
				Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/f"}`),
			}, nil
		},
	}

	h, e := setupArtifactTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("workspace_id", wsID)
	c.SetPath("/artifacts/:artifact_id")
	c.SetParamNames("artifact_id")
	c.SetParamValues(artifactID.String())

	require.NoError(t, h.GetByID(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var result map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))

	assert.Equal(t, "/api/v1/artifacts/"+artifactID.String()+"/download", result["download_path"],
		"download_path must be the stable endpoint path, not a presigned URL")

	rawMeta, ok := result["metadata"].(map[string]any)
	require.True(t, ok, "metadata should be a JSON object")
	assert.Equal(t, "https://relay.example.com/f", rawMeta["tr_public_url"],
		"tr_public_url must remain present and unchanged for existing consumers")
}

// --- Upload handler tests (task #82ce594a) ---

// buildMultipartUploadRequest mirrors the exact repro from the bug report:
// name + description + file, no artifact_type. description has no server-side
// field and is silently ignored — it exists only to prove extra fields don't
// break the request.
func buildMultipartUploadRequest(t *testing.T, taskID uuid.UUID, extraFields map[string]string) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range extraFields {
		require.NoError(t, w.WriteField(k, v))
	}
	fw, err := w.CreateFormFile("file", "screenshot.png")
	require.NoError(t, err)
	_, err = fw.Write([]byte("fake-png-bytes"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", &buf)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	return req
}

func newArtifactUploadContext(e *echo.Echo, req *http.Request, taskID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/artifacts")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	return c, rec
}

// TestArtifactHandler_Upload_Multipart_NoArtifactType reproduces the exact
// multipart repro from the bug report: name + description + file, no
// artifact_type. Before the fix, the empty artifact_type reached Postgres as
// "" against the artifact_type enum column, failed with 22P02, and surfaced
// as a generic 400 "invalid value for field" even though every required
// field was present.
func TestArtifactHandler_Upload_Multipart_NoArtifactType(t *testing.T) {
	var gotInput service.UploadArtifactInput
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
			gotInput = input
			return &domain.Artifact{ID: uuid.New(), TaskID: input.TaskID, Name: input.Name, ArtifactType: input.ArtifactType}, nil
		},
	}
	h, e := setupArtifactTest(mockSvc)
	taskID := uuid.New()

	req := buildMultipartUploadRequest(t, taskID, map[string]string{
		"name":        "screenshot.png",
		"description": "1440px desktop screenshot",
	})
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, domain.ArtifactTypeFile, gotInput.ArtifactType, "empty artifact_type must default to \"file\", matching the DB default")
	assert.Equal(t, "screenshot.png", gotInput.Name)
}

// TestArtifactHandler_Upload_Multipart_ExplicitArtifactType proves the
// default does not clobber a caller-supplied artifact_type.
func TestArtifactHandler_Upload_Multipart_ExplicitArtifactType(t *testing.T) {
	var gotInput service.UploadArtifactInput
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
			gotInput = input
			return &domain.Artifact{ID: uuid.New()}, nil
		},
	}
	h, e := setupArtifactTest(mockSvc)
	taskID := uuid.New()

	req := buildMultipartUploadRequest(t, taskID, map[string]string{
		"name":          "results.json",
		"artifact_type": "report",
	})
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, domain.ArtifactTypeReport, gotInput.ArtifactType)
}

// TestArtifactHandler_Upload_JSON_Base64 reproduces the JSON repro from the
// bug report: name + description + content(base64). Before the fix, POST
// /tasks/:task_id/artifacts had no JSON branch at all — c.FormValue on a
// JSON body always returned "", so every JSON request failed validation with
// "name is required" regardless of what the body actually contained.
func TestArtifactHandler_Upload_JSON_Base64(t *testing.T) {
	var gotInput service.UploadArtifactInput
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
			gotInput = input
			data, err := io.ReadAll(input.Reader)
			require.NoError(t, err)
			assert.Equal(t, "hello artifact", string(data))
			return &domain.Artifact{ID: uuid.New(), TaskID: input.TaskID, Name: input.Name, ArtifactType: input.ArtifactType}, nil
		},
	}
	h, e := setupArtifactTest(mockSvc)
	taskID := uuid.New()

	content := base64.StdEncoding.EncodeToString([]byte("hello artifact"))
	body, err := json.Marshal(map[string]any{
		"name":        "notes.txt",
		"description": "context notes",
		"content":     content,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "notes.txt", gotInput.Name)
	assert.Equal(t, domain.ArtifactTypeFile, gotInput.ArtifactType)
	assert.Equal(t, int64(len("hello artifact")), gotInput.Size)
}

// TestArtifactHandler_Upload_JSON_TextEncoding covers the explicit
// "encoding":"text" opt-out for callers that don't have binary content.
func TestArtifactHandler_Upload_JSON_TextEncoding(t *testing.T) {
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
			data, err := io.ReadAll(input.Reader)
			require.NoError(t, err)
			assert.Equal(t, `{"passed": 42}`, string(data))
			return &domain.Artifact{ID: uuid.New()}, nil
		},
	}
	h, e := setupArtifactTest(mockSvc)
	taskID := uuid.New()

	body, err := json.Marshal(map[string]any{
		"name":     "results.json",
		"content":  `{"passed": 42}`,
		"encoding": "text",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
}

func TestArtifactHandler_Upload_JSON_MissingName(t *testing.T) {
	h, e := setupArtifactTest(&MockArtifactService{})
	taskID := uuid.New()

	body, err := json.Marshal(map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte("x")),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "name is required")
}

func TestArtifactHandler_Upload_JSON_InvalidBase64(t *testing.T) {
	h, e := setupArtifactTest(&MockArtifactService{})
	taskID := uuid.New()

	body, err := json.Marshal(map[string]any{
		"name":    "screenshot.png",
		"content": "not-valid-base64!!",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "base64"), "body: %s", rec.Body.String())
}

func TestArtifactHandler_Upload_Multipart_MissingName(t *testing.T) {
	h, e := setupArtifactTest(&MockArtifactService{})
	taskID := uuid.New()

	req := buildMultipartUploadRequest(t, taskID, nil)
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "name is required")
}

// --- Download handler tests ---

func TestArtifactHandler_Download_Disposition(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantInline bool
	}{
		{name: "default is attachment", query: "", wantInline: false},
		{name: "disposition=inline requests inline", query: "?disposition=inline", wantInline: true},
		{name: "unknown disposition value is not inline", query: "?disposition=bogus", wantInline: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			artifactID := uuid.New()
			var gotInline bool
			mockSvc := &MockArtifactService{
				GetDownloadURLFunc: func(_ context.Context, _ uuid.UUID, inline bool) (string, error) {
					gotInline = inline
					return "https://s3.example.com/presigned", nil
				},
			}

			h, e := setupArtifactTest(mockSvc)

			req := httptest.NewRequest(http.MethodGet, "/"+tt.query, http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/artifacts/:artifact_id/download")
			c.SetParamNames("artifact_id")
			c.SetParamValues(artifactID.String())

			require.NoError(t, h.Download(c))
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantInline, gotInline)
		})
	}
}

// --- List handler tests ---

func TestArtifactHandler_List_StripsTrAgentKey(t *testing.T) {
	taskID := uuid.New()

	mockSvc := &MockArtifactService{
		ListByTaskFunc: func(_ context.Context, tid uuid.UUID, _ pagination.Params) (*pagination.Page[domain.Artifact], error) {
			return &pagination.Page[domain.Artifact]{
				Items: []domain.Artifact{
					{
						ID:       uuid.New(),
						Name:     "file1.txt",
						Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/1","tr_agent_key":"tr_agent_secret1"}`),
					},
					{
						ID:       uuid.New(),
						Name:     "file2.txt",
						Metadata: json.RawMessage(`{"tr_public_url":"https://relay.example.com/2","tr_agent_key":"tr_agent_secret2"}`),
					},
				},
				TotalCount: 2,
			}, nil
		},
	}

	h, e := setupArtifactTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/artifacts")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var result struct {
		Items []struct {
			ID           string         `json:"id"`
			Metadata     map[string]any `json:"metadata"`
			DownloadPath string         `json:"download_path"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Items, 2)

	for i, item := range result.Items {
		assert.NotContains(t, item.Metadata, "tr_agent_key", "item %d: tr_agent_key must not appear in list response", i)
		assert.Equal(t, "/api/v1/artifacts/"+item.ID+"/download", item.DownloadPath,
			"item %d: download_path must be the stable endpoint path for that artifact's own ID", i)
	}
}
