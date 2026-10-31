package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const bodyV1 = `{"blocklist":[{"id":"hello","reason":"Extension suspended","since":"2026-10-17T04:53:09Z"}],"extensions":[{"id":"mesh-ext-hello","name":"Hello","latest":{"version":"1.0.0","mesh_api":">=1 <3","commit_sha":"abc","artifact_sha256":"deadbeef"}}],"generated_at":"2026-10-18T14:15:05Z","schema":1}`

func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(priv ed25519.PrivateKey, body string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(body)))
}

// The signature header captured from the live Spark catalog (E4.6) verifies
// against the pinned production key; this guards the pinned constant itself.
func TestVerify_LiveSparkCapture(t *testing.T) {
	const body = `{"blocklist":[{"id":"hello","reason":"Extension suspended","since":"2026-10-17T04:53:09.485971Z"},{"id":"pubcheck-e48","reason":"Extension suspended","since":"2026-10-17T04:53:09.485971Z"}],"extensions":[],"generated_at":"2026-10-18T14:15:05.697932Z","schema":1}`
	const sig = "5rjQnGrsd9g7MOF8vghMLrG3ibN/CcizewvmouBcHRe7yGlBr28ityrnrDDHKlkVQgxGwyo2Zw452HZwfSs7AA=="
	key, err := ParseKey(PinnedPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := Verify(key, []byte(body), sig)
	if err != nil {
		t.Fatalf("live capture must verify: %v", err)
	}
	if _, ok := cat.Blocks("hello", "1.0.0"); !ok {
		t.Fatal("hello must be blocked for every version")
	}
	if ETagOf([]byte(body)) != `"5194695ae8d17d9a1dd2c0a310acb25c316f969c1232d8f5ad65546cd5b1358c"` {
		t.Fatal("ETag is not the quoted sha256 of the body")
	}
}

func TestVerify_RejectsForgeries(t *testing.T) {
	pub, priv := keypair(t)
	_, evil := keypair(t)
	good := sign(priv, bodyV1)
	if _, err := Verify(pub, []byte(bodyV1), good); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	cases := map[string]struct {
		body string
		sig  string
		want error
	}{
		"foreign key":   {bodyV1, sign(evil, bodyV1), ErrBadSignature},
		"tampered body": {bodyV1 + " ", good, ErrBadSignature},
		"no signature":  {bodyV1, "", ErrNoSignature},
		"garbage":       {bodyV1, "not-base64!!", ErrBadSignature},
		"short":         {bodyV1, base64.StdEncoding.EncodeToString([]byte("short")), ErrBadSignature},
	}
	for name, tc := range cases {
		if _, err := Verify(pub, []byte(tc.body), tc.sig); err != tc.want {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestParseKey(t *testing.T) {
	if _, err := ParseKey("zzz"); err != ErrBadKey {
		t.Fatalf("got %v", err)
	}
	if _, err := ParseKey(base64.StdEncoding.EncodeToString([]byte("short"))); err != ErrBadKey {
		t.Fatalf("got %v", err)
	}
}

type fakeSpark struct {
	body, sig string
	status    int
	hits, inm int32
}

func (f *fakeSpark) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&f.hits, 1)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	et := ETagOf([]byte(f.body))
	if r.Header.Get("If-None-Match") == et {
		atomic.AddInt32(&f.inm, 1)
		w.Header().Set("ETag", et)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", et)
	w.Header().Set(SignatureHeader, f.sig)
	_, _ = w.Write([]byte(f.body))
}

func newTestClient(srv *httptest.Server, pub ed25519.PublicKey) *Client {
	k := base64.StdEncoding.EncodeToString(pub)
	return NewClient(func() string { return srv.URL }, func() string { return k })
}

// Red control: a catalog signed by someone else is refused and the client
// keeps living on the previous verified copy.
func TestRefresh_ForgedCatalogKeepsPreviousCopy(t *testing.T) {
	pub, priv := keypair(t)
	_, evil := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := newTestClient(srv, pub)
	ctx := context.Background()

	if err := c.Refresh(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.body = `{"blocklist":[],"extensions":[],"generated_at":"x","schema":1}`
	f.sig = sign(evil, f.body)
	if err := c.Refresh(ctx, 1); err != ErrBadSignature {
		t.Fatalf("forged catalog must be refused with ErrBadSignature, got %v", err)
	}
	cat, err := c.Current()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Blocks("hello", "1"); !ok || len(cat.Extensions) != 1 {
		t.Fatal("previous verified copy was replaced by the forged one")
	}
	if _, lastErr := c.Status(); lastErr != ErrBadSignature {
		t.Fatalf("status must expose the failure, got %v", lastErr)
	}
}

func TestRefresh_ETagAnd304AndOutage(t *testing.T) {
	pub, priv := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := newTestClient(srv, pub)
	ctx := context.Background()

	if _, err := c.Current(); err != ErrNoCatalog {
		t.Fatalf("empty client: %v", err)
	}
	if err := c.Refresh(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if f.inm != 1 {
		t.Fatalf("second refresh must send If-None-Match and get 304, inm=%d", f.inm)
	}
	f.status = http.StatusBadGateway
	if err := c.Refresh(ctx, 1); err == nil {
		t.Fatal("outage must surface as an error")
	}
	if cat, err := c.Current(); err != nil || len(cat.Extensions) != 1 {
		t.Fatalf("outage must keep the copy: %v", err)
	}
}

func TestRefresh_NoPinnedFallbackWhenKeyMismatch(t *testing.T) {
	_, priv := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	// Empty key setting falls back to the pinned production key, which this
	// test server did not sign with.
	c := NewClient(func() string { return srv.URL }, func() string { return "" })
	if err := c.Refresh(context.Background(), 1); err != ErrBadSignature {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Current(); err != ErrNoCatalog {
		t.Fatal("nothing verified, so no copy")
	}
}

func TestBlocks_VersionScope(t *testing.T) {
	c := &Catalog{Blocklist: []Block{{ID: "a", Version: "1.0.0", Reason: "r"}, {ID: "b", Reason: "all"}}}
	if _, ok := c.Blocks("a", "1.0.0"); !ok {
		t.Fatal("a@1.0.0 must be blocked")
	}
	if _, ok := c.Blocks("a", "1.1.0"); ok {
		t.Fatal("a@1.1.0 must not be blocked")
	}
	if b, ok := c.Blocks("b", "9.9.9"); !ok || b.Reason != "all" {
		t.Fatal("b must be blocked for every version")
	}
}

// Spark publishes capabilities as an object, not a list. A decode failure here
// would reject the whole catalog as soon as one extension is published.
func TestVerify_CapabilitiesObjectDecodes(t *testing.T) {
	pub, priv := keypair(t)
	body := `{"blocklist":[],"extensions":[{"id":"hello","latest":{"version":"1.0.0","mesh_api":">=1","commit_sha":"c","artifact_sha256":null,` +
		`"capabilities":{"api":["tasks:read"],"events":["task.updated"],"webhooks_in":[],"ui":[],"secrets":[]}},"releases":[]}],"generated_at":"x","schema":1}`
	cat, err := Verify(pub, []byte(body), sign(priv, body))
	if err != nil {
		t.Fatalf("catalog with an object of capabilities must decode: %v", err)
	}
	got := cat.Extensions[0].Latest.Flatten()
	if len(got) != 2 || got[0] != "api:tasks:read" || got[1] != "events:task.updated" {
		t.Fatalf("flatten: %v", got)
	}
	if (Release{}).Flatten() != nil || (Release{Capabilities: []byte(`[1]`)}).Flatten() != nil {
		t.Fatal("unreadable capabilities must flatten to nil")
	}
}

// Spark's capabilities object also carries "network", an object rather than a
// list of strings. It must not hide the permission groups next to it: an update
// that adds an api capability has to ask for consent.
func TestReleaseFlatten_IgnoresNonListGroups(t *testing.T) {
	r := Release{Capabilities: []byte(`{"api":["tasks:read","ext_token:self_rotate"],"events":["task.updated"],` +
		`"network":{"allowed_domains":[],"reasoning":"talks only to this Mesh instance"}}`)}
	got := r.Flatten()
	want := []string{"api:tasks:read", "api:ext_token:self_rotate", "events:task.updated"}
	if len(got) != len(want) {
		t.Fatalf("flatten: %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("flatten: %v, want %v", got, want)
		}
	}
	if (Release{Capabilities: []byte(`{"api":"tasks:read"}`)}).Flatten() != nil {
		t.Fatal("a permission group that is not a list of strings must flatten to nil")
	}
	// null is an empty group: dropping the other groups would hide a new
	// capability from the consent check.
	got = (Release{Capabilities: []byte(`{"api":null,"events":["task.updated"]}`)}).Flatten()
	if len(got) != 1 || got[0] != "events:task.updated" {
		t.Fatalf("null group: %v, want [events:task.updated]", got)
	}
}

func TestClientDetail_MapsSparkCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mesh-extensions/mesh-ext-hello" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"mesh-ext-hello","name":"Hello","readme":"# Hi","installs":3,"releases":[
			{"version":"1.1.0","mesh_api":"1","checks":{"schema":{"status":"pass"},"readme":{"status":"fail"}},"capabilities_delta":{"added":{"events":["task.created"]},"removed":{}},"yanked":false,"block_reason":null},
			{"version":"1.0.0","mesh_api":"1","checks":{},"capabilities_delta":{},"yanked":true,"block_reason":"bad"}]}`))
	}))
	defer srv.Close()
	c := NewClient(func() string { return srv.URL }, nil)
	card, err := c.Detail(context.Background(), "mesh-ext-hello")
	if err != nil {
		t.Fatal(err)
	}
	if card.Readme != "# Hi" || len(card.Versions) != 2 {
		t.Fatalf("card = %+v", card)
	}
	if got := card.Versions[0].CapabilitiesAdded; len(got) != 1 || got[0] != "events:task.created" {
		t.Fatalf("added = %v", got)
	}
	if !card.Versions[1].Yanked || !card.Versions[1].Blocked {
		t.Fatalf("version 1.0.0 flags = %+v", card.Versions[1])
	}
	if card.Checks == nil || card.Checks.Result != "failed" {
		t.Fatalf("checks = %+v", card.Checks)
	}
	if _, err = c.Detail(context.Background(), "nope"); err == nil {
		t.Fatal("unknown slug must error")
	}
}

// A compressing proxy in front of Spark labels the ETag `"<sha>-gzip"`. The
// client must still accept the catalog, and keep refusing a real mismatch.
func TestRefresh_GzipSuffixedETag(t *testing.T) {
	pub, priv := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	var gotEnc string
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEnc = r.Header.Get("Accept-Encoding")
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, r)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		if et := rec.Header().Get("ETag"); et != "" {
			w.Header().Set("ETag", et[:len(et)-1]+`-gzip"`)
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
	srv := httptest.NewServer(proxy)
	defer srv.Close()
	c := newTestClient(srv, pub)
	if err := c.Refresh(context.Background(), 1); err != nil {
		t.Fatalf("gzip-suffixed ETag must be accepted: %v", err)
	}
	if gotEnc != "identity" {
		t.Fatalf("client must request identity encoding, sent %q", gotEnc)
	}
	if got := normalizeETag(`"abc"`); got != `"abc"` {
		t.Fatalf("plain ETag changed: %s", got)
	}
	if got := normalizeETag(`W/"abc-gzip"`); got != `"abc"` {
		t.Fatalf("weak gzip ETag: %s", got)
	}
}

func TestRefresh_RealETagMismatchStillRefused(t *testing.T) {
	pub, priv := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"deadbeef"`)
		w.Header().Set(SignatureHeader, f.sig)
		_, _ = w.Write([]byte(f.body))
	}))
	defer bad.Close()
	c := newTestClient(bad, pub)
	if err := c.Refresh(context.Background(), 1); err == nil {
		t.Fatal("a wrong ETag must still be refused")
	}
}

// RefreshNow reports the identity of the copy it holds: a newer signed body
// replaces the cache, a forged body or an outage leaves the last-good receipt.
func TestRefreshNow_ReceiptAndLastGood(t *testing.T) {
	pub, priv := keypair(t)
	_, evil := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := newTestClient(srv, pub)
	ctx := context.Background()

	if _, err := c.Receipt(); err != ErrNoCatalog {
		t.Fatalf("empty cache must say ErrNoCatalog, got %v", err)
	}
	r1, err := c.RefreshNow(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantSHA := strings.Trim(ETagOf([]byte(bodyV1)), `"`)
	if r1.ContentSHA256 != wantSHA || r1.FetchedAt.IsZero() || r1.Signature != f.sig {
		t.Fatalf("receipt does not identify the accepted body: %+v", r1)
	}
	if len(r1.Extensions) != 1 || r1.Extensions[0].Version != "1.0.0" || r1.Extensions[0].CommitSHA != "abc" ||
		r1.Extensions[0].ArtifactSHA256 == nil || *r1.Extensions[0].ArtifactSHA256 != "deadbeef" {
		t.Fatalf("receipt versions: %+v", r1.Extensions)
	}

	// upstream moves to a newer release: the forced refresh picks it up now
	bodyV2 := strings.Replace(bodyV1, `"1.0.0"`, `"1.1.0"`, 1)
	f.body, f.sig = bodyV2, sign(priv, bodyV2)
	r2, err := c.RefreshNow(ctx, 1)
	if err != nil || r2.Extensions[0].Version != "1.1.0" || r2.ContentSHA256 == r1.ContentSHA256 {
		t.Fatalf("newer catalog not taken: %+v %v", r2, err)
	}

	// forged body: refused, receipt is still the last-good v2
	forged := strings.Replace(bodyV1, `"1.0.0"`, `"9.9.9"`, 1)
	f.body, f.sig = forged, sign(evil, forged)
	r3, err := c.RefreshNow(ctx, 1)
	if err != ErrBadSignature {
		t.Fatalf("forged catalog must give ErrBadSignature, got %v", err)
	}
	if r3.ContentSHA256 != r2.ContentSHA256 || r3.Extensions[0].Version != "1.1.0" {
		t.Fatalf("last-good was lost on a bad signature: %+v", r3)
	}

	// outage: same
	f.status = http.StatusBadGateway
	r4, err := c.RefreshNow(ctx, 1)
	if err == nil || r4.ContentSHA256 != r2.ContentSHA256 {
		t.Fatalf("outage must keep last-good and report the error: %+v %v", r4, err)
	}
}

// A forced refresh must not run OnRefresh (it changes installed extensions);
// the next regular refresh still delivers the catalog it accepted.
func TestRefreshNow_DefersOnRefreshToRegularRefresh(t *testing.T) {
	pub, priv := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := newTestClient(srv, pub)
	var seen []string
	c.OnRefresh = func(_ context.Context, cat *Catalog) { seen = append(seen, cat.Extensions[0].Latest.Version) }
	ctx := context.Background()

	if _, err := c.RefreshNow(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("forced refresh ran OnRefresh: %v", seen)
	}
	if err := c.Refresh(ctx, 1); err != nil { // 304: body unchanged
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "1.0.0" {
		t.Fatalf("regular refresh must deliver the owed catalog once, got %v", seen)
	}
	if err := c.Refresh(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("catalog delivered twice: %v", seen)
	}
}

// Regular and forced refreshes share one lock: while a forced refresh is in
// flight a regular one waits, so a stale response cannot land after a newer one.
func TestRefresh_ForcedAndRegularNeverOverlap(t *testing.T) {
	pub, priv := keypair(t)
	var inflight, maxInflight int32
	body := bodyV1
	sig := sign(priv, body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			m := atomic.LoadInt32(&maxInflight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInflight, m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("ETag", ETagOf([]byte(body)))
		w.Header().Set(SignatureHeader, sig)
		_, _ = w.Write([]byte(body))
		atomic.AddInt32(&inflight, -1)
	}))
	defer srv.Close()
	c := newTestClient(srv, pub)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = c.RefreshNow(ctx, 1)
			} else {
				_ = c.Refresh(ctx, 1)
			}
		}(i)
	}
	wg.Wait()
	if m := atomic.LoadInt32(&maxInflight); m != 1 {
		t.Fatalf("refreshes overlapped: %d in flight at once", m)
	}
}
