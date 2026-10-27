package middleware

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// DB-backed, no build tag (same convention as the other *_db_test.go files):
// skipped when Postgres is not reachable. The point is the real chain —
// ExtensionAuth -> ExtensionCapabilityGuard against the real extension_actors
// table — because a mock cannot show that a revoked row stops authenticating.

func extTestDB(t *testing.T) *sqlx.DB {
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
	return db
}

type extFixture struct {
	db      *sqlx.DB
	ws      *domain.Workspace
	conn    *domain.IntegrationConfig
	svc     service.ExtensionActorService
	token   string
	e       *echo.Echo
	reached map[string]int
}

func newExtFixture(t *testing.T, consented []string) *extFixture {
	t.Helper()
	db := extTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	owner := &domain.User{ID: uuid.New(), Email: "ext-" + suffix + "@example.com", PasswordHash: "x", Name: "Ext Owner", Username: "ext-" + suffix, IsActive: true}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, owner))
	ws := &domain.Workspace{ID: uuid.New(), Name: "ext-ws", Slug: "ext-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))

	integ := postgres.NewIntegrationRepo(db)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: domain.IntegrationProviderGitLab, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, integ.Upsert(ctx, conn))

	svc := service.NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, postgres.NewWorkspaceRepo(db))
	issued, err := svc.Issue(ctx, ws.ID, conn.ID, "gitlab", "GitLab", consented)
	require.NoError(t, err)
	assert.Equal(t, "ext:gitlab@"+ws.ID.String(), issued.Actor)

	f := &extFixture{db: db, ws: ws, conn: conn, svc: svc, token: issued.Token, e: echo.New(), reached: map[string]int{}}
	g := f.e.Group("/api/v1")
	g.Use(ExtensionAuth(svc))
	g.Use(ExtensionCapabilityGuard(svc))
	ok := func(name string) echo.HandlerFunc {
		return func(c echo.Context) error {
			f.reached[name]++
			by, byType := actorctx.FromContext(c.Request().Context())
			assert.Equal(t, issued.ActorID, by)
			assert.Equal(t, domain.ActorTypeExtension, byType)
			return c.NoContent(http.StatusOK)
		}
	}
	g.GET("/projects/:proj_id/tasks", ok("tasks:list"))
	g.POST("/projects/:proj_id/tasks", ok("tasks:create"))
	g.POST("/tasks/:task_id/decisions", ok("unmapped"))
	g.PUT("/tasks/:task_id/external-ref", ok("external-ref:put"))
	g.POST("/tasks/:task_id/human-gate", ok("human-gate:arm"))
	g.DELETE("/tasks/:task_id/human-gate", ok("human-gate:clear"))
	g.POST("/tasks/:task_id/human-gate-decisions", ok("human-gate:decide"))
	g.GET("/ext/me", ok("me"))
	return f
}

func (f *extFixture) do(method, path, token string) int {
	req := httptest.NewRequest(method, path, http.NoBody)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec.Code
}

func (f *extFixture) violations(t *testing.T) int64 {
	t.Helper()
	row, err := f.svc.Get(context.Background(), f.conn.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	return row.ViolationCount
}

// Red control: an actor that consented to tasks:read only is refused tasks:write,
// and the refusal is counted on the connection.
func TestExtension_OutOfCapabilityIs403AndCounted(t *testing.T) {
	f := newExtFixture(t, []string{"api:tasks:read"})
	p := "/api/v1/projects/" + uuid.New().String() + "/tasks"

	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, p, f.token))
	assert.Equal(t, int64(0), f.violations(t))

	assert.Equal(t, http.StatusForbidden, f.do(http.MethodPost, p, f.token))
	assert.Equal(t, 0, f.reached["tasks:create"], "handler must not run")
	assert.Equal(t, int64(1), f.violations(t))

	// An unmapped route (human-gate style) is closed even for a rich actor.
	assert.Equal(t, http.StatusForbidden, f.do(http.MethodPost, "/api/v1/tasks/"+uuid.New().String()+"/decisions", f.token))
	assert.Equal(t, int64(2), f.violations(t))

	// /ext/me needs no capability.
	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, "/api/v1/ext/me", f.token))
}

func TestExtension_ConsentedCapabilityPasses(t *testing.T) {
	f := newExtFixture(t, []string{"api:tasks:read", "api:tasks:write"})
	assert.Equal(t, http.StatusOK, f.do(http.MethodPost, "/api/v1/projects/"+uuid.New().String()+"/tasks", f.token))
	assert.Equal(t, 1, f.reached["tasks:create"])
}

