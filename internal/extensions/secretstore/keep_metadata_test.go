package secretstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// metaFixture is an HTTPS stand-in for Keep at the external Mesh->Keep
// boundary only. It re-derives the signature from the wire request with its own
// code, so a client that signs something other than the contract fails here.
type metaFixture struct {
	t     *testing.T
	org   uuid.UUID
	ws    uuid.UUID
	key   []byte
	calls atomic.Int32
	// reply writes the answer once the request has been verified.
	reply func(w http.ResponseWriter, r *http.Request)
	seen  []string // nonces
}

func newMetaFixture(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) (*HTTPKeepClient, *metaFixture) {
	t.Helper()
	f := &metaFixture{t: t, org: uuid.New(), ws: uuid.New(), key: []byte("test-events-key"), reply: reply}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	return &HTTPKeepClient{
		BaseURL: srv.URL, OrgID: f.org, WorkspaceID: f.ws, EventsSecret: f.key,
		HTTP: srv.Client(),
	}, f
}

func (f *metaFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		http.Error(w, "shape", http.StatusBadRequest)
		return
	}
	if r.Header.Get("X-Mesh-Workspace") != f.ws.String() || r.Header.Get("Accept") != "application/json" {
		http.Error(w, "headers", http.StatusUnauthorized)
		return
	}
	ts, nonce := r.Header.Get("X-Mesh-Timestamp"), r.Header.Get("X-Mesh-Nonce")
	mac := hmac.New(sha256.New, f.key)
	mac.Write([]byte("keep-mesh-secret-v1\n" + ts + "\n" + nonce + "\nGET\n" + r.URL.EscapedPath() + "\n" +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"))
	if r.Header.Get("X-Mesh-Signature") != "v1="+hex.EncodeToString(mac.Sum(nil)) {
		http.Error(w, "sig", http.StatusUnauthorized)
		return
	}
	for _, n := range f.seen {
		if n == nonce {
			http.Error(w, "replay", http.StatusUnauthorized)
			return
		}
	}
	f.seen = append(f.seen, nonce)
	f.reply(w, r)
}

func jsonReply(status int, cache, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if cache != "" {
			w.Header().Set("Cache-Control", cache)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const goodMeta = `{"version":"7","length":42,"request_id":"keep-req.1"}`

func TestMetadata_Positive(t *testing.T) {
	c, f := newMetaFixture(t, jsonReply(200, "no-store", goodMeta))
	got, err := c.Metadata(context.Background(), "mesh-secret.workspace.workspace.TOKEN."+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if got != (KeepMetadata{Version: "7", Length: 42}) {
		t.Fatalf("got %+v", got)
	}
	// A second call spends a fresh nonce: the fixture rejects a replay with 401.
	if _, err := c.Metadata(context.Background(), "abc"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("calls=%d, want exactly 2 (no retry)", f.calls.Load())
	}
}

func TestMetadata_VersionBounds(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{`"1"`, true},
		{`"9223372036854775807"`, true},
		{`"9223372036854775808"`, false},
		{`"0"`, false},
		{`"01"`, false},
		{`"-1"`, false},
		{`"1.5"`, false},
		{`""`, false},
		{`7`, false}, // JSON number instead of string
		{`null`, false},
		{`true`, false},
	} {
		c, _ := newMetaFixture(t, jsonReply(200, "no-store", `{"version":`+tc.version+`,"length":1,"request_id":"r"}`))
		_, err := c.Metadata(context.Background(), "abc")
		if (err == nil) != tc.ok {
			t.Errorf("version %s: err=%v, want ok=%v", tc.version, err, tc.ok)
		}
	}
}

func TestMetadata_LengthBounds(t *testing.T) {
	for _, tc := range []struct {
		length string
		ok     bool
	}{
		{`1`, true},
		{`32768`, true},
		{`0`, false},
		{`32769`, false},
		{`-1`, false},
		{`1.0`, false},
		{`1e2`, false},
		{`"5"`, false},
		{`true`, false},
		{`null`, false},
		{`007`, false},
		{`100000`, false},
	} {
		c, _ := newMetaFixture(t, jsonReply(200, "no-store", `{"version":"1","length":`+tc.length+`,"request_id":"r"}`))
		_, err := c.Metadata(context.Background(), "abc")
		if (err == nil) != tc.ok {
			t.Errorf("length %s: err=%v, want ok=%v", tc.length, err, tc.ok)
		}
	}
}

func TestMetadata_ClosedSchema(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":   `{"version":"1","length":1,"request_id":"r","extra":1}`,
		"missing field":   `{"version":"1","length":1}`,
		"duplicate key":   `{"version":"1","version":"2","length":1,"request_id":"r"}`,
		"trailing json":   goodMeta + `{}`,
		"trailing text":   goodMeta + ` x`,
		"array":           `[` + goodMeta + `]`,
		"null":            `null`,
		"empty":           ``,
		"bad request id":  `{"version":"1","length":1,"request_id":"a b"}`,
		"long request id": `{"version":"1","length":1,"request_id":"` + strings.Repeat("a", 129) + `"}`,
		"request id num":  `{"version":"1","length":1,"request_id":5}`,
		"value leaked":    `{"version":"1","length":1,"request_id":"r","value":"s3cret"}`,
	} {
		c, _ := newMetaFixture(t, jsonReply(200, "no-store", body))
		_, err := c.Metadata(context.Background(), "abc")
		if !errors.Is(err, ErrKeepMetadataUnavailable) || !errors.Is(err, ErrKeepMetadataSchema) {
			t.Errorf("%s: err=%v, want schema/unavailable", name, err)
		}
	}
}

func TestMetadata_Oversize4096PlusEOF(t *testing.T) {
	pad := strings.Repeat("a", 4096)
	// Exactly 4096 bytes of valid JSON passes; 4097 does not.
	base := `{"version":"1","length":1,"request_id":"r"}`
	space := 4096 - len(base)
	ok := base + strings.Repeat(" ", space)
	if len(ok) != 4096 {
		t.Fatal("fixture miscounted")
	}
	c, _ := newMetaFixture(t, jsonReply(200, "no-store", ok))
	if _, err := c.Metadata(context.Background(), "abc"); err != nil {
		t.Fatalf("4096 bytes: %v", err)
	}
	c, _ = newMetaFixture(t, jsonReply(200, "no-store", ok+" "))
	if _, err := c.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepMetadataSchema) {
		t.Fatalf("4097 bytes: %v", err)
	}
	c, _ = newMetaFixture(t, jsonReply(200, "no-store", `{"version":"1","length":1,"request_id":"r"}`+pad))
	if _, err := c.Metadata(context.Background(), "abc"); err == nil {
		t.Fatal("oversize with trailing junk accepted")
	}
}

func TestMetadata_CacheAndContentType(t *testing.T) {
	for name, cache := range map[string]string{"missing": "", "public": "public, max-age=60", "substring": "x-no-store-ish"} {
		c, _ := newMetaFixture(t, jsonReply(200, cache, goodMeta))
		if _, err := c.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepCache) || !errors.Is(err, ErrKeepMetadataUnavailable) {
			t.Errorf("cache %s: %v", name, err)
		}
	}
	c, _ := newMetaFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(goodMeta))
	})
	if _, err := c.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepMetadataUnavailable) {
		t.Errorf("content type: %v", err)
	}
	c, _ = newMetaFixture(t, jsonReply(200, "private, NO-STORE", goodMeta))
	if _, err := c.Metadata(context.Background(), "abc"); err != nil {
		t.Errorf("no-store among directives: %v", err)
	}
}

