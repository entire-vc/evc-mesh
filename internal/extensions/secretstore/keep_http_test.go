package secretstore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// contractKeep is an independent reading of the Keep K5.9 contract (evc-keep
// docs/mesh-extension.md): it verifies the signature with its own code, spends
// nonces, enforces org ownership and namespaces refs per workspace, answers
// no-store, and journals calls without the value.
type contractKeep struct {
	mu      sync.Mutex
	org     uuid.UUID            // organisation that owns the installations
	secrets map[uuid.UUID][]byte // workspace -> events secret (a missing entry = revoked)
	vals    map[string]string    // workspace/ref -> value
	nonces  map[string]bool
	audit   []string
	noStore bool
	now     func() time.Time
	// putVersion is what PUT answers as "version"; Keep sends a string
	// (evc-keep MeshSecretPutResult.Version), a nil one leaves it out.
	putVersion any
}

func newContractKeep(t *testing.T, ws uuid.UUID, key []byte) (*contractKeep, *httptest.Server) {
	t.Helper()
	k := &contractKeep{
		org: uuid.New(), secrets: map[uuid.UUID][]byte{ws: key}, vals: map[string]string{},
		nonces: map[string]bool{}, noStore: true, now: time.Now, putVersion: "1",
	}
	srv := httptest.NewTLSServer(k)
	t.Cleanup(srv.Close)
	return k, srv
}

func (k *contractKeep) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	deny := func() { http.Error(w, `{"error":"authentication failed"}`, http.StatusUnauthorized) }
	body, _ := io.ReadAll(r.Body)
	ws, err := uuid.Parse(r.Header.Get("X-Mesh-Workspace"))
	if err != nil {
		deny()
		return
	}
	tsRaw := r.Header.Get("X-Mesh-Timestamp")
	secs, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		deny()
		return
	}
	if d := k.now().Sub(time.Unix(secs, 0)); d > 5*time.Minute || d < -5*time.Minute {
		deny()
		return
	}
	nonce := r.Header.Get("X-Mesh-Nonce")
	sig, ok := strings.CutPrefix(r.Header.Get("X-Mesh-Signature"), "v1=")
	got, derr := hex.DecodeString(sig)
	key, known := k.secrets[ws]
	if !ok || derr != nil || !known || len(nonce) < 16 || len(nonce) > 64 {
		deny()
		return
	}
	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "keep-mesh-secret-v1\n%s\n%s\n%s\n%s\n%s", tsRaw, nonce, r.Method, r.URL.EscapedPath(), hex.EncodeToString(sum[:]))
	if !hmac.Equal(mac.Sum(nil), got) || k.nonces[ws.String()+nonce] {
		deny()
		return
	}
	k.nonces[ws.String()+nonce] = true

	// /orgs/{org}/mesh/secrets/{ref}[/resolve]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "orgs" || parts[2] != "mesh" || parts[3] != "secrets" || len(parts) < 5 {
		http.NotFound(w, r)
		return
	}
	if parts[1] != k.org.String() {
		k.audit = append(k.audit, "refused ws="+ws.String())
		http.Error(w, `{"error":"request refused"}`, http.StatusForbidden)
		return
	}
	ref := parts[4]
	slot := ws.String() + "/" + ref
	if k.noStore {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPut && len(parts) == 5:
		var in struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(body, &in) != nil || in.Value == "" {
			http.Error(w, `{"error":"invalid secret request"}`, http.StatusBadRequest)
			return
		}
		k.vals[slot] = in.Value
		fp := sha256.Sum256([]byte(in.Value))
		k.audit = append(k.audit, fmt.Sprintf("put ws=%s ref=%s len=%d fp=%x", ws, ref, len(in.Value), fp[:4]))
		answer := map[string]any{"ref": ref, "fingerprint": hex.EncodeToString(fp[:4])}
		if k.putVersion != nil {
			answer["version"] = k.putVersion
		}
		_ = json.NewEncoder(w).Encode(answer)
	case r.Method == http.MethodPost && len(parts) == 6 && parts[5] == "resolve" && len(body) == 0:
		v, found := k.vals[slot]
		if !found {
			k.audit = append(k.audit, "resolve-miss ws="+ws.String()+" ref="+ref)
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		k.audit = append(k.audit, "resolve ws="+ws.String()+" ref="+ref)
		_ = json.NewEncoder(w).Encode(map[string]any{"value": v, "version": 1})
	default:
		http.NotFound(w, r)
	}
}

