package service

import (
	"context"
	"encoding/json"
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

// stubRelayPublisher returns a fixed public URL and records that it was called.
type stubRelayPublisher struct {
	mu        sync.Mutex
	publicURL string
	called    bool
}

func (s *stubRelayPublisher) Publish(_ context.Context, _ uuid.UUID, _ string, _ []byte, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called = true
	return s.publicURL, nil
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
			stored := artifactRepo.items[artifact.ID]
			if stored == nil || len(stored.Metadata) == 0 {
				return false
			}
			var m map[string]any
			if jErr := json.Unmarshal(stored.Metadata, &m); jErr != nil {
				return false
			}
			return m["tr_public_url"] == "https://relay.example.com/mesh/foo.md"
		}, 2*time.Second, 10*time.Millisecond, "tr_public_url should be persisted to metadata")
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
		stored := artifactRepo.items[artifact.ID]
		require.NotNil(t, stored)
		if len(stored.Metadata) > 0 {
			var m map[string]any
			require.NoError(t, json.Unmarshal(stored.Metadata, &m))
			_, has := m["tr_public_url"]
			assert.False(t, has, "tr_public_url should not be set when relay returns empty URL")
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
// TestArtifactService_GetDownloadURL
// ---------------------------------------------------------------------------

func TestArtifactService_GetDownloadURL(t *testing.T) {
	tests := []struct {
		name    string
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

			url, err := svc.GetDownloadURL(ctx, id)

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