func TestMetadata_StatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{404, ErrKeepMetadataUnavailable}, // fixture body is not the not_found envelope
		{429, ErrKeepRateLimited},
		{401, ErrKeepMetadataUnavailable},
		{403, ErrKeepMetadataUnavailable},
		{400, ErrKeepMetadataUnavailable},
		{500, ErrKeepMetadataUnavailable},
		{503, ErrKeepMetadataUnavailable},
		{204, ErrKeepMetadataUnavailable},
	} {
		c, _ := newMetaFixture(t, jsonReply(tc.status, "no-store", `{"error_class":"x","message":"raw upstream reason","request_id":"r"}`))
		_, err := c.Metadata(context.Background(), "abc")
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: %v, want %v", tc.status, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "raw upstream reason") {
			t.Errorf("status %d: upstream text leaked into the error", tc.status)
		}
	}
}

func TestMetadata_RedirectNotFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		jsonReply(200, "no-store", goodMeta)(w, nil)
	}))
	defer target.Close()
	c, _ := newMetaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusTemporaryRedirect)
	})
	if _, err := c.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepMetadataUnavailable) {
		t.Fatalf("redirect: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("redirect target was called with the signed request")
	}
}

func TestMetadata_Timeout(t *testing.T) {
	c, _ := newMetaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	hc := *c.HTTP
	hc.Timeout = 100 * time.Millisecond
	c.HTTP = &hc
	start := time.Now()
	if _, err := c.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepMetadataUnavailable) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not honoured")
	}
}

