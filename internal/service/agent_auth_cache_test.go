package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// Test double: an AgentService that counts how often the expensive path ran.
// ---------------------------------------------------------------------------

// countingAgentService implements just enough of AgentService to exercise the
// cache wrapper, and counts every call that the cache is supposed to elide.
type countingAgentService struct {
	AgentService // nil: any method not overridden below panics if called

	authCalls   atomic.Int64
	rotateCalls atomic.Int64
	deleteCalls atomic.Int64

	// agent is returned by Authenticate when authErr is nil.
	mu           sync.Mutex
	agent        *domain.Agent
	authErr      error
	rotErr       error
	delErr       error
	configed     bool
	configedRepo repository.AgentActivityLogRepository
}

func (s *countingAgentService) Authenticate(_ context.Context, _, _ string) (*domain.Agent, error) {
	s.authCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authErr != nil {
		return nil, s.authErr
	}
	// Return a fresh pointer each time, as the real service does.
	cp := *s.agent
	return &cp, nil
}

func (s *countingAgentService) RotateAPIKey(_ context.Context, _ uuid.UUID) (string, error) {
	s.rotateCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rotErr != nil {
		return "", s.rotErr
	}
	return "agk_acme_rotated", nil
}

func (s *countingAgentService) Delete(_ context.Context, _ uuid.UUID) error {
	s.deleteCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delErr
}

func (s *countingAgentService) SetAgentActivityLogRepo(repo repository.AgentActivityLogRepository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configed = true
	s.configedRepo = repo
}

// newCacheFixture returns a wrapper over a counting inner service, plus a
// controllable clock.
func newCacheFixture(t *testing.T, ttl time.Duration) (*cachedAgentAuth, *countingAgentService, *time.Time) {
	t.Helper()
	inner := &countingAgentService{
		agent: &domain.Agent{
			ID:          uuid.New(),
			WorkspaceID: uuid.New(),
			Name:        "Bill",
		},
	}
	clock := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	wrapped := NewCachedAgentAuth(inner, ttl).(*cachedAgentAuth)
	wrapped.now = func() time.Time { return clock }
	return wrapped, inner, &clock
}

// ---------------------------------------------------------------------------
// Cache hit / miss behaviour
// ---------------------------------------------------------------------------

func TestCachedAgentAuth_SecondCallSkipsInnerService(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Minute)
	ctx := context.Background()

	first, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	require.NotNil(t, first)

	for i := 0; i < 25; i++ {
		got, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
		require.NoError(t, err)
		assert.Equal(t, first.ID, got.ID)
		assert.Equal(t, first.WorkspaceID, got.WorkspaceID)
		assert.Equal(t, "Bill", got.Name)
	}

	assert.Equal(t, int64(1), inner.authCalls.Load(),
		"bcrypt path must run exactly once for 26 requests with the same key")
}

func TestCachedAgentAuth_DifferentKeysDoNotShareEntries(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Minute)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_one")
	require.NoError(t, err)
	_, err = c.Authenticate(ctx, "acme", "agk_acme_two")
	require.NoError(t, err)

	assert.Equal(t, int64(2), inner.authCalls.Load())
}

// A hit computed under one workspace slug must never answer a request made
// under a different one — the slug is what resolves the workspace.
func TestCachedAgentAuth_SlugIsPartOfTheCacheKey(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Minute)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	_, err = c.Authenticate(ctx, "other", "agk_acme_secret")
	require.NoError(t, err)

	assert.Equal(t, int64(2), inner.authCalls.Load())
}

// Guards the NUL separator in agentAuthCacheKey: without it, ("ab","c") and
// ("a","bc") would collide.
func TestAgentAuthCacheKey_NoConcatenationCollision(t *testing.T) {
	assert.NotEqual(t, agentAuthCacheKey("ab", "c"), agentAuthCacheKey("a", "bc"))
	assert.Equal(t, agentAuthCacheKey("acme", "k"), agentAuthCacheKey("acme", "k"))
}

// The map key must be a digest, not the credential itself.
func TestAgentAuthCacheKey_DoesNotEmbedRawKey(t *testing.T) {
	raw := "agk_acme_0123456789abcdef"
	key := agentAuthCacheKey("acme", raw)
	assert.NotContains(t, string(key[:]), raw)
	assert.Len(t, key, sha256.Size)
}

// ---------------------------------------------------------------------------
// Expiry
// ---------------------------------------------------------------------------

