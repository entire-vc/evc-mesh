package service

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Task #2045c890 (parent #93644be7, subtask 2 of 5): now that workspace
// deletion frees a workspace's slug for reuse (migration 20260911002 +
// WorkspaceRepo.Delete's rename-on-delete, task #c164a5df), a NEW workspace
// can legitimately claim a slug a DIFFERENT, now-deleted workspace used to
// hold. Agent keys are literally shaped `agk_{slug}_{random}` — the slug is
// baked into the plaintext at issue time and never changes with it. These
// tests prove, against real Postgres (not a mock — the thing being proven
// is real SQL scoping behavior, not this package's own logic), that
// Authenticate resolves the caller's identity by the immutable workspace ID
// the key's row actually carries, never by re-trusting whichever workspace
// the embedded slug happens to name NOW.
//
// No //go:build integration tag — same convention as every other *_db_test.go
// in this package (see push_membership_db_test.go's doc comment): CI's
// untagged `go test ./...` runs these against a migrated DATABASE_URL, and a
// plain local run skips cleanly when no database is reachable.
func agentKeyWsReuseTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func agentKeyWsReuseOwner(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	suffix := uuid.New().String()[:8]
	owner := &domain.User{
		ID: uuid.New(), Email: "agentkey-wsreuse-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Agent Key WS Reuse Owner", Username: "agentkey-wsreuse-" + suffix, IsActive: true,
	}
	require.NoError(t, postgres.NewUserRepo(db).Create(context.Background(), owner))
	return owner.ID
}

// agentKeyWsReuseFixture wires the real service (agentService, optionally
// wrapped exactly like cmd/api/main.go wraps it in production with
// NewCachedAgentAuth) against real Postgres repos — no mocks anywhere in the
// dependency graph, so a passing test proves the actual SQL scoping.
type agentKeyWsReuseFixture struct {
	db            *sqlx.DB
	ctx           context.Context
	workspaceRepo *postgres.WorkspaceRepo
	agentRepo     *postgres.AgentRepo
	grantRepo     *postgres.AgentWorkspaceGrantRepo
	ownerID       uuid.UUID

	// authLegacy has NO grant repo wired — every Authenticate call takes the
	// pre-U2 path. Used for the home-key scenario.
	authLegacy AgentService
	// authGranted has the grant repo wired, matching production shape, and
	// is wrapped in the same TTL cache production runs behind. Used for the
	// guest-key scenario and the cache-staleness check.
	authGranted AgentService
	grantSvc    AgentWorkspaceGrantService
}

func setupAgentKeyWsReuseFixture(t *testing.T) *agentKeyWsReuseFixture {
	t.Helper()
	db := agentKeyWsReuseTestDB(t)
	wsRepo := postgres.NewWorkspaceRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	grantRepo := postgres.NewAgentWorkspaceGrantRepo(db)
	userRepo := postgres.NewUserRepo(db)
	activityRepo := postgres.NewActivityLogRepo(db)

	legacy := NewAgentService(agentRepo, activityRepo, wsRepo, userRepo)

	granted := NewAgentService(agentRepo, activityRepo, wsRepo, userRepo)
	if configurable, ok := granted.(AgentServiceConfigurable); ok {
		configurable.SetAgentWorkspaceGrantRepo(grantRepo)
	} else {
		t.Fatal("agentService no longer implements AgentServiceConfigurable")
	}
	cachedGranted := NewCachedAgentAuth(granted, 0)

	grantSvc := NewAgentWorkspaceGrantService(grantRepo, agentRepo, wsRepo, activityRepo)

	return &agentKeyWsReuseFixture{
		db:            db,
		ctx:           context.Background(),
		workspaceRepo: wsRepo,
		agentRepo:     agentRepo,
		grantRepo:     grantRepo,
		ownerID:       agentKeyWsReuseOwner(t, db),
		authLegacy:    legacy,
		authGranted:   cachedGranted,
		grantSvc:      grantSvc,
	}
}