// An injected client without its own Timeout must not lift the call's bound.
func TestMetadata_TimeoutWithUnboundedClient(t *testing.T) {
	old := keepMetadataTimeout
	keepMetadataTimeout = 100 * time.Millisecond
	t.Cleanup(func() { keepMetadataTimeout = old })
	c, _ := newMetaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	hc := *c.HTTP
	hc.Timeout = 0
	c.HTTP = &hc
	start := time.Now()
	if _, err := c.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepMetadataUnavailable) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("client without Timeout lifted the call bound")
	}
}

func TestMetadata_RefAndTransportGuards(t *testing.T) {
	c, f := newMetaFixture(t, jsonReply(200, "no-store", goodMeta))
	for _, bad := range []string{"", "../x", "a/b", "a..b", "-a", ".a", "a%2fb", "a?x=1", strings.Repeat("a", 129)} {
		if _, err := c.Metadata(context.Background(), bad); !errors.Is(err, ErrKeepBadID) {
			t.Errorf("ref %q: %v", bad, err)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("a malformed ref reached Keep")
	}
	plain := &HTTPKeepClient{BaseURL: "http://keep.example", OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("k")}
	if _, err := plain.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepNotTLS) {
		t.Fatalf("http base: %v", err)
	}
}

// The probe must reach only the metadata path: a client whose answer would be a
// value is not something this method can produce.
func TestMetadata_UsesMetadataPathOnly(t *testing.T) {
	var path string
	c, _ := newMetaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		jsonReply(200, "no-store", goodMeta)(w, r)
	})
	if _, err := c.Metadata(context.Background(), "ref-1"); err != nil {
		t.Fatal(err)
	}
	if want := "/orgs/" + c.OrgID.String() + "/mesh/secrets/ref-1/metadata"; path != want {
		t.Fatalf("path %q, want %q", path, want)
	}
}

var _ KeepMetadataReader = (*HTTPKeepClient)(nil)

const keepNotFoundBody = `{"error_class":"not_found","message":"not found","request_id":"req-1"}`

// Old Keep without the metadata route answers a bare 404: that is 503 in Mesh,
// not "secret not found".
func TestReviewOldKeepMissingEndpoint(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"http.NotFound":         func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"json other class":      jsonReply(404, "no-store", `{"error_class":"invalid_argument","message":"not found","request_id":"r"}`),
		"json other message":    jsonReply(404, "no-store", `{"error_class":"not_found","message":"nope","request_id":"r"}`),
		"extra field":           jsonReply(404, "no-store", `{"error_class":"not_found","message":"not found","request_id":"r","x":1}`),
		"duplicate error_class": jsonReply(404, "no-store", `{"error_class":"invalid_argument","error_class":"not_found","message":"not found","request_id":"r"}`),
		"duplicate message":     jsonReply(404, "no-store", `{"error_class":"not_found","message":"x","message":"not found","request_id":"r"}`),
		"non-string value":      jsonReply(404, "no-store", `{"error_class":"not_found","message":"not found","request_id":7}`),
		"missing request_id":    jsonReply(404, "no-store", `{"error_class":"not_found","message":"not found"}`),
		"trailing":              jsonReply(404, "no-store", keepNotFoundBody+`{}`),
		"empty":                 jsonReply(404, "no-store", ``),
	} {
		c, _ := newMetaFixture(t, h)
		_, err := c.Metadata(context.Background(), "abc")
		if errors.Is(err, ErrKeepNotFound) || !errors.Is(err, ErrKeepMetadataUnavailable) {
			t.Errorf("%s: got %v, want ErrKeepMetadataUnavailable only", name, err)
		}
	}
	// positive control: the compatible closed 404 stays ErrKeepNotFound.
	c, _ := newMetaFixture(t, jsonReply(404, "no-store", keepNotFoundBody))
	if _, err := c.Metadata(context.Background(), "abc"); !errors.Is(err, ErrKeepNotFound) {
		t.Errorf("compatible 404: %v, want ErrKeepNotFound", err)
	}
}
