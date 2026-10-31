package refext

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// These tests stand in for Mesh with a local key and JWKS. That the verifier
// accepts tokens Mesh really issues is proven by Mesh's own test
// (go test ./internal/extensions/uictx -run RefExt).

type fakeMesh struct {
	srv     *httptest.Server
	key     *ecdsa.PrivateKey
	kid     string
	fetches atomic.Int32
	status  int
}

func newFakeMesh(t *testing.T) *fakeMesh {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMesh{key: key, kid: "k1", status: http.StatusOK}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != JWKSPath {
			http.NotFound(w, r)
			return
		}
		m.fetches.Add(1)
		if m.status != http.StatusOK {
			w.WriteHeader(m.status)
			return
		}
		pt, _ := m.key.PublicKey.Bytes() // 0x04 || X || Y
		enc := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "ignored"},
			{"kty": "EC", "crv": "P-256", "kid": "short", "x": "AQ", "y": "AQ"},
			{"kty": "EC", "crv": "P-256", "alg": "ES256", "kid": m.kid, "x": enc(pt[1:33]), "y": enc(pt[33:])},
		}})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeMesh) token(t *testing.T, mutate func(jwt.MapClaims, *jwt.Token)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": m.srv.URL, "aud": "reference-ext", "sub": "u1", "email": "a@example.com",
		"workspace_id": ws, "project_id": "p1", "task_id": "t1", "roles": []string{"member", "admin"},
		"exp": t0.Add(time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = m.kid
	if mutate != nil {
		mutate(claims, tok)
	}
	s, err := tok.SignedString(m.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (m *fakeMesh) verifier(now *time.Time) *ContextVerifier {
	return &ContextVerifier{MeshOrigin: m.srv.URL, ExtensionID: "reference-ext", now: func() time.Time { return *now }}
}

func TestContextVerifier(t *testing.T) {
	m := newFakeMesh(t)
	now := t0
	v := m.verifier(&now)
	ctx := context.Background()

	c, err := v.Verify(ctx, m.token(t, nil))
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	if c.UserID != "u1" || c.Email != "a@example.com" || c.WorkspaceID != ws || c.ProjectID != "p1" || c.TaskID != "t1" || strings.Join(c.Roles, ",") != "member,admin" {
		t.Fatalf("context: %+v", c)
	}

	refused := map[string]func(jwt.MapClaims, *jwt.Token){
		"wrong audience": func(c jwt.MapClaims, _ *jwt.Token) { c["aud"] = "other-ext" },
		"wrong issuer":   func(c jwt.MapClaims, _ *jwt.Token) { c["iss"] = "https://evil.example" },
		"expired":        func(c jwt.MapClaims, _ *jwt.Token) { c["exp"] = t0.Add(-time.Second).Unix() },
		"no expiry":      func(c jwt.MapClaims, _ *jwt.Token) { delete(c, "exp") },
		"no workspace":   func(c jwt.MapClaims, _ *jwt.Token) { delete(c, "workspace_id") },
		"no subject":     func(c jwt.MapClaims, _ *jwt.Token) { delete(c, "sub") },
	}
	for name, mutate := range refused {
		if _, err := v.Verify(ctx, m.token(t, mutate)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Signed by a key Mesh never published, under Mesh's kid.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	forged := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": m.srv.URL, "aud": "reference-ext", "sub": "u1", "workspace_id": ws, "exp": t0.Add(time.Minute).Unix()})
	forged.Header["kid"] = m.kid
	raw, _ := forged.SignedString(other)
	if _, err := v.Verify(ctx, raw); err == nil {
		t.Error("foreign signature: accepted")
	}
	if _, err := v.Verify(ctx, "not-a-jwt"); err == nil {
		t.Error("garbage: accepted")
	}
	if got := m.fetches.Load(); got != 1 {
		t.Fatalf("known kid within the TTL must use the cache: %d fetches", got)
	}
}

func TestContextVerifier_KeyRefetch(t *testing.T) {
	m := newFakeMesh(t)
	now := t0
	v := m.verifier(&now)
	ctx := context.Background()
	unknown := func(_ jwt.MapClaims, tok *jwt.Token) { tok.Header["kid"] = "rotated" }

	if _, err := v.Verify(ctx, m.token(t, nil)); err != nil {
		t.Fatal(err)
	}
	// A stream of unknown kids inside jwksMinRefetch costs no extra fetch.
	for range 3 {
		if _, err := v.Verify(ctx, m.token(t, unknown)); err == nil {
			t.Fatal("unknown kid accepted")
		}
	}
	if got := m.fetches.Load(); got != 1 {
		t.Fatalf("unknown kids within %s: %d fetches, want 1", jwksMinRefetch, got)
	}
	// After it, an unknown kid refetches once (the key may have rotated).
	now = t0.Add(jwksMinRefetch + time.Second)
	if _, err := v.Verify(ctx, m.token(t, unknown)); err == nil {
		t.Fatal("still unknown after refetch, accepted")
	}
	if got := m.fetches.Load(); got != 2 {
		t.Fatalf("unknown kid after %s: %d fetches, want 2", jwksMinRefetch, got)
	}
	// A known kid past the TTL refetches too.
	now = now.Add(jwksTTL + time.Second)
	m.kid = "k1"
	tok := m.token(t, func(c jwt.MapClaims, _ *jwt.Token) { c["exp"] = now.Add(time.Minute).Unix() })
	if _, err := v.Verify(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if got := m.fetches.Load(); got != 3 {
		t.Fatalf("expired cache: %d fetches, want 3", got)
	}

	// Mesh answering an error is not a key.
	m.status = http.StatusBadGateway
	now = now.Add(jwksTTL + time.Second)
	tok = m.token(t, func(c jwt.MapClaims, _ *jwt.Token) { c["exp"] = now.Add(time.Minute).Unix() })
	if _, err := v.Verify(ctx, tok); err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("jwks 502: %v", err)
	}
	// Nor is Mesh unreachable.
	down := &ContextVerifier{MeshOrigin: "http://127.0.0.1:1", ExtensionID: "reference-ext", HTTP: &http.Client{Timeout: time.Second}}
	if _, err := down.Verify(ctx, m.token(t, func(c jwt.MapClaims, _ *jwt.Token) { c["exp"] = time.Now().Add(time.Minute).Unix() })); err == nil {
		t.Fatal("unreachable jwks: accepted")
	}
}

func TestScreens(t *testing.T) {
	m := newFakeMesh(t)
	now := t0
	s := newServer()
	s.UI = m.verifier(&now)

	get := func(path, token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?token="+token, http.NoBody))
		return rec
	}
	rec := get(PathUITask, m.token(t, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("task screen: %d %s", rec.Code, rec.Body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "frame-ancestors "+m.srv.URL {
		t.Fatalf("CSP: %q", csp)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("screen must not be cached")
	}
	for _, want := range []string{"Hello, a@example.com", "Task t1 in project p1", "member, admin", `"mesh.ready"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("page lacks %q", want)
		}
	}

	noTask := m.token(t, func(c jwt.MapClaims, _ *jwt.Token) { delete(c, "task_id") })
	if rec := get(PathUIHello, noTask); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Task ") {
		t.Fatalf("sidebar screen: %d", rec.Code)
	}
	if rec := get(PathUITask, noTask); rec.Code != http.StatusBadRequest {
		t.Fatalf("task screen without a task: want 400, got %d", rec.Code)
	}
	if rec := get(PathUIHello, "bad"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", rec.Code)
	}

	// No verifier, no screens.
	s.UI = nil
	if rec := get(PathUIHello, m.token(t, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("screens without UI: want 404, got %d", rec.Code)
	}
}
