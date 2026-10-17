package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Contract R-ROT-3 §6 end to end against real Postgres: an extension token with
// only api:agent_key:rotate drives POST/GET/DELETE /agents/{id}/keys through the
// real ExtensionAuth + ExtensionCapabilityGuard chain and the real handler.

type rotateFixture struct {
	db      *sqlx.DB
	e       *echo.Echo
	ws      *domain.Workspace
	agent   *service.RegisterAgentOutput
	other   *service.RegisterAgentOutput // agent of another workspace
	agents  service.AgentService
	token   string // api:agent_key:rotate only
	tasksTk string // api:tasks:write only
	slug    string
}

func newRotateFixture(t *testing.T) *rotateFixture {
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
	mkWS := func(tag string) *domain.Workspace {
		owner := &domain.User{ID: uuid.New(), Email: tag + "-" + suffix + "@example.com", PasswordHash: "x", Name: tag, Username: tag + "-" + suffix, IsActive: true}
		require.NoError(t, userRepo.Create(ctx, owner))
		ws := &domain.Workspace{ID: uuid.New(), Name: tag, Slug: tag + "-" + suffix, OwnerID: owner.ID}
		require.NoError(t, wsRepo.Create(ctx, ws))
		return ws
	}
	ws, otherWS := mkWS("rot"), mkWS("rotx")

	agentRepo := postgres.NewAgentRepo(db)
	actLog := postgres.NewActivityLogRepo(db)
	keyRepo := postgres.NewAgentAPIKeyRepo(db)
	agentSvc := service.NewCachedAgentAuth(service.NewAgentService(agentRepo, actLog, wsRepo, userRepo), service.AgentAuthCacheTTL)
	agentSvc.(service.AgentServiceConfigurable).SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
	agentSvc.(service.AgentAPIKeyConfigurable).SetAgentAPIKeyRepo(keyRepo)

	reg, err := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: ws.ID, Name: "Rotated " + suffix, AgentType: domain.AgentTypeClaudeCode})
	require.NoError(t, err)
	regOther, err := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: otherWS.ID, Name: "Foreign " + suffix, AgentType: domain.AgentTypeClaudeCode})
	require.NoError(t, err)

	integ := postgres.NewIntegrationRepo(db)
	extSvc := service.NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, wsRepo)
	issue := func(provider domain.IntegrationProvider, id string, caps []string) string {
		conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: provider, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		require.NoError(t, integ.Upsert(ctx, conn))
		out, ierr := extSvc.Issue(ctx, ws.ID, conn.ID, id, id, caps)
		require.NoError(t, ierr)
		return out.Token
	}

	keySvc := service.NewAgentKeyService(agentRepo, wsRepo, keyRepo, actLog, nil, agentSvc.(service.AgentCacheInvalidator))
	h := handler.NewAgentKeyHandler(keySvc)
	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(middleware.ExtensionAuth(extSvc))
	g.Use(middleware.ExtensionCapabilityGuard(extSvc))
	g.GET("/agents/:agent_id/keys", h.List)
	g.POST("/agents/:agent_id/keys", h.Issue)
	g.DELETE("/agents/:agent_id/keys/:key_id", h.Revoke)
	g.POST("/projects/:proj_id/tasks", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	return &rotateFixture{
		db: db, e: e, ws: ws, agent: reg, other: regOther, agents: agentSvc, slug: ws.Slug,
		token:   issue(domain.IntegrationProviderGitLab, "keep-rotator", []string{"api:agent_key:rotate"}),
		tasksTk: issue(domain.IntegrationProviderGitHub, "tasks-only", []string{"api:tasks:write"}),
	}
}

