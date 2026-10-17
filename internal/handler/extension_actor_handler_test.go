package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type fakeExtActorService struct {
	rotateErr error
	row       *domain.ExtensionActor
	getErr    error
	rotatedWS uuid.UUID

	selfErr       error
	selfPresented string
	selfGrace     time.Duration
}

func (f *fakeExtActorService) Issue(context.Context, uuid.UUID, uuid.UUID, string, string, []string) (*service.IssuedExtensionToken, error) {
	return nil, nil
}

func (f *fakeExtActorService) Rotate(_ context.Context, ws, conn uuid.UUID) (*service.IssuedExtensionToken, error) {
	f.rotatedWS = ws
	if f.rotateErr != nil {
		return nil, f.rotateErr
	}
	return &service.IssuedExtensionToken{Token: "ext_new", Actor: "ext:gitlab@" + ws.String(), Grace: 5 * time.Minute}, nil
}
func (f *fakeExtActorService) SelfRotate(_ context.Context, p *service.ExtensionPrincipal, presented string, grace time.Duration) (*service.IssuedExtensionToken, error) {
	f.selfPresented, f.selfGrace = presented, grace
	if f.selfErr != nil {
		return nil, f.selfErr
	}
	return &service.IssuedExtensionToken{Token: "ext_NEWTOKENVALUE", ActorID: p.ActorID, Actor: p.Principal(), Grace: grace, IssuedAt: time.Unix(1_800_000_000, 0)}, nil
}
func (f *fakeExtActorService) SelfRevokePrevious(_ context.Context, _ *service.ExtensionPrincipal, presented string) error {
	f.selfPresented = presented
	return f.selfErr
}
func (f *fakeExtActorService) Revoke(context.Context, uuid.UUID) error { return nil }
func (f *fakeExtActorService) Authenticate(context.Context, string) (*service.ExtensionPrincipal, error) {
	return nil, nil
}
func (f *fakeExtActorService) RecordViolation(context.Context, uuid.UUID, string) {}
func (f *fakeExtActorService) Get(context.Context, uuid.UUID) (*domain.ExtensionActor, error) {
	return f.row, f.getErr
}

func actorCall(t *testing.T, h echo.HandlerFunc, method string, set func(c echo.Context)) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, "/", http.NoBody), rec)
	if set != nil {
		set(c)
	}
	require.NoError(t, h(c))
	return rec
}

func TestExtensionActorHandler_Me(t *testing.T) {
	ws, conn := uuid.New(), uuid.New()
	now := time.Now()
	svc := &fakeExtActorService{row: &domain.ExtensionActor{ViolationCount: 3, LastViolationAt: &now, LastViolation: "POST /x"}}
	h := NewExtensionActorHandler(svc)

	// Not an extension: 403.
	assert.Equal(t, http.StatusForbidden, actorCall(t, h.Me, http.MethodGet, nil).Code)

	set := func(c echo.Context) {
		c.Set(middleware.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{
			ConnectionID: conn, WorkspaceID: ws, ActorID: uuid.New(), ExtensionID: "gitlab",
			Capabilities: map[string][]string{"api": {"tasks:read"}},
		})
	}
	rec := actorCall(t, h.Me, http.MethodGet, set)
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "ext:gitlab@"+ws.String(), out["actor"])
	assert.Equal(t, ws.String(), out["workspace_id"])
	assert.Equal(t, float64(3), out["violations"].(map[string]any)["count"])
	assert.NotContains(t, rec.Body.String(), "token")

	// No actor row (or a read error): the identity is still returned.
	svc.row, svc.getErr = nil, nil
	rec = actorCall(t, h.Me, http.MethodGet, set)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "violations")
}

