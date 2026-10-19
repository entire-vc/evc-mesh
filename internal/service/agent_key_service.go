package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Public event names on the extension event list (internal/extensions). Spelled
// out here because service must not import extensions.
const (
	eventAgentKeyIssued  = "agent.key.issued"
	eventAgentKeyRevoked = "agent.key.revoked"
)

// maxActiveAgentKeys bounds the active keys of one agent, the original one
// included: the old and the new key during a change, never a third.
const maxActiveAgentKeys = 2

// AgentKeyInfo is the listing view of a key: never the key itself.
type AgentKeyInfo struct {
	ID      string `json:"id"`
	Primary bool   `json:"primary"`
	Label   string `json:"label,omitempty"`
	// Fingerprint is the stored key prefix: enough to tell keys apart, not to use one.
	Fingerprint string     `json:"fingerprint,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// IssuedAgentKey is the one response that carries the raw key.
type IssuedAgentKey struct {
	KeyID     uuid.UUID  `json:"key_id"`
	APIKey    string     `json:"api_key"`
	AgentID   uuid.UUID  `json:"agent_id"`
	Label     string     `json:"label,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// AgentKeyService issues and revokes agent API keys so a key can be changed in
// two phases: issue the new one, move the consumer, revoke the old one, with both
// valid in between.
type AgentKeyService interface {
	// Issue creates an additional key for the agent. The raw key is returned
	// once and is not stored or logged anywhere.
	Issue(ctx context.Context, agentID uuid.UUID, label string, ttl time.Duration) (*IssuedAgentKey, error)
	// Revoke revokes one key. keyID is a key_id from Issue or domain.AgentPrimaryKeyID
	// for the agent's original key.
	Revoke(ctx context.Context, agentID uuid.UUID, keyID string) error
	List(ctx context.Context, agentID uuid.UUID) ([]AgentKeyInfo, error)
}

// AgentCacheInvalidator drops cached authentications of an agent (satisfied by
// cachedAgentAuth).
type AgentCacheInvalidator interface {
	InvalidateAgent(agentID uuid.UUID)
}

type agentKeyService struct {
	agents     repository.AgentRepository
	workspaces repository.WorkspaceRepository
	keys       repository.AgentAPIKeyRepository
	activity   repository.ActivityLogRepository
	webhooks   WebhookService
	cache      AgentCacheInvalidator
	sponsors   AgentSponsorRecorder
}

// SetAgentSponsorRecorder wires the optional sponsor recorder: a key issued by a
// user makes that user the agent's sponsor.
func (s *agentKeyService) SetAgentSponsorRecorder(r AgentSponsorRecorder) { s.sponsors = r }

// NewAgentKeyService wires the service. webhooks and cache may be nil (events
// are then not dispatched / no cache to invalidate).
func NewAgentKeyService(
	agents repository.AgentRepository,
	workspaces repository.WorkspaceRepository,
	keys repository.AgentAPIKeyRepository,
	activity repository.ActivityLogRepository,
	webhooks WebhookService,
	cache AgentCacheInvalidator,
) AgentKeyService {
	return &agentKeyService{agents: agents, workspaces: workspaces, keys: keys, activity: activity, webhooks: webhooks, cache: cache}
}

type keyScopeCtxKey struct{}

// WithAgentKeyScope confines the key operations in ctx to agents of one
// workspace: an agent of any other workspace is then a plain 404. Set for
// extension tokens, which carry a workspace but no workspace role.
func WithAgentKeyScope(ctx context.Context, workspaceID uuid.UUID) context.Context {
	return context.WithValue(ctx, keyScopeCtxKey{}, workspaceID)
}

func (s *agentKeyService) agent(ctx context.Context, agentID uuid.UUID) (*domain.Agent, error) {
	a, err := s.agents.GetByID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apierror.NotFound("Agent")
	}
	if ws, ok := ctx.Value(keyScopeCtxKey{}).(uuid.UUID); ok && ws != a.WorkspaceID {
		return nil, apierror.NotFound("Agent")
	}
	return a, nil
}

func (s *agentKeyService) Issue(ctx context.Context, agentID uuid.UUID, label string, ttl time.Duration) (out *IssuedAgentKey, err error) {
	err = s.keys.WithAgentLock(ctx, agentID, func() error {
		var ierr error
		out, ierr = s.issue(ctx, agentID, label, ttl)
		return ierr
	})
	return out, err
}

