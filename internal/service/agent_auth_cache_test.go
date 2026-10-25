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

	authCalls        atomic.Int64
	rotateCalls      atomic.Int64
	deleteCalls      atomic.Int64
	revokeCheckCalls atomic.Int64

	// agent is returned by Authenticate when authErr is nil.
	mu           sync.Mutex
	agent        *domain.Agent
	authErr      error
	rotErr       error
	delErr       error
	configed     bool
	configedRepo repository.AgentActivityLogRepository

	// revoked/revokeCheckErr drive IsGrantRevoked, below. A test that never
	// authenticates a GrantID-bearing agent never reaches this method at all
	// (cachedAuthStillValid short-circuits on a nil GrantID) — every
	// pre-existing test in this file is exactly that case, unchanged.
	revoked        bool
	revokeCheckErr error

	// wsDeleted/wsCheckErr drive IsWorkspaceDeleted, below — same shape as
	// the grant pair above, but this one runs on EVERY hit (see
	// cachedAuthStillValid), not just grant-derived ones.
	wsDeleted           atomic.Bool
	wsDeletedCheckErr   atomic.Pointer[error]
	wsDeletedCheckCalls atomic.Int64
}

// IsGrantRevoked implements GrantRevocationChecker, so this fixture doubles
// as both branches cachedAuthStillValid can take for a grant-derived agent:
// checker present, revoked/valid per s.revoked, or erroring per
// s.revokeCheckErr.
func (s *countingAgentService) IsGrantRevoked(_ context.Context, _ uuid.UUID) (bool, error) {
	s.revokeCheckCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revokeCheckErr != nil {
		return false, s.revokeCheckErr
	}
	return s.revoked, nil
}

// IsWorkspaceDeleted implements WorkspaceDeletionChecker, so this fixture
// doubles as both branches cachedAuthStillValid can take for the
// workspace-deletion half: present, deleted/live per s.wsDeleted, or erroring
// per s.wsDeletedCheckErr.
func (s *countingAgentService) IsWorkspaceDeleted(_ context.Context, _ uuid.UUID) (bool, error) {
	s.wsDeletedCheckCalls.Add(1)
	if errPtr := s.wsDeletedCheckErr.Load(); errPtr != nil {
		return false, *errPtr
	}
	return s.wsDeleted.Load(), nil
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

func (s *countingAgentService) SetCheckoutHeartbeatExtender(_ CheckoutHeartbeatExtender) {}

func (s *countingAgentService) SetAgentWorkspaceGrantRepo(_ repository.AgentWorkspaceGrantRepository) {
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
	wrapped.jitter = func(ttl time.Duration) time.Duration { return ttl }
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
// Grant revocation freshness (task U2 AC4, an internal ticket)
//
// A grant can be revoked by a direct SQL UPDATE — the only revoke path that
// exists until U3 ships an API — which is a change this process never gets
// told about via InvalidateAgent. These tests are the regression guard for
// the gap independent review found before the fix: a cache HIT must not
// answer from a stale success once the grant behind it has been revoked,
// even though the revoke happened entirely outside this process.
// ---------------------------------------------------------------------------

// newGrantCacheFixture is newCacheFixture, but the inner agent carries a
// GrantID — i.e. it looks like a login that went through
// agentService.authenticateViaGrant, the only path that ever sets GrantID.
func newGrantCacheFixture(t *testing.T, ttl time.Duration) (*cachedAgentAuth, *countingAgentService, *time.Time) {
	t.Helper()
	c, inner, clock := newCacheFixture(t, ttl)
	grantID := uuid.New()
	inner.agent.GrantID = &grantID
	return c, inner, clock
}

// The exact scenario Gary's independent review reproduced against the
// deployed cachedAgentAuth+agentService pairing: a grant-derived key is
// cached, the grant is revoked mid-TTL by something outside this process,
// and the SAME key must be denied on the very next request — not up to
// AgentAuthCacheTTL later.
func TestCachedAgentAuth_GrantRevokedMidTTL_DeniesImmediately(t *testing.T) {
	c, inner, _ := newGrantCacheFixture(t, time.Hour)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	require.Equal(t, int64(1), inner.authCalls.Load(), "first call must be a real verification")

	// Simulate a direct SQL revoke: nothing in this process called
	// InvalidateAgent, so the cache entry itself is untouched — only what the
	// (now revoked) grant would report has changed.
	inner.mu.Lock()
	inner.revoked = true
	inner.authErr = apierror.Unauthorized("invalid API key")
	inner.mu.Unlock()

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.Error(t, err, "a revoked grant must not be served from a still-warm cache entry")
	assert.Equal(t, int64(1), inner.revokeCheckCalls.Load(),
		"the freshness check must have run to notice the revoke")
	assert.Equal(t, int64(2), inner.authCalls.Load(),
		"denial must come from a real re-authenticate, not be synthesized by the cache")
}

// A grant-derived hit that is still valid pays one freshness check per
// request — that's the cost this fix accepts — but never re-pays bcrypt.
func TestCachedAgentAuth_GrantStillValid_RecheckedButNotReAuthenticated(t *testing.T) {
	c, inner, _ := newGrantCacheFixture(t, time.Hour)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
		require.NoError(t, err)
	}

	assert.Equal(t, int64(1), inner.authCalls.Load(),
		"a valid grant must still skip the bcrypt path on every hit")
	assert.Equal(t, int64(10), inner.revokeCheckCalls.Load(),
		"but the freshness check runs on every one of those hits")
}

// If the freshness check itself cannot answer (a transient DB error), the
// entry must NOT be trusted on the strength of "we couldn't prove it's bad" —
// that would quietly reopen exactly the hole this fix closes.
func TestCachedAgentAuth_GrantRevocationCheckErrors_IsNotTrusted(t *testing.T) {
	c, inner, _ := newGrantCacheFixture(t, time.Hour)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)

	inner.mu.Lock()
	inner.revokeCheckErr = errors.New("db unavailable")
	inner.mu.Unlock()

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err, "the inner service is still healthy; only the freshness check errored")
	assert.Equal(t, int64(2), inner.authCalls.Load(),
		"an unreadable freshness check must fall through to a real re-authenticate, not trust the cache")
}