// Keep opens a gate card: create, pin its external ref, arm the gate. Clearing
// or deciding the gate stays human-only even with tasks:write.
func TestExtension_GateCardArmButNotClear(t *testing.T) {
	task := "/api/v1/tasks/" + uuid.New().String()

	f := newExtFixture(t, []string{"api:tasks:read", "api:tasks:write"})
	assert.Equal(t, http.StatusOK, f.do(http.MethodPut, task+"/external-ref", f.token))
	assert.Equal(t, http.StatusOK, f.do(http.MethodPost, task+"/human-gate", f.token))
	assert.Equal(t, http.StatusForbidden, f.do(http.MethodDelete, task+"/human-gate", f.token))
	assert.Equal(t, http.StatusForbidden, f.do(http.MethodPost, task+"/human-gate-decisions", f.token))
	assert.Equal(t, 0, f.reached["human-gate:clear"])
	assert.Equal(t, 0, f.reached["human-gate:decide"])

	// Without tasks:write neither write gets through.
	ro := newExtFixture(t, []string{"api:tasks:read"})
	assert.Equal(t, http.StatusForbidden, ro.do(http.MethodPut, task+"/external-ref", ro.token))
	assert.Equal(t, http.StatusForbidden, ro.do(http.MethodPost, task+"/human-gate", ro.token))
	assert.Equal(t, 0, ro.reached["external-ref:put"]+ro.reached["human-gate:arm"])
}

func TestExtension_DisableAndRevokeAre401(t *testing.T) {
	f := newExtFixture(t, []string{"api:tasks:read"})
	p := "/api/v1/projects/" + uuid.New().String() + "/tasks"
	require.Equal(t, http.StatusOK, f.do(http.MethodGet, p, f.token))

	off := false
	_, err := postgres.NewIntegrationRepo(f.db).Update(context.Background(), f.conn.ID, domain.UpdateIntegrationInput{IsActive: &off})
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, p, f.token), "disabled connection")

	on := true
	_, err = postgres.NewIntegrationRepo(f.db).Update(context.Background(), f.conn.ID, domain.UpdateIntegrationInput{IsActive: &on})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, f.do(http.MethodGet, p, f.token))

	require.NoError(t, f.svc.Revoke(context.Background(), f.conn.ID))
	assert.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, p, f.token), "revoked actor")
	// A garbage ext_ token never falls through to another credential.
	assert.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, p, "ext_nonsense"))
}

func TestExtension_RotationGraceThenOldTokenDies(t *testing.T) {
	f := newExtFixture(t, []string{"api:tasks:read"})
	p := "/api/v1/projects/" + uuid.New().String() + "/tasks"
	old := f.token

	rot, err := f.svc.Rotate(context.Background(), f.ws.ID, f.conn.ID)
	require.NoError(t, err)
	assert.Equal(t, service.ExtensionTokenRotationGrace, rot.Grace)
	assert.NotEqual(t, old, rot.Token)

	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, p, old), "old token lives during grace")
	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, p, rot.Token))

	// Grace elapsed (move the stored expiry into the past instead of sleeping 5 min).
	_, err = f.db.Exec(`UPDATE extension_actors SET prev_token_expires_at = now() - interval '1 second' WHERE connection_id = $1`, f.conn.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, p, old), "old token dead after grace")
	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, p, rot.Token))

	// Another workspace's admin cannot rotate this connection.
	_, err = f.svc.Rotate(context.Background(), uuid.New(), f.conn.ID)
	assert.Error(t, err)
}

