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
	piRepo        repository.ProjectIntegrationRepository
	relayResolver teamrelay.ConnectionResolver
	projects      repository.ProjectRepository
}

type ProjectIntegrationServiceOption func(*projectIntegrationService)

// WithProjectIntegrationRelayResolver supplies the same provider used by the mount service.
func WithProjectIntegrationRelayResolver(r teamrelay.ConnectionResolver, projects repository.ProjectRepository) ProjectIntegrationServiceOption {
	return func(s *projectIntegrationService) {
		s.relayResolver = r
		s.projects = projects
	}
}

// NewProjectIntegrationService returns a new ProjectIntegrationService.
func NewProjectIntegrationService(piRepo repository.ProjectIntegrationRepository, opts ...ProjectIntegrationServiceOption) ProjectIntegrationService {
	s := &projectIntegrationService{piRepo: piRepo}
	for _, opt := range opts {
		opt(s)
	}
	return s
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

	// Exclusions are matched against the FIRST path segment of a files-index
	// entry, so an entry that names more than one segment is a configuration
	// mistake that would silently exclude nothing — refused here, at the write,
	// rather than discovered from a mount that ignores it. Blank entries are
	// dropped rather than refused: trailing commas in a comma-separated form
	// field are ordinary typing, "artifacts/,archive" must not fail the save.
	excluded := make([]string, 0, len(input.ExcludedPaths))
	for _, p := range input.ExcludedPaths {
		t := strings.TrimSpace(strings.Trim(strings.TrimSpace(p), "/"))
		if t == "" {
			continue
		}
		if strings.ContainsAny(t, "/\\") || t == "." || t == ".." {
			return nil, apierror.ValidationError(map[string]string{"excluded_paths": "each entry must be a single top-level folder name, not a path"})
		}
		excluded = append(excluded, t)
	}

	if input.SyncIntervalMinutes != nil && *input.SyncIntervalMinutes != 0 && *input.SyncIntervalMinutes < int(MinTeamRelaySyncInterval/time.Minute) {
		return nil, apierror.ValidationError(map[string]string{
			"sync_interval_minutes": "must be 0 (scheduled sync off) or at least 5 minutes",
		})
	}

	// A nil ExcludedPaths here means "the caller did not send the field" —
	// stored as absent so the read-side default applies (see
	// effectiveExcludedPaths). An empty-but-present list is preserved: it is
	// the deliberate "mount everything, artifacts included" red-control state.
	var excludedStored []string
	if input.ExcludedPaths != nil {
		excludedStored = excluded
	}
	settings := domain.TeamRelaySettings{
		ShareID:             input.ShareID,
		ShareSlug:           input.ShareSlug,
		Subfolder:           input.Subfolder,
		IncludeProjectSlug:  input.IncludeProjectSlug,
		DocsMountPath:       input.DocsMountPath,
		ExcludedPaths:       excludedStored,
		SyncIntervalMinutes: input.SyncIntervalMinutes,
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

// UpsertProjectBindingInput is a project binding of a generic extension.
// AgentKey is the one secret column a binding has: empty keeps the stored one.
type UpsertProjectBindingInput struct {
	Enabled   bool
	Settings  map[string]any
	AgentKey  string
	CreatedBy *uuid.UUID
}

// GetBinding returns the project binding of an extension, NotFound when never set.
func (s *projectIntegrationService) GetBinding(ctx context.Context, projectID uuid.UUID, extensionID string) (*domain.ProjectIntegration, error) {
	pi, err := s.piRepo.Get(ctx, projectID, domain.ProjectIntegrationType(extensionID))
	if err != nil {
		return nil, err
	}
	if pi == nil {
		return nil, apierror.NotFound("ProjectExtension")
	}
	return pi, nil
}

// UpsertBinding creates or replaces the project binding of an extension.
func (s *projectIntegrationService) UpsertBinding(ctx context.Context, projectID uuid.UUID, extensionID string, input UpsertProjectBindingInput) (*domain.ProjectIntegration, error) {
	settings := input.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return nil, apierror.BadRequest("settings are not valid JSON")
	}
	now := time.Now()
	pi := &domain.ProjectIntegration{
		ID:        uuid.New(),
		ProjectID: projectID,
		Type:      domain.ProjectIntegrationType(extensionID),
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
	return s.GetBinding(ctx, projectID, extensionID)
}

// DeleteBinding removes the project binding of an extension.
func (s *projectIntegrationService) DeleteBinding(ctx context.Context, projectID uuid.UUID, extensionID string) error {
	return s.piRepo.Delete(ctx, projectID, domain.ProjectIntegrationType(extensionID))
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

	if !pi.Enabled {
		return nil, apierror.NotFound("TeamRelayShare")
	}
	if s.projects == nil {
		return nil, apierror.NotFound("TeamRelayConnection")
	}
	project, err := s.projects.GetByID(ctx, pi.ProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, apierror.NotFound("Project")
	}
	connection, enabled, err := teamrelay.ResolveConnection(ctx, s.relayResolver, project.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, apierror.NotFound("TeamRelayConnection")
	}
	all, err := teamrelay.ListShareFiles(ctx, connection.RelayURL, shareSlug, pi.AgentKey)
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
