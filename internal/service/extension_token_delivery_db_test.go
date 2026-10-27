package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// The token Mesh delivers to an extension's service (external.token_path),
// end to end over the real extension_actors table: what the service received
// authenticates, the old token keeps its grace, and a refused delivery leaves
// the previous token exactly as it was.

type tokenReceiver struct {
	mu     sync.Mutex
	status int
	tokens []string
	srv    *httptest.Server
}

func newTokenReceiver(t *testing.T) *tokenReceiver {
	t.Helper()
	r := &tokenReceiver{status: http.StatusOK}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			ExtensionToken string `json:"extension_token"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.status != http.StatusOK {
			w.WriteHeader(r.status)
			return
		}
		r.tokens = append(r.tokens, in.ExtensionToken)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *tokenReceiver) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tokens...)
}

func (r *tokenReceiver) setStatus(s int) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

type tokenDeliveryRig struct {
	f        *extActorFixture
	hs       *external.Handshaker
	p        *external.Provider
	settings map[string]any
}

func newTokenDeliveryRig(t *testing.T, base string) *tokenDeliveryRig {
	t.Helper()
	f := newExtActorFixture(t)
	m, err := extensions.ParseManifest([]byte(`{"schema":1,"id":"gitlab","name":"GitLab","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":[],"ui":[]},
 "external":{"base_url_setting":"url","token_path":"/mesh/token"}}`))
	require.NoError(t, err)
	client := &http.Client{Timeout: 3 * time.Second}
	sec := &extRtSecrets{}
	hs := &external.Handshaker{Client: client, Conns: postgres.NewIntegrationRepo(f.db), Secrets: sec, Reader: sec, Subs: &extRtSubs{}, Tokens: f.svc}
	p, err := external.NewProvider(m, extensions.Deps{}, client, hs)
	require.NoError(t, err)
	return &tokenDeliveryRig{f: f, hs: hs, p: p, settings: map[string]any{"url": base}}
}

func TestTokenDelivery_InstallDeliversOnceOverDB(t *testing.T) {
	svc := newTokenReceiver(t)
	r := newTokenDeliveryRig(t, svc.srv.URL)
	ctx := context.Background()
	issued, err := r.f.svc.Issue(ctx, r.f.ws.ID, r.f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read"})
	require.NoError(t, err)

	require.NoError(t, r.hs.Run(ctx, r.p, r.f.ws.ID, r.settings))
	got := svc.received()
	require.Len(t, got, 1)
	_, err = r.f.svc.Authenticate(ctx, got[0])
	assert.NoError(t, err, "the delivered token authenticates")
	_, err = r.f.svc.Authenticate(ctx, issued.Token)
	assert.NoError(t, err, "the connect-time token keeps its grace")
	at, err := r.f.svc.TokenDeliveredAt(ctx, r.f.conn.ID)
	require.NoError(t, err)
	require.NotNil(t, at)

	r.hs.Forget(r.f.ws.ID, "gitlab")
	require.NoError(t, r.hs.Run(ctx, r.p, r.f.ws.ID, r.settings))
	assert.Len(t, svc.received(), 1, "a repeated handshake does not rotate")

	// A reconnect re-issues: the service must get the new token again.
	_, err = r.f.svc.Issue(ctx, r.f.ws.ID, r.f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read"})
	require.NoError(t, err)
	at, err = r.f.svc.TokenDeliveredAt(ctx, r.f.conn.ID)
	require.NoError(t, err)
	assert.Nil(t, at)
}

func TestTokenDelivery_RotateAndRefusedRotateOverDB(t *testing.T) {
	svc := newTokenReceiver(t)
	r := newTokenDeliveryRig(t, svc.srv.URL)
	ctx := context.Background()
	_, err := r.f.svc.Issue(ctx, r.f.ws.ID, r.f.conn.ID, "gitlab", "GitLab", []string{"api:tasks:read"})
	require.NoError(t, err)
	require.NoError(t, r.hs.Run(ctx, r.p, r.f.ws.ID, r.settings))
	first := svc.received()[0]

	res, err := r.hs.RotateAndDeliver(ctx, r.p, r.f.ws.ID, r.f.conn.ID, r.settings)
	require.NoError(t, err)
	assert.True(t, res.Rotated)
	second := svc.received()[1]
	assert.NotEqual(t, first, second)
	for _, tok := range []string{first, second} {
		_, err = r.f.svc.Authenticate(ctx, tok)
		assert.NoError(t, err, "new token, and the old one inside grace")
	}
	before, err := r.f.svc.Get(ctx, r.f.conn.ID)
	require.NoError(t, err)

	// The service refuses: the API errs, the previous state comes back whole.
	svc.setStatus(http.StatusUnauthorized)
	_, err = r.hs.RotateAndDeliver(ctx, r.p, r.f.ws.ID, r.f.conn.ID, r.settings)
	var de *external.DeliveryError
	require.ErrorAs(t, err, &de)
	_, err = r.f.svc.Authenticate(ctx, second)
	assert.NoError(t, err, "the current token works as before the call")
	_, err = r.f.svc.Authenticate(ctx, first)
	assert.NoError(t, err, "and so does the previous one, within its original grace")
	after, err := r.f.svc.Get(ctx, r.f.conn.ID)
	require.NoError(t, err)
	assert.Equal(t, before.TokenHash, after.TokenHash)
	assert.Equal(t, before.PrevTokenHash, after.PrevTokenHash)
	assert.True(t, before.PrevTokenExpiresAt.Equal(*after.PrevTokenExpiresAt))
	assert.True(t, before.TokenDeliveredAt.Equal(*after.TokenDeliveredAt))
	assert.Len(t, svc.received(), 2, "the refused token was never accepted")
}

func TestExtensionActorRepo_RestoreTokenOnlyWhileCurrent(t *testing.T) {
	f := newExtActorFixture(t)
	ctx := context.Background()
	_, err := f.svc.Issue(ctx, f.ws.ID, f.conn.ID, "gitlab", "GitLab", nil)
	require.NoError(t, err)
	tok, restore, err := f.svc.RotateForDelivery(ctx, f.ws.ID, f.conn.ID)
	require.NoError(t, err)
	require.NotNil(t, restore)
	// Someone rotated after us: our restore must not undo their token.
	later, err := f.svc.Rotate(ctx, f.ws.ID, f.conn.ID)
	require.NoError(t, err)
	require.NoError(t, restore(ctx))
	_, err = f.svc.Authenticate(ctx, later.Token)
	assert.NoError(t, err)
	assert.Error(t, f.svc.MarkTokenDelivered(ctx, f.conn.ID, tok, time.Now()), "only the current token can be marked")
}
