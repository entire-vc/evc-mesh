package main

import (
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// stubArtifactService is a fallback when S3 is not available.
type stubArtifactService struct {
	repo repository.ArtifactRepository
}

func newStubArtifactService(repo repository.ArtifactRepository) *stubArtifactService {
	return &stubArtifactService{repo: repo}
}

func (s *stubArtifactService) Upload(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
	if input.Reader != nil {
		_, _ = io.Copy(io.Discard, input.Reader)
	}
	artifact := &domain.Artifact{
		ID:             uuid.New(),
		TaskID:         input.TaskID,
		Name:           input.Name,
		ArtifactType:   input.ArtifactType,
		MimeType:       input.MimeType,
		StorageKey:     "stub/" + uuid.New().String(),
		SizeBytes:      input.Size,
		UploadedBy:     input.UploadedBy,
		UploadedByType: input.UploadedByType,
	}
	if err := s.repo.Create(context.Background(), artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

func (s *stubArtifactService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apierror.NotFound("Artifact")
	}
	return a, nil
}

func (s *stubArtifactService) GetDownloadURL(ctx context.Context, id uuid.UUID) (string, error) {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", apierror.NotFound("Artifact")
	}
	return "/stub/download/" + a.StorageKey, nil
}

func (s *stubArtifactService) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
}

func (s *stubArtifactService) ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error) {
	return s.repo.ListByTask(ctx, taskID, pg)
}
