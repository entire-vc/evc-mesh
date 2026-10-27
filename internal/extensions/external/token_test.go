package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// tokenSvc is a stand-in service with a token endpoint: it verifies the
// signature with the secret it got at install and refuses a timestamp not
// newer than the last one it accepted.
type tokenSvc struct {
	mu       sync.Mutex
	secret   string
	lastTS   int64
	tokens   []string
	posts    int
	status   int    // forced answer of /mesh/token when non-zero
	body     string // forced body when non-empty
	srv      *httptest.Server
	rejected []string
}

func newTokenSvc(t *testing.T) *tokenSvc {
	t.Helper()
	s := &tokenSvc{}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *tokenSvc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/mesh/install":
		var in installRequest
		_ = json.Unmarshal(body, &in)
		s.secret = in.SigningSecret
	case "/mesh/token":
		s.posts++
		ts := r.Header.Get("X-Mesh-Timestamp")
		n, err := strconv.ParseInt(ts, 10, 64)
		switch {
		case r.Header.Get("Authorization") != "":
			s.rejected = append(s.rejected, "authorization header")
		case err != nil || r.Header.Get("X-Mesh-Signature") != Sign(s.secret, ts, body):
			s.rejected = append(s.rejected, "signature")
		case n <= s.lastTS:
			s.rejected = append(s.rejected, "timestamp not newer")
		}
		if len(s.rejected) > 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if s.status != 0 {
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.body))
			return
		}
		var in tokenRequest
		_ = json.Unmarshal(body, &in)
		s.lastTS = n
		s.tokens = append(s.tokens, in.ExtensionToken)
		_, _ = w.Write([]byte(`{"ok":true}`))
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (s *tokenSvc) got() (tokens []string, posts int, rejected []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...), s.posts, append([]string(nil), s.rejected...)
}

// fakeTokens mirrors the actor service: a current token, the previous one,
// a conditional restore and the delivery marker.
type fakeTokens struct {
	mu          sync.Mutex
	current     string
	prev        string
	seq         int
	deliveredAt *time.Time
	restores    int
	rotateErr   error
	readErr     error
	restoreErr  error
	markErr     error
}

func (f *fakeTokens) RotateForDelivery(context.Context, uuid.UUID, uuid.UUID) (token string, restore func(context.Context) error, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rotateErr != nil {
		return "", nil, f.rotateErr
	}
	f.seq++
	tok := fmt.Sprintf("ext_SECRETTOKEN%03d", f.seq)
	if f.current == "" {
		f.current = tok
		return tok, nil, nil
	}
	before, beforePrev := f.current, f.prev
	f.prev, f.current = f.current, tok
	return tok, func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.restoreErr != nil {
			return f.restoreErr
		}
		if f.current == tok {
			f.current, f.prev = before, beforePrev
			f.restores++
		}
		return nil
	}, nil
}

func (f *fakeTokens) TokenDeliveredAt(context.Context, uuid.UUID) (*time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deliveredAt, f.readErr
}

func (f *fakeTokens) MarkTokenDelivered(_ context.Context, _ uuid.UUID, token string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return f.markErr
	}
	if token != f.current {
		return fmt.Errorf("not current")
	}
	f.deliveredAt = &at
	return nil
}

const tokenManifestJSON = `{"schema":1,"id":"hello","name":"Hello","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":false},
 "capabilities":{"api":["tasks:read"],"events":["task.updated"],"ui":[]},
 "external":{"base_url_setting":"hello_url","install_path":"/mesh/install","events_path":"/mesh/events","token_path":"/mesh/token"}}`

func newTokenRig(t *testing.T, url string) (*rig, *fakeTokens) {
	t.Helper()
	r := newRig(t, url)
	tokens := &fakeTokens{}
	r.hs.Tokens = tokens
	m, err := extensions.ParseManifest([]byte(tokenManifestJSON))
	require.NoError(t, err)
	r.p, err = NewProvider(m, extensions.Deps{Conns: r.conns}, r.hs.Client, r.hs)
	require.NoError(t, err)
	return r, tokens
}

func TestTokenDelivery_InstallDeliversOnceSigned(t *testing.T) {
	s := newTokenSvc(t)
	r, tokens := newTokenRig(t, s.srv.URL)
	ctx := context.Background()

	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	got, posts, rejected := s.got()
	assert.Empty(t, rejected, "signed with the connection key, no Authorization")
	require.Equal(t, []string{tokens.current}, got, "the service holds the current token")
	assert.Equal(t, 1, posts)
	require.NotNil(t, tokens.deliveredAt)

	// A repeated handshake (health, restart) neither rotates nor posts again.
	r.hs.Forget(r.ws, "hello")
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	_, posts, _ = s.got()
	assert.Equal(t, 1, posts)
	assert.Equal(t, 1, tokens.seq)
}