func TestCachedAgentAuth_EntryExpiresAfterTTL(t *testing.T) {
	c, inner, clock := newCacheFixture(t, time.Minute)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)

	*clock = clock.Add(59 * time.Second)
	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	assert.Equal(t, int64(1), inner.authCalls.Load(), "still inside the TTL")

	*clock = clock.Add(2 * time.Second)
	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	assert.Equal(t, int64(2), inner.authCalls.Load(), "TTL elapsed: must re-verify")
}

// A key whose own ExpiresAt passes mid-TTL must stop authenticating from cache,
// otherwise the cache would extend the life of an expired credential.
func TestCachedAgentAuth_KeyExpiryBeatsCacheTTL(t *testing.T) {
	c, inner, clock := newCacheFixture(t, time.Hour)
	ctx := context.Background()

	expiry := clock.Add(10 * time.Second)
	inner.agent.ExpiresAt = &expiry

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	assert.Equal(t, int64(1), inner.authCalls.Load())

	// Past the key's own expiry but well inside the one-hour cache TTL.
	*clock = clock.Add(30 * time.Second)
	inner.mu.Lock()
	inner.authErr = apierror.Unauthorized("API key expired")
	inner.mu.Unlock()

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.Error(t, err, "expired key must not be served from cache")
	assert.Equal(t, int64(2), inner.authCalls.Load(),
		"must fall through to the real check rather than answer from cache")
}

// ---------------------------------------------------------------------------
// Failures are not cached
// ---------------------------------------------------------------------------

func TestCachedAgentAuth_FailuresAreNotCached(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Minute)
	ctx := context.Background()
	inner.authErr = apierror.Unauthorized("invalid API key")

	for i := 0; i < 3; i++ {
		_, err := c.Authenticate(ctx, "acme", "agk_acme_wrong")
		require.Error(t, err)
	}
	assert.Equal(t, int64(3), inner.authCalls.Load(),
		"a rejected key must never be remembered: the map keys would be attacker-chosen")
}

// ---------------------------------------------------------------------------
// Invalidation
// ---------------------------------------------------------------------------

func TestCachedAgentAuth_RotateInvalidatesImmediately(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()

	agent, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	require.Equal(t, int64(1), inner.authCalls.Load())

	_, err = c.RotateAPIKey(ctx, agent.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), inner.rotateCalls.Load())

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	assert.Equal(t, int64(2), inner.authCalls.Load(),
		"the pre-rotation key must be re-verified, not answered from cache")
}

// A rotation that errors may still have written the row; the cache must not
// keep serving the superseded key in that case either.
func TestCachedAgentAuth_RotateInvalidatesOnError(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()

	agent, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)

	inner.rotErr = errors.New("update failed")
	_, err = c.RotateAPIKey(ctx, agent.ID)
	require.Error(t, err)

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	assert.Equal(t, int64(2), inner.authCalls.Load())
}

func TestCachedAgentAuth_DeleteInvalidatesImmediately(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()

	agent, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)

	require.NoError(t, c.Delete(ctx, agent.ID))
	assert.Equal(t, int64(1), inner.deleteCalls.Load())

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	assert.Equal(t, int64(2), inner.authCalls.Load(),
		"a deleted agent's key must be re-verified against the database")
}

func TestCachedAgentAuth_DeletePropagatesError(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)
	inner.delErr = apierror.NotFound("Agent")
	assert.Error(t, c.Delete(context.Background(), uuid.New()))
}

// Invalidation must drop every key an agent has cached, not just the newest —
// a rotation racing an in-flight request can leave two.
func TestCachedAgentAuth_InvalidateAgentDropsAllKeysForThatAgent(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()

	agent, err := c.Authenticate(ctx, "acme", "agk_acme_one")
	require.NoError(t, err)
	_, err = c.Authenticate(ctx, "acme", "agk_acme_two")
	require.NoError(t, err)
	require.Equal(t, int64(2), inner.authCalls.Load())

	c.InvalidateAgent(agent.ID)

	c.mu.RLock()
	entries := len(c.entries)
	back := len(c.byAgent)
	c.mu.RUnlock()
	assert.Zero(t, entries, "no entry may survive invalidation")
	assert.Zero(t, back, "the agent back-reference must be dropped too")
}

// Invalidating an agent that has nothing cached must be a no-op, not a panic.
func TestCachedAgentAuth_InvalidateUnknownAgentIsNoop(t *testing.T) {
	c, _, _ := newCacheFixture(t, time.Hour)
	assert.NotPanics(t, func() { c.InvalidateAgent(uuid.New()) })
}