func (s *agentKeyService) issue(ctx context.Context, agentID uuid.UUID, label string, ttl time.Duration) (*IssuedAgentKey, error) {
	agent, err := s.agent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	label = strings.TrimSpace(label)
	if len(label) > 128 {
		return nil, apierror.ValidationError(map[string]string{"label": "must be at most 128 characters"})
	}
	n, err := s.activeKeys(ctx, agent)
	if err != nil {
		return nil, err
	}
	if n >= maxActiveAgentKeys {
		return nil, apierror.Conflict(fmt.Sprintf("agent already has %d active keys; revoke one first", n))
	}
	ws, err := s.workspaces.GetByID(ctx, agent.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, apierror.NotFound("Workspace")
	}

	if op := OperatorUserFromContext(ctx); op != uuid.Nil {
		checker, isChecker := s.sponsors.(AgentOperatorRecorder)
		if !isChecker {
			return nil, apierror.BadRequest("operator_user_id is not supported here")
		}
		can, cerr := checker.UserCanOperate(ctx, agent.WorkspaceID, op)
		if cerr != nil {
			return nil, cerr
		}
		if !can {
			return nil, apierror.ValidationError(map[string]string{"operator_user_id": "must be an active member of the agent's workspace"})
		}
	}

	by, byType := actorctx.FromContext(ctx)
	var expires *time.Time
	if ttl > 0 {
		t := timeNow().Add(ttl)
		expires = &t
	}

	// A prefix collision inside one workspace is a unique-violation on
	// (workspace_id, key_prefix); a fresh random key resolves it.
	var raw string
	var row *domain.AgentAPIKey
	for attempt := 0; attempt < 5; attempt++ {
		raw, err = generateAPIKey(ws.Slug)
		if err != nil {
			return nil, apierror.InternalError("failed to generate API key")
		}
		row = &domain.AgentAPIKey{
			ID: uuid.New(), AgentID: agentID, WorkspaceID: agent.WorkspaceID,
			KeyPrefix: extractPrefix(raw, ws.Slug), KeySHA256: agentKeyDigest(raw),
			Label: label, IssuedByType: byType, IssuedByID: by, ExpiresAt: expires,
		}
		if row.IssuedByType == "" {
			row.IssuedByType = domain.ActorTypeSystem
		}
		err = s.keys.Create(ctx, row)
		if err == nil || !isUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	recordSponsor(ctx, s.sponsors, agentID, nil)
	s.audit(ctx, agent, "key_issued", row.ID.String(), label)
	s.dispatch(agent, eventAgentKeyIssued, row.ID.String(), label)
	return &IssuedAgentKey{KeyID: row.ID, APIKey: raw, AgentID: agentID, Label: label, ExpiresAt: expires}, nil
}

func (s *agentKeyService) Revoke(ctx context.Context, agentID uuid.UUID, keyID string) error {
	return s.keys.WithAgentLock(ctx, agentID, func() error { return s.revoke(ctx, agentID, keyID) })
}

func (s *agentKeyService) revoke(ctx context.Context, agentID uuid.UUID, keyID string) error {
	agent, err := s.agent(ctx, agentID)
	if err != nil {
		return err
	}

	active, err := s.activeKeys(ctx, agent)
	if err != nil {
		return err
	}
	// The last active key is never revoked: the agent would be locked out and
	// nothing left to issue a replacement with.
	lastKey := apierror.Conflict("cannot revoke the last active key of the agent; issue a new key first")

	if keyID == domain.AgentPrimaryKeyID {
		if agentPrimaryActive(agent) && active <= 1 {
			return lastKey
		}
		if err := s.revokePrimary(ctx, agent); err != nil {
			return err
		}
	} else {
		id, perr := uuid.Parse(keyID)
		if perr != nil {
			return apierror.BadRequest("invalid key_id")
		}
		row, err := s.keys.GetByID(ctx, agentID, id)
		if err != nil {
			return err
		}
		if row == nil {
			return apierror.NotFound("Agent key")
		}
		if row.RevokedAt == nil && (row.ExpiresAt == nil || timeNow().Before(*row.ExpiresAt)) && active <= 1 {
			return lastKey
		}
		if _, err := s.keys.Revoke(ctx, agentID, id); err != nil {
			return err
		}
	}

	// Drop this process's cached logins at once; other instances re-check the
	// key row on every cache hit (AgentKeyRevocationChecker).
	if s.cache != nil {
		s.cache.InvalidateAgent(agentID)
	}
	s.audit(ctx, agent, "key_revoked", keyID, "")
	s.dispatch(agent, eventAgentKeyRevoked, keyID, "")
	return nil
}

// agentPrimaryActive reports whether the agent's original key still logs in.
func agentPrimaryActive(a *domain.Agent) bool {
	return a.APIKeyHash != "" && (a.ExpiresAt == nil || timeNow().Before(*a.ExpiresAt))
}

// activeKeys counts the keys of the agent that can log in now, the original one
// included.
func (s *agentKeyService) activeKeys(ctx context.Context, agent *domain.Agent) (int, error) {
	n, err := s.keys.CountActiveByAgent(ctx, agent.ID, timeNow())
	if err != nil {
		return 0, err
	}
	if agentPrimaryActive(agent) {
		n++
	}
	return n, nil
}

// revokePrimary kills the agent's original key by replacing its key material, on
// the agent row and its home grant row in one step (RotateHomeGrantKey), with a
// random key nobody is given. A bare "clear the hash" would not do: the grant
// row keeps authenticating under the old material.
func (s *agentKeyService) revokePrimary(ctx context.Context, agent *domain.Agent) error {
	ws, err := s.workspaces.GetByID(ctx, agent.WorkspaceID)
	if err != nil {
		return err
	}
	if ws == nil {
		return apierror.NotFound("Workspace")
	}
	discarded, err := generateAPIKey(ws.Slug)
	if err != nil {
		return apierror.InternalError("failed to generate API key")
	}
	hash, err := bcrypt.GenerateFromPassword(bcryptInput(discarded), bcryptCost)
	if err != nil {
		return apierror.InternalError("failed to hash API key")
	}
	now := timeNow()
	agent.APIKeyHash = string(hash)
	agent.APIKeySHA256 = agentKeyDigest(discarded)
	agent.APIKeyPrefix = extractPrefix(discarded, ws.Slug)
	agent.LastRotatedAt = &now
	agent.ExpiresAt = &now // expired as well: nothing can resurrect it by accident
	agent.UpdatedAt = now
	return s.agents.RotateHomeGrantKey(ctx, agent)
}

func (s *agentKeyService) List(ctx context.Context, agentID uuid.UUID) ([]AgentKeyInfo, error) {
	agent, err := s.agent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.keys.ListByAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	primary := AgentKeyInfo{ID: domain.AgentPrimaryKeyID, Primary: true, ExpiresAt: agent.ExpiresAt, Fingerprint: agent.APIKeyPrefix}
	if t, perr := s.keys.PrimaryLastUsed(ctx, agentID); perr == nil {
		primary.LastUsedAt = t
	}
	if agent.LastRotatedAt != nil {
		primary.CreatedAt = agent.LastRotatedAt
	} else {
		t := agent.CreatedAt
		primary.CreatedAt = &t
	}
	if agent.ExpiresAt != nil && !timeNow().Before(*agent.ExpiresAt) {
		primary.RevokedAt = agent.ExpiresAt
	}
	out := []AgentKeyInfo{primary}
	for i := range rows {
		r := rows[i]
		ca := r.CreatedAt
		out = append(out, AgentKeyInfo{ID: r.ID.String(), Label: r.Label, Fingerprint: r.KeyPrefix, CreatedAt: &ca, LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt})
	}
	return out, nil
}

// audit writes the activity entry. Failure to write it is logged by the repo
// layer's caller convention elsewhere; here it must not undo a key change.
func (s *agentKeyService) audit(ctx context.Context, agent *domain.Agent, action, keyID, label string) {
	if s.activity == nil {
		return
	}
	by, byType := actorctx.FromContext(ctx)
	if byType == "" {
		byType = domain.ActorTypeSystem
	}
	changes, _ := json.Marshal(map[string]string{"key_id": keyID, "label": label})
	_ = s.activity.Create(ctx, &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: agent.WorkspaceID, EntityType: "agent", EntityID: agent.ID,
		Action: action, ActorID: by, ActorType: byType, Changes: changes, Source: "api",
	})
}

// dispatch publishes the public event. The payload names the key by id only.
func (s *agentKeyService) dispatch(agent *domain.Agent, event, keyID, label string) {
	if s.webhooks == nil {
		return
	}
	payload := map[string]any{"agent_id": agent.ID, "key_id": keyID, "workspace_id": agent.WorkspaceID}
	if label != "" {
		payload["label"] = label
	}
	s.webhooks.Dispatch(context.Background(), agent.WorkspaceID, event, payload)
}

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}
