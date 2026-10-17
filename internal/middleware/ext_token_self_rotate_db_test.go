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

// An extension actor with api:ext_token:self_rotate rotates its OWN ext_ token
// through the real ExtensionAuth + ExtensionCapabilityGuard chain, the real
// handler and real Postgres.

func selfRotateDB(t *testing.T) *sqlx.DB {
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

type selfRotateFixture struct {
	e      *echo.Echo
	human  *echo.Echo // same routes, no ExtensionAuth: a non-extension principal
	svc    service.ExtensionActorService
	self   string // ext_ token holding ext_token:self_rotate
	other  string // second actor in the same workspace, also holding it
	nocap  string // ext_ token without the capability
	selfID uuid.UUID
}

func newSelfRotateFixture(t *testing.T) *selfRotateFixture {
	t.Helper()
	db := selfRotateDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	owner := &domain.User{ID: uuid.New(), Email: "sr-" + suffix + "@example.com", PasswordHash: "x", Name: "SR", Username: "sr-" + suffix, IsActive: true}
	require.NoError(t, postgres.NewUserRepo(db).Create(ctx, owner))
	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "sr-ws", Slug: "sr-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, wsRepo.Create(ctx, ws))

	integ := postgres.NewIntegrationRepo(db)
	svc := service.NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, wsRepo)
	issue := func(provider domain.IntegrationProvider, id string, caps []string) *service.IssuedExtensionToken {
		conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: provider, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		require.NoError(t, integ.Upsert(ctx, conn))
		out, err := svc.Issue(ctx, ws.ID, conn.ID, id, id, caps)
		require.NoError(t, err)
		return out
	}
	self := issue(domain.IntegrationProviderGitLab, "keep", []string{"api:ext_token:self_rotate"})
	other := issue(domain.IntegrationProviderGitHub, "other", []string{"api:ext_token:self_rotate"})
	nocap := issue(domain.IntegrationProviderSpark, "nocap", []string{"api:tasks:read"})

	h := handler.NewExtensionActorHandler(svc)
	routes := func(g *echo.Group) {
		g.GET("/ext/me", h.Me)
		g.POST("/ext/me/token/rotate", h.SelfRotate)
		g.DELETE("/ext/me/token/previous", h.SelfRevokePrevious)
	}
	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(middleware.ExtensionAuth(svc))
	g.Use(middleware.ExtensionCapabilityGuard(svc))
	routes(g)
	he := echo.New()
	routes(he.Group("/api/v1"))

	return &selfRotateFixture{e: e, human: he, svc: svc, self: self.Token, other: other.Token, nocap: nocap.Token, selfID: self.ActorID}
}

func (f *selfRotateFixture) do(e *echo.Echo, method, path, token, body string) (status int, resp []byte) {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *selfRotateFixture) me(token string) int {
	code, _ := f.do(f.e, http.MethodGet, "/api/v1/ext/me", token, "")
	return code
}

const (
	rotatePath = "/api/v1/ext/me/token/rotate"
	prevPath   = "/api/v1/ext/me/token/previous"
)

func TestExtTokenSelfRotate_Refusals(t *testing.T) {
	f := newSelfRotateFixture(t)

	// Red control: an extension without the capability is 403 on both routes.
	for _, c := range []struct{ m, p string }{{http.MethodPost, rotatePath}, {http.MethodDelete, prevPath}} {
		code, _ := f.do(f.e, c.m, c.p, f.nocap, "")
		assert.Equal(t, http.StatusForbidden, code, "%s %s without capability", c.m, c.p)
	}
	// ...and the refused call changed nothing: its token still works.
	assert.Equal(t, http.StatusOK, f.me(f.nocap))

	// A non-extension principal (no ext_ token / human JWT path) is 403 from the handler.
	for _, c := range []struct{ m, p string }{{http.MethodPost, rotatePath}, {http.MethodDelete, prevPath}} {
		code, _ := f.do(f.human, c.m, c.p, "", "")
		assert.Equal(t, http.StatusForbidden, code, "%s %s by non-extension", c.m, c.p)
	}

	// Unknown ext_ token: 401, never a fall-through.
	code, _ := f.do(f.e, http.MethodPost, rotatePath, "ext_doesnotexist", "")
	assert.Equal(t, http.StatusUnauthorized, code)

	// Bad grace values are refused before anything changes.
	code, _ = f.do(f.e, http.MethodPost, rotatePath, f.self, `{"grace_seconds":99999}`)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = f.do(f.e, http.MethodPost, rotatePath, f.self, `{"grace_seconds":-1}`)
	assert.Equal(t, http.StatusBadRequest, code)
	// 9223372037 s overflows time.Duration to a negative value: must still be refused.
	code, _ = f.do(f.e, http.MethodPost, rotatePath, f.self, `{"grace_seconds":9223372037}`)
	assert.Equal(t, http.StatusBadRequest, code, "overflowing grace_seconds")
	code, _ = f.do(f.e, http.MethodPost, rotatePath, f.self, `{"grace_seconds":3601}`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, http.StatusOK, f.me(f.self), "token untouched by refused rotations")
}