// syntheticSecret is generated per run: never a fleet, Mesh or prod value.
func syntheticSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "synthetic-" + hex.EncodeToString(b)
}

func testKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func clientFor(k *contractKeep, srv *httptest.Server, ws uuid.UUID, key []byte) *HTTPKeepClient {
	return &HTTPKeepClient{BaseURL: srv.URL, OrgID: k.org, WorkspaceID: ws, EventsSecret: key, HTTP: srv.Client()}
}

var testRef = Ref{ExtensionID: "gitlab", Field: "token"}

// The leak check comes first and must be shown red on a deliberately bad
// input: a client that put the value into an error would fail it.
func TestNoLeakCheckCatchesALeak(t *testing.T) {
	secret := syntheticSecret(t)
	if !leaks(fmt.Errorf("keep: put failed for %s", secret), secret) {
		t.Fatal("red control: the leak check must flag an error that carries the value")
	}
}

func leaks(err error, secret string) bool { return err != nil && strings.Contains(err.Error(), secret) }

func TestKeepAuthenticatedRoundTrip(t *testing.T) {
	ws, key, secret := uuid.New(), testKey(t), syntheticSecret(t)
	k, srv := newContractKeep(t, ws, key)
	r := &Router{Builtin: Builtin{}, Keep: Keep{Client: clientFor(k, srv, ws, key)}, Selected: func(context.Context) (string, error) { return ProviderKeep, nil }}

	stored, err := r.Put(context.Background(), testRef, secret)
	if err != nil || stored != KeepPrefix+"gitlab.token" {
		t.Fatalf("put: %q %v", stored, err)
	}
	if strings.Contains(stored, secret) {
		t.Fatal("the settings row must hold a reference, not the value")
	}
	if got := r.Resolve(context.Background(), stored); got.Degraded || got.Value != secret {
		t.Fatalf("resolve must return the value just put: %+v", got)
	}
	for _, line := range k.audit {
		if strings.Contains(line, secret) {
			t.Fatalf("audit record carries the value: %q", line)
		}
	}
	if len(k.audit) != 2 {
		t.Fatalf("want one audit record per call, got %v", k.audit)
	}
}

// Keep answers PUT with a string version; an older Keep sent a number. Both
// are a stored version, an empty or missing one is not.
func TestKeepPutAcceptsStringOrNumberVersion(t *testing.T) {
	cases := []struct {
		name    string
		version any
		ok      bool
	}{
		{"string", "1", true},
		{"string-later", "17", true},
		{"number", 1, true},
		{"empty-string", "", false},
		{"zero", 0, false},
		{"negative", -1, false},
		{"fraction", 1.5, false},
		{"missing", nil, false},
		{"null", json.RawMessage("null"), false},
		{"bool", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, key, secret := uuid.New(), testKey(t), syntheticSecret(t)
			k, srv := newContractKeep(t, ws, key)
			k.putVersion = tc.version
			id, err := clientFor(k, srv, ws, key).PutValue(context.Background(), testRef, secret)
			if leaks(err, secret) {
				t.Fatalf("error carries the value: %v", err)
			}
			if tc.ok && (err != nil || id != "gitlab.token") {
				t.Fatalf("want ok: %q %v", id, err)
			}
			if !tc.ok && (err == nil || err.Error() != "keep: unexpected put answer") {
				t.Fatalf("want unexpected put answer, got %q %v", id, err)
			}
		})
	}
}

