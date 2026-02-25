package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// vcsLinkService implements VCSLinkService.
type vcsLinkService struct {
	repo repository.VCSLinkRepository
}

// NewVCSLinkService creates a new vcsLinkService.
func NewVCSLinkService(repo repository.VCSLinkRepository) VCSLinkService {
	return &vcsLinkService{repo: repo}
}

// Create creates a new VCS link.
func (s *vcsLinkService) Create(ctx context.Context, input domain.CreateVCSLinkInput) (*domain.VCSLink, error) {
	if input.TaskID == uuid.Nil {
		return nil, apierror.BadRequest("task_id is required")
	}
	if input.URL == "" {
		return nil, apierror.BadRequest("url is required")
	}
	if input.ExternalID == "" {
		return nil, apierror.BadRequest("external_id is required")
	}
	if input.LinkType == "" {
		return nil, apierror.BadRequest("link_type is required")
	}

	provider := input.Provider
	if provider == "" {
		provider = domain.VCSProviderGitHub
	}

	metadata := input.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}

	link := &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     input.TaskID,
		Provider:   provider,
		LinkType:   input.LinkType,
		ExternalID: input.ExternalID,
		URL:        input.URL,
		Title:      input.Title,
		Status:     input.Status,
		Metadata:   metadata,
		CreatedAt:  time.Now(),
	}

	if err := s.repo.Create(ctx, link); err != nil {
		return nil, fmt.Errorf("create vcs link: %w", err)
	}
	return link, nil
}

// GetByID retrieves a VCS link by ID.
func (s *vcsLinkService) GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error) {
	link, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get vcs link: %w", err)
	}
	if link == nil {
		return nil, apierror.NotFound("VCSLink")
	}
	return link, nil
}

// Delete removes a VCS link by ID.
func (s *vcsLinkService) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete vcs link: %w", err)
	}
	return nil
}

// ListByTask returns all VCS links for a given task.
func (s *vcsLinkService) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error) {
	links, err := s.repo.ListByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("list vcs links: %w", err)
	}
	return links, nil
}
