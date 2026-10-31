package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// No //go:build integration tag — same convention as agent_key_rotate_db_test.go;
// skips without a reachable Postgres.
//
// an internal ticket: POST /internal/secrets/materialize behind DirectOnly ->
// SpawnerAuth, wired as cmd/api/main.go wires it, with the production agent
// auth (cached service, grants, additional keys) against real Postgres — so
// "deleted agent" and "rotated key" are real deletions and rotations, not a
// mock returning an error.

type spawnerFixture struct {
	e        *echo.Echo
	agents   service.AgentService
	spawner  *service.RegisterAgentOutput // listed in MESH_SPAWNER_AGENT_IDS
	stranger *service.RegisterAgentOutput // valid agent, not listed
	slug     string
	reached  *uuid.UUID // agent id the handler saw, uuid.Nil when it ran without one
}

func newSpawnerFixture(t *testing.T) *spawnerFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	userRepo := postgres.NewUserRepo(db)
	wsRepo := postgres.NewWorkspaceRepo(db)
	owner := &domain.User{ID: uuid.New(), Email: "spawner-" + suffix + "@example.com", PasswordHash: "x", Name: "spawner", Username: "spawner-" + suffix, IsActive: true}
	require.NoError(t, userRepo.Create(ctx, owner))
	ws := &domain.Workspace{ID: uuid.New(), Name: "spawner", Slug: "spawner-" + suffix, OwnerID: owner.ID}
	require.NoError(t, wsRepo.Create(ctx, ws))

	agentRepo := postgres.NewAgentRepo(db)
	agentSvc := service.NewCachedAgentAuth(service.NewAgentService(agentRepo, postgres.NewActivityLogRepo(db), wsRepo, userRepo), service.AgentAuthCacheTTL)
	agentSvc.(service.AgentServiceConfigurable).SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
	agentSvc.(service.AgentAPIKeyConfigurable).SetAgentAPIKeyRepo(postgres.NewAgentAPIKeyRepo(db))

	register := func(name string) *service.RegisterAgentOutput {
		out, rerr := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: ws.ID, Name: name + " " + suffix, AgentType: domain.AgentTypeClaudeCode})
		require.NoError(t, rerr)
		return out
	}

	f := &spawnerFixture{agents: agentSvc, slug: ws.Slug, reached: new(uuid.UUID)}
	f.spawner, f.stranger = register("Spawner"), register("Stranger")

	f.e = echo.New()
	f.e.POST("/internal/secrets/materialize", func(c echo.Context) error {
		*f.reached = uuid.Nil
		if id, gerr := middleware.GetAgentID(c); gerr == nil {
			*f.reached = id
		}
		return c.NoContent(http.StatusOK)
	}, middleware.DirectOnly(), middleware.SpawnerAuth(agentSvc))

	t.Setenv(middleware.EnvSpawnerAgentIDs, f.spawner.Agent.ID.String())
	return f
}

func (f *spawnerFixture) post(headers map[string]string) int {
	*f.reached = uuid.New() // sentinel: overwritten only if the handler runs
	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", http.NoBody)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec.Code
}

func key(k string) map[string]string { return map[string]string{"X-Agent-Key": k} }

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "") // registers restore of the previous value
	require.NoError(t, os.Unsetenv(name))
}

func TestSpawnerAuthDB_AllowlistedSpawnerKeyPasses(t *testing.T) {
	f := newSpawnerFixture(t)
	assert.Equal(t, http.StatusOK, f.post(key(f.spawner.APIKey)))
	assert.Equal(t, f.spawner.Agent.ID, *f.reached, "handler must see the spawner's identity")
}

func TestSpawnerAuthDB_ValidAgentOutsideAllowlistIsForbidden(t *testing.T) {
	f := newSpawnerFixture(t)
	// Positive control: the key is a working key, not a typo — the ordinary
	// agent auth accepts it, so 403 below is the allowlist and nothing else.
	_, err := f.agents.Authenticate(context.Background(), f.slug, f.stranger.APIKey)
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, f.post(key(f.stranger.APIKey)))
}

func TestSpawnerAuthDB_NoHeadersIsUnauthorized(t *testing.T) {
	f := newSpawnerFixture(t)
	assert.Equal(t, http.StatusUnauthorized, f.post(nil))
}