func TestKeepRefIsNamespacedPerWorkspace(t *testing.T) {
	ws, key := uuid.New(), testKey(t)
	k, srv := newContractKeep(t, ws, key)
	c := clientFor(k, srv, ws, key)
	id, err := c.PutValue(context.Background(), Ref{WorkspaceID: uuid.New(), ExtensionID: "gitlab", Field: "token"}, syntheticSecret(t))
	if err != nil || !strings.HasPrefix(id, "gitlab.token.") {
		t.Fatalf("workspace-level ref must carry the workspace: %q %v", id, err)
	}
}

func TestKeepRejectsSigningWithTheWrongKey(t *testing.T) {
	ws, key, secret := uuid.New(), testKey(t), syntheticSecret(t)
	k, srv := newContractKeep(t, ws, key)
	_, err := clientFor(k, srv, ws, testKey(t)).PutValue(context.Background(), testRef, secret)
	if err != ErrKeepUnauthorized || leaks(err, secret) {
		t.Fatalf("a bad signature is the uniform 401: %v", err)
	}
}

func TestKeepRevokedActorIsDegradedWithReason(t *testing.T) {
	ws, key, secret := uuid.New(), testKey(t), syntheticSecret(t)
	k, srv := newContractKeep(t, ws, key)
	c := clientFor(k, srv, ws, key)
	if _, err := c.PutValue(context.Background(), testRef, secret); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	delete(k.secrets, ws) // the installation is revoked
	k.mu.Unlock()
	got := Keep{Client: c}.Resolve(context.Background(), KeepPrefix+"gitlab.token")
	if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "revoked actor") {
		t.Fatalf("revoked actor must be degraded with a reason, never an empty value: %+v", got)
	}
}

func TestKeepOtherOrganisationIsRefused(t *testing.T) {
	ws, key, secret := uuid.New(), testKey(t), syntheticSecret(t)
	k, srv := newContractKeep(t, ws, key)
	c := clientFor(k, srv, ws, key)
	if _, err := c.PutValue(context.Background(), testRef, secret); err != nil {
		t.Fatal(err)
	}
	c.OrgID = uuid.New() // a foreign org in the path
	got := Keep{Client: c}.Resolve(context.Background(), KeepPrefix+"gitlab.token")
	if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "does not belong") {
		t.Fatalf("foreign org must be refused: %+v", got)
	}
}

func TestKeepWorkspaceCannotReadAnotherWorkspacesRef(t *testing.T) {
	wsA, keyA, secret := uuid.New(), testKey(t), syntheticSecret(t)
	k, srv := newContractKeep(t, wsA, keyA)
	wsB, keyB := uuid.New(), testKey(t)
	k.secrets[wsB] = keyB
	if _, err := clientFor(k, srv, wsA, keyA).PutValue(context.Background(), testRef, secret); err != nil {
		t.Fatal(err)
	}
	got := Keep{Client: clientFor(k, srv, wsB, keyB)}.Resolve(context.Background(), KeepPrefix+"gitlab.token")
	if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "not found") {
		t.Fatalf("namespace isolation: %+v", got)
	}
}

func TestKeepRequiresNoStore(t *testing.T) {
	ws, key, secret := uuid.New(), testKey(t), syntheticSecret(t)
	k, srv := newContractKeep(t, ws, key)
	c := clientFor(k, srv, ws, key)
	if _, err := c.PutValue(context.Background(), testRef, secret); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	k.noStore = false
	k.mu.Unlock()
	got := Keep{Client: c}.Resolve(context.Background(), KeepPrefix+"gitlab.token")
	if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "no-store") {
		t.Fatalf("an answer without no-store must not be used: %+v", got)
	}
}