func TestTokenDelivery_NoTokenPathChangesNothing(t *testing.T) {
	s := newTokenSvc(t)
	r := newRig(t, s.srv.URL) // manifest without token_path
	tokens := &fakeTokens{}
	r.hs.Tokens = tokens
	require.NoError(t, r.hs.Run(context.Background(), r.p, r.ws, r.settings(s.srv.URL)))
	_, posts, _ := s.got()
	assert.Zero(t, posts)
	assert.Zero(t, tokens.seq)
	_, err := r.hs.RotateAndDeliver(context.Background(), r.p, r.ws, r.connID, r.settings(s.srv.URL))
	assert.Error(t, err)
}

func TestTokenDelivery_RotateSameSecondIsMonotonic(t *testing.T) {
	s := newTokenSvc(t)
	r, tokens := newTokenRig(t, s.srv.URL)
	ctx := context.Background()
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))

	// Three rotations well inside one second: each must be signed later than the last.
	for i := 0; i < 3; i++ {
		res, err := r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
		require.NoError(t, err)
		assert.True(t, res.Rotated)
	}
	got, _, rejected := s.got()
	assert.Empty(t, rejected)
	require.Len(t, got, 4)
	assert.Equal(t, tokens.current, got[3])
	assert.Greater(t, tokens.deliveredAt.Unix(), time.Now().Unix(), "timestamps ran ahead of the clock instead of repeating")
}

func TestTokenDelivery_FailureRestoresPreviousToken(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(s *tokenSvc){
		"401":            func(s *tokenSvc) { s.status = http.StatusUnauthorized },
		"500":            func(s *tokenSvc) { s.status = http.StatusInternalServerError },
		"204":            func(s *tokenSvc) { s.status = http.StatusNoContent },
		"200 without ok": func(s *tokenSvc) { s.status, s.body = http.StatusOK, `{"ok":false}` },
		"200 not json":   func(s *tokenSvc) { s.status, s.body = http.StatusOK, `ok` },
		"unreachable":    func(s *tokenSvc) { s.srv.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			s := newTokenSvc(t)
			r, tokens := newTokenRig(t, s.srv.URL)
			require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
			old, delivered := tokens.current, *tokens.deliveredAt
			setup(s)

			_, err := r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
			var de *DeliveryError
			require.ErrorAs(t, err, &de)
			assert.Contains(t, err.Error(), "127.0.0.1", "names the host")
			assert.NotContains(t, err.Error(), "ext_", "never the token")
			assert.Equal(t, old, tokens.current, "the previous token is current again")
			assert.Equal(t, 1, tokens.restores)
			assert.Equal(t, delivered, *tokens.deliveredAt)
		})
	}
}

func TestTokenDelivery_InstallFailureRetriesOnNextHandshake(t *testing.T) {
	s := newTokenSvc(t)
	r, tokens := newTokenRig(t, s.srv.URL)
	ctx := context.Background()
	s.status = http.StatusServiceUnavailable
	require.Error(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	assert.Nil(t, tokens.deliveredAt)

	s.mu.Lock()
	s.status = 0
	s.mu.Unlock()
	require.NoError(t, r.hs.Ensure(ctx, r.p, r.ws, r.settings(s.srv.URL)), "not memoised after a failed delivery")
	got, _, _ := s.got()
	require.Len(t, got, 1)
	assert.Equal(t, tokens.current, got[0])
}

func TestTokenDelivery_TokenNeverLogged(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := newTokenSvc(t)
	r, tokens := newTokenRig(t, s.srv.URL)
	ctx := context.Background()
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	tokens.rotateErr = nil
	s.status = http.StatusUnauthorized
	_, err := r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
	require.Error(t, err)
	assert.NotContains(t, logs.String(), "ext_SECRETTOKEN")
	assert.NotContains(t, err.Error(), "ext_SECRETTOKEN")
}

func TestTokenDelivery_Refusals(t *testing.T) {
	ctx := context.Background()
	s := newTokenSvc(t)

	r, _ := newTokenRig(t, s.srv.URL)
	_, err := r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
	assert.ErrorContains(t, err, "signing secret is missing", "no install handshake yet")

	_, err = r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings("http://example.com"))
	assert.ErrorIs(t, err, errAddress)

	r.secrets.readErr = errors.New("db down")
	_, err = r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
	assert.ErrorContains(t, err, "signing secret lookup failed")
	r.secrets.readErr = nil

	r.hs.Tokens = nil
	_, err = r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
	assert.ErrorContains(t, err, "not configured")
}

