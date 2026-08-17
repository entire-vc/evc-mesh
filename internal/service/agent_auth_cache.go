package service

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

// AgentAuthCacheTTL is how long a successful key verification is trusted.
//
// The number is a deliberate trade: a revoked or rotated key that this process
// never sees an invalidation for stays usable for at most this long. Rotation
// and deletion DO invalidate explicitly (see RotateAPIKey/Delete below), so the
// window only applies to changes made by another process against the same
// database — a second API replica, or a manual UPDATE.
const AgentAuthCacheTTL = 60 * time.Second

// agentAuthCacheMaxEntries bounds the map so a caller that presents a large
// number of distinct VALID keys cannot grow it without limit. Only successful
// authentications are ever stored, so reaching this bound requires that many
// real agents; the purge below is a safety net, not a hot path.
const agentAuthCacheMaxEntries = 8192

// cachedAuthEntry is one verified (workspaceSlug, apiKey) -> agent result.
//
// agent is a private copy: Authenticate hands every caller its own copy, so a
// caller mutating the returned struct can never corrupt what the next caller
// sees.
type cachedAuthEntry struct {
	agent     domain.Agent
	expiresAt time.Time
}

// cachedAgentAuth decorates an AgentService with an in-process cache of
// successful API-key verifications.
//
// Why this exists: Authenticate ends in bcrypt.CompareHashAndPassword against a
// cost-12 hash, which is ~163 ms of pure CPU — measured as 87% of the wall time
// of the single most frequent authenticated endpoint (GET /agents/me/tasks) and
// ~56% of all API time across the fleet. bcrypt is deliberately slow; the fix at
// this layer is to stop paying it on every request from an already-verified key.
//
// What is NOT cached: failures. A wrong secret still costs a full bcrypt
// comparison, by design — caching negatives would let an unauthenticated caller
// pick the map's keys. Removing the cost of a failed comparison needs the key
// scheme itself to change (HMAC-SHA256 instead of bcrypt); that is a separate
// change.
//
// Every method other than Authenticate/RotateAPIKey/Delete is inherited
// unchanged from the embedded AgentService.
type cachedAgentAuth struct {
	AgentService

	mu sync.RWMutex
	// entries is keyed by SHA-256 of (workspaceSlug, apiKey) — never by the raw
	// key, so a heap dump or a map iteration in a debugger does not hand over
	// live credentials.
	entries map[[sha256.Size]byte]cachedAuthEntry
	// byAgent maps agent ID -> its cache keys, so rotation and deletion can
	// evict without knowing the raw key. An agent normally has exactly one
	// live key, but a rotation racing with an in-flight request can briefly
	// leave two.
	byAgent map[uuid.UUID]map[[sha256.Size]byte]struct{}

	ttl time.Duration
	// now is injectable so tests can drive expiry without sleeping.
	now func() time.Time
}

// NewCachedAgentAuth wraps inner so that repeated requests carrying an
// already-verified API key skip the bcrypt comparison for ttl.
//
// A ttl <= 0 selects AgentAuthCacheTTL.
func NewCachedAgentAuth(inner AgentService, ttl time.Duration) AgentService {
	if ttl <= 0 {
		ttl = AgentAuthCacheTTL
	}
	return &cachedAgentAuth{
		AgentService: inner,
		entries:      make(map[[sha256.Size]byte]cachedAuthEntry),
		byAgent:      make(map[uuid.UUID]map[[sha256.Size]byte]struct{}),
		ttl:          ttl,
		now:          time.Now,
	}
}

// agentAuthCacheKey derives the map key for a (workspaceSlug, apiKey) pair.
//
// The slug is part of the digest, not just a prefix of the key: Authenticate
// takes it as its own argument and resolves the workspace from it, so keying on
// the API key alone would let a hit computed for one slug answer a request made
// under another.
func agentAuthCacheKey(workspaceSlug, apiKey string) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(workspaceSlug))
	// NUL separator: without it ("ab","c") and ("a","bc") hash identically.
	h.Write([]byte{0})
	h.Write([]byte(apiKey))
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Authenticate returns the cached agent for a key verified within the TTL, and
// otherwise delegates to the wrapped service and caches a success.
func (c *cachedAgentAuth) Authenticate(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error) {
	key := agentAuthCacheKey(workspaceSlug, apiKey)
	now := c.now()

	if agent, ok := c.lookup(key, now); ok {
		metrics.RecordAgentAuth("hit")
		return agent, nil
	}

	agent, err := c.AgentService.Authenticate(ctx, workspaceSlug, apiKey)
	if err != nil {
		metrics.RecordAgentAuth("miss")
		return nil, err
	}

	c.store(key, agent, now)
	metrics.RecordAgentAuth("miss")
	return agent, nil
}