func (f *rotateFixture) do(method, path, token string) (status int, body []byte) {
	req := httptest.NewRequest(method, path, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *rotateFixture) auth(key string) error {
	_, err := f.agents.Authenticate(context.Background(), f.slug, key)
	return err
}

func TestAgentKeyRotateAPI_Contract(t *testing.T) {
	var logs bytes.Buffer
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&logs)
	t.Cleanup(func() { slog.SetDefault(prevSlog); log.SetOutput(os.Stderr) })

	f := newRotateFixture(t)
	f.e.Logger.SetOutput(&logs)
	base := "/api/v1/agents/" + f.agent.Agent.ID.String() + "/keys"
	oldKey := f.agent.APIKey
	require.NoError(t, f.auth(oldKey))

	// Red control: a token without agent_key:rotate is 403 on all three calls,
	// and the rotate token cannot reach anything else (POST /tasks).
	code, _ := f.do(http.MethodPost, "/api/v1/projects/"+uuid.New().String()+"/tasks", f.token)
	assert.Equal(t, http.StatusForbidden, code, "rotate token must not create tasks")
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		code, _ = f.do(m, base, f.tasksTk)
		assert.Equal(t, http.StatusForbidden, code, "%s without agent_key:rotate", m)
	}
	code, _ = f.do(http.MethodDelete, base+"/primary", f.tasksTk)
	assert.Equal(t, http.StatusForbidden, code)

	// An agent of another workspace is invisible to the token.
	code, _ = f.do(http.MethodGet, "/api/v1/agents/"+f.other.Agent.ID.String()+"/keys", f.token)
	assert.Equal(t, http.StatusNotFound, code, "foreign workspace agent")

	// POST: raw key once; a second POST while two keys are active is 409.
	code, body := f.do(http.MethodPost, base, f.token)
	require.Equal(t, http.StatusCreated, code, string(body))
	var issued struct {
		KeyID  string `json:"key_id"`
		APIKey string `json:"api_key"`
	}
	require.NoError(t, json.Unmarshal(body, &issued))
	require.True(t, strings.HasPrefix(issued.APIKey, "agk_"))
	newKey := issued.APIKey
	code, _ = f.do(http.MethodPost, base, f.token)
	assert.Equal(t, http.StatusConflict, code, "third active key")

	// The new key was never used: last_used_at is still in the listing (null).
	code, body = f.do(http.MethodGet, base, f.token)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, 2, strings.Count(string(body), `"last_used_at"`), "field present for every key, null when unused")

	// Window: both keys log in; GET shows fingerprint and last_used, never a value.
	require.NoError(t, f.auth(oldKey))
	require.NoError(t, f.auth(newKey))
	code, body = f.do(http.MethodGet, base, f.token)
	require.Equal(t, http.StatusOK, code)
	assert.NotContains(t, string(body), oldKey)
	assert.NotContains(t, string(body), newKey)
	var listed struct {
		Keys []struct {
			ID          string     `json:"id"`
			Fingerprint string     `json:"fingerprint"`
			CreatedAt   *time.Time `json:"created_at"`
			LastUsedAt  *time.Time `json:"last_used_at"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(body, &listed))
	require.Len(t, listed.Keys, 2)
	for _, k := range listed.Keys {
		assert.NotEmpty(t, k.ID)
		assert.NotEmpty(t, k.Fingerprint)
		assert.NotNil(t, k.CreatedAt)
		assert.NotNil(t, k.LastUsedAt, "key %s was used", k.ID)
	}

	// DELETE the old key: 204, and the old key is 401-equivalent at once (the
	// auth cache must not keep it alive); the new one works.
	code, _ = f.do(http.MethodDelete, base+"/primary", f.token)
	require.Equal(t, http.StatusNoContent, code)
	assert.Error(t, f.auth(oldKey), "old key after DELETE")
	assert.NoError(t, f.auth(newKey))

	// Red control: revoking the last active key is 409 and the key still works.
	code, _ = f.do(http.MethodDelete, base+"/"+issued.KeyID, f.token)
	assert.Equal(t, http.StatusConflict, code, "last active key")
	assert.NoError(t, f.auth(newKey))

	// Audit: one entry per operation (issue, revoke), none for the refusals.
	var audited []string
	require.NoError(t, f.db.Select(&audited, `SELECT action FROM activity_log WHERE entity_id = $1 AND action LIKE 'key_%' ORDER BY created_at`, f.agent.Agent.ID))
	assert.Equal(t, []string{"key_issued", "key_revoked"}, audited)

	// At rest: only digests. In logs: no values.
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM agent_api_keys WHERE key_sha256 = $1`, newKey))
	assert.Zero(t, n, "raw key must not be stored")
	assert.NotContains(t, logs.String(), newKey)
	assert.NotContains(t, logs.String(), oldKey)
	assert.NotContains(t, logs.String(), f.token)
}

