package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type projectIntegrationService struct {
	piRepo repository.ProjectIntegrationRepository
}

// NewProjectIntegrationService returns a new ProjectIntegrationService.
func NewProjectIntegrationService(piRepo repository.ProjectIntegrationRepository) ProjectIntegrationService {
	return &projectIntegrationService{piRepo: piRepo}
}

// GetTeamRelay returns the Team Relay integration for the given project.
// Returns apierror.NotFound if no integration has been configured.
func (s *projectIntegrationService) GetTeamRelay(ctx context.Context, projectID uuid.UUID) (*domain.ProjectIntegration, error) {
	pi, err := s.piRepo.Get(ctx, projectID, "team_relay")
	if err != nil {
		return nil, err
	}
	if pi == nil {
		return nil, apierror.NotFound("TeamRelayIntegration")
	}
	return pi, nil
}

// UpsertTeamRelay creates or updates the Team Relay integration for the given project.
func (s *projectIntegrationService) UpsertTeamRelay(ctx context.Context, projectID uuid.UUID, input UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
	// Validation: enabled=true requires share_id or share_slug.
	if input.Enabled {
		if input.ShareID == "" && input.ShareSlug == "" {
			return nil, apierror.ValidationError(map[string]string{"share_slug": "share_id or share_slug required when enabled"})
		}
		if input.ShareID != "" {
			if _, err := uuid.Parse(input.ShareID); err != nil {
				return nil, apierror.ValidationError(map[string]string{"share_id": "must be a valid UUID"})
			}
		}
		// Validate agent_key length when supplied.
		if input.AgentKey != "" && len(input.AgentKey) < 20 {
			return nil, apierror.ValidationError(map[string]string{"agent_key": "must be at least 20 characters"})
		}
		// On first create, agent_key is mandatory.
		existing, err := s.piRepo.Get(ctx, projectID, "team_relay")
		if err != nil {
			return nil, err
		}
		if existing == nil && input.AgentKey == "" {
			return nil, apierror.ValidationError(map[string]string{"agent_key": "required when enabling for the first time"})
		}
	}

	settings := domain.TeamRelaySettings{
		ShareID:            input.ShareID,
		ShareSlug:          input.ShareSlug,
		Subfolder:          input.Subfolder,
		IncludeProjectSlug: input.IncludeProjectSlug,
	}
	settingsJSON, _ := json.Marshal(settings)

	now := time.Now()
	pi := &domain.ProjectIntegration{
		ID:        uuid.New(),
		ProjectID: projectID,
		Type:      "team_relay",
		Enabled:   input.Enabled,
		Settings:  settingsJSON,
		AgentKey:  input.AgentKey,
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: input.CreatedBy,
	}

	if err := s.piRepo.Upsert(ctx, pi); err != nil {
		return nil, err
	}

	return s.piRepo.Get(ctx, projectID, "team_relay")
}

// DeleteTeamRelay removes the Team Relay integration for the given project.
func (s *projectIntegrationService) DeleteTeamRelay(ctx context.Context, projectID uuid.UUID) error {
	return s.piRepo.Delete(ctx, projectID, "team_relay")
}

// List returns all integrations for the given project.
func (s *projectIntegrationService) List(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectIntegration, error) {
	return s.piRepo.ListByProject(ctx, projectID)
}