// newFrozenCachedRealAgentService wraps a real *agentService (as produced by
// setupGrantAuthFixture) in cachedAgentAuth, pinning the cache's OWN clock to
// frozenTime — the same clock seedHomeAgent/seedGrant's ExpiresAt is computed
// against, via the package-level timeNow var setupGrantAuthFixture overrides.
//
// Without this, cachedAgentAuth defaults to real time.Now(), which drifts
// arbitrarily far past a key minted under frozenTime as wall-clock time
// passes — and IsKeyExpiredAt on a cache lookup starts returning true for a
// reason that has nothing to do with whatever the test is actually trying to
// exercise. That is exactly how independent review found
// TestCachedAgentAuth_WrappingRealAgentService_WorkspaceSoftDeletedDenies
// Immediately vacuous: it kept passing even with the fix
// reverted, because by the time this suite runs in 2026, the "hit" it
// exercised was never a hit at all — the entry had already fallen through to
// a real, unrelated re-Authenticate on key expiry before cachedAuthStillValid
// ever ran. Its older sibling below shared the same latent bug — passing for
// the same wrong reason since before this fix existed.
func newFrozenCachedRealAgentService(inner AgentService, ttl time.Duration) *cachedAgentAuth {
	wrapped := NewCachedAgentAuth(inner, ttl).(*cachedAgentAuth)
	wrapped.now = func() time.Time { return frozenTime }
	return wrapped
}

// TestCachedAgentAuth_WrappingRealAgentService_RevokedGrantDeniesImmediately
// wires the EXACT composition cmd/api/main.go uses in prod —
// NewCachedAgentAuth(NewAgentService(...), ttl) over a real *agentService,
// not the countingAgentService stand-in above — and reproduces the scenario
// Gary's independent review found manually against a live stand before
// this fix: a grant cached warm, then revoked by a direct SQL-shaped UPDATE
// (grantRepo.Revoke, mirroring the only revoke path that exists pre-U3),
// must deny the same key on the very next request rather than up to
// AgentAuthCacheTTL later. TestAgentService_Authenticate_RevokedGrant_Denies
// AndDoesNotFallBack (above, in agent_service_grant_auth_test.go) proves the
// bare *agentService gets this right; this proves the wrapped pairing prod
// actually deploys does too — which is exactly the gap the bare-service test
// alone could not catch.
func TestCachedAgentAuth_WrappingRealAgentService_RevokedGrantDeniesImmediately(t *testing.T) {
	f, homeWS := setupGrantAuthFixture()
	agent, _ := seedHomeAgent(t, f, homeWS, "Grant Agent")

	guestWS := &domain.Workspace{ID: uuid.New(), Name: "Guest Co", Slug: "guest"}
	f.svc.workspaceRepo.(*MockWorkspaceRepository).items[guestWS.ID] = guestWS
	guestKey := seedGrant(t, f, agent.ID, guestWS, "admin", false)

	cached := newFrozenCachedRealAgentService(f.svc, time.Hour)
	ctx := context.Background()

	first, err := cached.Authenticate(ctx, guestWS.Slug, guestKey)
	require.NoError(t, err)
	require.NotNil(t, first.GrantID, "a grant-derived login must carry GrantID for the freshness check to find")

	// Mid-TTL revoke, exactly as a direct SQL UPDATE would do it — no call
	// anywhere in this test touches InvalidateAgent.
	f.grantRepo.SeedRevoke(*first.GrantID, frozenTime)

	_, err = cached.Authenticate(ctx, guestWS.Slug, guestKey)
	requireUnauthorized(t, err)
}

