package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// presignedURLExpiry is the duration for presigned download URLs.
const presignedURLExpiry = 1 * time.Hour

// inlineSafeMimeTypes are the only content types GetDownloadURL will ever
// serve without a Content-Disposition: attachment header, no matter what the
// caller requests. The artifact bucket is proxied through our own origin
// (mesh.entire.host/s3/...), so a type the browser renders instead of
// downloading executes there with the reader's cookies/session. Every type
// on this list is either passive raster data or has no script-execution path
// in a browser; text/* (including text/html) and image/svg+xml are
// deliberately excluded — SVG carries its own <script>/event-handler surface.
var inlineSafeMimeTypes = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"application/pdf": true,
}

// isInlineSafeMimeType reports whether mimeType may be rendered inline by a
// browser. Parameters (e.g. "; charset=utf-8") are ignored, matching how
// artifact.MimeType is compared elsewhere.
func isInlineSafeMimeType(mimeType string) bool {
	base := mimeType
	if i := strings.Index(base, ";"); i >= 0 {
		base = base[:i]
	}
	return inlineSafeMimeTypes[strings.ToLower(strings.TrimSpace(base))]
}

// StorageClient is the interface for S3-compatible object storage.
type StorageClient interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
	// GetPresignedURL generates a time-limited download URL.
	// contentType sets the response Content-Type override (charset=utf-8 is added
	// automatically for text/* types); empty string leaves the stored value unchanged.
	// filename sets Content-Disposition: attachment; empty string omits the header.
	GetPresignedURL(ctx context.Context, key string, expiry time.Duration, contentType, filename string) (string, error)
	Delete(ctx context.Context, key string) error
	// Download fetches object contents. Caller must close the returned ReadCloser.
	Download(ctx context.Context, key string) (io.ReadCloser, error)
}

type artifactService struct {
	artifactRepo   repository.ArtifactRepository
	storage        StorageClient
	activityRepo   repository.ActivityLogRepository
	relayPublisher RelayPublisher
}

// NewArtifactService returns a new ArtifactService backed by the given repositories and storage.
func NewArtifactService(
	artifactRepo repository.ArtifactRepository,
	storage StorageClient,
	activityRepo repository.ActivityLogRepository,
) ArtifactService {
	return &artifactService{
		artifactRepo: artifactRepo,
		storage:      storage,
		activityRepo: activityRepo,
	}
}

// SetRelayPublisher wires an optional Team Relay publisher into the artifact service.
// The publisher is called asynchronously after each successful upload.
func (s *artifactService) SetRelayPublisher(p RelayPublisher) {
	s.relayPublisher = p
}

// Upload stores a file in S3 and creates an artifact record.
func (s *artifactService) Upload(ctx context.Context, input UploadArtifactInput) (*domain.Artifact, error) {
	if s.storage == nil {
		return nil, apierror.ServiceUnavailable("storage backend not configured; set S3_ENDPOINT, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_BUCKET")
	}

	id := uuid.New()
	storageKey := fmt.Sprintf("%s/%s/%s/%s", input.TaskID, id, input.Name, input.Name)

	if err := s.storage.Upload(ctx, storageKey, input.Reader, input.Size, input.MimeType); err != nil {
		return nil, apierror.InternalError("failed to upload artifact to storage")
	}

	artifact := &domain.Artifact{
		ID:             id,
		TaskID:         input.TaskID,
		Name:           input.Name,
		ArtifactType:   input.ArtifactType,
		MimeType:       input.MimeType,
		StorageKey:     storageKey,
		SizeBytes:      input.Size,
		UploadedBy:     input.UploadedBy,
		UploadedByType: input.UploadedByType,
		CreatedAt:      timeNow(),
	}

	if err := s.artifactRepo.Create(ctx, artifact); err != nil {
		// Best-effort cleanup: try to remove the uploaded file.
		_ = s.storage.Delete(ctx, storageKey)
		return nil, err
	}

	// Fire-and-forget relay publish (best-effort, never blocks the caller).
	if s.relayPublisher != nil {
		rp := s.relayPublisher
		art := *artifact
		stor := s.storage
		repo := s.artifactRepo
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("teamrelay publish panic: %v", r)
				}
			}()
			var content []byte
			if stor != nil {
				rc, dlErr := stor.Download(context.Background(), art.StorageKey)
				if dlErr != nil {
					log.Printf("teamrelay: failed to fetch artifact %s from storage: %v — skipping relay publish", art.StorageKey, dlErr)
					return
				}
				defer rc.Close()
				var readErr error
				content, readErr = io.ReadAll(rc)
				if readErr != nil {
					log.Printf("teamrelay: failed to read artifact %s from storage: %v — skipping relay publish", art.StorageKey, readErr)
					return
				}
			}
			publicURL, _, _ := rp.Publish(context.Background(), art.TaskID, art.Name, content, art.MimeType)
			// Persist only the relay public URL.
			//
			// The share's agent key was persisted here too, in the clear, so the UI
			// could build tr_public_url + ?agent_key= without a round-trip. That put
			// a long-lived credential — one we encrypt at rest in project_integrations
			// — into artifacts.metadata, readable by anything with DB or backup
			// access, and it reached the API through the one read path that did not
			// redact it. The UI resolves the key server-side via the preview-url
			// endpoint instead; nothing needs it stored.
			if publicURL != "" {
				meta := mergeMetadata(art.Metadata, "tr_public_url", publicURL)
				if upErr := repo.UpdateMetadata(context.Background(), art.ID, meta); upErr != nil {
					log.Printf("teamrelay: failed to persist TR metadata for artifact %s: %v", art.ID, upErr)
				}
			}
		}()
	}

	return artifact, nil
}

