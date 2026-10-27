package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type fakeTokenDeliverer struct {
	handled bool
	res     external.Delivered
	err     error
	calls   int
}

func (f *fakeTokenDeliverer) RotateAndDeliver(context.Context, uuid.UUID, uuid.UUID) (external.Delivered, bool, error) {
	f.calls++
	return f.res, f.handled, f.err
}

func rotateCall(t *testing.T, h *ExtensionActorHandler, ws, conn uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	return actorCall(t, h.RotateToken, http.MethodPost, func(c echo.Context) {
		c.SetParamNames("int_id")
		c.SetParamValues(conn.String())
		c.Set(middleware.ContextKeyWorkspaceID, ws)
	})
}

func TestExtensionActorHandler_RotateTokenDelivered(t *testing.T) {
	ws, conn := uuid.New(), uuid.New()
	svc := &fakeExtActorService{row: &domain.ExtensionActor{ExtensionID: "hello", WorkspaceID: ws}}
	d := &fakeTokenDeliverer{handled: true, res: external.Delivered{Rotated: true}}
	h := NewExtensionActorHandler(svc).WithTokenDelivery(d)

	rec := rotateCall(t, h, ws, conn)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.NotContains(t, out, "extension_token", "the token went to the service, not into the response")
	assert.Equal(t, true, out["delivered"])
	assert.Equal(t, "ext:hello@"+ws.String(), out["actor"])
	assert.Equal(t, float64(300), out["previous_valid_for"])
	assert.Equal(t, uuid.Nil, svc.rotatedWS, "plain Rotate is not called")

	d.res = external.Delivered{Rotated: false}
	require.NoError(t, json.Unmarshal(rotateCall(t, h, ws, conn).Body.Bytes(), &out))
	assert.Equal(t, float64(0), out["previous_valid_for"], "a first token has no previous one")
}

func TestExtensionActorHandler_RotateTokenDeliveryFailures(t *testing.T) {
	ws, conn := uuid.New(), uuid.New()
	svc := &fakeExtActorService{row: &domain.ExtensionActor{ExtensionID: "hello", WorkspaceID: ws}}
	d := &fakeTokenDeliverer{handled: true}
	h := NewExtensionActorHandler(svc).WithTokenDelivery(d)

	// The service refused: 502, and the message is the delivery error (status + host).
	d.err = deliveryErr(t)
	rec := rotateCall(t, h, ws, conn)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "answered 401")
	assert.NotContains(t, rec.Body.String(), "extension_token")

	// The actor service refused (revoked): its own status.
	d.err = apierror.Conflict("extension actor is revoked; reconnect the extension to issue a new token")
	assert.Equal(t, http.StatusConflict, rotateCall(t, h, ws, conn).Code)

	// Not a token_path extension: rotate as before, token in the response.
	d.handled, d.err = false, nil
	rec = rotateCall(t, h, ws, conn)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"extension_token":"ext_new"`)
	assert.Equal(t, ws, svc.rotatedWS)

	d.err = errors.New("db down")
	assert.Equal(t, http.StatusInternalServerError, rotateCall(t, h, ws, conn).Code)

	// Delivered, but the actor row cannot be read back for the answer.
	d.handled, d.err = true, nil
	svc.row = nil
	assert.Equal(t, http.StatusInternalServerError, rotateCall(t, h, ws, conn).Code)
}

// deliveryErr produces a real *external.DeliveryError through the external
// package: a service that answers 401 to the token post.
func deliveryErr(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/t" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	p, hs, ws, conn, settings := tokenPathRig(t, srv.URL, &canaryTokens{token: "ext_CANARYTOKEN"})
	_, err := hs.RotateAndDeliver(context.Background(), p, ws, conn, settings)
	require.Error(t, err)
	return err
}

// canaryTokens hands out one known token value so the logs and the response
// can be searched for it.
type canaryTokens struct{ token string }

func (c *canaryTokens) RotateForDelivery(context.Context, uuid.UUID, uuid.UUID) (token string, restore func(context.Context) error, err error) {
	return c.token, func(context.Context) error { return nil }, nil
}
func (c *canaryTokens) TokenDeliveredAt(context.Context, uuid.UUID) (*time.Time, error) {
	return nil, nil
}
func (c *canaryTokens) MarkTokenDelivered(context.Context, uuid.UUID, string, time.Time) error {
	return nil
}

type staticSecrets map[string]string

func (s staticSecrets) Create(context.Context, domain.CreateSecretInput) (domain.Secret, error) {
	return domain.Secret{}, nil
}
func (s staticSecrets) ResolveCurrentValues(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID) ([]domain.MaterializedSecret, error) {
	var out []domain.MaterializedSecret
	for k, v := range s {
		out = append(out, domain.MaterializedSecret{Name: k, Value: v})
	}
	return out, nil
}

func tokenPathRig(t *testing.T, base string, tokens external.TokenStore) (p *external.Provider, hs *external.Handshaker, ws, conn uuid.UUID, settings map[string]any) {
	t.Helper()
	m, err := extensions.ParseManifest([]byte(`{"schema":1,"id":"hello","name":"Hello","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":[],"ui":[]},
 "external":{"base_url_setting":"hello_url","token_path":"/t"}}`))
	require.NoError(t, err)
	sec := staticSecrets{external.SecretName("hello"): "k"}
	hs = &external.Handshaker{Client: http.DefaultClient, Secrets: sec, Reader: sec, Tokens: tokens}
	p, err = external.NewProvider(m, extensions.Deps{}, http.DefaultClient, hs)
	require.NoError(t, err)
	return p, hs, uuid.New(), uuid.New(), map[string]any{"hello_url": base}
}

// Canary: the token Mesh delivers must appear nowhere but in the body posted to
// the service: not in slog, not in the API response, not in an error.
func TestExtensionActorHandler_RotateTokenNeverLeaksToken(t *testing.T) {
	const canary = "ext_CANARYTOKEN"
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var received string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ExtensionToken string `json:"extension_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		received = in.ExtensionToken
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	p, hs, ws, conn, settings := tokenPathRig(t, srv.URL, &canaryTokens{token: canary})
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(p))
	cfg, _ := json.Marshal(settings)
	conns := staticConns{conn: &domain.IntegrationConfig{ID: conn, WorkspaceID: ws, Provider: "hello", Config: cfg, IsActive: true}}
	svc := &fakeExtActorService{row: &domain.ExtensionActor{ExtensionID: "hello", WorkspaceID: ws}}
	h := NewExtensionActorHandler(svc).WithTokenDelivery(&external.TokenRotator{Reg: reg, Conns: conns, HS: hs})

	var bodies []string
	for _, st := range []int{http.StatusOK, http.StatusUnauthorized} {
		status = st
		rec := rotateCall(t, h, ws, conn)
		bodies = append(bodies, rec.Body.String())
	}
	assert.Equal(t, canary, received, "the service got the token")
	assert.Contains(t, bodies[0], `"delivered":true`)
	assert.Contains(t, bodies[1], "answered 401")
	for _, b := range bodies {
		assert.NotContains(t, b, canary, "API response")
	}
	assert.Contains(t, logs.String(), "extension token delivered to the service", "the success was logged")
	assert.NotContains(t, logs.String(), canary, "slog")
}

type staticConns struct{ conn *domain.IntegrationConfig }

func (s staticConns) GetByID(_ context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	if s.conn != nil && s.conn.ID == id {
		return s.conn, nil
	}
	return nil, nil
}
