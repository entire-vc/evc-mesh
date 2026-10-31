package external

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/examples/external-extension/refext"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// Contract tests between Mesh's outbound calls to an external service and the
// reference extension (examples/external-extension), which is written from the
// public documents only. The handshake and the token delivery here are the real
// Mesh code; only the stores behind them are in memory. A failure means a
// partner's service built the same way stops working: either Mesh regressed,
// or the change is breaking (docs/extensions/extension-actor-contract.md).
//
// Run all of them with: go test ./internal/... -run RefExt

const refextID = "reference-ext"

func refextManifest(t *testing.T) extensions.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "external-extension", "mesh-extension.json"))
	require.NoError(t, err)
	m, err := extensions.ParseManifest(raw)
	require.NoError(t, err)
	require.Equal(t, refextID, m.ID)
	return m
}

type refextRig struct {
	*rig
	tokens *fakeTokens
	ext    *refext.Server
	url    string
}

func newRefextRig(t *testing.T, installCode string) *refextRig {
	t.Helper()
	ext := &refext.Server{ExtensionID: refextID}
	ext.IssueInstallCode(installCode)
	srv := httptest.NewServer(ext)
	t.Cleanup(srv.Close)

	r := &rig{conns: &fakeConns{rows: map[uuid.UUID]*domain.IntegrationConfig{}}, secrets: &fakeSecrets{}, subs: &fakeSubs{}, ws: uuid.New(), connID: uuid.New()}
	cfg, _ := json.Marshal(map[string]any{"service_url": srv.URL})
	r.conns.rows[r.connID] = &domain.IntegrationConfig{ID: r.connID, WorkspaceID: r.ws, Provider: refextID, Config: cfg, IsActive: true}
	tokens := &fakeTokens{}
	client := &http.Client{Timeout: 3 * time.Second}
	r.hs = &Handshaker{Client: client, Conns: r.conns, Secrets: r.secrets, Reader: r.secrets, Subs: r.subs, Tokens: tokens}
	var err error
	r.p, err = NewProvider(refextManifest(t), extensions.Deps{Conns: r.conns}, client, r.hs)
	require.NoError(t, err)
	return &refextRig{rig: r, tokens: tokens, ext: ext, url: srv.URL}
}

func (r *refextRig) settings() map[string]any { return map[string]any{"service_url": r.url} }

func TestContract_RefExt_InstallTokenAndSubscriptions(t *testing.T) {
	r := newRefextRig(t, "")
	ctx := context.Background()

	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings()))
	assert.True(t, r.ext.Installed(r.ws.String()), "the reference service accepted Mesh's signed install")
	require.NotEmpty(t, r.tokens.current)
	assert.Equal(t, r.tokens.current, r.ext.Token(r.ws.String()), "and the token Mesh delivered")
	require.NotNil(t, r.tokens.deliveredAt, "Mesh counted the delivery as confirmed")

	var events []string
	for _, s := range r.subs.subs {
		assert.Equal(t, r.url+refext.PathEvents, s.Target)
		assert.Equal(t, SecretName(refextID), s.SecretRef)
		events = append(events, s.Event)
	}
	assert.ElementsMatch(t, []string{"task.created", "task.moved", "comment.created"}, events)

	// Health: the probe path the manifest names answers.
	assert.True(t, r.p.Health(ctx, extensions.Resolved{Enabled: true, WorkspaceID: r.ws, Settings: r.settings()}).OK)

	// Rotation reaches the service; the previous token is replaced there.
	before := r.tokens.current
	res, err := r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings())
	require.NoError(t, err)
	assert.True(t, res.Rotated)
	assert.NotEqual(t, before, r.tokens.current)
	assert.Equal(t, r.tokens.current, r.ext.Token(r.ws.String()))
}

// A re-key (the workspace signing secret rotated) is accepted and later calls
// verify with the new secret: the token delivered after it is accepted.
// A rotated signing secret reaches the service with a fresh install code;
// without one the service keeps the secret it has (a self-signed install
// request cannot prove it came from Mesh).
func TestContract_RefExt_SecretRotation(t *testing.T) {
	r := newRefextRig(t, "")
	ctx := context.Background()
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings()))

	r.secrets.mu.Lock()
	r.secrets.vals[SecretName(refextID)] = "rotated-signing-secret"
	r.secrets.mu.Unlock()
	err := r.hs.Ensure(ctx, r.p, r.ws, r.settings())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install endpoint answered 403")

	r.ext.IssueInstallCode("BIND-R0T8")
	r.secrets.mu.Lock()
	r.secrets.vals[InstallCodeName(refextID)] = "BIND-R0T8"
	r.secrets.mu.Unlock()
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings()))

	_, err = r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings())
	require.NoError(t, err)
	assert.Equal(t, r.tokens.current, r.ext.Token(r.ws.String()))
}

// The install code Mesh holds for the workspace is what binds it. The red half:
// without the code the same service refuses, and Mesh reports the install failed
// instead of delivering a token.
func TestContract_RefExt_InstallCode(t *testing.T) {
	ctx := context.Background()

	r := newRefextRig(t, "BIND-7Q2X")
	err := r.hs.Run(ctx, r.p, r.ws, r.settings())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install endpoint answered 403")
	assert.False(t, r.ext.Installed(r.ws.String()))
	assert.Empty(t, r.ext.Token(r.ws.String()))

	r.secrets.mu.Lock()
	r.secrets.vals[InstallCodeName(refextID)] = "BIND-7Q2X"
	r.secrets.mu.Unlock()
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings()))
	assert.True(t, r.ext.Installed(r.ws.String()))
	assert.Equal(t, r.tokens.current, r.ext.Token(r.ws.String()))

	// The code is one-time: rotating the signing secret while Mesh still holds
	// the spent code is refused, and a fresh code stored in Mesh re-keys.
	r.secrets.mu.Lock()
	r.secrets.vals[SecretName(refextID)] = "rotated-signing-secret"
	r.secrets.mu.Unlock()
	err = r.hs.Ensure(ctx, r.p, r.ws, r.settings())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install endpoint answered 403")

	r.ext.IssueInstallCode("BIND-9K4M")
	r.secrets.mu.Lock()
	r.secrets.vals[InstallCodeName(refextID)] = "BIND-9K4M"
	r.secrets.mu.Unlock()
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings()))
}