// ---------------------------------------------------------------------------
// Workspace soft-delete freshness — Kim's live side-finding
// against the an internal ticket deploy: a guest key already warm in cache kept
// reading its own now-soft-deleted, now-empty workspace's member routes for
// the rest of the TTL, because a cache hit skips the very Authenticate call
// that would otherwise have re-run workspaceRepo.GetBySlug/GetByID's own
// deleted_at IS NULL filter.
// ---------------------------------------------------------------------------

// The workspace-deletion analogue of TestCachedAgentAuth_GrantRevokedMidTTL_
// DeniesImmediately: a key is cached warm, its workspace is soft-deleted mid-
// TTL by something outside this process (nothing here calls InvalidateAgent),
// and the SAME key must be denied on the very next request — not up to
// AgentAuthCacheTTL later.
func TestCachedAgentAuth_WorkspaceSoftDeletedMidTTL_DeniesImmediately(t *testing.T) {
	c, inner, _ := newGrantCacheFixture(t, time.Hour)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	require.Equal(t, int64(1), inner.authCalls.Load(), "first call must be a real verification")

	// Simulate a soft-delete via a DELETE /workspaces/:id request handled by
	// a different request (or process): nothing here touches InvalidateAgent
	// or the cache entry itself — only what the workspace repo would now
	// report has changed.
	inner.wsDeleted.Store(true)
	inner.mu.Lock()
	inner.authErr = apierror.Unauthorized("invalid API key")
	inner.mu.Unlock()

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.Error(t, err, "a key for a soft-deleted workspace must not be served from a still-warm cache entry")
	assert.Equal(t, int64(1), inner.wsDeletedCheckCalls.Load(),
		"the freshness check must have run to notice the soft-delete")
	assert.Equal(t, int64(2), inner.authCalls.Load(),
		"denial must come from a real re-authenticate, not be synthesized by the cache")
}

// Same property, but for a legacy-path entry (GrantID nil) — the deletion
// check is not conditional on there being a grant to also check.
func TestCachedAgentAuth_WorkspaceSoftDeletedMidTTL_LegacyEntry_DeniesImmediately(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour) // GrantID nil — see newCacheFixture
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)

	inner.wsDeleted.Store(true)
	inner.mu.Lock()
	inner.authErr = apierror.Unauthorized("invalid API key")
	inner.mu.Unlock()

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.Error(t, err, "a legacy-path key for a soft-deleted workspace must not be served from cache either")
	assert.Zero(t, inner.revokeCheckCalls.Load(), "still no grant to check")
}

// If the freshness check itself cannot answer (a transient DB error), the
// entry must NOT be trusted on the strength of "we couldn't prove it's bad" —
// that would quietly reopen exactly the hole this fix closes. Mirrors
// TestCachedAgentAuth_GrantRevocationCheckErrors_IsNotTrusted for the
// workspace-deletion half.
func TestCachedAgentAuth_WorkspaceDeletionCheckErrors_IsNotTrusted(t *testing.T) {
	c, inner, _ := newGrantCacheFixture(t, time.Hour)
	ctx := context.Background()

	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)

	dbErr := errors.New("db unavailable")
	inner.wsDeletedCheckErr.Store(&dbErr)

	_, err = c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err, "the inner service is still healthy; only the freshness check errored")
	assert.Equal(t, int64(2), inner.authCalls.Load(),
		"an unreadable freshness check must fall through to a real re-authenticate, not trust the cache")
}

