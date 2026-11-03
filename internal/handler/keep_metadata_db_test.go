package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Acceptance controls for the Keep metadata probe route (route and database controls). The chain is the production one, in the production order: the closed
// envelope first, then WorkspaceRLS, RequireWorkspaceMemberScoped, and on the
// route RequireUserAuth and RequirePermission(PermManageSecrets) over real
// Postgres repositories. The only fixture is Keep, the external Mesh->Keep
// boundary: an HTTPS server that checks the HMAC signature with its own code.
// Authentication itself (JWT / agent key parsing) is replaced by a setter that
// puts the same context keys DualAuth puts; that parser has its own tests.
// Skips without a reachable Postgres, like the neighbouring *_db_test.go files.

const (
	kmPath = "/api/v1/workspaces/%s/secrets/%s/keep-metadata"
	kmGood = `{"version":"3","length":19,"request_id":"keep-1"}`
)

// kmKeep is the Keep fixture. calls records the signed request paths.
type kmKeep struct {
	mu     sync.Mutex
	key    []byte
	ws     uuid.UUID
	status int
	body   string
	calls  []string
}

func (k *kmKeep) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	mac := hmac.New(sha256.New, k.key)
	mac.Write([]byte("keep-mesh-secret-v1\n" + r.Header.Get("X-Mesh-Timestamp") + "\n" + r.Header.Get("X-Mesh-Nonce") +
		"\nGET\n" + r.URL.EscapedPath() + "\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"))
	if r.Method != http.MethodGet || r.Header.Get("X-Mesh-Workspace") != k.ws.String() ||
		r.Header.Get("X-Mesh-Signature") != "v1="+hex.EncodeToString(mac.Sum(nil)) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	k.calls = append(k.calls, r.URL.EscapedPath())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(k.status)
	_, _ = w.Write([]byte(k.body))
}

func (k *kmKeep) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.calls)
}

type kmEnv struct {
	db       *sqlx.DB
	e        *echo.Echo
	keep     *kmKeep
	wsA      uuid.UUID // caller's workspace
	wsB      uuid.UUID // someone else's workspace
	owner    uuid.UUID // owner-role member of wsA
	viewer   uuid.UUID // viewer-role member of wsA
	member   uuid.UUID // member-role member of wsA
	stranger uuid.UUID // member of nothing
	secretA  uuid.UUID // current workspace secret of wsA
	secretB  uuid.UUID // current workspace secret of wsB
}

// auth sets what DualAuth would, per header, for the test.
func kmAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Header.Get("X-Test-Actor") {
			case "":
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "raw 401 text that must not survive"})
			case "agent":
				c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeAgent)
			case "extension":
				c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeExtension)
			default:
				id, err := uuid.Parse(c.Request().Header.Get("X-Test-Actor"))
				if err != nil {
					return c.JSON(http.StatusUnauthorized, nil)
				}
				c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeUser)
				c.Set(middleware.ContextKeyUserID, id)
			}
			return next(c)
		}
	}
}