// lookup returns a copy of the cached agent when the entry is present, unexpired
// as a cache entry, and the agent's own key has not expired in the meantime.
//
// The second check matters: the entry is valid for ttl, but the KEY carries its
// own ExpiresAt, and the uncached path rejects an expired key. Without
// re-checking here a key that expires mid-TTL would keep working until the entry
// aged out.
func (c *cachedAgentAuth) lookup(key [sha256.Size]byte, now time.Time) (*domain.Agent, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || !now.Before(entry.expiresAt) {
		return nil, false
	}
	if entry.agent.IsKeyExpiredAt(now) {
		return nil, false
	}
	agent := entry.agent // copy out from under the map
	return &agent, true
}

// store records a successful verification.
func (c *cachedAgentAuth) store(key [sha256.Size]byte, agent *domain.Agent, now time.Time) {
	if agent == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= agentAuthCacheMaxEntries {
		c.purgeExpiredLocked(now)
		if len(c.entries) >= agentAuthCacheMaxEntries {
			// Still full of live entries: drop everything rather than grow
			// without bound. The cost is one bcrypt per active agent.
			c.entries = make(map[[sha256.Size]byte]cachedAuthEntry)
			c.byAgent = make(map[uuid.UUID]map[[sha256.Size]byte]struct{})
		}
	}

	c.entries[key] = cachedAuthEntry{agent: *agent, expiresAt: now.Add(c.ttl)}
	keys, ok := c.byAgent[agent.ID]
	if !ok {
		keys = make(map[[sha256.Size]byte]struct{}, 1)
		c.byAgent[agent.ID] = keys
	}
	keys[key] = struct{}{}
}

// purgeExpiredLocked drops aged-out entries. Caller must hold the write lock.
func (c *cachedAgentAuth) purgeExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			c.deleteEntryLocked(key, entry.agent.ID)
		}
	}
}

// deleteEntryLocked removes one entry and its byAgent back-reference.
// Caller must hold the write lock.
func (c *cachedAgentAuth) deleteEntryLocked(key [sha256.Size]byte, agentID uuid.UUID) {
	delete(c.entries, key)
	if keys, ok := c.byAgent[agentID]; ok {
		delete(keys, key)
		if len(keys) == 0 {
			delete(c.byAgent, agentID)
		}
	}
}

// InvalidateAgent drops every cached verification belonging to agentID.
func (c *cachedAgentAuth) InvalidateAgent(agentID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.byAgent[agentID] {
		delete(c.entries, key)
	}
	delete(c.byAgent, agentID)
}

// RotateAPIKey invalidates the agent's cached verifications before returning.
//
// This is the half of the TTL trade that must not be left to the clock: an
// operator who rotates a key because it leaked expects the old key to stop
// working now, not within a minute.
func (c *cachedAgentAuth) RotateAPIKey(ctx context.Context, agentID uuid.UUID) (string, error) {
	rawKey, err := c.AgentService.RotateAPIKey(ctx, agentID)
	// Invalidate on the error path too: a rotation that failed after the row was
	// written would otherwise leave this process serving the superseded key.
	c.InvalidateAgent(agentID)
	return rawKey, err
}

// Delete invalidates the agent's cached verifications before returning, so a
// deleted agent's key stops authenticating immediately.
func (c *cachedAgentAuth) Delete(ctx context.Context, id uuid.UUID) error {
	err := c.AgentService.Delete(ctx, id)
	c.InvalidateAgent(id)
	return err
}

// SetAgentActivityLogRepo forwards the optional dependency to the wrapped
// service, so that wrapping does not break the AgentServiceConfigurable type
// assertion done at wiring time in cmd/api. Without this, the wrapper would
// fail the assertion and the activity log would silently stop being written.
func (c *cachedAgentAuth) SetAgentActivityLogRepo(repo repository.AgentActivityLogRepository) {
	if configurable, ok := c.AgentService.(AgentServiceConfigurable); ok {
		configurable.SetAgentActivityLogRepo(repo)
	}
}

// Compile-time proof that the wrapper still satisfies the optional-dependency
// interface the wiring code asserts on.
var _ AgentServiceConfigurable = (*cachedAgentAuth)(nil)
