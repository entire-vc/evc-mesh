package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	// agentKeyDefaultTTL is the lifetime granted to a newly registered or rotated key.
	agentKeyDefaultTTL = 365 * 24 * time.Hour
	// defaultMaxConcurrentTasks is applied when registration omits the field.
	// 0 reads as "takes no tasks" even though dispatch does not actually
	// enforce it yet (#8b39a38e) — a freshly registered agent should not
	// start out looking parked.
	defaultMaxConcurrentTasks = 5
)

type agentService struct {
	agentRepo       repository.AgentRepository
	activityRepo    repository.ActivityLogRepository
	agentActLogRepo repository.AgentActivityLogRepository
	// workspaceRepo is used to resolve workspace slugs during authentication.
	workspaceRepo repository.WorkspaceRepository
	// userRepo backs the username↔slug collision guard in Register (task
	// fee35355) — optional, nil in tests that don't exercise it, in which case
	// the guard is a no-op (see the nil check in Register).
	userRepo repository.UserRepository
}

// NewAgentService returns a new AgentService backed by the given repositories.
// userRepo may be nil — Register's username-collision guard is then skipped,
// which is fine for tests that don't touch it but means a caller wiring this
// for real (see cmd/api/main.go) must pass a real one for the guard to apply.
func NewAgentService(
	agentRepo repository.AgentRepository,
	activityRepo repository.ActivityLogRepository,
	workspaceRepo repository.WorkspaceRepository,
	userRepo repository.UserRepository,
) AgentService {
	return &agentService{
		agentRepo:     agentRepo,
		activityRepo:  activityRepo,
		workspaceRepo: workspaceRepo,
		userRepo:      userRepo,
	}
}

