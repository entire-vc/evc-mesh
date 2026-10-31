package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// setupArtifactService returns an artifactService wired to fresh mocks.
func setupArtifactService() (*artifactService, *MockArtifactRepository, *MockStorageClient) {
	artifactRepo := NewMockArtifactRepository()
	storage := NewMockStorageClient()
	activityRepo := NewMockActivityLogRepository()
	svc := NewArtifactService(artifactRepo, storage, activityRepo).(*artifactService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, artifactRepo, storage
}

// ---------------------------------------------------------------------------
// TestArtifactService_Upload
// ---------------------------------------------------------------------------

func TestArtifactService_Upload(t *testing.T) {
	tests := []struct {
		name      string
		input     UploadArtifactInput
		wantErr   bool
		checkFunc func(t *testing.T, artifact *domain.Artifact, storage *MockStorageClient, repo *MockArtifactRepository)
	}{
		{
			name: "success",
			input: UploadArtifactInput{
				TaskID:         uuid.New(),
				Name:           "report.pdf",
				ArtifactType:   domain.ArtifactTypeReport,
				MimeType:       "application/pdf",
				UploadedBy:     uuid.New(),
				UploadedByType: domain.UploaderTypeUser,
				Reader:         strings.NewReader("fake pdf content"),
				Size:           16,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, artifact *domain.Artifact, storage *MockStorageClient, repo *MockArtifactRepository) {
				assert.NotEqual(t, uuid.Nil, artifact.ID)
				assert.Equal(t, "report.pdf", artifact.Name)
				assert.Equal(t, domain.ArtifactTypeReport, artifact.ArtifactType)
				assert.Equal(t, "application/pdf", artifact.MimeType)
				assert.Equal(t, int64(16), artifact.SizeBytes)
				assert.NotEmpty(t, artifact.StorageKey)
				assert.Equal(t, frozenTime, artifact.CreatedAt)

				// Verify persisted in repo.
				stored := repo.items[artifact.ID]
				require.NotNil(t, stored)

				// Verify uploaded to storage.
				assert.Contains(t, storage.objects, artifact.StorageKey)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, artifactRepo, storage := setupArtifactService()
			ctx := context.Background()

			artifact, err := svc.Upload(ctx, tt.input)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, artifact)
			} else {
				require.NoError(t, err)
				require.NotNil(t, artifact)
				if tt.checkFunc != nil {
					tt.checkFunc(t, artifact, storage, artifactRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestArtifactService_Upload_RelayPublishWritesPublicURL
// ---------------------------------------------------------------------------

// stubRelayPublisher returns a fixed public URL / agent key and records that it was called.
// err simulates a transport-level publish failure (error/timeout); the service must
// not persist any TR metadata in that case.
type stubRelayPublisher struct {
	mu        sync.Mutex
	publicURL string
	agentKey  string
	err       error
	called    bool
}

func (s *stubRelayPublisher) Publish(_ context.Context, _ uuid.UUID, _ string, _ []byte, _ string) (publicURL, agentKey string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called = true
	return s.publicURL, s.agentKey, s.err
}

func (s *stubRelayPublisher) wasCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

func TestArtifactService_Upload_RelayPublishWritesPublicURL(t *testing.T) {
	t.Run("public url is merged into metadata", func(t *testing.T) {
		svc, artifactRepo, _ := setupArtifactService()
		pub := &stubRelayPublisher{publicURL: "https://relay.example.com/mesh/foo.md"}
		svc.SetRelayPublisher(pub)
		ctx := context.Background()

		artifact, err := svc.Upload(ctx, UploadArtifactInput{
			TaskID:         uuid.New(),
			Name:           "doc.md",
			ArtifactType:   domain.ArtifactTypeReport,
			MimeType:       "text/markdown",
			UploadedBy:     uuid.New(),
			UploadedByType: domain.UploaderTypeUser,
			Reader:         strings.NewReader("# hello"),
			Size:           7,
		})
		require.NoError(t, err)
		require.NotNil(t, artifact)

		// Relay publish + metadata write happen on a background goroutine.
		assert.Eventually(t, func() bool {
			md := artifactRepo.MetadataOf(artifact.ID)
			if len(md) == 0 {
				return false
			}
			var m map[string]any
			if jErr := json.Unmarshal(md, &m); jErr != nil {
				return false
			}
			return m["tr_public_url"] == "https://relay.example.com/mesh/foo.md"
		}, 2*time.Second, 10*time.Millisecond, "tr_public_url should be persisted to metadata")
	})

	// an internal ticket: a confirmed publish must also stamp tr_published=true so the
	// frontend can gate the "Open in Team Relay" button on publication being real,
	// not on a URL the relay optimistically constructed. RED control: on the code
	// before this change tr_public_url was written without any tr_published flag.
	t.Run("confirmed publish persists tr_published=true alongside tr_public_url", func(t *testing.T) {
		svc, artifactRepo, _ := setupArtifactService()
		pub := &stubRelayPublisher{publicURL: "https://relay.example.com/mesh/foo.md"}
		svc.SetRelayPublisher(pub)
		ctx := context.Background()

		artifact, err := svc.Upload(ctx, UploadArtifactInput{
			TaskID:         uuid.New(),
			Name:           "doc.md",
			ArtifactType:   domain.ArtifactTypeReport,
			MimeType:       "text/markdown",
			UploadedBy:     uuid.New(),
			UploadedByType: domain.UploaderTypeUser,
			Reader:         strings.NewReader("# hello"),
			Size:           7,
		})
		require.NoError(t, err)
		require.NotNil(t, artifact)

		assert.Eventually(t, func() bool {
			md := artifactRepo.MetadataOf(artifact.ID)
			if len(md) == 0 {
				return false
			}
			var m map[string]any
			if jErr := json.Unmarshal(md, &m); jErr != nil {
				return false
			}
			return m["tr_public_url"] == "https://relay.example.com/mesh/foo.md" &&
				m["tr_published"] == true
		}, 2*time.Second, 10*time.Millisecond, "confirmed publish must persist tr_public_url and tr_published=true")
	})

	// an internal ticket: publish error/timeout means the publication is NOT confirmed —
	// no TR metadata may be written, not even the URL the publisher reported before
	// failing. RED control: the pre-fix code ignored the Publish error and persisted
	// a non-empty publicURL anyway.
	t.Run("publish error leaves metadata without TR keys", func(t *testing.T) {
		svc, artifactRepo, _ := setupArtifactService()
		pub := &stubRelayPublisher{
			publicURL: "https://relay.example.com/mesh/foo.md",
			err:       errors.New("relay sync-upload timeout"),
		}
		svc.SetRelayPublisher(pub)
		ctx := context.Background()

		artifact, err := svc.Upload(ctx, UploadArtifactInput{
			TaskID:         uuid.New(),
			Name:           "doc.md",
			ArtifactType:   domain.ArtifactTypeReport,
			MimeType:       "text/markdown",
			UploadedBy:     uuid.New(),
			UploadedByType: domain.UploaderTypeUser,
			Reader:         strings.NewReader("# hello"),
			Size:           7,
		})
		require.NoError(t, err)

		// Wait for the publish attempt itself, then give the (forbidden) metadata
		// write 500ms to appear — absence asserted with Never so the test cannot
		// pass by checking before a wrong write lands.
		assert.Eventually(t, pub.wasCalled, 2*time.Second, 10*time.Millisecond)
		assert.Never(t, func() bool {
			var m map[string]any
			if jErr := json.Unmarshal(artifactRepo.MetadataOf(artifact.ID), &m); jErr != nil {
				return false
			}
			_, hasURL := m["tr_public_url"]
			_, hasFlag := m["tr_published"]
			return hasURL || hasFlag
		}, 500*time.Millisecond, 25*time.Millisecond, "no TR metadata may be written when Publish returns an error")
	})

	// Flipped from "agent key is merged into metadata alongside public url", which
	// asserted the behaviour this test now forbids. The share's agent key is a
	// long-lived credential encrypted at rest in project_integrations; persisting
	// a plaintext copy on every artifact made the weakest copy the one that
	// defined its protection, and one API read path served it unredacted.
	t.Run("agent key is never persisted to metadata even when the publisher returns one", func(t *testing.T) {
		svc, artifactRepo, _ := setupArtifactService()
		pub := &stubRelayPublisher{
			publicURL: "https://relay.example.com/mesh/foo.md",
			agentKey:  "tr_agent_abc123",
		}
		svc.SetRelayPublisher(pub)
		ctx := context.Background()

		artifact, err := svc.Upload(ctx, UploadArtifactInput{
			TaskID:         uuid.New(),
			Name:           "doc.md",
			ArtifactType:   domain.ArtifactTypeReport,
			MimeType:       "text/markdown",
			UploadedBy:     uuid.New(),
			UploadedByType: domain.UploaderTypeUser,
			Reader:         strings.NewReader("# hello"),
			Size:           7,
		})
		require.NoError(t, err)
		require.NotNil(t, artifact)

		// Wait on the public URL, not on the key: waiting for the key would only
		// ever time out, so the assertion below must run after a state we can
		// positively observe — otherwise it would pass against a publish that
		// never happened at all.
		assert.Eventually(t, func() bool {
			md := artifactRepo.MetadataOf(artifact.ID)
			if len(md) == 0 {
				return false
			}
			var m map[string]any
			if jErr := json.Unmarshal(md, &m); jErr != nil {
				return false
			}
			return m["tr_public_url"] == "https://relay.example.com/mesh/foo.md"
		}, 2*time.Second, 10*time.Millisecond, "tr_public_url should be persisted to metadata")

		var m map[string]any
		require.NoError(t, json.Unmarshal(artifactRepo.MetadataOf(artifact.ID), &m))
		assert.NotContains(t, m, "tr_agent_key",
			"the share agent key must never be written to artifact metadata")
		assert.NotContains(t, string(artifactRepo.MetadataOf(artifact.ID)), "tr_agent_abc123",
			"the credential value must not appear in metadata under any key")
	})

	t.Run("no agent key leaves tr_agent_key absent from metadata", func(t *testing.T) {
		svc, artifactRepo, _ := setupArtifactService()
		pub := &stubRelayPublisher{
			publicURL: "https://relay.example.com/public/foo.md",
			agentKey:  "", // public share — no key
		}
		svc.SetRelayPublisher(pub)
		ctx := context.Background()

		artifact, err := svc.Upload(ctx, UploadArtifactInput{
			TaskID:         uuid.New(),
			Name:           "doc.md",
			ArtifactType:   domain.ArtifactTypeReport,
			MimeType:       "text/markdown",
			UploadedBy:     uuid.New(),
			UploadedByType: domain.UploaderTypeUser,
			Reader:         strings.NewReader("# hello"),
			Size:           7,
		})
		require.NoError(t, err)

		assert.Eventually(t, pub.wasCalled, 2*time.Second, 10*time.Millisecond)
		md := artifactRepo.MetadataOf(artifact.ID)
		if len(md) > 0 {
			var m map[string]any
			require.NoError(t, json.Unmarshal(md, &m))
			_, hasKey := m["tr_agent_key"]
			assert.False(t, hasKey, "tr_agent_key should not be set for public shares")
			assert.Equal(t, "https://relay.example.com/public/foo.md", m["tr_public_url"])
		}
	})

	t.Run("empty public url leaves metadata without the key", func(t *testing.T) {
		svc, artifactRepo, _ := setupArtifactService()
		pub := &stubRelayPublisher{publicURL: ""} // relay reported no URL
		svc.SetRelayPublisher(pub)
		ctx := context.Background()

		artifact, err := svc.Upload(ctx, UploadArtifactInput{
			TaskID:         uuid.New(),
			Name:           "doc.md",
			ArtifactType:   domain.ArtifactTypeReport,
			MimeType:       "text/markdown",
			UploadedBy:     uuid.New(),
			UploadedByType: domain.UploaderTypeUser,
			Reader:         strings.NewReader("# hello"),
			Size:           7,
		})
		require.NoError(t, err)

		// Wait for the goroutine to run, then assert no tr_public_url was written.
		assert.Eventually(t, pub.wasCalled, 2*time.Second, 10*time.Millisecond)
		md := artifactRepo.MetadataOf(artifact.ID)
		if len(md) > 0 {
			var m map[string]any
			require.NoError(t, json.Unmarshal(md, &m))
			_, has := m["tr_public_url"]
			assert.False(t, has, "tr_public_url should not be set when relay returns empty URL")
			_, hasFlag := m["tr_published"]
			assert.False(t, hasFlag, "tr_published should not be set for a private/unpublished share")
		}
	})
}

// ---------------------------------------------------------------------------
// TestArtifactService_GetByID
// ---------------------------------------------------------------------------

func TestArtifactService_GetByID(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockArtifactRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "found",
			setup: func(repo *MockArtifactRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Artifact{ID: id, Name: "test.txt"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found",
			setup: func(_ *MockArtifactRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, artifactRepo, _ := setupArtifactService()
			ctx := context.Background()
			id := tt.setup(artifactRepo)

			artifact, err := svc.GetByID(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Nil(t, artifact)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, artifact)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestArtifactService_GetByIDInWorkspace — IDOR isolation check
// ---------------------------------------------------------------------------

func TestArtifactService_GetByIDInWorkspace(t *testing.T) {
	wsA := uuid.New()
	wsB := uuid.New()

	tests := []struct {
		name        string
		setup       func(repo *MockArtifactRepository) uuid.UUID
		queryWs     uuid.UUID
		wantErr     bool
		wantErrCode int
	}{
		{
			name: "correct workspace returns artifact",
			setup: func(repo *MockArtifactRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Artifact{ID: id, Name: "report.md"}
				repo.SetWorkspace(id, wsA)
				return id
			},
			queryWs: wsA,
			wantErr: false,
		},
		{
			name: "wrong workspace returns 404 (IDOR blocked)",
			setup: func(repo *MockArtifactRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Artifact{ID: id, Name: "secret.txt"}
				repo.SetWorkspace(id, wsA)
				return id
			},
			queryWs:     wsB,
			wantErr:     true,
			wantErrCode: http.StatusNotFound,
		},
		{
			name: "artifact not found at all",
			setup: func(_ *MockArtifactRepository) uuid.UUID {
				return uuid.New()
			},
			queryWs:     wsA,
			wantErr:     true,
			wantErrCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, _ := setupArtifactService()
			ctx := context.Background()
			id := tt.setup(repo)

			artifact, err := svc.GetByIDInWorkspace(ctx, id, tt.queryWs)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.wantErrCode, apiErr.Code)
				assert.Nil(t, artifact)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, artifact)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestArtifactService_GetDownloadURL
// ---------------------------------------------------------------------------

func TestArtifactService_GetDownloadURL(t *testing.T) {
	tests := []struct {
		name    string
		inline  bool
		setup   func(repo *MockArtifactRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "success",
			setup: func(repo *MockArtifactRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.Artifact{ID: id, Name: "test.txt", StorageKey: "ws/task/id/test.txt"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found",
			setup: func(_ *MockArtifactRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, artifactRepo, _ := setupArtifactService()
			ctx := context.Background()
			id := tt.setup(artifactRepo)

			url, err := svc.GetDownloadURL(ctx, id, tt.inline)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				assert.Empty(t, url)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, url)
				assert.Contains(t, url, "https://s3.example.com/")
			}
		})
	}
}

func TestArtifactService_GetDownloadURL_Disposition(t *testing.T) {
	svc, artifactRepo, storage := setupArtifactService()
	ctx := context.Background()
	id := uuid.New()
	artifactRepo.items[id] = &domain.Artifact{ID: id, Name: "test.png", StorageKey: "ws/task/id/test.png", MimeType: "image/png"}

	_, err := svc.GetDownloadURL(ctx, id, false)
	require.NoError(t, err)
	assert.Equal(t, "test.png", storage.lastFilename, "attachment download must set Content-Disposition via filename")

	_, err = svc.GetDownloadURL(ctx, id, true)
	require.NoError(t, err)
	assert.Empty(t, storage.lastFilename, "inline preview of a safe-listed type must omit filename so no Content-Disposition header is set")
}

// TestArtifactService_GetDownloadURL_HTMLNeverInlines is the regression test for
// an internal ticket: the artifact bucket is proxied through our own origin, so serving
// text/html without Content-Disposition: attachment lets it execute as a page
// on mesh.entire.host with the reader's session. inline=true must not bypass
// that for any type outside the explicit safe-list, no matter what the caller
// requests — this asserts the negative (still forced to attachment) so the
// test would have failed against the pre-fix code, which honoured inline
// unconditionally.
func TestArtifactService_GetDownloadURL_HTMLNeverInlines(t *testing.T) {
	svc, artifactRepo, storage := setupArtifactService()
	ctx := context.Background()
	id := uuid.New()
	artifactRepo.items[id] = &domain.Artifact{ID: id, Name: "report.html", StorageKey: "ws/task/id/report.html", MimeType: "text/html; charset=utf-8"}

	_, err := svc.GetDownloadURL(ctx, id, true)
	require.NoError(t, err)
	assert.Equal(t, "report.html", storage.lastFilename, "text/html must always get Content-Disposition: attachment, even when inline=true was requested")
}

// TestArtifactService_GetDownloadURL_UnknownMimeTypeNeverInlines guards the
// default: a type not on the explicit allowlist (including an artifact with
// no MimeType recorded at all) must download, not render. The allowlist must
// stay opt-in, never opt-out.
func TestArtifactService_GetDownloadURL_UnknownMimeTypeNeverInlines(t *testing.T) {
	svc, artifactRepo, storage := setupArtifactService()
	ctx := context.Background()
	id := uuid.New()
	artifactRepo.items[id] = &domain.Artifact{ID: id, Name: "data.bin", StorageKey: "ws/task/id/data.bin"}

	_, err := svc.GetDownloadURL(ctx, id, true)
	require.NoError(t, err)
	assert.Equal(t, "data.bin", storage.lastFilename, "a MIME type absent from the allowlist must always download")
}

// ---------------------------------------------------------------------------
// TestIsInlineSafeMimeType
// ---------------------------------------------------------------------------

func TestIsInlineSafeMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		want     bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"application/pdf", true},
		{"image/png; charset=binary", true},
		{"IMAGE/PNG", true},
		{"text/html", false},
		{"text/html; charset=utf-8", false},
		{"text/plain", false},
		{"text/markdown", false},
		// SVG carries its own <script>/event-handler surface — it must NOT be
		// treated as passive raster data despite the image/ prefix.
		{"image/svg+xml", false},
		{"application/javascript", false},
		{"application/xml", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			assert.Equal(t, tt.want, isInlineSafeMimeType(tt.mimeType))
		})
	}
}

// ---------------------------------------------------------------------------
// TestArtifactService_Delete
// ---------------------------------------------------------------------------

func TestArtifactService_Delete(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockArtifactRepository, storage *MockStorageClient) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "success",
			setup: func(repo *MockArtifactRepository, storage *MockStorageClient) uuid.UUID {
				id := uuid.New()
				key := "ws/task/id/file.txt"
				repo.items[id] = &domain.Artifact{ID: id, Name: "file.txt", StorageKey: key}
				storage.objects[key] = []byte("content")
				return id
			},
			wantErr: false,
		},
		{
			name: "not found",
			setup: func(_ *MockArtifactRepository, _ *MockStorageClient) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, artifactRepo, storage := setupArtifactService()
			ctx := context.Background()
			id := tt.setup(artifactRepo, storage)

			err := svc.Delete(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				_, exists := artifactRepo.items[id]
				assert.False(t, exists, "artifact should be deleted from repo")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestArtifactService_ListByTask
// ---------------------------------------------------------------------------

func TestArtifactService_ListByTask(t *testing.T) {
	svc, artifactRepo, _ := setupArtifactService()
	ctx := context.Background()

	taskID := uuid.New()
	for i := 0; i < 3; i++ {
		id := uuid.New()
		artifactRepo.items[id] = &domain.Artifact{ID: id, TaskID: taskID, Name: "file"}
	}
	// Another task.
	otherID := uuid.New()
	artifactRepo.items[otherID] = &domain.Artifact{ID: otherID, TaskID: uuid.New(), Name: "other"}

	page, err := svc.ListByTask(ctx, taskID, pagination.Params{Page: 1, PageSize: 50})

	require.NoError(t, err)
	require.NotNil(t, page)
	assert.Len(t, page.Items, 3)
}

// ---------------------------------------------------------------------------
// TestArtifactService_Upload_SizeLimits
// ---------------------------------------------------------------------------

func sizedUpload(taskID uuid.UUID, size int) UploadArtifactInput {
	return UploadArtifactInput{
		TaskID:         taskID,
		Name:           "bundle.tar.gz",
		ArtifactType:   domain.ArtifactTypeFile,
		MimeType:       "application/gzip",
		UploadedBy:     uuid.New(),
		UploadedByType: domain.UploaderTypeAgent,
		Reader:         strings.NewReader(strings.Repeat("x", size)),
		Size:           int64(size),
	}
}

func requireTooLarge(t *testing.T, err error, limit int64) {
	t.Helper()
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusRequestEntityTooLarge, apiErr.Code)
	assert.Equal(t, limit, apiErr.Context["limit_bytes"])
	assert.Contains(t, apiErr.Message, fmt.Sprint(limit))
	assert.Contains(t, apiErr.Details, "git or CI artifacts")
}

func TestArtifactService_Upload_SizeLimits(t *testing.T) {
	newSvc := func() (*artifactService, *MockStorageClient) {
		svc, _, storage := setupArtifactService()
		WithArtifactLimits(10, 25)(svc)
		return svc, storage
	}

	t.Run("exactly the file limit is accepted", func(t *testing.T) {
		svc, storage := newSvc()
		a, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 10))
		require.NoError(t, err)
		assert.Contains(t, storage.objects, a.StorageKey)
	})

	t.Run("file limit plus one is refused before storage", func(t *testing.T) {
		svc, storage := newSvc()
		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 11))
		requireTooLarge(t, err, 10)
		assert.Empty(t, storage.objects)
	})

	t.Run("task total up to the limit is accepted, one byte more is refused", func(t *testing.T) {
		svc, storage := newSvc()
		taskID := uuid.New()
		for _, size := range []int{10, 10, 5} { // 25 = the task limit
			_, err := svc.Upload(context.Background(), sizedUpload(taskID, size))
			require.NoError(t, err)
		}
		_, err := svc.Upload(context.Background(), sizedUpload(taskID, 1))
		requireTooLarge(t, err, 25)
		assert.Len(t, storage.objects, 3)

		// Another task's quota is its own.
		_, err = svc.Upload(context.Background(), sizedUpload(uuid.New(), 10))
		require.NoError(t, err)
	})

	t.Run("a task filled by another upload refuses before storage", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		taskID := uuid.New()
		// Another upload in flight already holds the room.
		repo.reservations[uuid.New()] = domain.Artifact{TaskID: taskID, SizeBytes: 25}

		_, err := svc.Upload(context.Background(), sizedUpload(taskID, 1))
		requireTooLarge(t, err, 25)
		assert.Empty(t, storage.objects, "a refused upload must never reach storage")
		assert.Empty(t, repo.items)
		assert.Len(t, repo.reservations, 1)
	})

	t.Run("the artifact is recorded only after its object is stored", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		taskID := uuid.New()
		svc.storage = visibilityCheckingStorage{storage, repo, t}

		a, err := svc.Upload(context.Background(), sizedUpload(taskID, 10))
		require.NoError(t, err)
		assert.Contains(t, repo.items, a.ID)
		assert.Empty(t, repo.reservations, "the reservation passes to the artifact")
	})

	t.Run("options passed to the constructor apply; non-positive keeps the default", func(t *testing.T) {
		svc := NewArtifactService(NewMockArtifactRepository(), NewMockStorageClient(), NewMockActivityLogRepository(),
			WithArtifactLimits(7, 0)).(*artifactService)
		assert.EqualValues(t, 7, svc.maxFileBytes)
		assert.Equal(t, DefaultArtifactTaskMaxBytes, svc.maxTaskBytes)
	})

	t.Run("a failing reservation is returned before any upload", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		repo.errToReturn = errors.New("db down")
		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 1))
		require.EqualError(t, err, "db down")
		assert.Empty(t, storage.objects)
	})

	t.Run("an upload that outlived its reservation is not recorded", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		// The reservation expires and is purged while the object is uploading.
		svc.storage = expiringUploadStorage{storage, repo}

		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 1))
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusInternalServerError, apiErr.Code)
		assert.Contains(t, apiErr.Message, "longer than its reservation")
		assert.Empty(t, repo.items, "its room may already belong to another upload")
		assert.Empty(t, storage.objects)
	})

	t.Run("the upload is cut off when its reservation expires", func(t *testing.T) {
		svc, _, storage := newSvcWithRepo()
		deadlines := &deadlineRecordingStorage{MockStorageClient: storage}
		svc.storage = deadlines

		before := time.Now()
		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 1))
		require.NoError(t, err)
		require.False(t, deadlines.deadline.IsZero(), "the upload must run under a deadline")
		assert.WithinDuration(t, before.Add(artifactReservationTTL), deadlines.deadline, 5*time.Second)
	})

	t.Run("a storage failure is a 500 and releases the reservation", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		storage.errToReturn = errors.New("minio down")
		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 1))
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusInternalServerError, apiErr.Code)
		assert.Empty(t, repo.items, "no artifact without its object")
		assert.Empty(t, repo.reservations, "a failed upload must not keep its reservation")
	})

	t.Run("a canceled request still releases the reservation", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		svc.artifactRepo = ctxCheckingReleaseArtifactRepo{repo}
		storage.errToReturn = context.Canceled
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the client is gone before the upload fails

		_, err := svc.Upload(ctx, sizedUpload(uuid.New(), 1))
		require.Error(t, err)
		assert.Empty(t, repo.reservations, "a dropped upload must not keep its reservation")
	})

	t.Run("a failed release after a storage failure still returns the 500", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		svc.artifactRepo = failingReleaseArtifactRepo{repo}
		storage.errToReturn = errors.New("minio down")
		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 1))
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusInternalServerError, apiErr.Code)
	})

	t.Run("a failed record removes the stored object and the reservation", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		svc.artifactRepo = failingRecordArtifactRepo{repo}
		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 1))
		require.EqualError(t, err, "insert failed")
		assert.Empty(t, storage.objects, "no object without its artifact")
		assert.Empty(t, repo.reservations)
	})

	t.Run("a failed object delete after a failed record still returns the error", func(t *testing.T) {
		svc, repo, storage := newSvcWithRepo()
		svc.artifactRepo = failingRecordArtifactRepo{repo}
		svc.storage = failingDeleteStorage{storage}
		_, err := svc.Upload(context.Background(), sizedUpload(uuid.New(), 1))
		require.EqualError(t, err, "insert failed")
		assert.Empty(t, repo.reservations)
	})

	t.Run("defaults are 25 MB per file and 250 MB per task", func(t *testing.T) {
		svc, _, _ := setupArtifactService()
		assert.Equal(t, int64(26214400), svc.maxFileBytes)
		assert.Equal(t, int64(262144000), svc.maxTaskBytes)
	})
}

