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
	"os"
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
// that tr_public_url itself is unchanged by this addition (an internal ticket:
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

// --- Upload handler tests ---

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

// --- Size limit ---

func buildSizedMultipartRequest(t *testing.T, taskID uuid.UUID, size int, chunked bool) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("name", "bundle.tar.gz"))
	fw, err := w.CreateFormFile("file", "bundle.tar.gz")
	require.NoError(t, err)
	_, err = fw.Write(bytes.Repeat([]byte("x"), size))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var body io.Reader = &buf
	if chunked {
		// Hide the length so the cap has to come from reading the body.
		body = io.MultiReader(&buf)
	}
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", body)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	if chunked {
		req.ContentLength = -1
	}
	return req
}

func TestArtifactHandler_Upload_BodyOverLimit_413(t *testing.T) {
	const limit = 1 << 10
	over := limit + uploadBodySlack + 1

	cases := []struct {
		name  string
		build func(t *testing.T, taskID uuid.UUID) *http.Request
	}{
		{"multipart with content-length", func(t *testing.T, taskID uuid.UUID) *http.Request {
			return buildSizedMultipartRequest(t, taskID, over, false)
		}},
		{"multipart chunked", func(t *testing.T, taskID uuid.UUID) *http.Request {
			return buildSizedMultipartRequest(t, taskID, over, true)
		}},
		{"json chunked", func(t *testing.T, taskID uuid.UUID) *http.Request {
			payload, err := json.Marshal(map[string]string{
				"name":    "bundle.tar.gz",
				"content": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), over)),
			})
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", io.MultiReader(bytes.NewReader(payload)))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.ContentLength = -1
			return req
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			mockSvc := &MockArtifactService{
				UploadFunc: func(_ context.Context, _ service.UploadArtifactInput) (*domain.Artifact, error) {
					called = true
					return &domain.Artifact{ID: uuid.New()}, nil
				},
			}
			h, e := setupArtifactTest(mockSvc)
			h.WithMaxFileBytes(limit)
			taskID := uuid.New()
			c, rec := newArtifactUploadContext(e, tc.build(t, taskID), taskID)

			require.NoError(t, h.Upload(c))
			assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "body: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), "1024 bytes per file")
			assert.Contains(t, rec.Body.String(), "git or CI artifacts")
			assert.False(t, called, "an oversized body must not reach the service")
		})
	}
}

func TestArtifactHandler_Upload_FileAtLimitReachesService(t *testing.T) {
	const limit = 1 << 10
	var gotSize int64
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
			gotSize = input.Size
			return &domain.Artifact{ID: uuid.New()}, nil
		},
	}
	h, e := setupArtifactTest(mockSvc)
	h.WithMaxFileBytes(limit)
	taskID := uuid.New()
	c, rec := newArtifactUploadContext(e, buildSizedMultipartRequest(t, taskID, limit, true), taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	assert.EqualValues(t, limit, gotSize)
}

func TestArtifactHandler_Upload_ServiceLimitIs413(t *testing.T) {
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, _ service.UploadArtifactInput) (*domain.Artifact, error) {
			return nil, service.ArtifactTooLargeError("task already holds too much", 262144000)
		},
	}
	h, e := setupArtifactTest(mockSvc)
	taskID := uuid.New()
	c, rec := newArtifactUploadContext(e, buildMultipartUploadRequest(t, taskID, map[string]string{"name": "a.png"}), taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), `"limit_bytes":262144000`)
}

// A file one byte over the limit fits inside the body cap (limit + slack), so
// only the streamed file part can refuse it: before any service call and
// without leaving a spooled copy behind.
func TestArtifactHandler_Upload_FileOverLimitWithinBodyCap_413(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	const limit = 1 << 10
	called := false
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, _ service.UploadArtifactInput) (*domain.Artifact, error) {
			called = true
			return &domain.Artifact{ID: uuid.New()}, nil
		},
	}
	h, e := setupArtifactTest(mockSvc)
	h.WithMaxFileBytes(limit)
	taskID := uuid.New()
	c, rec := newArtifactUploadContext(e, buildSizedMultipartRequest(t, taskID, limit+1, false), taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "1024 bytes per file")
	assert.False(t, called, "an over-limit file must not reach the service")
	left, err := os.ReadDir(tmp)
	require.NoError(t, err)
	assert.Empty(t, left, "the spooled part must be removed")
}

// Fields may follow the file part; the streamed reader must still see them.
func TestArtifactHandler_Upload_Multipart_FieldsAfterFile(t *testing.T) {
	var got service.UploadArtifactInput
	var content []byte
	mockSvc := &MockArtifactService{
		UploadFunc: func(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
			got = input
			content, _ = io.ReadAll(input.Reader)
			return &domain.Artifact{ID: uuid.New()}, nil
		},
	}
	h, e := setupArtifactTest(mockSvc)
	taskID := uuid.New()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "notes.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, w.WriteField("name", "notes.txt"))
	require.NoError(t, w.WriteField("artifact_type", "report"))
	require.NoError(t, w.Close())
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/artifacts", &buf)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	c, rec := newArtifactUploadContext(e, req, taskID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "notes.txt", got.Name)
	assert.Equal(t, domain.ArtifactTypeReport, got.ArtifactType)
	assert.EqualValues(t, 5, got.Size)
	assert.Equal(t, "hello", string(content))
}

func TestArtifactHandler_Upload_Multipart_MissingFileAndBadMetadata(t *testing.T) {
	h, e := setupArtifactTest(&MockArtifactService{})
	taskID := uuid.New()

	build := func(fields map[string]string, withFile bool) *http.Request {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for k, v := range fields {
			require.NoError(t, w.WriteField(k, v))
		}
		if withFile {
			fw, err := w.CreateFormFile("file", "a.txt")
			require.NoError(t, err)
			_, _ = fw.Write([]byte("x"))
		}
		require.NoError(t, w.Close())
		req := httptest.NewRequest(http.MethodPost, "/", &buf)
		req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
		return req
	}

	c, rec := newArtifactUploadContext(e, build(map[string]string{"name": "a.txt"}, false), taskID)
	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "file is required")

	c, rec = newArtifactUploadContext(e, build(map[string]string{"name": "a.txt", "metadata": "{"}, true), taskID)
	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "metadata must be valid JSON")
}
