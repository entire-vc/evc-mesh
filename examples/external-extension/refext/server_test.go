package refext

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests pin the receiver's own rules with hand-made requests. That the
// rules accept what Mesh really sends is proven by Mesh's tests that drive this
// package with the real senders (go test ./internal/... -run RefExt).

const ws = "7b0e1d6c-0000-4000-8000-000000000001"

var t0 = time.Unix(1_800_000_000, 0)

func newServer() *Server {
	return &Server{ExtensionID: "reference-ext", now: func() time.Time { return t0 }}
}

func signed(method, path string, body []byte, secret string, ts int64, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	tss := strconv.FormatInt(ts, 10)
	r.Header.Set(HeaderTimestamp, tss)
	r.Header.Set(HeaderSignature, Sign(secret, tss, body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func serve(s *Server, r *http.Request) int {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	return rec.Code
}

func install(s *Server, secret, code string) int {
	body, _ := json.Marshal(InstallRequest{ExtensionID: "reference-ext", WorkspaceID: ws, SigningSecret: secret, InstallCode: code})
	return serve(s, signed(http.MethodPost, PathInstall, body, secret, t0.Unix(), nil))
}

func TestInstall(t *testing.T) {
	s := newServer()
	if got := install(s, "s1", ""); got != http.StatusOK || !s.Installed(ws) {
		t.Fatalf("first install: %d", got)
	}
	if got := install(s, "s1", ""); got != http.StatusOK {
		t.Fatalf("repeat install must be a no-op, got %d", got)
	}
	// Without codes nobody can re-key a bound workspace: the request's own
	// secret signs it, so it proves nothing about the sender.
	if got := install(s, "attacker", ""); got != http.StatusForbidden {
		t.Fatalf("re-key without codes: want 403, got %d", got)
	}

	// A body signed with another secret than the one it carries is refused.
	body, _ := json.Marshal(InstallRequest{ExtensionID: "reference-ext", WorkspaceID: ws, SigningSecret: "s2"})
	if got := serve(s, signed(http.MethodPost, PathInstall, body, "other", t0.Unix(), nil)); got != http.StatusUnauthorized {
		t.Fatalf("bad signature: want 401, got %d", got)
	}
	// Another extension id is not ours.
	body, _ = json.Marshal(InstallRequest{ExtensionID: "x", WorkspaceID: ws, SigningSecret: "s2"})
	if got := serve(s, signed(http.MethodPost, PathInstall, body, "s2", t0.Unix(), nil)); got != http.StatusNotFound {
		t.Fatalf("foreign id: want 404, got %d", got)
	}
}

func TestInstall_CodeGuardsBindAndRekey(t *testing.T) {
	s := newServer()
	s.IssueInstallCode("BIND-1")
	if got := install(s, "s1", ""); got != http.StatusForbidden {
		t.Fatalf("no code: want 403, got %d", got)
	}
	if got := install(s, "s1", "BIND-2"); got != http.StatusForbidden {
		t.Fatalf("wrong code: want 403, got %d", got)
	}
	if got := install(s, "s1", "BIND-1"); got != http.StatusOK {
		t.Fatalf("right code: want 200, got %d", got)
	}
	// Mesh repeats the handshake with the same secret and the code it holds.
	if got := install(s, "s1", "BIND-1"); got != http.StatusOK {
		t.Fatalf("same secret again is a no-op, got %d", got)
	}
	if got := install(s, "attacker", ""); got != http.StatusForbidden {
		t.Fatalf("re-key without code: want 403, got %d", got)
	}
	// The code is spent: replaying it re-keys nothing.
	if got := install(s, "attacker", "BIND-1"); got != http.StatusForbidden {
		t.Fatalf("re-key with a used code: want 403, got %d", got)
	}
	// A fresh code re-keys once.
	s.IssueInstallCode("BIND-3")
	if got := install(s, "s2", "BIND-3"); got != http.StatusOK {
		t.Fatalf("re-key with a fresh code: want 200, got %d", got)
	}
	if got := install(s, "attacker", "BIND-3"); got != http.StatusForbidden {
		t.Fatalf("second re-key with the same code: want 403, got %d", got)
	}
	s.IssueInstallCode("") // an empty code is not a code
	if got := install(s, "attacker", ""); got != http.StatusForbidden {
		t.Fatalf("empty code issued: want 403, got %d", got)
	}
}

func token(s *Server, secret, tok string, ts int64) int {
	body, _ := json.Marshal(TokenRequest{ExtensionID: "reference-ext", WorkspaceID: ws, ExtensionToken: tok})
	return serve(s, signed(http.MethodPost, PathToken, body, secret, ts, nil))
}

func TestToken(t *testing.T) {
	s := newServer()
	if got := token(s, "s1", "ext_a", t0.Unix()); got != http.StatusUnauthorized {
		t.Fatalf("before install: want 401, got %d", got)
	}
	install(s, "s1", "")
	if got := token(s, "wrong", "ext_a", t0.Unix()); got != http.StatusUnauthorized || s.Token(ws) != "" {
		t.Fatalf("wrong secret: want 401 and no token, got %d", got)
	}
	if got := token(s, "s1", "ext_a", t0.Unix()); got != http.StatusOK || s.Token(ws) != "ext_a" {
		t.Fatalf("delivery: %d %q", got, s.Token(ws))
	}
	if got := token(s, "s1", "ext_old", t0.Unix()); got != http.StatusConflict || s.Token(ws) != "ext_a" {
		t.Fatalf("same timestamp is a replay: want 409, got %d", got)
	}
	if got := token(s, "s1", "ext_b", t0.Unix()+1); got != http.StatusOK || s.Token(ws) != "ext_b" {
		t.Fatalf("rotation: %d %q", got, s.Token(ws))
	}
}

func event(s *Server, secret, delivery string, ts int64) int {
	// The body carries the delivery id: Mesh retries a delivery with the same
	// body, and the receiver tells deliveries apart by the signed body.
	body := []byte(`{"task_id":"` + delivery + `","workspace_id":"` + ws + `"}`)
	return serve(s, signed(http.MethodPost, PathEvents, body, secret, ts,
		map[string]string{HeaderEvent: "task.created", HeaderDelivery: delivery}))
}

func TestEvents(t *testing.T) {
	s := newServer()
	var got []Event
	fail := false
	s.OnEvent = func(_ context.Context, ev Event) error {
		if fail {
			return errors.New("downstream down")
		}
		got = append(got, ev)
		return nil
	}
	if code := event(s, "s1", "d1", t0.Unix()); code != http.StatusUnauthorized {
		t.Fatalf("not installed: want 401, got %d", code)
	}
	install(s, "s1", "")

	if code := event(s, "wrong", "d1", t0.Unix()); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: want 401, got %d", code)
	}
	if code := event(s, "s1", "d1", t0.Add(-6*time.Minute).Unix()); code != http.StatusUnauthorized {
		t.Fatalf("stale timestamp: want 401, got %d", code)
	}
	if len(got) != 0 {
		t.Fatalf("refused requests reached OnEvent: %v", got)
	}

	fail = true
	if code := event(s, "s1", "d1", t0.Unix()); code != http.StatusInternalServerError {
		t.Fatalf("handler error: want 500 so Mesh retries, got %d", code)
	}
	fail = false
	if code := event(s, "s1", "d1", t0.Unix()); code != http.StatusNoContent {
		t.Fatalf("retry after failure is processed: %d", code)
	}
	if code := event(s, "s1", "d1", t0.Unix()); code != http.StatusOK {
		t.Fatalf("retry of a processed delivery is acknowledged: %d", code)
	}
	if code := event(s, "s1", "d2", t0.Unix()); code != http.StatusNoContent {
		t.Fatalf("new delivery: %d", code)
	}
	if len(got) != 2 || got[0].DeliveryID != "d1" || got[1].DeliveryID != "d2" || got[0].Name != "task.created" || got[0].WorkspaceID != ws {
		t.Fatalf("each delivery once, got %+v", got)
	}

	// Red control: the same signed body under a fresh, unsigned X-Mesh-Delivery
	// is a replay, not a new delivery.
	replay := signed(http.MethodPost, PathEvents, []byte(`{"task_id":"d1","workspace_id":"`+ws+`"}`), "s1", t0.Unix(),
		map[string]string{HeaderEvent: "task.created", HeaderDelivery: "forged"})
	if code := serve(s, replay); code != http.StatusOK || len(got) != 2 {
		t.Fatalf("replay under a new delivery header: want 200 and no new run, got %d, %d runs", code, len(got))
	}

	// No workspace_id: refused before any secret is looked up.
	r := signed(http.MethodPost, PathEvents, []byte(`{"task_id":"t1"}`), "s1", t0.Unix(),
		map[string]string{HeaderEvent: "task.created", HeaderDelivery: "d3"})
	if code := serve(s, r); code != http.StatusBadRequest {
		t.Fatalf("no workspace_id: want 400, got %d", code)
	}
}

func TestEvents_ProjectScope(t *testing.T) {
	s := newServer()
	install(s, "s1", "")
	var got []string
	s.OnEvent = func(_ context.Context, ev Event) error { got = append(got, ev.DeliveryID); return nil }
	s.ProjectAllowed = func(_, project string) bool { return project == "p-in" }

	send := func(delivery, body string) int {
		return serve(s, signed(http.MethodPost, PathEvents, []byte(body), "s1", t0.Unix(),
			map[string]string{HeaderEvent: "task.created", HeaderDelivery: delivery}))
	}
	if code := send("in", `{"project_id":"p-in","workspace_id":"`+ws+`"}`); code != http.StatusNoContent {
		t.Fatalf("in scope: %d", code)
	}
	if code := send("out", `{"project_id":"p-out","workspace_id":"`+ws+`"}`); code != http.StatusNoContent {
		t.Fatalf("out of scope is acknowledged, not retried: %d", code)
	}
	if code := send("ws", `{"user_id":"u1","workspace_id":"`+ws+`"}`); code != http.StatusNoContent {
		t.Fatalf("no project: %d", code)
	}
	if strings.Join(got, ",") != "in,ws" {
		t.Fatalf("handled %v, want in,ws", got)
	}
}

func TestServer_MalformedRequests(t *testing.T) {
	s := newServer()
	install(s, "s1", "")
	post := func(path, body string) int {
		return serve(s, signed(http.MethodPost, path, []byte(body), "s1", t0.Unix(), nil))
	}

	if got := serve(s, httptest.NewRequest(http.MethodGet, PathHealth, http.NoBody)); got != http.StatusOK {
		t.Fatalf("health: %d", got)
	}
	if got := serve(s, httptest.NewRequest(http.MethodGet, PathEvents, http.NoBody)); got != http.StatusNotFound {
		t.Fatalf("GET events: want 404, got %d", got)
	}
	if got := post(PathEvents, `{"workspace_id":"`+ws+`","pad":"`+strings.Repeat("x", maxBody)+`"}`); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: want 413, got %d", got)
	}
	for _, path := range []string{PathInstall, PathToken, PathEvents} {
		if got := post(path, `[`); got != http.StatusBadRequest {
			t.Errorf("%s with broken JSON: want 400, got %d", path, got)
		}
	}
	if got := post(PathToken, `{"extension_id":"x","workspace_id":"`+ws+`","extension_token":"ext_a"}`); got != http.StatusNotFound {
		t.Fatalf("token for a foreign id: want 404, got %d", got)
	}
	r := signed(http.MethodPost, PathEvents, []byte(`{"workspace_id":"`+ws+`"}`), "s1", t0.Unix(), map[string]string{HeaderEvent: "task.created"})
	if got := serve(s, r); got != http.StatusBadRequest {
		t.Fatalf("no delivery id: want 400, got %d", got)
	}
	if s.Token("other-ws") != "" || s.Installed("other-ws") {
		t.Fatal("unknown workspace reported as installed")
	}
}

func TestServer_Defaults(t *testing.T) {
	// Real clock and the default skew: a request stamped now passes, one
	// stamped beyond DefaultMaxSkew does not; a custom MaxSkew widens it.
	s := &Server{ExtensionID: "reference-ext"}
	install(s, "s1", "")
	now := time.Now()
	send := func(delivery string, ts time.Time) int {
		return serve(s, signed(http.MethodPost, PathEvents, []byte(`{"task_id":"`+delivery+`","workspace_id":"`+ws+`"}`), "s1", ts.Unix(),
			map[string]string{HeaderEvent: "task.created", HeaderDelivery: delivery}))
	}
	if got := send("now", now); got != http.StatusNoContent {
		t.Fatalf("fresh: %d", got)
	}
	if got := send("old", now.Add(-DefaultMaxSkew-time.Minute)); got != http.StatusUnauthorized {
		t.Fatalf("beyond default skew: want 401, got %d", got)
	}
	s.MaxSkew = time.Hour
	if got := send("older", now.Add(-DefaultMaxSkew-time.Minute)); got != http.StatusNoContent {
		t.Fatalf("within custom skew: %d", got)
	}
}

func TestEvents_ConcurrentCopyRunsOnce(t *testing.T) {
	s := newServer()
	install(s, "s1", "")
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	runs := 0
	s.OnEvent = func(_ context.Context, _ Event) error {
		mu.Lock()
		runs++
		n := runs
		mu.Unlock()
		if n == 1 { // only the first copy waits, so a second run fails fast instead of hanging
			entered <- struct{}{}
			<-release
		}
		return nil
	}

	first := make(chan int)
	go func() { first <- event(s, "s1", "d1", t0.Unix()) }()
	<-entered
	// The same delivery while the first copy is still in OnEvent.
	if code := event(s, "s1", "d1", t0.Unix()); code != http.StatusConflict {
		t.Fatalf("concurrent copy: want 409 so Mesh retries later, got %d", code)
	}
	close(release)
	if code := <-first; code != http.StatusNoContent {
		t.Fatalf("first copy: %d", code)
	}
	if code := event(s, "s1", "d1", t0.Unix()); code != http.StatusOK {
		t.Fatalf("later retry: want 200, got %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Fatalf("OnEvent ran %d times, want 1", runs)
	}
}

func TestEvents_UnsignedHeadersAreBounded(t *testing.T) {
	s := newServer()
	install(s, "s1", "")
	ran := 0
	s.OnEvent = func(context.Context, Event) error { ran++; return nil }
	for _, bad := range []string{"x\nforged log line", "a b", strings.Repeat("a", 129)} {
		r := signed(http.MethodPost, PathEvents, []byte(`{"workspace_id":"`+ws+`"}`), "s1", t0.Unix(),
			map[string]string{HeaderEvent: "task.created", HeaderDelivery: bad})
		if code := serve(s, r); code != http.StatusBadRequest {
			t.Fatalf("delivery header %q: want 400, got %d", bad, code)
		}
	}
	if ran != 0 {
		t.Fatalf("handler ran %d times for malformed headers", ran)
	}
}

func TestEvents_EventIDTellsEventsApart(t *testing.T) {
	s := newServer()
	install(s, "s1", "")
	ran := 0
	s.OnEvent = func(context.Context, Event) error { ran++; return nil }
	send := func(eventID, delivery string, ts int64) int {
		body := []byte(`{"event_id":"` + eventID + `","task_id":"t1","workspace_id":"` + ws + `"}`)
		return serve(s, signed(http.MethodPost, PathEvents, body, "s1", ts,
			map[string]string{HeaderEvent: "task.updated", HeaderDelivery: delivery}))
	}
	if code := send("e1", "d1", t0.Unix()); code != http.StatusNoContent {
		t.Fatalf("first event: %d", code)
	}
	// Equal in everything but event_id: a different event, handled on its own.
	if code := send("e2", "d1", t0.Unix()); code != http.StatusNoContent || ran != 2 {
		t.Fatalf("different event_id: want 204 and 2 runs, got %d, %d", code, ran)
	}
	// A retry (same event_id, fresh timestamp) and a replay under a forged
	// X-Mesh-Delivery are one event: acknowledged, not run again.
	if code := send("e1", "d1", t0.Unix()+5); code != http.StatusOK || ran != 2 {
		t.Fatalf("retry of e1: want 200 and 2 runs, got %d, %d", code, ran)
	}
	if code := send("e1", "forged", t0.Unix()); code != http.StatusOK || ran != 2 {
		t.Fatalf("replay of e1 under another delivery header: want 200 and 2 runs, got %d, %d", code, ran)
	}
}

func TestEvents_PanicReleasesTheReservation(t *testing.T) {
	s := newServer()
	install(s, "s1", "")
	calls := 0
	s.OnEvent = func(context.Context, Event) error {
		calls++
		if calls == 1 {
			panic("handler bug")
		}
		return nil
	}
	func() {
		defer func() { _ = recover() }() // net/http recovers a handler panic per request
		event(s, "s1", "d1", t0.Unix())
	}()
	// The retry is not turned away as "being processed": it runs again.
	if code := event(s, "s1", "d1", t0.Unix()+1); code != http.StatusNoContent || calls != 2 {
		t.Fatalf("retry after a panic: want 204 and 2 runs, got %d, %d", code, calls)
	}
}

func TestEvents_KeyExpiresAfterSeenFor(t *testing.T) {
	s := newServer()
	install(s, "s1", "")
	ran := 0
	s.OnEvent = func(context.Context, Event) error { ran++; return nil }
	now := t0
	s.now = func() time.Time { return now }
	send := func() int {
		body := []byte(`{"event_id":"e1","workspace_id":"` + ws + `"}`)
		return serve(s, signed(http.MethodPost, PathEvents, body, "s1", now.Unix(),
			map[string]string{HeaderEvent: "task.updated", HeaderDelivery: "d1"}))
	}
	if code := send(); code != http.StatusNoContent {
		t.Fatalf("first: %d", code)
	}
	now = now.Add(seenFor - time.Minute)
	if code := send(); code != http.StatusOK || ran != 1 {
		t.Fatalf("inside the window: want 200 and 1 run, got %d, %d", code, ran)
	}
	// Nothing was processed in between, so nothing pruned the key: it must
	// still be forgotten once seenFor has passed.
	now = now.Add(2 * time.Minute)
	if code := send(); code != http.StatusNoContent || ran != 2 {
		t.Fatalf("after the window: want 204 and 2 runs, got %d, %d", code, ran)
	}
}