func newKMEnv(t *testing.T, keepConfigured bool) *kmEnv {
	t.Helper()
	db := reservedTagTestDB(t)
	env := &kmEnv{db: db, e: echo.New()}

	mkUser := func(label string) uuid.UUID {
		id := uuid.New()
		short := strings.ReplaceAll(id.String(), "-", "")[:12]
		_, err := db.Exec(`INSERT INTO users (id, email, password_hash, display_name, username)
			VALUES ($1, $2, 'x', $3, $4)`, id, "km-"+short+"@example.invalid", "KM "+label, "km-"+short)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	mkWS := func(owner uuid.UUID) uuid.UUID {
		id := uuid.New()
		short := strings.ReplaceAll(id.String(), "-", "")[:12]
		_, err := db.Exec(`INSERT INTO workspaces (id, name, slug, owner_id, settings) VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
			id, "km-"+short, "km-"+short, owner)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM secret_refs WHERE workspace_id = $1`, id)
			_, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1`, id)
			_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, id)
		})
		return id
	}
	member := func(ws, user uuid.UUID, role string) {
		_, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, $4)`,
			uuid.New(), ws, user, role)
		require.NoError(t, err)
	}
	env.owner, env.viewer, env.member, env.stranger = mkUser("owner"), mkUser("viewer"), mkUser("member"), mkUser("stranger")
	ownerB := mkUser("ownerB")
	env.wsA, env.wsB = mkWS(env.owner), mkWS(ownerB)
	member(env.wsA, env.owner, domain.RoleOwner)
	member(env.wsA, env.viewer, domain.RoleViewer)
	member(env.wsA, env.member, domain.RoleMember)
	member(env.wsB, ownerB, domain.RoleOwner)

	// Keep fixture, and the real client pointed at it as installation I != W.
	installation := uuid.New()
	env.keep = &kmKeep{key: []byte("km-events-key"), ws: installation, status: http.StatusOK, body: kmGood}
	srv := httptest.NewTLSServer(env.keep)
	t.Cleanup(srv.Close)
	client := &secretstore.HTTPKeepClient{
		BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: installation, EventsSecret: env.keep.key, HTTP: srv.Client(),
	}

	secretRepo := postgres.NewSecretRefRepo(db, &memKeep{values: map[string]string{}})
	mk := func(ws uuid.UUID) uuid.UUID {
		s, err := secretRepo.Create(context.Background(), domain.CreateSecretInput{
			WorkspaceID: ws, Scope: domain.SecretScopeWorkspace, Name: "KM_TOKEN",
			Value: "km-canary-value-1", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		})
		require.NoError(t, err)
		return s.ID
	}
	env.secretA, env.secretB = mk(env.wsA), mk(env.wsB)

	var reader secretstore.KeepMetadataReader
	if keepConfigured {
		reader = client
	}
	h := NewKeepMetadataHandler(secretRepo, reader)
	memberRepo := postgres.NewWorkspaceMemberRepo(db)
	api := env.e.Group("/api/v1")
	api.Use(middleware.KeepMetadataEnvelope())
	api.Use(kmAuth())
	api.Use(middleware.WorkspaceRLS(db, postgres.NewProjectRepo(db)))
	api.Use(middleware.RequireWorkspaceMemberScoped(db))
	rbac := middleware.RequirePermission(middleware.PermManageSecrets, memberRepo)
	api.GET("/workspaces/:ws_id/secrets/:secret_id/keep-metadata", h.Get, middleware.RequireUserAuth(), rbac)
	api.HEAD("/workspaces/:ws_id/secrets/:secret_id/keep-metadata", h.Head, middleware.RequireUserAuth(), rbac)
	return env
}

type kmResp struct {
	code int
	hdr  http.Header
	raw  string
	body map[string]any
}

func (env *kmEnv) do(t *testing.T, method, actor, ws, secret, suffix string) kmResp {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/workspaces/"+ws+"/secrets/"+secret+"/keep-metadata"+suffix, http.NoBody)
	if actor != "" {
		req.Header.Set("X-Test-Actor", actor)
	}
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	r := kmResp{code: rec.Code, hdr: rec.Header(), raw: rec.Body.String()}
	if method != http.MethodHead {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r.body), "body must be JSON: %q", r.raw)
	}
	return r
}

// assertClosed checks what every answer of the route shares.
func assertClosed(t *testing.T, r kmResp) {
	t.Helper()
	assert.Equal(t, "no-store", r.hdr.Get("Cache-Control"))
	assert.Contains(t, r.hdr.Get("Content-Type"), "application/json")
	assert.Less(t, len(r.raw), 4096)
	id, _ := r.body["request_id"].(string)
	assert.Regexp(t, `^[A-Za-z0-9._-]{1,128}$`, id)
}

func assertError(t *testing.T, r kmResp, code int, class, msg string) {
	t.Helper()
	assert.Equal(t, code, r.code, r.raw)
	assertClosed(t, r)
	assert.Equal(t, map[string]any{"error_class": class, "message": msg, "request_id": r.body["request_id"]}, r.body, r.raw)
}

func assertNotFound(t *testing.T, r kmResp) {
	t.Helper()
	assertError(t, r, http.StatusNotFound, "not_found", "not found")
}