func TestTokenDelivery_StoreFailures(t *testing.T) {
	ctx := context.Background()
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := newTokenSvc(t)
	r, tokens := newTokenRig(t, s.srv.URL)

	tokens.readErr = errors.New("db down")
	require.ErrorContains(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)), "read token delivery state", "install")
	_, err := r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
	require.ErrorContains(t, err, "read token delivery state", "rotate")
	tokens.readErr = nil

	tokens.rotateErr = errors.New("actor revoked")
	require.ErrorContains(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)), "actor revoked")
	tokens.rotateErr = nil

	// Delivered but the marker could not be written: success, logged.
	tokens.markErr = errors.New("db down")
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	assert.Contains(t, logs.String(), "delivered but not recorded")
	assert.Nil(t, tokens.deliveredAt, "so the next handshake delivers again")
	tokens.markErr = nil
	// Without the marker the next delivery is signed from the clock again; the
	// service accepts it from the next second on (a health retry), simulated here.
	s.mu.Lock()
	s.lastTS = 0
	s.mu.Unlock()
	r.hs.Forget(r.ws, "hello")
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	require.NotNil(t, tokens.deliveredAt)

	// Refused, and the restore itself failed: the delivery error stands, the restore failure is logged.
	tokens.restoreErr = errors.New("db down")
	s.mu.Lock()
	s.status = http.StatusUnauthorized
	s.mu.Unlock()
	_, err = r.hs.RotateAndDeliver(ctx, r.p, r.ws, r.connID, r.settings(s.srv.URL))
	var de *DeliveryError
	require.ErrorAs(t, err, &de)
	assert.Contains(t, logs.String(), "restoring the previous token failed")
	assert.NotContains(t, logs.String(), "ext_SECRETTOKEN")
}

type connsByID func(id uuid.UUID) (*domain.IntegrationConfig, error)

func (f connsByID) GetByID(_ context.Context, id uuid.UUID) (*domain.IntegrationConfig, error) {
	return f(id)
}

func TestTokenRotator(t *testing.T) {
	ctx := context.Background()
	s := newTokenSvc(t)
	r, tokens := newTokenRig(t, s.srv.URL)
	require.NoError(t, r.hs.Run(ctx, r.p, r.ws, r.settings(s.srv.URL)))
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(r.p))
	row := *r.conns.rows[r.connID]
	var connErr error
	rot := &TokenRotator{Reg: reg, HS: r.hs, Conns: connsByID(func(id uuid.UUID) (*domain.IntegrationConfig, error) {
		if connErr != nil {
			return nil, connErr
		}
		if id != row.ID {
			return nil, nil
		}
		c := row
		return &c, nil
	})}

	res, handled, err := rot.RotateAndDeliver(ctx, r.ws, r.connID)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.True(t, res.Rotated)
	got, _, _ := s.got()
	assert.Equal(t, tokens.current, got[len(got)-1])

	for name, call := range map[string]func() (bool, error){
		"unknown connection": func() (bool, error) { _, h, e := rot.RotateAndDeliver(ctx, r.ws, uuid.New()); return h, e },
		"other workspace":    func() (bool, error) { _, h, e := rot.RotateAndDeliver(ctx, uuid.New(), r.connID); return h, e },
	} {
		h, e := call()
		assert.False(t, h, name)
		assert.NoError(t, e, name)
	}

	connErr = errors.New("db down")
	_, handled, err = rot.RotateAndDeliver(ctx, r.ws, r.connID)
	assert.False(t, handled)
	assert.Error(t, err)
	connErr = nil

	row.Config = []byte("not json")
	_, handled, err = rot.RotateAndDeliver(ctx, r.ws, r.connID)
	assert.True(t, handled)
	assert.ErrorContains(t, err, "unreadable")

	// An extension without token_path, and one that is not registered: not handled.
	plain := newRig(t, s.srv.URL)
	plainReg := extensions.NewRegistry()
	require.NoError(t, plainReg.Register(plain.p))
	rot.Reg = plainReg
	_, handled, err = rot.RotateAndDeliver(ctx, r.ws, r.connID)
	assert.False(t, handled)
	assert.NoError(t, err)
	rot.Reg = extensions.NewRegistry()
	_, handled, err = rot.RotateAndDeliver(ctx, r.ws, r.connID)
	assert.False(t, handled)
	assert.NoError(t, err)
}

// Concurrent install handshakes for one connection deliver exactly once: the
// delivery marker is checked under the per-connection lock, not before it.
func TestTokenDelivery_ConcurrentInstallDeliversOnce(t *testing.T) {
	s := newTokenSvc(t)
	s.secret = "install-secret"
	r, tokens := newTokenRig(t, s.srv.URL)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- r.hs.deliverOnInstall(ctx, r.p, r.ws, r.connID, s.srv.URL, "install-secret")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	got, posts, rejected := s.got()
	assert.Empty(t, rejected)
	assert.Equal(t, 1, posts, "one delivery for one connection")
	assert.Equal(t, []string{tokens.current}, got)
	assert.Equal(t, 1, tokens.seq, "no second rotation")
}
