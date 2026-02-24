package service

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// presignedURLExpiry is the duration for presigned download URLs.
const presignedURLExpiry = 1 * time.Hour

// StorageClient is the interface for S3-compatible object storage.
type StorageClient interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
	GetPresignedURL(ctx context.Context, key string, expiry time.Duration) (string, error)
	Delete(ctx context.Context, key string) error
}

type artifactService struct {
	artifactRepo repository.ArtifactRepository
	storage      StorageClient
	activityRepo repository.ActivityLogRepository
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

// Upload stores a file in S3 and creates an artifact record.
func (s *artifactService) Upload(ctx context.Context, input UploadArtifactInput) (*domain.Artifact, error) {
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

// GetDownloadURL generates a presigned URL for downloading the artifact.
func (s *artifactService) GetDownloadURL(ctx context.Context, id uuid.UUID) (string, error) {
	artifact, err := s.artifactRepo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if artifact == nil {
		return "", apierror.NotFound("Artifact")
	}

	url, err := s.storage.GetPresignedURL(ctx, artifact.StorageKey, presignedURLExpiry)
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