func TestKeepMetadataDB_PositiveIsExactAndSignedOnce(t *testing.T) {
	env := newKMEnv(t, true)
	r := env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), env.secretA.String(), "")
	assert.Equal(t, http.StatusOK, r.code, r.raw)
	assertClosed(t, r)
	assert.Equal(t, map[string]any{"version": "3", "length": float64(19), "request_id": r.body["request_id"]}, r.body)
	assert.NotContains(t, r.raw, "keep-1", "the upstream request id is not forwarded")
	require.Equal(t, 1, env.keep.count())
	assert.Contains(t, env.keep.calls[0], "/mesh/secrets/mesh-secret.workspace.workspace.KM_TOKEN."+env.wsA.String()+"/metadata")
	assert.NotContains(t, r.raw, "km-canary-value-1")
}

// A foreign secret and an absent one are the same answer, and neither reaches
// Keep: Mesh denies before the downstream call.
func TestKeepMetadataDB_ForeignAndAbsentAreIdentical(t *testing.T) {
	env := newKMEnv(t, true)
	foreign := env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), env.secretB.String(), "")
	absent := env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), uuid.NewString(), "")
	assertNotFound(t, foreign)
	assertNotFound(t, absent)
	assert.NotEqual(t, foreign.body["request_id"], absent.body["request_id"])
	delete(foreign.body, "request_id")
	delete(absent.body, "request_id")
	assert.Equal(t, foreign.body, absent.body)
	assert.Equal(t, 0, env.keep.count(), "a denied probe must not call Keep")
}

func TestKeepMetadataDB_OtherWorkspaceNamedInPathIsDenied(t *testing.T) {
	env := newKMEnv(t, true)
	// Caller is not a member of wsB: the route's own secret, wrong tenant.
	assertNotFound(t, env.do(t, http.MethodGet, env.owner.String(), env.wsB.String(), env.secretB.String(), ""))
	assertNotFound(t, env.do(t, http.MethodGet, env.stranger.String(), env.wsA.String(), env.secretA.String(), ""))
	assert.Equal(t, 0, env.keep.count())
}

func TestKeepMetadataDB_RoleAndActorGates(t *testing.T) {
	env := newKMEnv(t, true)
	for name, actor := range map[string]string{
		"viewer": env.viewer.String(), "member": env.member.String(), "agent": "agent", "extension": "extension",
	} {
		t.Run(name, func(t *testing.T) {
			assertNotFound(t, env.do(t, http.MethodGet, actor, env.wsA.String(), env.secretA.String(), ""))
		})
	}
	assert.Equal(t, 0, env.keep.count())

	// Revoked role: the same user is refused on the very next request.
	_, err := env.db.Exec(`UPDATE workspace_members SET role = $3 WHERE workspace_id = $1 AND user_id = $2`,
		env.wsA, env.owner, domain.RoleViewer)
	require.NoError(t, err)
	assertNotFound(t, env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), env.secretA.String(), ""))
	_, err = env.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, env.wsA, env.owner)
	require.NoError(t, err)
	assertNotFound(t, env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), env.secretA.String(), ""))
	assert.Equal(t, 0, env.keep.count())
}

func TestKeepMetadataDB_NoAuthIs401InTheClosedEnvelope(t *testing.T) {
	env := newKMEnv(t, true)
	r := env.do(t, http.MethodGet, "", env.wsA.String(), env.secretA.String(), "")
	assertError(t, r, http.StatusUnauthorized, "unauthenticated", "authentication failed")
	assert.NotContains(t, r.raw, "raw 401 text")
	assert.Equal(t, 0, env.keep.count())
}

// Request shape is judged before existence, so it cannot be an oracle.
func TestKeepMetadataDB_MalformedRequestIs400ForEverySecret(t *testing.T) {
	env := newKMEnv(t, true)
	for _, secret := range []string{env.secretA.String(), env.secretB.String(), uuid.NewString()} {
		assertError(t, env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), secret, "?x=1"),
			http.StatusBadRequest, "invalid_argument", "invalid metadata request")
	}
	assertError(t, env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), "not-a-uuid", ""),
		http.StatusBadRequest, "invalid_argument", "invalid metadata request")
	assertError(t, env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), strings.ToUpper(env.secretA.String()), ""),
		http.StatusBadRequest, "invalid_argument", "invalid metadata request")
	assertError(t, env.do(t, http.MethodGet, env.owner.String(), "x", env.secretA.String(), ""),
		http.StatusBadRequest, "invalid_argument", "invalid metadata request")
	assert.Equal(t, 0, env.keep.count())
}

