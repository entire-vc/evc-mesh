package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// integrationService implements IntegrationService.
type integrationService struct {
	repo   repository.IntegrationRepository
	guards map[domain.IntegrationProvider]ProviderGuard
}

// ProviderGuard is what a provider's extension says about a generic write.
// Write validates (and may refuse) a settings write; Delete refuses or allows
// removing the connection. A nil func allows the operation.
type ProviderGuard struct {
	Write  func(config json.RawMessage) error
	Delete func() error
}

// IntegrationServiceOption configures the integration service.
type IntegrationServiceOption func(*integrationService)

// WithProviderGuard installs the guard for the given provider names (a
// renamed provider lists both its names).
func WithProviderGuard(g ProviderGuard, providers ...domain.IntegrationProvider) IntegrationServiceOption {
	return func(s *integrationService) {
		for _, p := range providers {
			s.guards[p] = g
		}
	}
}

// NewIntegrationService creates a new integrationService.
func NewIntegrationService(repo repository.IntegrationRepository, opts ...IntegrationServiceOption) IntegrationService {
	s := &integrationService{repo: repo, guards: map[domain.IntegrationProvider]ProviderGuard{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *integrationService) guardWrite(provider domain.IntegrationProvider, config json.RawMessage) error {
	if g, ok := s.guards[provider]; ok && g.Write != nil {
		return g.Write(config)
	}
	return nil
}

func (s *integrationService) guardDelete(provider domain.IntegrationProvider) error {
	if g, ok := s.guards[provider]; ok && g.Delete != nil {
		return g.Delete()
	}
	return nil
}

// Configure creates or updates an integration config (upsert by workspace+provider).
func (s *integrationService) Configure(ctx context.Context, input domain.CreateIntegrationInput) (*domain.IntegrationConfig, error) {
	if err := s.guardWrite(input.Provider, input.Config); err != nil {
		return nil, err
	}
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
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if gerr := s.guardWrite(existing.Provider, input.Config); gerr != nil {
			return nil, gerr
		}
	}
	cfg, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return nil, fmt.Errorf("update integration: %w", err)
	}
	return cfg, nil
}

// Delete removes an integration config.
func (s *integrationService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := s.guardDelete(existing.Provider); err != nil {
			return err
		}
	}
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