func newSvcWithRepo() (*artifactService, *MockArtifactRepository, *MockStorageClient) {
	svc, repo, storage := setupArtifactService()
	WithArtifactLimits(10, 25)(svc)
	return svc, repo, storage
}

// failingReleaseArtifactRepo fails releasing a reservation.
type failingReleaseArtifactRepo struct{ *MockArtifactRepository }

func (failingReleaseArtifactRepo) ReleaseReservation(context.Context, uuid.UUID) error {
	return errors.New("release failed")
}

// failingRecordArtifactRepo fails writing the artifact after its upload.
type failingRecordArtifactRepo struct{ *MockArtifactRepository }

func (failingRecordArtifactRepo) CreateFromReservation(context.Context, uuid.UUID, *domain.Artifact) error {
	return errors.New("insert failed")
}

// ctxCheckingReleaseArtifactRepo refuses a release on a done context, as the
// database driver does.
type ctxCheckingReleaseArtifactRepo struct{ *MockArtifactRepository }

func (r ctxCheckingReleaseArtifactRepo) ReleaseReservation(ctx context.Context, id uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.MockArtifactRepository.ReleaseReservation(ctx, id)
}

// expiringUploadStorage stores the object while every reservation lapses,
// as the purge does for an upload slower than the reservation TTL.
type expiringUploadStorage struct {
	*MockStorageClient
	repo *MockArtifactRepository
}