func TestExtensionActorHandler_RotateToken(t *testing.T) {
	ws, conn := uuid.New(), uuid.New()
	svc := &fakeExtActorService{}
	h := NewExtensionActorHandler(svc)
	set := func(id string, withWS bool) func(echo.Context) {
		return func(c echo.Context) {
			c.SetParamNames("int_id")
			c.SetParamValues(id)
			if withWS {
				c.Set(middleware.ContextKeyWorkspaceID, ws)
			}
		}
	}

	assert.Equal(t, http.StatusBadRequest, actorCall(t, h.RotateToken, http.MethodPost, set("nope", true)).Code)
	assert.Equal(t, http.StatusForbidden, actorCall(t, h.RotateToken, http.MethodPost, set(conn.String(), false)).Code)

	rec := actorCall(t, h.RotateToken, http.MethodPost, set(conn.String(), true))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, ws, svc.rotatedWS)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "ext_new", out["extension_token"])
	assert.Equal(t, float64(300), out["previous_valid_for"])

	svc.rotateErr = apierror.NotFound("Extension actor")
	assert.Equal(t, http.StatusNotFound, actorCall(t, h.RotateToken, http.MethodPost, set(conn.String(), true)).Code)
}

func TestExtensionActorHandler_SelfRotateAndRevokePrevious(t *testing.T) {
	ws, conn, actor := uuid.New(), uuid.New(), uuid.New()
	svc := &fakeExtActorService{}
	h := NewExtensionActorHandler(svc)
	principal := &service.ExtensionPrincipal{ConnectionID: conn, WorkspaceID: ws, ActorID: actor, ExtensionID: "keep"}
	call := func(hf echo.HandlerFunc, method, body string, authed bool) *httptest.ResponseRecorder {
		e := echo.New()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/", strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer ext_presented")
		c := e.NewContext(req, rec)
		if authed {
			c.Set(middleware.ContextKeyExtensionPrincipal, principal)
		}
		require.NoError(t, hf(c))
		return rec
	}

	// Not an extension actor: 403, the service is never reached.
	assert.Equal(t, http.StatusForbidden, call(h.SelfRotate, http.MethodPost, "", false).Code)
	assert.Equal(t, http.StatusForbidden, call(h.SelfRevokePrevious, http.MethodDelete, "", false).Code)
	assert.Empty(t, svc.selfPresented)

	// Default grace; the value is in the body once, with prefix/length, never cached.
	rec := call(h.SelfRotate, http.MethodPost, "", true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "ext_presented", svc.selfPresented)
	assert.Zero(t, svc.selfGrace, "service applies its own default")
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "ext_NEWTOKENVALUE", out["extension_token"])
	assert.Equal(t, "ext_NEWT", out["token_prefix"])
	assert.EqualValues(t, len("ext_NEWTOKENVALUE"), out["token_length"])
	assert.Equal(t, actor.String(), out["actor_id"])
	assert.Equal(t, conn.String(), out["connection_id"])

	// Explicit grace is passed on.
	require.Equal(t, http.StatusOK, call(h.SelfRotate, http.MethodPost, `{"grace_seconds":120}`, true).Code)
	assert.Equal(t, 120*time.Second, svc.selfGrace)

	// Out of range (including one that would overflow a Duration), negative, malformed: 400, service untouched.
	svc.selfGrace = -1
	for _, body := range []string{`{"grace_seconds":3601}`, `{"grace_seconds":9223372037}`, `{"grace_seconds":-5}`, `{"grace_seconds":"x"}`} {
		assert.Equal(t, http.StatusBadRequest, call(h.SelfRotate, http.MethodPost, body, true).Code, body)
	}
	assert.Equal(t, time.Duration(-1), svc.selfGrace)

	// Service errors keep their status.
	svc.selfErr = apierror.Unauthorized("invalid extension token")
	assert.Equal(t, http.StatusUnauthorized, call(h.SelfRotate, http.MethodPost, "", true).Code)
	svc.selfErr = apierror.Conflict("current token required")
	assert.Equal(t, http.StatusConflict, call(h.SelfRevokePrevious, http.MethodDelete, "", true).Code)

	// Revoke previous: 204 on success.
	svc.selfErr = nil
	assert.Equal(t, http.StatusNoContent, call(h.SelfRevokePrevious, http.MethodDelete, "", true).Code)
}

func TestTokenPrefix(t *testing.T) {
	assert.Equal(t, "ext_abcd", tokenPrefix("ext_abcdefgh"))
	assert.Empty(t, tokenPrefix("ext_abcd"))
}