func TestExtTokenSelfRotate_TwoPhaseAndIsolation(t *testing.T) {
	f := newSelfRotateFixture(t)

	code, body := f.do(f.e, http.MethodPost, rotatePath, f.self, "")
	require.Equal(t, http.StatusOK, code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	newTok, _ := resp["extension_token"].(string)
	require.NotEmpty(t, newTok)
	assert.NotEqual(t, f.self, newTok)
	assert.Equal(t, newTok[:8], resp["token_prefix"])
	assert.EqualValues(t, len(newTok), resp["token_length"])
	assert.EqualValues(t, 300, resp["previous_valid_for"])
	assert.Equal(t, f.selfID.String(), resp["actor_id"])
	assert.NotEmpty(t, resp["previous_valid_until"])

	// Window: both work.
	assert.Equal(t, http.StatusOK, f.me(f.self), "old token during window")
	assert.Equal(t, http.StatusOK, f.me(newTok), "new token during window")

	// Another actor with the same capability was not touched.
	assert.Equal(t, http.StatusOK, f.me(f.other), "other actor unaffected")

	// The old token cannot end the window (it would end itself): 409, nothing changes.
	code, _ = f.do(f.e, http.MethodDelete, prevPath, f.self, "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, http.StatusOK, f.me(f.self))

	// Phase two: the new token revokes the previous one; repeat is idempotent.
	for i := 0; i < 2; i++ {
		code, _ = f.do(f.e, http.MethodDelete, prevPath, newTok, "")
		assert.Equal(t, http.StatusNoContent, code, "revoke previous, call %d", i+1)
	}
	assert.Equal(t, http.StatusUnauthorized, f.me(f.self), "old token after revoke")
	assert.Equal(t, http.StatusOK, f.me(newTok), "new token after revoke")
	assert.Equal(t, http.StatusOK, f.me(f.other), "other actor still unaffected")
}

// A lost response must not lock the caller out: rotating again with the token it
// still holds keeps that token valid as the previous one.
func TestExtTokenSelfRotate_RetryAfterLostResponse(t *testing.T) {
	f := newSelfRotateFixture(t)

	code, _ := f.do(f.e, http.MethodPost, rotatePath, f.self, "") // response "lost"
	require.Equal(t, http.StatusOK, code)
	code, body := f.do(f.e, http.MethodPost, rotatePath, f.self, `{"grace_seconds":600}`) // retry with the old token
	require.Equal(t, http.StatusOK, code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.EqualValues(t, 600, resp["previous_valid_for"])
	newTok := resp["extension_token"].(string)

	assert.Equal(t, http.StatusOK, f.me(f.self), "caller's token survives the retry")
	assert.Equal(t, http.StatusOK, f.me(newTok))
}

func TestExtTokenSelfRotate_ExpiredPreviousCannotRotate(t *testing.T) {
	f := newSelfRotateFixture(t)
	code, body := f.do(f.e, http.MethodPost, rotatePath, f.self, `{"grace_seconds":1}`)
	require.Equal(t, http.StatusOK, code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, http.StatusUnauthorized, f.me(f.self), "previous token after its window")
	code, _ = f.do(f.e, http.MethodPost, rotatePath, f.self, "")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, http.StatusOK, f.me(resp["extension_token"].(string)))
}

// Canary: no token value reaches logs, and none is stored raw.
func TestExtTokenSelfRotate_NoTokenInLogsOrStorage(t *testing.T) {
	var buf bytes.Buffer
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&buf)
	t.Cleanup(func() { slog.SetDefault(prevSlog); log.SetOutput(os.Stderr) })

	f := newSelfRotateFixture(t)
	f.e.Logger.SetOutput(&buf)
	code, body := f.do(f.e, http.MethodPost, rotatePath, f.self, "")
	require.Equal(t, http.StatusOK, code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	newTok := resp["extension_token"].(string)
	f.do(f.e, http.MethodDelete, prevPath, newTok, "")
	f.do(f.e, http.MethodPost, rotatePath, f.nocap, "") // 403 path

	assert.NotContains(t, buf.String(), f.self)
	assert.NotContains(t, buf.String(), newTok)
	assert.Contains(t, buf.String(), "rotated its own token", "the event itself is logged, without the value")

	db := selfRotateDB(t)
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM extension_actors WHERE token_hash IN ($1,$2) OR prev_token_hash IN ($1,$2)`, f.self, newTok))
	assert.Zero(t, n, "raw token must not be stored")
}

// A revoked actor cannot rotate: 401 at auth.
func TestExtTokenSelfRotate_RevokedActor(t *testing.T) {
	f := newSelfRotateFixture(t)
	row, err := f.svc.Authenticate(context.Background(), f.self)
	require.NoError(t, err)
	require.NoError(t, f.svc.Revoke(context.Background(), row.ConnectionID))
	code, _ := f.do(f.e, http.MethodPost, rotatePath, f.self, "")
	assert.Equal(t, http.StatusUnauthorized, code)
}
