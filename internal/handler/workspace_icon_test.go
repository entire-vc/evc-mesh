package handler

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/entire-vc/evc-mesh/internal/storage"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// fakeIconStorage is a stand-in for object storage that lets a test choose the
// exact failure the storage layer reports.
type fakeIconStorage struct {
	uploadErr error
	getErr    error

	body []byte
	info storage.ObjectInfo

	uploaded     []byte
	uploadedKey  string
	uploadedType string
}

func (f *fakeIconStorage) Upload(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	b, _ := io.ReadAll(r)
	f.uploaded = b
	f.uploadedKey = key
	f.uploadedType = contentType
	return nil
}

func (f *fakeIconStorage) GetObject(_ context.Context, _ string) (io.ReadCloser, storage.ObjectInfo, error) {
	if f.getErr != nil {
		return nil, storage.ObjectInfo{}, f.getErr
	}
	return io.NopCloser(bytes.NewReader(f.body)), f.info, nil
}

func iconWorkspace(id uuid.UUID, key *string) *domain.Workspace {
	return &domain.Workspace{ID: id, Name: "Acme", Slug: "acme", IconStorageKey: key}
}

func setupIconTest(ws *domain.Workspace, st IconStorage) (*WorkspaceHandler, *echo.Echo) {
	svc := &MockWorkspaceService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Workspace, error) { return ws, nil },
		UpdateFunc:  func(_ context.Context, _ *domain.Workspace) error { return nil },
	}
	h := NewWorkspaceHandler(svc)
	if st != nil {
		h.WithStorage(st)
	}
	return h, echo.New()
}

func iconRequest(e *echo.Echo, wsID uuid.UUID, ifNoneMatch string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/icon")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())
	return c, rec
}

// --- GetIcon: streaming ---

func TestGetIcon_StreamsBytesInsteadOfRedirecting(t *testing.T) {
	wsID := uuid.New()
	key := "workspaces/x/icon.png"
	png := []byte("\x89PNG\r\n\x1a\nfake-body")

	st := &fakeIconStorage{
		body: png,
		info: storage.ObjectInfo{Size: int64(len(png)), ContentType: "image/png", ETag: "abc123"},
	}
	h, e := setupIconTest(iconWorkspace(wsID, &key), st)

	c, rec := iconRequest(e, wsID, "")
	require.NoError(t, h.GetIcon(c))

	// A 302 here is the old behaviour: it pointed the browser at the
	// compose-internal MinIO host, which never resolves for a real visitor.
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"), "must not redirect to storage")
	assert.Equal(t, png, rec.Body.Bytes())
	assert.Contains(t, rec.Header().Get("Content-Type"), "image/png")
	assert.Equal(t, `"abc123"`, rec.Header().Get("ETag"))
	assert.Equal(t, iconCacheControl, rec.Header().Get("Cache-Control"))
}

func TestGetIcon_HonoursIfNoneMatch(t *testing.T) {
	wsID := uuid.New()
	key := "workspaces/x/icon.png"
	st := &fakeIconStorage{
		body: []byte("\x89PNGdata"),
		info: storage.ObjectInfo{ContentType: "image/png", ETag: `"abc123"`},
	}
	h, e := setupIconTest(iconWorkspace(wsID, &key), st)

	c, rec := iconRequest(e, wsID, `"abc123"`)
	require.NoError(t, h.GetIcon(c))

	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
}

