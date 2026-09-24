package service

import (
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

const (
	// oauthAuthCacheTTL bounds how stale a positive mot_ authentication can be.
	// AuthenticateAccessToken costs ~7 DB round trips (token, grant, workspace
	// membership, agent, agent workspace grant, ...) and runs on every API call
	// the connector makes. A cache hit re-reads only the oauth grant (one PK
	// lookup), so revoking the grant itself takes effect on the very next
	// request — from any API replica, and whatever path revoked it. Everything
	// else the full path checks (the user's workspace membership, the connector
	// agent existing, its agent_workspace_grants connection) is served from the
	// cache, and none of those paths lives in this service: an admin cutting
	// the agent's connection, deleting the agent, or removing the user from the
	// workspace cannot evict an entry. For those the TTL IS the worst-case delay
	// before the token stops working. Eviction is in-process, so with several API
	// replicas the token-revoke and family-revoke paths are also TTL-bounded on
	// the replicas that did not serve the revoke. 15 s is the bound the review
	// of the OAuth server asked for: short enough to read as "immediate" to a
	// human revoking access, long enough to collapse a burst of tool calls.
	oauthAuthCacheTTL = 15 * time.Second

	// oauthAuthCacheMaxEntries caps memory. Reaching it drops expired entries
	// first and, if that is not enough, everything: a cache miss only costs the
	// uncached path, so a crude reset is safe and needs no LRU bookkeeping.
	oauthAuthCacheMaxEntries = 4096
)

// oauthAuthEntry is one cached positive authentication.
type oauthAuthEntry struct {
	agent    domain.Agent
	grantID  uuid.UUID
	familyID uuid.UUID
	// expires is the earlier of cache TTL and the token's own expires_at, so the
	// cache can never outlive the token it vouches for.
	expires time.Time
}

// oauthAuthCache holds resolved mot_ authentications keyed by token digest.
// Only successes are cached: a rejected token is re-checked every time, so a
// revocation can never be masked by a stale "invalid" and nothing an attacker
// sends can fill the cache.
type oauthAuthCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]oauthAuthEntry
	// gen counts evictions. A request snapshots it before it starts reading the
	// database and put drops the result if it moved: without that, a request
	// that read a still-valid token just before a revoke could insert its
	// (now stale) answer AFTER the eviction and have it served for a full TTL.
	gen uint64
}

func newOAuthAuthCache(ttl time.Duration) *oauthAuthCache {
	return &oauthAuthCache{ttl: ttl, entries: make(map[string]oauthAuthEntry)}
}

// generation returns the eviction counter to pass back to put.
func (c *oauthAuthCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// get returns a copy of the cached agent (the struct is copied, so reassigning
// its fields cannot corrupt what the next request sees; slice/pointer fields
// are shared) and the grant it was authenticated through.
func (c *oauthAuthCache) get(key string, now time.Time) (*domain.Agent, uuid.UUID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return nil, uuid.Nil, false
	}
	e, ok := c.entries[key]
	if !ok {
		return nil, uuid.Nil, false
	}
	if !now.Before(e.expires) {
		delete(c.entries, key)
		return nil, uuid.Nil, false
	}
	a := e.agent
	return &a, e.grantID, true
}

// put stores a positive authentication read while the eviction counter stood at
// gen; it is dropped if anything was evicted since.
func (c *oauthAuthCache) put(key string, agent *domain.Agent, grantID, familyID uuid.UUID, tokenExpires, now time.Time, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 || gen != c.gen {
		return
	}
	exp := now.Add(c.ttl)
	if tokenExpires.Before(exp) {
		exp = tokenExpires
	}
	if len(c.entries) >= oauthAuthCacheMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= oauthAuthCacheMaxEntries {
			c.entries = make(map[string]oauthAuthEntry)
		}
	}
	c.entries[key] = oauthAuthEntry{agent: *agent, grantID: grantID, familyID: familyID, expires: exp}
}

func (c *oauthAuthCache) evictGrant(grantID uuid.UUID) {
	c.evictWhere(func(e oauthAuthEntry) bool { return e.grantID == grantID })
}

func (c *oauthAuthCache) evictFamily(familyID uuid.UUID) {
	c.evictWhere(func(e oauthAuthEntry) bool { return e.familyID == familyID })
}

func (c *oauthAuthCache) evictToken(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	delete(c.entries, key)
}

func (c *oauthAuthCache) evictWhere(match func(oauthAuthEntry) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k, e := range c.entries {
		if match(e) {
			delete(c.entries, k)
		}
	}
}