func (s expiringUploadStorage) Upload(ctx context.Context, key string, r io.Reader, size int64, mime string) error {
	s.repo.mu.Lock()
	clear(s.repo.reservations)
	s.repo.mu.Unlock()
	return s.MockStorageClient.Upload(ctx, key, r, size, mime)
}

// deadlineRecordingStorage records the deadline the upload ran under.
type deadlineRecordingStorage struct {
	*MockStorageClient
	deadline time.Time
}

func (s *deadlineRecordingStorage) Upload(ctx context.Context, key string, r io.Reader, size int64, mime string) error {
	s.deadline, _ = ctx.Deadline()
	return s.MockStorageClient.Upload(ctx, key, r, size, mime)
}

// failingDeleteStorage stores objects but cannot delete them.
type failingDeleteStorage struct{ *MockStorageClient }

func (failingDeleteStorage) Delete(context.Context, string) error {
	return errors.New("delete failed")
}

// visibilityCheckingStorage fails the test if the artifact is already
// recorded while its object is being stored.
type visibilityCheckingStorage struct {
	*MockStorageClient
	repo *MockArtifactRepository
	t    *testing.T
}

func (s visibilityCheckingStorage) Upload(ctx context.Context, key string, r io.Reader, size int64, mime string) error {
	s.repo.mu.RLock()
	recorded := len(s.repo.items)
	s.repo.mu.RUnlock()
	assert.Zero(s.t, recorded, "artifact visible before its object is stored")
	return s.MockStorageClient.Upload(ctx, key, r, size, mime)
}
