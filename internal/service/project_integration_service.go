package service

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
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
	// A supplied agent_key is validated whether or not the integration is being
	// enabled: a malformed value (typically the "••••" mask or a browser-filled
	// password) must be refused here with a readable reason, not fall through to
	// the repository's generic 400. Empty means "keep the stored key".
	if input.AgentKey != "" && !teamRelayAgentKeyPattern.MatchString(input.AgentKey) {
		return nil, apierror.ValidationError(map[string]string{"agent_key": teamRelayAgentKeyHint(input.AgentKey)})
	}

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
		DocsMountPath:      input.DocsMountPath,
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

// SearchTR looks up the TR integration by share_slug, proxies the relay file-list API,
// and returns in-memory filtered results.
func (s *projectIntegrationService) SearchTR(ctx context.Context, shareSlug, q string, limit int) ([]domain.RelayFileItem, error) {
	pi, err := s.piRepo.GetByShareSlug(ctx, shareSlug)
	if err != nil {
		return nil, err
	}
	if pi == nil {
		return nil, apierror.NotFound("TeamRelayShare")
	}

	all, err := teamrelay.ListShareFiles(ctx, shareSlug, pi.AgentKey)
	if err != nil {
		return nil, err
	}
	if all == nil {
		// relay returned 404 for the slug
		return nil, apierror.NotFound("TeamRelayShare")
	}

	filtered := make([]domain.RelayFileItem, 0, len(all))
	qLower := strings.ToLower(q)
	for _, item := range all {
		if qLower == "" || strings.Contains(strings.ToLower(item.Name), qLower) || strings.Contains(strings.ToLower(item.Path), qLower) {
			filtered = append(filtered, item)
			if len(filtered) == limit {
				break
			}
		}
	}
	return filtered, nil
}

// teamRelayAgentKeyPattern matches a genuine Team Relay agent key; the
// repository enforces the same shape as a last line of defence.
var teamRelayAgentKeyPattern = regexp.MustCompile(`^tr_agent_[0-9a-f]{48}$`)

func teamRelayAgentKeyHint(key string) string {
	if strings.ContainsRune(key, '•') {
		return "agent_key looks like the masked placeholder, not a real key; leave the field empty to keep the stored key or paste a new key (tr_agent_ + 48 hex characters)"
	}
	return "agent_key must be \"tr_agent_\" followed by 48 lowercase hex characters; leave the field empty to keep the stored key"
}
