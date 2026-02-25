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

// integrationService implements IntegrationService.
type integrationService struct {
	repo repository.IntegrationRepository
}

// NewIntegrationService creates a new integrationService.
func NewIntegrationService(repo repository.IntegrationRepository) IntegrationService {
	return &integrationService{repo: repo}
}

// Configure creates or updates an integration config (upsert by workspace+provider).
func (s *integrationService) Configure(ctx context.Context, input domain.CreateIntegrationInput) (*domain.IntegrationConfig, error) {
	if input.WorkspaceID == uuid.Nil {
		return nil, apierror.BadRequest("workspace_id is required")
	}
	if input.Provider == "" {
		return nil, apierror.BadRequest("provider is required")
	}

	now := time.Now()
	cfg := &domain.IntegrationConfig{
		ID:          uuid.New(),
		WorkspaceID: input.WorkspaceID,
		Provider:    input.Provider,
		Config:      input.Config,
		IsActive:    input.IsActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if cfg.Config == nil {
		cfg.Config = []byte("{}")
	}

	if err := s.repo.Upsert(ctx, cfg); err != nil {
		return nil, fmt.Errorf("configure integration: %w", err)
	}

	// Reload so that ON CONFLICT updates are reflected (the ID may differ from what we inserted).
	stored, err := s.repo.GetByProvider(ctx, input.WorkspaceID, input.Provider)
	if err != nil {
		return nil, fmt.Errorf("reload integration: %w", err)
	}
	if stored == nil {
		return cfg, nil
	}
	return stored, nil
}

// GetByID retrieves an integration config by ID.
func (s *integrationService) GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	cfg, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get integration: %w", err)
	}
	if cfg == nil {
		return nil, apierror.NotFound("Integration")
	}
	return cfg, nil
}

// Update applies a partial update to an integration config.
func (s *integrationService) Update(ctx context.Context, id uuid.UUID, input domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	cfg, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return nil, fmt.Errorf("update integration: %w", err)
	}
	return cfg, nil
}

// Delete removes an integration config.
func (s *integrationService) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete integration: %w", err)
	}
	return nil
}

// ListByWorkspace returns all integration configs for a workspace.
func (s *integrationService) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.IntegrationConfig, error) {
	cfgs, err := s.repo.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	return cfgs, nil
}