// ---------------------------------------------------------------------------
// Copy-out: a caller mutating its result must not corrupt the cache
// ---------------------------------------------------------------------------

func TestCachedAgentAuth_ReturnsIndependentCopies(t *testing.T) {
	c, _, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()

	first, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	first.Name = "MUTATED"

	second, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	assert.Equal(t, "Bill", second.Name,
		"the cached entry must not be reachable through a returned pointer")
	assert.NotSame(t, first, second)
}

// ---------------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------------

func TestCachedAgentAuth_BoundedByMaxEntries(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()

	// Each call presents a distinct valid key belonging to a distinct agent.
	for i := 0; i < agentAuthCacheMaxEntries+50; i++ {
		inner.mu.Lock()
		inner.agent = &domain.Agent{ID: uuid.New(), WorkspaceID: uuid.New(), Name: "a"}
		inner.mu.Unlock()
		_, err := c.Authenticate(ctx, "acme", fmt.Sprintf("agk_acme_%d", i))
		require.NoError(t, err)
	}

	c.mu.RLock()
	entries := len(c.entries)
	back := len(c.byAgent)
	c.mu.RUnlock()
	assert.LessOrEqual(t, entries, agentAuthCacheMaxEntries)
	assert.LessOrEqual(t, back, agentAuthCacheMaxEntries)
}

// Expired entries are reclaimed rather than merely ignored.
func TestCachedAgentAuth_PurgeReclaimsExpiredEntries(t *testing.T) {
	c, inner, clock := newCacheFixture(t, time.Minute)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		inner.mu.Lock()
		inner.agent = &domain.Agent{ID: uuid.New(), WorkspaceID: uuid.New()}
		inner.mu.Unlock()
		_, err := c.Authenticate(ctx, "acme", fmt.Sprintf("agk_acme_%d", i))
		require.NoError(t, err)
	}

	*clock = clock.Add(2 * time.Minute)
	c.mu.Lock()
	c.purgeExpiredLocked(*clock)
	entries := len(c.entries)
	back := len(c.byAgent)
	c.mu.Unlock()

	assert.Zero(t, entries)
	assert.Zero(t, back, "back-references must be reclaimed with their entries")
}

// ---------------------------------------------------------------------------
// Wiring / pass-through
// ---------------------------------------------------------------------------

// cmd/api asserts AgentServiceConfigurable on the value it wires; the wrapper
// must keep satisfying it or the agent activity log silently stops recording.
func TestCachedAgentAuth_ForwardsActivityLogRepo(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)

	configurable, ok := AgentService(c).(AgentServiceConfigurable)
	require.True(t, ok, "wrapper must satisfy AgentServiceConfigurable")

	configurable.SetAgentActivityLogRepo(nil)

	inner.mu.Lock()
	defer inner.mu.Unlock()
	assert.True(t, inner.configed, "the dependency must reach the wrapped service")
}

func TestNewCachedAgentAuth_NonPositiveTTLFallsBackToDefault(t *testing.T) {
	c := NewCachedAgentAuth(&countingAgentService{}, 0).(*cachedAgentAuth)
	assert.Equal(t, AgentAuthCacheTTL, c.ttl)

	c = NewCachedAgentAuth(&countingAgentService{}, -time.Second).(*cachedAgentAuth)
	assert.Equal(t, AgentAuthCacheTTL, c.ttl)
}

// store must ignore a nil agent rather than panic writing a nil deref.
func TestCachedAgentAuth_StoreIgnoresNilAgent(t *testing.T) {
	c, _, clock := newCacheFixture(t, time.Hour)
	assert.NotPanics(t, func() {
		c.store(agentAuthCacheKey("acme", "k"), nil, *clock)
	})
	c.mu.RLock()
	defer c.mu.RUnlock()
	assert.Zero(t, len(c.entries))
}

// ---------------------------------------------------------------------------
// Concurrency (this file's whole point is a shared map on the request path)
// ---------------------------------------------------------------------------

func TestCachedAgentAuth_ConcurrentAccessIsRaceFree(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()
	agentID := inner.agent.ID

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				switch j % 4 {
				case 0:
					_, _ = c.Authenticate(ctx, "acme", fmt.Sprintf("agk_acme_%d", i%3))
				case 1:
					_, _ = c.Authenticate(ctx, "acme", "agk_acme_shared")
				case 2:
					c.InvalidateAgent(agentID)
				default:
					_, _ = c.Authenticate(ctx, "acme", "agk_acme_shared")
				}
			}
		}(i)
	}
	wg.Wait()
}
