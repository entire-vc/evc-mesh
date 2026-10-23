package secretstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const testSecretID = "0a1b2c3d-0000-4000-8000-000000000001"

func newFakeKeepServer(t *testing.T, org uuid.UUID, status int) (srv *httptest.Server, vals map[string]string) {
	t.Helper()
	vals = map[string]string{}
	mux := http.NewServeMux()
	base := "/orgs/" + org.String() + "/secrets"
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer keep_u_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		if status != 0 {
			w.WriteHeader(status)
			return false
		}
		return true
	}
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": testSecretID})
		}
	})
	mux.HandleFunc("PUT "+base+"/{id}/value", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		vals[r.PathValue("id")] = b["value"]
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "version": 1})
	})
	mux.HandleFunc("POST "+base+"/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		v, ok := vals[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": v, "version": 1})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, vals
}

func httpClient(url string, org uuid.UUID) *HTTPKeepClient {
	return &HTTPKeepClient{BaseURL: url, OrgID: org, Token: func(context.Context) (string, error) { return "keep_u_test", nil }}
}

func TestHTTPKeepRoundTripThroughRouter(t *testing.T) {
	org := uuid.New()
	srv, _ := newFakeKeepServer(t, org, 0)
	r := &Router{Builtin: Builtin{}, Keep: Keep{Client: httpClient(srv.URL, org)}, Selected: func(context.Context) string { return ProviderKeep }}
	st, err := r.Put(context.Background(), Ref{WorkspaceID: uuid.New(), ExtensionID: "gitlab", Field: "token"}, "glpat-1")
	if err != nil || st != KeepPrefix+testSecretID {
		t.Fatalf("put: %q %v", st, err)
	}
	if got := r.Resolve(context.Background(), st); got.Degraded || got.Value != "glpat-1" {
		t.Fatalf("resolve: %+v", got)
	}
	if got := r.Resolve(context.Background(), KeepPrefix+"0a1b2c3d-0000-4000-8000-0000000000ff"); !got.Degraded || !strings.Contains(got.Reason, "not found") {
		t.Fatalf("404 must be degraded with a reason: %+v", got)
	}
}

func TestHTTPKeepErrorCodesAreDegraded(t *testing.T) {
	org := uuid.New()
	for code, want := range map[int]string{403: "no scope", 503: "status 503"} {
		srv, _ := newFakeKeepServer(t, org, code)
		k := Keep{Client: httpClient(srv.URL, org)}
		got := k.Resolve(context.Background(), KeepPrefix+testSecretID)
		if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, want) {
			t.Fatalf("%d: %+v", code, got)
		}
	}
	// Keep not listening at all.
	srv, _ := newFakeKeepServer(t, org, 0)
	srv.Close()
	got := Keep{Client: httpClient(srv.URL, org)}.Resolve(context.Background(), KeepPrefix+testSecretID)
	if !got.Degraded || got.Value != "" {
		t.Fatalf("down: %+v", got)
	}
}

// A stored value is never trusted into a request path: a traversal id must not
// leave the secret's own URL, and must not even reach Keep.
func TestHTTPKeepRejectsMalformedID(t *testing.T) {
	org := uuid.New()
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(srv.Close)
	k := Keep{Client: httpClient(srv.URL, org)}
	for _, id := range []string{"../../tokens/x/revoke", "a/b", "", "sec-1"} {
		got := k.Resolve(context.Background(), KeepPrefix+id)
		if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "malformed") {
			t.Fatalf("%q: %+v", id, got)
		}
	}
	if hit {
		t.Fatal("a malformed id reached Keep")
	}
}