// SetAgentActivityLogRepo wires the optional agent activity log repository.
func (s *agentService) SetAgentActivityLogRepo(repo repository.AgentActivityLogRepository) {
	s.agentActLogRepo = repo
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

	slug := slugify(input.Name)

	// Task fee35355: agents.slug and users.username live in separate namespaces
	// that nothing else keeps disjoint. If a member of this workspace already
	// answers to this handle, a new agent taking the same slug would make
	// @<slug> in a comment here ambiguous between the two — the exact class of
	// collision task f4f47938 stopped from being a SILENT loss (both branches
	// now resolve and get their own delivery) without stopping it from being
	// created. Fail before spending a bcrypt hash on a request we're about to
	// reject. userRepo is nil in tests that don't care about this.
	if s.userRepo != nil {
		existingUser, userErr := s.userRepo.GetByUsername(ctx, input.WorkspaceID, slug)
		if userErr != nil {
			return nil, apierror.Wrap(userErr)
		}
		if existingUser != nil {
			return nil, apierror.Conflict(fmt.Sprintf(
				"this handle is already used by user %q in this workspace — choose a different agent name",
				existingUser.Username))
		}
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
	expires := now.Add(agentKeyDefaultTTL)

	agent := &domain.Agent{
		ID:            uuid.New(),
		WorkspaceID:   input.WorkspaceID,
		ParentAgentID: input.ParentAgentID,
		Name:          input.Name,
		Slug:          slug,
		AgentType:     input.AgentType,
		APIKeyHash:    string(hash),
		// Written at issue time, the one moment the plaintext exists, so a
		// freshly registered agent never pays bcrypt at all.
		APIKeySHA256: agentKeyDigest(rawKey),
		APIKeyPrefix: prefix,
		Status:       domain.AgentStatusOffline,
		ExpiresAt:    &expires,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if len(input.Capabilities) > 0 {
		capBytes, err := json.Marshal(input.Capabilities)
		if err != nil {
			return nil, apierror.BadRequest("invalid capabilities")
		}
		agent.Capabilities = capBytes
	}

	// Role/ResponsibilityZone/WorkingHours/MaxConcurrentTasks: omitted (nil
	// pointer) means "apply a sane default", not "leave the column's own
	// SQL default in place" — the INSERT always supplies an explicit value
	// for every one of these columns (see AgentRepo.Create), so a Go zero
	// value here would silently override the schema default instead of
	// falling back to it. This is what produced agents with an empty
	// responsibility_zone and max_concurrent_tasks=0 straight out of
	// registration (task #85714565).
	if input.Role != nil && strings.TrimSpace(*input.Role) != "" {
		agent.Role = strings.TrimSpace(*input.Role)
	} else {
		agent.Role = "developer"
	}
	if input.ResponsibilityZone != nil {
		agent.ResponsibilityZone = strings.TrimSpace(*input.ResponsibilityZone)
	}
	if input.WorkingHours != nil && strings.TrimSpace(*input.WorkingHours) != "" {
		agent.WorkingHours = strings.TrimSpace(*input.WorkingHours)
	} else {
		agent.WorkingHours = "24/7"
	}
	if input.MaxConcurrentTasks != nil {
		agent.MaxConcurrentTasks = *input.MaxConcurrentTasks
	} else {
		agent.MaxConcurrentTasks = defaultMaxConcurrentTasks
	}
	if input.EscalationTo != nil && strings.TrimSpace(*input.EscalationTo) != "" {
		escBytes, err := json.Marshal(strings.TrimSpace(*input.EscalationTo))
		if err != nil {
			return nil, apierror.BadRequest("invalid escalation_to")
		}
		raw := json.RawMessage(escBytes)
		agent.EscalationTo = &raw
	}
	// AcceptsFrom: nil is left as-is on purpose — AgentRepo.Create already
	// defaults a nil value to `["*"]` (explicit "accept from anyone"), which
	// is exactly what an omitted field should mean here.
	if input.AcceptsFrom != nil {
		agent.AcceptsFrom = input.AcceptsFrom
	}

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

// Heartbeat updates the agent's heartbeat timestamp and optionally stores status/message/metadata.
func (s *agentService) Heartbeat(ctx context.Context, agentID uuid.UUID, input *HeartbeatInput) error {
	var params *repository.UpdateHeartbeatParams
	if input != nil {
		params = &repository.UpdateHeartbeatParams{
			Status:   input.Status,
			Message:  input.Message,
			Metadata: input.Metadata,
		}
	}
	if err := s.agentRepo.UpdateHeartbeat(ctx, agentID, params); err != nil {
		return err
	}

	// Write to agent activity log (best-effort).
	if s.agentActLogRepo != nil {
		agent, err := s.agentRepo.GetByID(ctx, agentID)
		if err == nil && agent != nil {
			entry := &domain.AgentActivityLog{
				AgentID:     agentID,
				WorkspaceID: agent.WorkspaceID,
				EventType:   "heartbeat",
			}
			if input != nil {
				entry.Message = input.Message
				entry.Metadata = input.Metadata
				entry.TaskID = input.CurrentTaskID
			}
			_ = s.agentActLogRepo.Create(ctx, entry)
		}
	}

	return nil
}

// TouchLastSeen bumps last_heartbeat for the agent without changing status.
func (s *agentService) TouchLastSeen(ctx context.Context, agentID uuid.UUID) error {
	return s.agentRepo.TouchLastSeenBatch(ctx, []uuid.UUID{agentID})
}

// CreateActivityLog writes an entry to the agent activity log.
func (s *agentService) CreateActivityLog(ctx context.Context, entry *domain.AgentActivityLog) error {
	if s.agentActLogRepo == nil {
		return nil
	}
	return s.agentActLogRepo.Create(ctx, entry)
}

// ListActivityLog returns paginated activity log entries for a given agent.
func (s *agentService) ListActivityLog(ctx context.Context, agentID uuid.UUID, filter repository.AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error) {
	if s.agentActLogRepo == nil {
		return pagination.NewPage([]domain.AgentActivityLog{}, 0, pg), nil
	}
	pg.Normalize()
	return s.agentActLogRepo.List(ctx, agentID, filter, pg)
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

	if err := s.verifyAPIKey(ctx, agent, apiKey); err != nil {
		return nil, err
	}

	if agent.IsKeyExpiredAt(timeNow()) {
		return nil, apierror.Unauthorized("API key expired")
	}

	return agent, nil
}

// verifyAPIKey checks apiKey against the agent's stored credentials, preferring
// the keyed digest and falling back to bcrypt.
//
// The fallback is not a permanent second branch: it exists because bcrypt is
// one-way, so no migration could have backfilled api_key_sha256 — the plaintext
// only exists at the moment a key is issued or presented. A row therefore leaves
// the slow path the first time its key is used, and never returns to it.
//
// Note what the fast path also fixes: a WRONG key against an agent whose digest
// is populated now costs one HMAC instead of a full bcrypt comparison. Before
// this, presenting a valid prefix with a wrong secret burned ~163 ms of server
// CPU per attempt, from an unauthenticated caller.
func (s *agentService) verifyAPIKey(ctx context.Context, agent *domain.Agent, apiKey string) error {
	if agent.APIKeySHA256 != "" {
		if !agentKeyDigestMatches(agent.APIKeySHA256, apiKey) {
			return apierror.Unauthorized("invalid API key")
		}
		return nil
	}

	if err := bcrypt.CompareHashAndPassword([]byte(agent.APIKeyHash), []byte(apiKey)); err != nil {
		return apierror.Unauthorized("invalid API key")
	}

	// Opportunistic write: the plaintext is in hand exactly here and nowhere
	// else. A failure is not the caller's problem — the key verified — so it
	// only costs this agent one more slow authentication.
	digest := agentKeyDigest(apiKey)
	if setErr := s.agentRepo.SetAPIKeySHA256(ctx, agent.ID, digest, agent.APIKeyHash); setErr == nil {
		agent.APIKeySHA256 = digest
	}
	return nil
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

	now := timeNow()
	expires := now.Add(agentKeyDefaultTTL)

	agent.APIKeyHash = string(hash)
	agent.APIKeySHA256 = agentKeyDigest(rawKey)
	agent.APIKeyPrefix = extractPrefix(rawKey, ws.Slug)
	agent.LastRotatedAt = &now
	agent.ExpiresAt = &expires
	agent.UpdatedAt = now

	if err := s.agentRepo.Update(ctx, agent); err != nil {
		return "", err
	}

	return rawKey, nil
}

// GetBySlug returns the agent with the given slug in a workspace.
// Returns (nil, nil) when no matching agent exists.
func (s *agentService) GetBySlug(ctx context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error) {
	return s.agentRepo.GetBySlug(ctx, workspaceID, slug)
}