func TestGetIcon_DefaultsContentTypeWhenStorageOmitsIt(t *testing.T) {
	wsID := uuid.New()
	key := "workspaces/x/icon.png"
	st := &fakeIconStorage{body: []byte("\x89PNG"), info: storage.ObjectInfo{}}
	h, e := setupIconTest(iconWorkspace(wsID, &key), st)

	c, rec := iconRequest(e, wsID, "")
	require.NoError(t, h.GetIcon(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "image/png")
	assert.Empty(t, rec.Header().Get("ETag"), "no ETag from storage means no ETag header")
}

// --- GetIcon: error paths ---

func TestGetIcon_NoIconIs404(t *testing.T) {
	wsID := uuid.New()
	h, e := setupIconTest(iconWorkspace(wsID, nil), &fakeIconStorage{})

	c, rec := iconRequest(e, wsID, "")
	require.NoError(t, h.GetIcon(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGetIcon_MissingObjectIs404NotServerError(t *testing.T) {
	wsID := uuid.New()
	key := "workspaces/x/icon.png"
	st := &fakeIconStorage{getErr: fmt.Errorf("wrapped: %w", storage.ErrNotFound)}
	h, e := setupIconTest(iconWorkspace(wsID, &key), st)

	c, rec := iconRequest(e, wsID, "")
	require.NoError(t, h.GetIcon(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestGetIcon_AllNotFoundOutcomesAreByteIdentical is the unit-level half of the
// existence-oracle guard in tests/integration/cross_tenant_test.go. This route is
// readable without authentication, so an unknown workspace must not be
// distinguishable from a workspace that simply has no icon — otherwise anyone
// could probe ids and learn which workspaces are real.
func TestGetIcon_AllNotFoundOutcomesAreByteIdentical(t *testing.T) {
	wsID := uuid.New()
	key := "workspaces/x/icon.png"

	cases := []struct {
		name string
		ws   *domain.Workspace
		err  error
		st   IconStorage
	}{
		// The workspace does not exist at all.
		{"unknown workspace", nil, apierror.NotFound("Workspace"), &fakeIconStorage{}},
		// The workspace exists but never had an icon.
		{"workspace without icon", iconWorkspace(wsID, nil), nil, &fakeIconStorage{}},
		// The row points at an object the bucket no longer holds.
		{"stored key is gone", iconWorkspace(wsID, &key), nil,
			&fakeIconStorage{getErr: fmt.Errorf("gone: %w", storage.ErrNotFound)}},
	}

	bodies := make(map[string]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &MockWorkspaceService{
				GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Workspace, error) {
					return tc.ws, tc.err
				},
			}
			h := NewWorkspaceHandler(svc)
			h.WithStorage(tc.st)

			c, rec := iconRequest(echo.New(), wsID, "")
			require.NoError(t, h.GetIcon(c))

			assert.Equal(t, http.StatusNotFound, rec.Code)
			bodies[tc.name] = rec.Body.String()
		})
	}

	require.Len(t, bodies, len(cases))
	var first string
	for _, tc := range cases {
		if first == "" {
			first = bodies[tc.name]
			continue
		}
		assert.Equal(t, first, bodies[tc.name],
			"%q answers differently from the other not-found cases — that turns this "+
				"public route into an existence oracle", tc.name)
	}
}

func TestGetIcon_StorageDownReportsWhatIsWrong(t *testing.T) {
	wsID := uuid.New()
	key := "workspaces/x/icon.png"
	st := &fakeIconStorage{getErr: fmt.Errorf("dial tcp: %w", storage.ErrUnreachable)}
	h, e := setupIconTest(iconWorkspace(wsID, &key), st)

	c, rec := iconRequest(e, wsID, "")
	require.NoError(t, h.GetIcon(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "S3_ENDPOINT")
}

func TestGetIcon_NoStorageConfiguredIs503(t *testing.T) {
	wsID := uuid.New()
	key := "workspaces/x/icon.png"
	h, e := setupIconTest(iconWorkspace(wsID, &key), nil)

	c, rec := iconRequest(e, wsID, "")
	require.NoError(t, h.GetIcon(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestGetIcon_InvalidWorkspaceIDIs400(t *testing.T) {
	h, e := setupIconTest(iconWorkspace(uuid.New(), nil), &fakeIconStorage{})

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/icon")
	c.SetParamNames("ws_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.GetIcon(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- UploadIcon: the error text an operator actually reads ---

func uploadIconRequest(t *testing.T, e *echo.Echo, wsID uuid.UUID, content []byte) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "icon.png")
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPut, "/", &buf)
	req.Header.Set(echo.HeaderContentType, mw.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/icon")
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())
	return c, rec
}

func TestUploadIcon_Success(t *testing.T) {
	wsID := uuid.New()
	st := &fakeIconStorage{}
	h, e := setupIconTest(iconWorkspace(wsID, nil), st)

	png := []byte("\x89PNG\r\n\x1a\npayload")
	c, rec := uploadIconRequest(t, e, wsID, png)
	require.NoError(t, h.UploadIcon(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, png, st.uploaded, "the full file must reach storage, header bytes included")
	assert.Equal(t, fmt.Sprintf("workspaces/%s/icon.png", wsID), st.uploadedKey)
	assert.Equal(t, "image/png", st.uploadedType)
}

func TestUploadIcon_ErrorsNameTheActualFault(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\npayload")

	tests := []struct {
		name      string
		storErr   error
		mustNotBe string
		wants     []string
	}{
		{
			name:      "missing bucket",
			storErr:   fmt.Errorf("creating bucket: %w", storage.ErrBucketMissing),
			mustNotBe: "upload failed",
			wants:     []string{"bucket", "S3_BUCKET"},
		},
		{
			name:      "bad credentials",
			storErr:   fmt.Errorf("put object: %w", storage.ErrAccessDenied),
			mustNotBe: "upload failed",
			wants:     []string{"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"},
		},
		{
			name:      "storage down",
			storErr:   fmt.Errorf("dial tcp: %w", storage.ErrUnreachable),
			mustNotBe: "upload failed",
			wants:     []string{"unreachable", "S3_ENDPOINT"},
		},
		{
			name:      "unrecognised failure still points at the log",
			storErr:   fmt.Errorf("some brand new S3 error"),
			mustNotBe: "upload failed",
			wants:     []string{"API log"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wsID := uuid.New()
			st := &fakeIconStorage{uploadErr: tt.storErr}
			h, e := setupIconTest(iconWorkspace(wsID, nil), st)

			c, rec := uploadIconRequest(t, e, wsID, png)
			require.NoError(t, h.UploadIcon(c))

			assert.Equal(t, http.StatusInternalServerError, rec.Code)
			body := rec.Body.String()
			for _, want := range tt.wants {
				assert.Contains(t, body, want, "operator-facing message must mention %q", want)
			}
			// The old message was the same three words for every cause.
			assert.NotEqual(t, `{"code":500,"message":"upload failed"}`, strings.TrimSpace(body))
		})
	}
}

func TestUploadIcon_RejectsNonPNG(t *testing.T) {
	wsID := uuid.New()
	st := &fakeIconStorage{}
	h, e := setupIconTest(iconWorkspace(wsID, nil), st)

	c, rec := uploadIconRequest(t, e, wsID, []byte("GIF89a-not-a-png"))
	require.NoError(t, h.UploadIcon(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, st.uploaded)
}

func TestUploadIcon_NoStorageConfiguredIs503(t *testing.T) {
	wsID := uuid.New()
	h, e := setupIconTest(iconWorkspace(wsID, nil), nil)

	c, rec := uploadIconRequest(t, e, wsID, []byte("\x89PNGx"))
	require.NoError(t, h.UploadIcon(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