type fakeAgentKeyService struct {
	issueErr, revokeErr, listErr error
	gotLabel                     string
	gotTTL                       time.Duration
	revoked                      string
}

func (f *fakeAgentKeyService) Issue(_ context.Context, agentID uuid.UUID, label string, ttl time.Duration) (*service.IssuedAgentKey, error) {
	f.gotLabel, f.gotTTL = label, ttl
	if f.issueErr != nil {
		return nil, f.issueErr
	}
	return &service.IssuedAgentKey{KeyID: uuid.New(), APIKey: "agk_ws_secret", AgentID: agentID, Label: label}, nil
}

func (f *fakeAgentKeyService) Revoke(_ context.Context, _ uuid.UUID, keyID string) error {
	f.revoked = keyID
	return f.revokeErr
}

func (f *fakeAgentKeyService) List(context.Context, uuid.UUID) ([]service.AgentKeyInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []service.AgentKeyInfo{{ID: "primary", Primary: true}}, nil
}

func keyCall(t *testing.T, h echo.HandlerFunc, method, body string, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	var names, values []string
	for k, v := range params {
		names, values = append(names, k), append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	require.NoError(t, h(c))
	return rec
}

func TestAgentKeyHandler_Issue(t *testing.T) {
	svc := &fakeAgentKeyService{}
	h := NewAgentKeyHandler(svc)
	id := uuid.New().String()

	assert.Equal(t, http.StatusBadRequest, keyCall(t, h.Issue, http.MethodPost, "", map[string]string{"agent_id": "x"}).Code)
	assert.Equal(t, http.StatusBadRequest, keyCall(t, h.Issue, http.MethodPost, "{bad", map[string]string{"agent_id": id}).Code)
	assert.Equal(t, http.StatusBadRequest, keyCall(t, h.Issue, http.MethodPost, `{"expires_in_seconds":-1}`, map[string]string{"agent_id": id}).Code)

	rec := keyCall(t, h.Issue, http.MethodPost, `{"label":"rotator","expires_in_seconds":60}`, map[string]string{"agent_id": id})
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), "agk_ws_secret", "the key is in the response exactly once")
	assert.Equal(t, "rotator", svc.gotLabel)
	assert.Equal(t, time.Minute, svc.gotTTL)

	// Empty body is fine: no label, no expiry.
	rec = keyCall(t, h.Issue, http.MethodPost, "", map[string]string{"agent_id": id})
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Zero(t, svc.gotTTL)

	svc.issueErr = apierror.Conflict("too many")
	assert.Equal(t, http.StatusConflict, keyCall(t, h.Issue, http.MethodPost, "", map[string]string{"agent_id": id}).Code)
}

func TestAgentKeyHandler_RevokeAndList(t *testing.T) {
	svc := &fakeAgentKeyService{}
	h := NewAgentKeyHandler(svc)
	id := uuid.New().String()

	assert.Equal(t, http.StatusBadRequest, keyCall(t, h.Revoke, http.MethodDelete, "", map[string]string{"agent_id": "x", "key_id": "primary"}).Code)
	assert.Equal(t, http.StatusNoContent, keyCall(t, h.Revoke, http.MethodDelete, "", map[string]string{"agent_id": id, "key_id": "primary"}).Code)
	assert.Equal(t, "primary", svc.revoked)
	svc.revokeErr = apierror.NotFound("Agent key")
	assert.Equal(t, http.StatusNotFound, keyCall(t, h.Revoke, http.MethodDelete, "", map[string]string{"agent_id": id, "key_id": uuid.New().String()}).Code)

	assert.Equal(t, http.StatusBadRequest, keyCall(t, h.List, http.MethodGet, "", map[string]string{"agent_id": "x"}).Code)
	rec := keyCall(t, h.List, http.MethodGet, "", map[string]string{"agent_id": id})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"primary":true`)
	assert.NotContains(t, rec.Body.String(), "agk_")
	svc.listErr = apierror.NotFound("Agent")
	assert.Equal(t, http.StatusNotFound, keyCall(t, h.List, http.MethodGet, "", map[string]string{"agent_id": id}).Code)
}