func TestKeepMetadataDB_HeadIsNotAProbe(t *testing.T) {
	env := newKMEnv(t, true)
	r := env.do(t, http.MethodHead, env.owner.String(), env.wsA.String(), env.secretA.String(), "")
	assert.Equal(t, http.StatusMethodNotAllowed, r.code)
	assert.Equal(t, "no-store", r.hdr.Get("Cache-Control"))
	assert.Equal(t, 0, env.keep.count())
}

// Rows that are not a current workspace secret are not probed.
func TestKeepMetadataDB_RotatedExpiredAndBadRefAreNotFound(t *testing.T) {
	env := newKMEnv(t, true)
	get := func() kmResp {
		return env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), env.secretA.String(), "")
	}
	require.Equal(t, http.StatusOK, get().code)
	before := env.keep.count()

	_, err := env.db.Exec(`UPDATE secret_refs SET expires_at = now() - interval '1 minute' WHERE id = $1`, env.secretA)
	require.NoError(t, err)
	assertNotFound(t, get())
	_, err = env.db.Exec(`UPDATE secret_refs SET expires_at = now() + interval '1 hour' WHERE id = $1`, env.secretA)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, get().code, "an unexpired row is probed again")
	before++

	_, err = env.db.Exec(`UPDATE secret_refs SET rotated_at = now() WHERE id = $1`, env.secretA)
	require.NoError(t, err)
	assertNotFound(t, get())
	assert.Equal(t, before, env.keep.count(), "rotated/expired rows never reach Keep")
}

func TestKeepMetadataDB_UpstreamMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   int
		class  string
		msg    string
	}{
		{"keep 404", 404, `{"error_class":"not_found","message":"not found","request_id":"x"}`, 404, "not_found", "not found"},
		// An old Keep without the metadata route (bare 404) or a malformed 404 is
		// "metadata unavailable", never "secret not found".
		{"keep 404 bare", 404, "404 page not found\n", 503, "not_ready", "metadata unavailable"},
		{"keep 404 other class", 404, `{"error_class":"invalid_argument","message":"not found","request_id":"x"}`, 503, "not_ready", "metadata unavailable"},
		{"keep 404 duplicate key", 404, `{"error_class":"invalid_argument","error_class":"not_found","message":"not found","request_id":"x"}`, 503, "not_ready", "metadata unavailable"},
		{"keep 429", 429, `{}`, 429, "rate_limited", "request rate limited"},
		{"keep 401", 401, `{}`, 503, "not_ready", "metadata unavailable"},
		{"keep 403", 403, `{}`, 503, "not_ready", "metadata unavailable"},
		{"keep 500", 500, `secret upstream reason`, 503, "not_ready", "metadata unavailable"},
		{"bad schema", 200, `{"version":"1","length":1,"request_id":"r","value":"v"}`, 503, "not_ready", "metadata unavailable"},
		{"number version", 200, `{"version":1,"length":1,"request_id":"r"}`, 503, "not_ready", "metadata unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newKMEnv(t, true)
			env.keep.status, env.keep.body = tc.status, tc.body
			r := env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), env.secretA.String(), "")
			assertError(t, r, tc.code, tc.class, tc.msg)
			assert.NotContains(t, r.raw, "upstream reason")
			assert.Equal(t, 1, env.keep.count(), "no automatic retry")
		})
	}
}

func TestKeepMetadataDB_NoKeepBindingIs503NotAFallback(t *testing.T) {
	env := newKMEnv(t, false)
	assertError(t, env.do(t, http.MethodGet, env.owner.String(), env.wsA.String(), env.secretA.String(), ""),
		http.StatusServiceUnavailable, "not_ready", "metadata unavailable")
	assert.Equal(t, 0, env.keep.count())
}