// TestCachedAgentAuth_WrappingRealAgentService_WorkspaceSoftDeletedDeniesImmediately
// wires the EXACT composition cmd/api/main.go uses in prod, same as
// TestCachedAgentAuth_WrappingRealAgentService_RevokedGrantDeniesImmediately
// above, but reproduces Kim's live an internal ticket finding instead: a grant-
// derived guest key cached warm, then its OWN workspace soft-deleted via
// workspaceRepo.Delete (the same row DELETE /workspaces/:id acts on), must
// deny the same key on the very next request. Deleting the fix's check in
// cachedAuthStillValid (revert cachedAuthStillValid to only look at
// IsGrantRevoked) reproduces the pre-fix 200-from-cache behavior this test
// exists to catch.
func TestCachedAgentAuth_WrappingRealAgentService_WorkspaceSoftDeletedDeniesImmediately(t *testing.T) {
	f, homeWS := setupGrantAuthFixture()
	agent, _ := seedHomeAgent(t, f, homeWS, "Grant Agent")

	guestWS := &domain.Workspace{ID: uuid.New(), Name: "Guest Co", Slug: "guest"}
	wsRepo := f.svc.workspaceRepo.(*MockWorkspaceRepository)
	wsRepo.items[guestWS.ID] = guestWS
	guestKey := seedGrant(t, f, agent.ID, guestWS, "admin", false)

	cached := newFrozenCachedRealAgentService(f.svc, time.Hour)
	ctx := context.Background()

	first, err := cached.Authenticate(ctx, guestWS.Slug, guestKey)
	require.NoError(t, err)
	require.NotNil(t, first.GrantID)

	// Soft-delete the guest workspace itself — the grant row is untouched,
	// same as Kim's live probe (explicit revoke is a separate, already-
	// covered case). Nothing here touches InvalidateAgent.
	require.NoError(t, wsRepo.Delete(ctx, guestWS.ID))

	_, err = cached.Authenticate(ctx, guestWS.Slug, guestKey)
	requireUnauthorized(t, err)
}

// A legacy-path entry (no connection row exists — GrantID is nil) has no
// grant to re-check, so the revocation half of the freshness check keeps
// being skipped exactly as before this fix. It is NOT exempt from the
// workspace-deletion half, though — a legacy agent's home
// workspace can be soft-deleted the same as any grant-derived one's, and
// before that check existed a legacy hit was trusted for its full TTL with
// zero re-verification of anything at all.
func TestCachedAgentAuth_LegacyEntry_SkipsRevocationCheckButNotWorkspaceDeletionCheck(t *testing.T) {
	c, inner, _ := newCacheFixture(t, time.Hour) // GrantID nil — see newCacheFixture
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
		require.NoError(t, err)
	}

	assert.Equal(t, int64(1), inner.authCalls.Load())
	assert.Zero(t, inner.revokeCheckCalls.Load(),
		"a legacy (non-grant) login has no grant for the revocation check to look at")
	assert.Equal(t, int64(4), inner.wsDeletedCheckCalls.Load(),
		"but its workspace is re-checked on every one of the 4 subsequent hits")
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

// slowAuthService makes the inner Authenticate take long enough that a burst
// of concurrent callers all arrive while the first one is still verifying.
type slowAuthService struct {
	*countingAgentService
	delay time.Duration
}

func (s *slowAuthService) Authenticate(ctx context.Context, slug, key string) (*domain.Agent, error) {
	time.Sleep(s.delay)
	return s.countingAgentService.Authenticate(ctx, slug, key)
}

// A burst of parallel requests carrying one cold key (an agent running several
// sessions, or every agent reconnecting after an API restart) must pay one
// bcrypt, not one per request: on a 2-core host N parallel cost-12 compares
// queue behind the CPU for seconds (an internal ticket, GET /agents/me 1.4-3.5s).
func TestCachedAgentAuth_ConcurrentMissesShareOneVerification(t *testing.T) {
	c, counting, _ := newCacheFixture(t, time.Minute)
	c.AgentService = &slowAuthService{countingAgentService: counting, delay: 150 * time.Millisecond}
	ctx := context.Background()

	const burst = 40
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, burst)
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
			if assert.NoError(t, err) {
				ids[i] = a.ID
			}
		}(i)
	}
	wg.Wait()

	assert.EqualValues(t, 1, counting.authCalls.Load(), "burst of %d cold requests must verify once", burst)
	for _, id := range ids {
		assert.Equal(t, counting.agent.ID, id)
	}
}

// Entries stored in the same instant must not all expire in the same instant.
func TestCachedAgentAuth_TTLIsJittered(t *testing.T) {
	c, _, clock := newCacheFixture(t, time.Minute)
	c.jitter = jitterTTL
	seen := map[time.Time]struct{}{}
	for i := 0; i < 50; i++ {
		c.store(agentAuthCacheKey("acme", uuid.NewString()), &domain.Agent{ID: uuid.New()}, *clock)
	}
	for _, e := range c.entries {
		assert.False(t, e.expiresAt.After(clock.Add(time.Minute)))
		assert.False(t, e.expiresAt.Before(clock.Add(45*time.Second)))
		seen[e.expiresAt] = struct{}{}
	}
	assert.Greater(t, len(seen), 10, "expiry times must be spread, not identical")
}