// GetByID retrieves an artifact by its ID.
func (s *artifactService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	artifact, err := s.artifactRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, apierror.NotFound("Artifact")
	}
	return artifact, nil
}

// GetByIDInWorkspace retrieves an artifact only when it belongs to workspaceID.
func (s *artifactService) GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Artifact, error) {
	artifact, err := s.artifactRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, apierror.NotFound("Artifact")
	}
	return artifact, nil
}

// GetDownloadURL generates a presigned URL for downloading the artifact.
// inline=true requests no Content-Disposition header, so the browser renders
// the file using the response Content-Type instead of forcing a download —
// but only when artifact.MimeType is on the inlineSafeMimeTypes allowlist.
// The bucket is proxied through our own origin, so any other type (notably
// text/html) always gets attachment disposition regardless of what the
// caller asked for: a browser must never execute artifact content as our
// origin's own page.
func (s *artifactService) GetDownloadURL(ctx context.Context, id uuid.UUID, inline bool) (string, error) {
	artifact, err := s.artifactRepo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if artifact == nil {
		return "", apierror.NotFound("Artifact")
	}

	if s.storage == nil {
		return "", apierror.ServiceUnavailable("storage backend not configured")
	}

	filename := artifact.Name
	if inline && isInlineSafeMimeType(artifact.MimeType) {
		filename = ""
	}

	url, err := s.storage.GetPresignedURL(ctx, artifact.StorageKey, presignedURLExpiry, artifact.MimeType, filename)
	if err != nil {
		return "", apierror.InternalError("failed to generate download URL")
	}

	return url, nil
}

// Delete removes an artifact from S3 and the database.
func (s *artifactService) Delete(ctx context.Context, id uuid.UUID) error {
	artifact, err := s.artifactRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if artifact == nil {
		return apierror.NotFound("Artifact")
	}

	if s.storage == nil {
		return apierror.ServiceUnavailable("storage backend not configured")
	}

	if err := s.storage.Delete(ctx, artifact.StorageKey); err != nil {
		return apierror.InternalError("failed to delete artifact from storage")
	}

	return s.artifactRepo.Delete(ctx, id)
}

// ListByTask returns a paginated list of artifacts for the given task.
func (s *artifactService) ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error) {
	pg.Normalize()
	return s.artifactRepo.ListByTask(ctx, taskID, pg)
}

// mergeMetadata sets key=value in an existing JSONB metadata blob, preserving any
// other keys. A nil/empty/invalid blob is treated as an empty object.
func mergeMetadata(existing json.RawMessage, key, value string) json.RawMessage {
	m := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &m) // on error, fall back to empty object
	}
	m[key] = value
	out, err := json.Marshal(m)
	if err != nil {
		return existing
	}
	return out
}