func TestSpawnerAuthDB_UnknownKeyIsUnauthorized(t *testing.T) {
	f := newSpawnerFixture(t)
	assert.Equal(t, http.StatusUnauthorized, f.post(key("agk_"+f.slug+"_doesnotexist0000000000000000000000")))
	assert.Equal(t, http.StatusUnauthorized, f.post(key("not-an-agent-key")))
}

func TestSpawnerAuthDB_DeletedSpawnerIsUnauthorized(t *testing.T) {
	f := newSpawnerFixture(t)
	require.Equal(t, http.StatusOK, f.post(key(f.spawner.APIKey)), "positive control before delete")

	require.NoError(t, f.agents.Delete(context.Background(), f.spawner.Agent.ID))
	assert.Equal(t, http.StatusUnauthorized, f.post(key(f.spawner.APIKey)))
}

func TestSpawnerAuthDB_RotatedSpawnerKeyIsUnauthorized(t *testing.T) {
	f := newSpawnerFixture(t)
	require.Equal(t, http.StatusOK, f.post(key(f.spawner.APIKey)), "positive control before rotation")

	newKey, err := f.agents.RotateAPIKey(context.Background(), f.spawner.Agent.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, f.post(key(f.spawner.APIKey)), "old key after rotation")
	assert.Equal(t, http.StatusOK, f.post(key(newKey)), "the new key of the same spawner")
}

func TestSpawnerAuthDB_EmptyAllowlistRefusesEvenTheSpawner(t *testing.T) {
	f := newSpawnerFixture(t)
	t.Setenv(middleware.EnvSpawnerAgentIDs, "")
	assert.Equal(t, http.StatusForbidden, f.post(key(f.spawner.APIKey)))
	unsetEnv(t, middleware.EnvSpawnerAgentIDs)
	assert.Equal(t, http.StatusForbidden, f.post(key(f.spawner.APIKey)))
}

func TestSpawnerAuthDB_GarbageInAllowlistIsIgnored(t *testing.T) {
	f := newSpawnerFixture(t)
	t.Setenv(middleware.EnvSpawnerAgentIDs, " not-a-uuid,,"+uuid.Nil.String()+" , "+f.spawner.Agent.ID.String()+" ,zzz")
	assert.Equal(t, http.StatusOK, f.post(key(f.spawner.APIKey)))
	assert.Equal(t, http.StatusForbidden, f.post(key(f.stranger.APIKey)))

	t.Setenv(middleware.EnvSpawnerAgentIDs, "not-a-uuid, ,"+uuid.Nil.String())
	assert.Equal(t, http.StatusForbidden, f.post(key(f.spawner.APIKey)))
}

// an internal ticket: the shared token is gone. Even with the old env var set to the
// value sent, a token-only request is 401, and a token alongside a key does
// not change what the key decides.
func TestSpawnerAuthDB_SpawnTokenNoLongerAccepted(t *testing.T) {
	f := newSpawnerFixture(t)
	t.Setenv("MESH_SPAWN_TOKEN", "spawner-db-test-token")
	tok := map[string]string{"X-Spawn-Token": "spawner-db-test-token"}
	assert.Equal(t, http.StatusUnauthorized, f.post(tok))
	assert.Equal(t, http.StatusForbidden, f.post(map[string]string{"X-Agent-Key": f.stranger.APIKey, "X-Spawn-Token": "spawner-db-test-token"}))
	assert.Equal(t, http.StatusOK, f.post(key(f.spawner.APIKey)), "key path unaffected")
}

func TestSpawnerAuthDB_OAuthBearerIsNotAccepted(t *testing.T) {
	f := newSpawnerFixture(t)
	assert.Equal(t, http.StatusUnauthorized, f.post(map[string]string{"Authorization": "Bearer mot_" + uuid.New().String()}))
}

func TestSpawnerAuthDB_ProxiedRequestIsNotFound(t *testing.T) {
	f := newSpawnerFixture(t)
	assert.Equal(t, http.StatusNotFound, f.post(map[string]string{"X-Agent-Key": f.spawner.APIKey, "X-Forwarded-For": "203.0.113.7"}))
	assert.Equal(t, http.StatusNotFound, f.post(map[string]string{"X-Agent-Key": "agk_" + f.slug + "_nope", "X-Forwarded-For": "203.0.113.7"}))
}
