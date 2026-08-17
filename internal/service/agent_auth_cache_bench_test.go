package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// benchAuthFixture builds a REAL agentService (mock repos, real bcrypt cost-12
// hash) plus the cached wrapper around it, so the two benchmarks below differ in
// exactly one thing: whether the bcrypt comparison runs.
func benchAuthFixture(b *testing.B) (inner, cached AgentService, rawKey string) {
	b.Helper()

	agentRepo := NewMockAgentRepository()
	activityRepo := NewMockActivityLogRepository()
	wsRepo := NewMockWorkspaceRepository()

	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme Corp", Slug: "acme"}
	wsRepo.items[ws.ID] = ws

	timeNow = func() time.Time { return frozenTime }

	inner = NewAgentService(agentRepo, activityRepo, wsRepo)
	out, err := inner.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Bench Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	if err != nil {
		b.Fatalf("register: %v", err)
	}

	// Sanity: the hash really is cost 12, so the "before" number is the one
	// production pays and not something the mock quietly weakened.
	cost, err := bcrypt.Cost([]byte(out.Agent.APIKeyHash))
	if err != nil || cost != bcryptCost {
		b.Fatalf("expected bcrypt cost %d, got %d (err=%v)", bcryptCost, cost, err)
	}

	wrapper := NewCachedAgentAuth(inner, AgentAuthCacheTTL).(*cachedAgentAuth)
	// Share the frozen clock the wrapped service uses. Without this the
	// wrapper's real-time clock sees the fixture's frozen ExpiresAt as already
	// past and correctly refuses every cache hit — which would make this
	// benchmark measure bcrypt twice.
	wrapper.now = func() time.Time { return timeNow() }

	return inner, wrapper, out.APIKey
}

// BenchmarkAgentAuthenticate_Uncached measures today's per-request cost.
func BenchmarkAgentAuthenticate_Uncached(b *testing.B) {
	inner, _, key := benchAuthFixture(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := inner.Authenticate(ctx, "acme", key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAgentAuthenticate_Cached measures the same call through the wrapper
// after the first verification.
func BenchmarkAgentAuthenticate_Cached(b *testing.B) {
	_, cached, key := benchAuthFixture(b)
	ctx := context.Background()

	if _, err := cached.Authenticate(ctx, "acme", key); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := cached.Authenticate(ctx, "acme", key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAgentAuthenticate_CachedParallel is the shape that matters on a
// 2-vCPU box: many concurrent requests hitting the same entry through one
// RWMutex.
func BenchmarkAgentAuthenticate_CachedParallel(b *testing.B) {
	_, cached, key := benchAuthFixture(b)
	ctx := context.Background()

	if _, err := cached.Authenticate(ctx, "acme", key); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := cached.Authenticate(ctx, "acme", key); err != nil {
				b.Fatal(err)
			}
		}
	})
}