// One caller disconnecting mid-verification must not fail the others that share
// its verification.
func TestCachedAgentAuth_LeaderCancelDoesNotFailWaiters(t *testing.T) {
	c, counting, _ := newCacheFixture(t, time.Minute)
	c.AgentService = &slowAuthService{countingAgentService: counting, delay: 200 * time.Millisecond}

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := c.Authenticate(leaderCtx, "acme", "agk_acme_secret")
		leaderDone <- err
	}()
	time.Sleep(30 * time.Millisecond)

	waiterDone := make(chan error, 1)
	go func() {
		_, err := c.Authenticate(context.Background(), "acme", "agk_acme_secret")
		waiterDone <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()

	assert.ErrorIs(t, <-leaderDone, context.Canceled)
	assert.NoError(t, <-waiterDone)
	assert.EqualValues(t, 1, counting.authCalls.Load())
}

// TestAgentAuthCacheTTL_OutlivesAgentPollPeriod is the ratchet for task
// an internal ticket: agents poll about once a minute, so a TTL at or below that period
// turns every poll into a bcrypt miss and the fleet into a per-minute burst.
func TestAgentAuthCacheTTL_OutlivesAgentPollPeriod(t *testing.T) {
	const agentPollPeriod = time.Minute
	assert.GreaterOrEqual(t, AgentAuthCacheTTL, 5*agentPollPeriod)
}

// slowWorkspaceCheckService stretches the per-hit freshness check so a burst
// of hits overlaps inside it.
type slowWorkspaceCheckService struct {
	*countingAgentService
	delay time.Duration
}

func (s *slowWorkspaceCheckService) IsWorkspaceDeleted(ctx context.Context, id uuid.UUID) (bool, error) {
	time.Sleep(s.delay)
	return s.countingAgentService.IsWorkspaceDeleted(ctx, id)
}

// A burst of polls from one key (every MCP process of an agent ticks on the
// same second) must share the revocation queries that are in flight, but an
// answer is never reused once the call has returned.
func TestCachedAgentAuth_ConcurrentHitsShareOneFreshnessCheck(t *testing.T) {
	c, counting, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()
	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	c.AgentService = &slowWorkspaceCheckService{countingAgentService: counting, delay: 150 * time.Millisecond}

	const burst = 40
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.Less(t, counting.wsDeletedCheckCalls.Load(), int64(burst/4),
		"overlapping hits must share the in-flight freshness check")
	assert.EqualValues(t, 1, counting.authCalls.Load(), "hits must not re-verify the key")

	// Sequential calls after the burst each run their own check: nothing is memoised.
	before := counting.wsDeletedCheckCalls.Load()
	for i := 0; i < 3; i++ {
		_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
		require.NoError(t, err)
	}
	assert.EqualValues(t, before+3, counting.wsDeletedCheckCalls.Load())
}

// gatedWorkspaceCheckService reads the workspace state, then holds the FIRST
// call until released, so a revocation can commit between "read valid" and
// "return valid".
type gatedWorkspaceCheckService struct {
	*countingAgentService
	firstRead chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (s *gatedWorkspaceCheckService) IsWorkspaceDeleted(ctx context.Context, id uuid.UUID) (bool, error) {
	deleted, err := s.countingAgentService.IsWorkspaceDeleted(ctx, id)
	s.once.Do(func() {
		close(s.firstRead)
		<-s.release
	})
	return deleted, err
}

// A request that starts after a revocation commits must not receive the result
// of a check that read the pre-revocation state.
func TestCachedAgentAuth_RequestAfterRevocationDoesNotJoinStaleCheck(t *testing.T) {
	c, counting, _ := newCacheFixture(t, time.Hour)
	ctx := context.Background()
	_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
	require.NoError(t, err)
	gated := &gatedWorkspaceCheckService{
		countingAgentService: counting,
		firstRead:            make(chan struct{}),
		release:              make(chan struct{}),
	}
	c.AgentService = gated

	first := make(chan error, 1)
	go func() {
		_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
		first <- err
	}()
	<-gated.firstRead // the in-flight check has read "live"

	counting.wsDeleted.Store(true) // revocation commits

	second := make(chan error, 1)
	go func() {
		_, err := c.Authenticate(ctx, "acme", "agk_acme_secret")
		second <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the second request queue behind the first
	close(gated.release)

	require.NoError(t, <-first, "the request that started before the revocation may succeed")
	require.NoError(t, <-second) // the fixture's inner Authenticate always succeeds
	assert.EqualValues(t, 2, counting.authCalls.Load(),
		"the late request must see the revocation and fall through to a full Authenticate")
}