func (f *rotateFixture) keySvc(agents service.AgentService) service.AgentKeyService {
	return service.NewAgentKeyService(postgres.NewAgentRepo(f.db), postgres.NewWorkspaceRepo(f.db), postgres.NewAgentAPIKeyRepo(f.db),
		postgres.NewActivityLogRepo(f.db), nil, agents.(service.AgentCacheInvalidator))
}

// A second API instance has its own auth cache. Revoking the original key
// through instance A must end instance B's warm login at once.
func TestAgentKeyRotateAPI_PrimaryRevokeReachesOtherInstanceCache(t *testing.T) {
	f := newRotateFixture(t)
	ctx := context.Background()
	userRepo, wsRepo := postgres.NewUserRepo(f.db), postgres.NewWorkspaceRepo(f.db)
	instB := service.NewCachedAgentAuth(service.NewAgentService(postgres.NewAgentRepo(f.db), postgres.NewActivityLogRepo(f.db), wsRepo, userRepo), service.AgentAuthCacheTTL)
	instB.(service.AgentServiceConfigurable).SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(f.db))
	instB.(service.AgentAPIKeyConfigurable).SetAgentAPIKeyRepo(postgres.NewAgentAPIKeyRepo(f.db))

	keysA := f.keySvc(f.agents)
	_, err := keysA.Issue(ctx, f.agent.Agent.ID, "new", 0)
	require.NoError(t, err)

	for i := 0; i < 2; i++ { // warm B's cache
		_, err = instB.Authenticate(ctx, f.slug, f.agent.APIKey)
		require.NoError(t, err)
	}
	require.NoError(t, keysA.Revoke(ctx, f.agent.Agent.ID, domain.AgentPrimaryKeyID))
	_, err = instB.Authenticate(ctx, f.slug, f.agent.APIKey)
	assert.Error(t, err, "revoked original key must not survive in another instance's cache")
}

// Concurrent requests cannot exceed the cap or revoke every key.
func TestAgentKeyRotateAPI_ConcurrencyKeepsInvariants(t *testing.T) {
	f := newRotateFixture(t)
	ctx := context.Background()
	keys := f.keySvc(f.agents)
	agentID := f.agent.Agent.ID

	var wg sync.WaitGroup
	var ok int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := keys.Issue(ctx, agentID, "race", 0); err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), ok, "original + one new key, never a third")

	listed, err := keys.List(ctx, agentID)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	ok = 0
	for _, k := range listed {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := keys.Revoke(ctx, agentID, id); err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}(k.ID)
	}
	wg.Wait()
	assert.Equal(t, int32(1), ok, "two concurrent revokes: exactly one wins, one key is left")
}

// An expired, unrevoked key is not a usable one: it does not count toward the
// cap and does not make revoking the last usable key look safe.
func TestAgentKeyRotateAPI_ExpiredKeyDoesNotCount(t *testing.T) {
	f := newRotateFixture(t)
	ctx := context.Background()
	keys := f.keySvc(f.agents)
	agentID := f.agent.Agent.ID

	issued, err := keys.Issue(ctx, agentID, "soon-dead", 0)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE agent_api_keys SET expires_at = now() - interval '1 hour' WHERE id = $1`, issued.KeyID)
	require.NoError(t, err)
	require.NoError(t, keys.Revoke(ctx, agentID, issued.KeyID.String()), "revoking a dead key is harmless")

	// Original key is the only usable one: revoking it must be refused.
	assert.Error(t, keys.Revoke(ctx, agentID, domain.AgentPrimaryKeyID))
	fresh, err := keys.Issue(ctx, agentID, "fresh", 0)
	require.NoError(t, err, "expired rows leave room for a new key")
	_, err = f.db.Exec(`UPDATE agent_api_keys SET expires_at = now() - interval '1 hour' WHERE id = $1`, fresh.KeyID)
	require.NoError(t, err)
	assert.Error(t, keys.Revoke(ctx, agentID, domain.AgentPrimaryKeyID), "the other key is expired: original is the last usable")
}