// The token exists only in the issue/rotate response: nothing on the way logs it
// and the database holds only its digest.
func TestExtension_TokenNeverLoggedOrStored(t *testing.T) {
	var buf bytes.Buffer
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&buf)
	t.Cleanup(func() { slog.SetDefault(prevSlog); log.SetOutput(os.Stderr) })

	f := newExtFixture(t, []string{"api:tasks:read"})
	f.e.Logger.SetOutput(&buf)
	p := "/api/v1/projects/" + uuid.New().String() + "/tasks"
	f.do(http.MethodGet, p, f.token)
	f.do(http.MethodPost, p, f.token) // 403 path
	rot, err := f.svc.Rotate(context.Background(), f.ws.ID, f.conn.ID)
	require.NoError(t, err)
	f.do(http.MethodGet, p, rot.Token)
	require.NoError(t, f.svc.Revoke(context.Background(), f.conn.ID))
	f.do(http.MethodGet, p, rot.Token) // 401 path

	assert.NotContains(t, buf.String(), f.token)
	assert.NotContains(t, buf.String(), rot.Token)

	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM extension_actors WHERE token_hash = $1 OR prev_token_hash = $1`, f.token))
	assert.Zero(t, n, "raw token must not be stored")
}

// Every route the capability map names must really exist; a typo would silently
// leave a capability that opens nothing (or, worse, hide a route that is meant to
// be closed). Deny-by-default is also asserted for known human-only routes.
func TestExtensionRouteCapabilities_NoHumanOnlyRoute(t *testing.T) {
	caps := ExtensionRouteCapabilities()
	for _, closed := range []string{
		"POST /api/v1/tasks/:task_id/checkout",
		"POST /api/v1/agents/:agent_id/regenerate-key",
		"POST /api/v1/integrations/:int_id/rotate-token",
		"DELETE /api/v1/agents/:agent_id",
		"DELETE /api/v1/tasks/:task_id/human-gate",
		"POST /api/v1/tasks/:task_id/human-gate-decisions",
		"POST /api/v1/human-gate-decisions/:decision_id/revoke",
	} {
		_, mapped := caps[closed]
		assert.False(t, mapped, "%s must stay closed to extensions", closed)
	}
}

// Two-phase key change of an agent (Keep's rotator): issue, both keys work,
// revoke the old one, only the new one works. Real Authenticate path, with the
// auth cache in front, so a cached login must not outlive the revoke.
func TestAgentKeys_TwoPhaseChange(t *testing.T) {
	db := extTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	owner := &domain.User{ID: uuid.New(), Email: "ak-" + suffix + "@example.com", PasswordHash: "x", Name: "AK Owner", Username: "ak-" + suffix, IsActive: true}
	userRepo := postgres.NewUserRepo(db)
	require.NoError(t, userRepo.Create(ctx, owner))
	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "ak-ws", Slug: "ak-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, wsRepo.Create(ctx, ws))

	agentRepo := postgres.NewAgentRepo(db)
	actLog := postgres.NewActivityLogRepo(db)
	grants := postgres.NewAgentWorkspaceGrantRepo(db)
	keyRepo := postgres.NewAgentAPIKeyRepo(db)
	agentSvc := service.NewCachedAgentAuth(service.NewAgentService(agentRepo, actLog, wsRepo, userRepo), service.AgentAuthCacheTTL)
	cfg := agentSvc.(service.AgentServiceConfigurable)
	cfg.SetAgentWorkspaceGrantRepo(grants)
	agentSvc.(service.AgentAPIKeyConfigurable).SetAgentAPIKeyRepo(keyRepo)

	reg, err := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: ws.ID, Name: "Rotated " + suffix, AgentType: domain.AgentTypeClaudeCode})
	require.NoError(t, err)
	oldKey := reg.APIKey

	keys := service.NewAgentKeyService(agentRepo, wsRepo, keyRepo, actLog, nil, agentSvc.(service.AgentCacheInvalidator))
	auth := func(k string) error { _, e := agentSvc.Authenticate(ctx, ws.Slug, k); return e }

	require.NoError(t, auth(oldKey))

	issued, err := keys.Issue(ctx, reg.Agent.ID, "rotator", 0)
	require.NoError(t, err)
	assert.NotEqual(t, oldKey, issued.APIKey)

	// Window: both keys work; repeat so the cache-hit path is exercised too.
	for i := 0; i < 2; i++ {
		assert.NoError(t, auth(oldKey), "old key during window")
		assert.NoError(t, auth(issued.APIKey), "new key during window")
	}

	listed, err := keys.List(ctx, reg.Agent.ID)
	require.NoError(t, err)
	assert.Len(t, listed, 2)

	// Phase two: revoke the old (primary) key.
	require.NoError(t, keys.Revoke(ctx, reg.Agent.ID, domain.AgentPrimaryKeyID))
	assert.Error(t, auth(oldKey), "old key after revoke (cache must not keep it alive)")
	assert.NoError(t, auth(issued.APIKey), "new key survives")

	// The last active key is never revoked (409), and keeps working.
	assert.Error(t, keys.Revoke(ctx, reg.Agent.ID, issued.KeyID.String()), "last active key")
	assert.NoError(t, auth(issued.APIKey), "last key survives the refused revoke")
	assert.Error(t, keys.Revoke(ctx, reg.Agent.ID, uuid.New().String()), "unknown key id")
}
