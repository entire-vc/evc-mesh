package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

const (
	// bcryptCost is the bcrypt work factor for hashing API keys.
	bcryptCost = 12
	// apiKeyRandomBytes is the number of random bytes for the API key suffix.
	apiKeyRandomBytes = 24
	// apiKeyPrefixLen is the length of the stored prefix (used for fast lookup).
	apiKeyPrefixLen = 8
)

type agentService struct {
	agentRepo    repository.AgentRepository
	activityRepo repository.ActivityLogRepository
	// workspaceRepo is used to resolve workspace slugs during authentication.
	workspaceRepo repository.WorkspaceRepository
}

// NewAgentService returns a new AgentService backed by the given repositories.
func NewAgentService(
	agentRepo repository.AgentRepository,
	activityRepo repository.ActivityLogRepository,
	workspaceRepo repository.WorkspaceRepository,
) AgentService {
	return &agentService{
		agentRepo:     agentRepo,
		activityRepo:  activityRepo,
		workspaceRepo: workspaceRepo,
	}
}

// generateAPIKey creates a raw API key in the format: agk_{workspaceSlug}_{random_hex}.
func generateAPIKey(workspaceSlug string) (string, error) {
	b := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return fmt.Sprintf("agk_%s_%s", workspaceSlug, hex.EncodeToString(b)), nil
}

// extractPrefix returns the stored prefix from a raw API key.
// The prefix is the first apiKeyPrefixLen characters of the random part (after "agk_{slug}_").
func extractPrefix(rawKey, workspaceSlug string) string {
	prefix := "agk_" + workspaceSlug + "_"
	rest := strings.TrimPrefix(rawKey, prefix)
	if len(rest) > apiKeyPrefixLen {
		return rest[:apiKeyPrefixLen]
	}
	return rest
}

// slugify converts a name to a URL-safe slug.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	// Remove characters that are not alphanumeric or hyphens.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Register creates a new agent, generates an API key, hashes it, and returns
// the agent along with the raw key (shown only once).
func (s *agentService) Register(ctx context.Context, input RegisterAgentInput) (*RegisterAgentOutput, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}

	// Look up the workspace to get its slug for the API key format.
	ws, err := s.workspaceRepo.GetByID(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, apierror.NotFound("Workspace")
	}

	rawKey, err := generateAPIKey(ws.Slug)
	if err != nil {
		return nil, apierror.InternalError("failed to generate API key")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(rawKey), bcryptCost)
	if err != nil {
		return nil, apierror.InternalError("failed to hash API key")
	}

	prefix := extractPrefix(rawKey, ws.Slug)
	now := timeNow()

	agent := &domain.Agent{
		ID:            uuid.New(),
		WorkspaceID:   input.WorkspaceID,
		ParentAgentID: input.ParentAgentID,
		Name:          input.Name,
		Slug:          slugify(input.Name),
		AgentType:     input.AgentType,
		APIKeyHash:    string(hash),
		APIKeyPrefix:  prefix,
		Status:        domain.AgentStatusOffline,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	// Capabilities are stored as-is; marshalling happens at the repo layer.
	// No additional processing is needed here.

	if err := s.agentRepo.Create(ctx, agent); err != nil {
		return nil, err
	}

	return &RegisterAgentOutput{
		Agent:  agent,
		APIKey: rawKey,
	}, nil
}

// GetByID retrieves an agent by its ID.
func (s *agentService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
	agent, err := s.agentRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, apierror.NotFound("Agent")
	}
	return agent, nil
}

// Update persists changes to an existing agent.
func (s *agentService) Update(ctx context.Context, agent *domain.Agent) error {
	existing, err := s.agentRepo.GetByID(ctx, agent.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Agent")
	}
	agent.UpdatedAt = timeNow()
	return s.agentRepo.Update(ctx, agent)
}

// Delete removes an agent after verifying it exists.
func (s *agentService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.agentRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Agent")
	}
	return s.agentRepo.Delete(ctx, id)
}

// List returns a paginated list of agents for the given workspace.
func (s *agentService) List(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error) {
	pg.Normalize()
	return s.agentRepo.List(ctx, workspaceID, filter, pg)
}

// Heartbeat updates the agent's heartbeat timestamp and sets status to online.
func (s *agentService) Heartbeat(ctx context.Context, agentID uuid.UUID) error {
	if err := s.agentRepo.UpdateHeartbeat(ctx, agentID); err != nil {
		return err
	}
	return s.agentRepo.UpdateStatus(ctx, agentID, domain.AgentStatusOnline)
}

// Authenticate verifies an API key against the stored hash.
// It resolves the workspace by slug, extracts the prefix for fast lookup,
// then does a bcrypt comparison.
func (s *agentService) Authenticate(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error) {
	ws, err := s.workspaceRepo.GetBySlug(ctx, workspaceSlug)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, apierror.Unauthorized("invalid API key")
	}

	prefix := extractPrefix(apiKey, workspaceSlug)
	agent, err := s.agentRepo.GetByAPIKeyPrefix(ctx, ws.ID, prefix)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, apierror.Unauthorized("invalid API key")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(agent.APIKeyHash), []byte(apiKey)); err != nil {
		return nil, apierror.Unauthorized("invalid API key")
	}

	return agent, nil
}

// ListSubAgents returns direct children (recursive=false) or all descendants
// (recursive=true, depth limited to 10) of the given parent agent.
func (s *agentService) ListSubAgents(ctx context.Context, parentID uuid.UUID, recursive bool) ([]domain.Agent, error) {
	// Verify parent agent exists first.
	parent, err := s.agentRepo.GetByID(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, apierror.NotFound("Agent")
	}

	if recursive {
		return s.agentRepo.GetSubAgentTree(ctx, parentID)
	}

	// Non-recursive: list only direct children (ignore pagination limits — return all).
	filter := repository.AgentFilter{
		ParentAgentID: &parentID,
	}
	page, err := s.agentRepo.List(ctx, parent.WorkspaceID, filter, pagination.Params{Page: 1, PageSize: 1000})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// RotateAPIKey generates a new API key for the agent, replacing the old one.
// Returns the new raw key (shown only once).
func (s *agentService) RotateAPIKey(ctx context.Context, agentID uuid.UUID) (string, error) {
	agent, err := s.agentRepo.GetByID(ctx, agentID)
	if err != nil {
		return "", err
	}
	if agent == nil {
		return "", apierror.NotFound("Agent")
	}

	ws, err := s.workspaceRepo.GetByID(ctx, agent.WorkspaceID)
	if err != nil {
		return "", err
	}
	if ws == nil {
		return "", apierror.NotFound("Workspace")
	}

	rawKey, err := generateAPIKey(ws.Slug)
	if err != nil {
		return "", apierror.InternalError("failed to generate API key")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(rawKey), bcryptCost)
	if err != nil {
		return "", apierror.InternalError("failed to hash API key")
	}

	agent.APIKeyHash = string(hash)
	agent.APIKeyPrefix = extractPrefix(rawKey, ws.Slug)
	agent.UpdatedAt = timeNow()

	if err := s.agentRepo.Update(ctx, agent); err != nil {
		return "", err
	}

	return rawKey, nil
}