func (f *agentKeyWsReuseFixture) createWorkspace(t *testing.T, slug, name string) *domain.Workspace {
	t.Helper()
	ws := &domain.Workspace{ID: uuid.New(), Name: name, Slug: slug, OwnerID: f.ownerID}
	require.NoError(t, f.workspaceRepo.Create(f.ctx, ws))
	return ws
}

// TestAuthenticate_LegacyHomeKey_AfterWorkspaceRecreatedWithSameSlug_Refused
// is the parent task's own scenario, run for real: issue a key against
// workspace A (slug=foo), soft-delete A (which frees "foo" via
// rename-on-delete), create a brand new workspace B claiming the exact same
// slug, then present the OLD key. It must be refused — the key's row is
// still scoped to A's immutable ID, which B does not share no matter what
// slug B now answers to.
func TestAuthenticate_LegacyHomeKey_AfterWorkspaceRecreatedWithSameSlug_Refused(t *testing.T) {
	f := setupAgentKeyWsReuseFixture(t)
	slug := "wsreuse-legacy-" + uuid.New().String()[:8]

	wsA := f.createWorkspace(t, slug, "Workspace A")
	regOut, err := f.authLegacy.Register(f.ctx, RegisterAgentInput{
		WorkspaceID: wsA.ID,
		Name:        "Legacy Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	oldKey := regOut.APIKey
	require.Contains(t, oldKey, "agk_"+slug+"_", "sanity: the issued key must literally embed the workspace's slug at issue time")

	require.NoError(t, f.workspaceRepo.Delete(f.ctx, wsA.ID))

	wsB := f.createWorkspace(t, slug, "Workspace B (unrelated, reused slug)")
	require.NotEqual(t, wsA.ID, wsB.ID)

	// The stale key's embedded slug ("foo") now resolves live to B — this is
	// the exact re-parse-the-slug step the parent task calls out as the
	// dangerous move. What matters is what happens next.
	agent, err := f.authLegacy.Authenticate(f.ctx, slug, oldKey)

	require.Error(t, err, "an agent key issued for a DELETED workspace must not authenticate against a DIFFERENT workspace that later reused its slug")
	require.Nil(t, agent)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Code)
	assert.Equal(t, "invalid API key", apiErr.Message)

	// Positive control, same fixture, so the refusal above is proven to be a
	// genuine identity check and not e.g. B having no agents at all: an
	// agent actually registered INTO B, using its own fresh key, works.
	regOutB, err := f.authLegacy.Register(f.ctx, RegisterAgentInput{
		WorkspaceID: wsB.ID,
		Name:        "Real Agent Of B",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	agentB, err := f.authLegacy.Authenticate(f.ctx, slug, regOutB.APIKey)
	require.NoError(t, err, "a key genuinely issued for the CURRENT holder of the slug must still authenticate")
	require.NotNil(t, agentB)
	assert.Equal(t, wsB.ID, agentB.WorkspaceID)
}

// TestAuthenticate_GrantKey_AfterWorkspaceRecreatedWithSameSlug_Refused is
// the same scenario through the newer, higher-risk agent_workspace_grants
// path (task U2/U3, multi-workspace guest keys) — flagged by the parent task
// as the part needing extra scrutiny. Also exercises the exact wrapper
// (NewCachedAgentAuth) production actually runs behind, not a bypass of it.
func TestAuthenticate_GrantKey_AfterWorkspaceRecreatedWithSameSlug_Refused(t *testing.T) {
	f := setupAgentKeyWsReuseFixture(t)
	slug := "wsreuse-grant-" + uuid.New().String()[:8]

	wsA := f.createWorkspace(t, slug, "Workspace A (grant)")
	// The agent's OWN home workspace is irrelevant here — register it
	// somewhere else entirely, so the only thing connecting it to A is the
	// grant being tested.
	wsHome := f.createWorkspace(t, "wsreuse-grant-home-"+uuid.New().String()[:8], "Agent Home")
	regOut, err := f.authLegacy.Register(f.ctx, RegisterAgentInput{
		WorkspaceID: wsHome.ID,
		Name:        "Guest Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	inviteOut, err := f.grantSvc.InviteAgent(f.ctx, wsA.ID, regOut.Agent.ID, domain.RoleMember, f.ownerID)
	require.NoError(t, err)
	guestKey := inviteOut.APIKey
	require.Contains(t, guestKey, "agk_"+slug+"_")

	// The guest key works against A before deletion — establishes the
	// baseline the refusal below is a change FROM, not an artifact of a
	// broken grant in the first place.
	agentViaGrant, err := f.authGranted.Authenticate(f.ctx, slug, guestKey)
	require.NoError(t, err)
	require.Equal(t, wsA.ID, agentViaGrant.WorkspaceID)

	require.NoError(t, f.workspaceRepo.Delete(f.ctx, wsA.ID))

	wsB := f.createWorkspace(t, slug, "Workspace B (unrelated, reused slug)")
	require.NotEqual(t, wsA.ID, wsB.ID)

	agent, err := f.authGranted.Authenticate(f.ctx, slug, guestKey)
	require.Error(t, err, "a grant-backed guest key issued for a DELETED workspace must not authenticate against a DIFFERENT workspace that later reused its slug")
	require.Nil(t, agent)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Code)
}

// TestAuthenticate_CachedGrantKey_WorkspaceRecreatedMidTTL_StaleHitEvicted
// closes the one place identity resolution does NOT go through a fresh
// GetBySlug/GetByID on every call: the in-process auth cache
// (agent_auth_cache.go), keyed by (slug, apiKey). A hit computed BEFORE
// deletion must not go on answering once the workspace behind it is gone,
// even within the cache's own TTL window, even though the cache key itself
// (built from the same never-changing slug string and key text) does not
// change across the deletion+recreation.
func TestAuthenticate_CachedGrantKey_WorkspaceRecreatedMidTTL_StaleHitEvicted(t *testing.T) {
	f := setupAgentKeyWsReuseFixture(t)
	slug := "wsreuse-cache-" + uuid.New().String()[:8]

	wsA := f.createWorkspace(t, slug, "Workspace A (cache)")
	regOut, err := f.authLegacy.Register(f.ctx, RegisterAgentInput{
		WorkspaceID: wsA.ID,
		Name:        "Cached Agent",
		AgentType:   domain.AgentTypeClaudeCode,
	})
	require.NoError(t, err)
	oldKey := regOut.APIKey

	// Warm the cache: this Authenticate call is a legacy (non-grant) hit,
	// cached under HMAC(slug, oldKey) with agent.WorkspaceID == wsA.ID.
	agent1, err := f.authGranted.Authenticate(f.ctx, slug, oldKey)
	require.NoError(t, err)
	require.Equal(t, wsA.ID, agent1.WorkspaceID)

	require.NoError(t, f.workspaceRepo.Delete(f.ctx, wsA.ID))
	wsB := f.createWorkspace(t, slug, "Workspace B (reused slug, mid TTL)")
	require.NotEqual(t, wsA.ID, wsB.ID)

	// Same slug string, same key text -> same cache key as the warm-up call
	// above, still well inside AgentAuthCacheTTL. If the cache trusted the
	// hit without re-checking, this would silently succeed as wsA.
	agent2, err := f.authGranted.Authenticate(f.ctx, slug, oldKey)
	require.Error(t, err, "a cache entry warmed before deletion must not keep answering once its workspace is gone, even mid-TTL and even though the (slug, key) cache key is unchanged by the deletion+recreation")
	require.Nil(t, agent2)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Code)

	_ = wsB // only its existence (same slug, different ID) matters above
}