func TestKeepSignatureIsSingleUseAndFresh(t *testing.T) {
	ws, key, secret := uuid.New(), testKey(t), syntheticSecret(t)
	k, srv := newContractKeep(t, ws, key)
	c := clientFor(k, srv, ws, key)
	c.Nonce = func() (string, error) { return "0123456789abcdef", nil }
	if _, err := c.PutValue(context.Background(), testRef, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutValue(context.Background(), testRef, secret); err != ErrKeepUnauthorized {
		t.Fatalf("a replayed nonce must be refused: %v", err)
	}
	c.Nonce = nil
	c.Now = func() time.Time { return time.Now().Add(-time.Hour) }
	if _, err := c.PutValue(context.Background(), testRef, secret); err != ErrKeepUnauthorized {
		t.Fatalf("a stale timestamp must be refused: %v", err)
	}
}

// Red control: Keep unreachable ends in a degraded Resolve with a reason.
func TestKeepUnavailableIsDegradedWithReason(t *testing.T) {
	ws, key := uuid.New(), testKey(t)
	k, srv := newContractKeep(t, ws, key)
	c := clientFor(k, srv, ws, key)
	srv.Close()
	got := Keep{Client: c}.Resolve(context.Background(), KeepPrefix+"gitlab.token")
	if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "keep unavailable") {
		t.Fatalf("down: %+v", got)
	}
}

func TestKeepStatusCodesAreDegraded(t *testing.T) {
	for code, want := range map[int]string{429: "rate limited", 503: "status 503"} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		c := &HTTPKeepClient{BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("k"), HTTP: srv.Client()}
		got := Keep{Client: c}.Resolve(context.Background(), KeepPrefix+"gitlab.token")
		srv.Close()
		if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, want) {
			t.Fatalf("%d: %+v", code, got)
		}
	}
}

func TestKeepRefusesPlainHTTP(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(srv.Close)
	c := &HTTPKeepClient{BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("k")}
	secret := syntheticSecret(t)
	if _, err := c.PutValue(context.Background(), testRef, secret); err != ErrKeepNotTLS || hit {
		t.Fatalf("value must not travel in clear: err=%v hit=%v", err, hit)
	}
}

// A stored value is never trusted into a request path: a traversal id must not
// leave the secret's own URL, and must not even reach Keep.
func TestHTTPKeepRejectsMalformedID(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(srv.Close)
	c := &HTTPKeepClient{BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("k"), HTTP: srv.Client()}
	for _, id := range []string{"../../tokens/x/revoke", "a/b", "", "..", "a..b", ".x", "-x"} {
		got := Keep{Client: c}.Resolve(context.Background(), KeepPrefix+id)
		if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "malformed") {
			t.Fatalf("%q: %+v", id, got)
		}
	}
	if hit {
		t.Fatal("a malformed id reached Keep")
	}
}

func TestHasNoStoreIsExactDirective(t *testing.T) {
	for v, want := range map[string]bool{
		"no-store": true, "private, No-Store": true, "no-cache,no-store, max-age=0": true,
		"public, no-storeish": false, "x-no-store": false, "": false, "no-cache": false,
	} {
		if hasNoStore(v) != want {
			t.Fatalf("%q: want %v", v, want)
		}
	}
}

// The request carries the secret and signed headers: a redirect must not be
// followed, even to another host.
func TestKeepDoesNotFollowRedirects(t *testing.T) {
	hit := false
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(other.Close)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", other.URL+"/redirected")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	c := &HTTPKeepClient{BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("k"), HTTP: srv.Client()}
	secret := syntheticSecret(t)
	_, err := c.PutValue(context.Background(), testRef, secret)
	if err == nil || hit || leaks(err, secret) {
		t.Fatalf("redirect must fail without reaching the target: err=%v hit=%v", err, hit)
	}
}

func TestKeepRefusesValueOutsideSizeLimitsBeforeSending(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(srv.Close)
	c := &HTTPKeepClient{BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("k"), HTTP: srv.Client()}
	for _, v := range []string{"", strings.Repeat("x", maxKeepValue+1)} {
		if _, err := c.PutValue(context.Background(), testRef, v); err != ErrKeepValueSize {
			t.Fatalf("len %d: %v", len(v), err)
		}
	}
	if hit {
		t.Fatal("an out-of-range value reached Keep")
	}
}
