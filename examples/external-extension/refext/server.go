package refext

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Paths of the reference manifest (mesh-extension.json, block "external").
const (
	PathHealth  = "/healthz"
	PathInstall = "/mesh/install"
	PathToken   = "/mesh/token"
	PathEvents  = "/mesh/events"
	PathUIHello = "/mesh/ui/hello"
	PathUITask  = "/mesh/ui/task"
)

// maxBody bounds every request body Mesh sends; real ones are a few KB.
const maxBody = 1 << 20

// seenFor is how long the dedupe key of a processed event is remembered. Mesh
// retries one delivery for about half a minute, so an hour covers every retry
// with room. For a body without event_id it is also the window in which two
// events with identical bodies in one workspace are taken for one.
const seenFor = time.Hour

// headerToken bounds the unsigned X-Mesh-Event and X-Mesh-Delivery values that
// reach the handler and the logs.
var headerToken = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// InstallRequest is the body of POST {install_path}.
type InstallRequest struct {
	ExtensionID   string   `json:"extension_id"`
	Version       string   `json:"version"`
	WorkspaceID   string   `json:"workspace_id"`
	SigningSecret string   `json:"signing_secret"`
	Events        []string `json:"events"`
	EventsPath    string   `json:"events_path"`
	InstallCode   string   `json:"install_code,omitempty"`
}

// TokenRequest is the body of POST {token_path}.
type TokenRequest struct {
	ExtensionID    string `json:"extension_id"`
	WorkspaceID    string `json:"workspace_id"`
	ExtensionToken string `json:"extension_token"`
}

// Event is one verified event delivery.
type Event struct {
	// DeliveryID is X-Mesh-Delivery. It is not signed, so it is for logs only and
	// is never used to deduplicate. Name is X-Mesh-Event, also unsigned: a routing
	// hint, not proof of what the signed body is. Both are checked against
	// headerToken before they get here.
	DeliveryID  string
	Name        string
	WorkspaceID string
	// Data is the JSON body as sent; workspace_id is always in it.
	Data map[string]json.RawMessage
}

// Server is the reference extension service. The zero value is not usable:
// set ExtensionID, then serve it.
//
// All its state (signing secrets, tokens, issued codes, hashes of processed
// bodies) lives in memory, for the life of one process. A restart forgets the
// workspaces, so they must be installed again, and forgets which bodies were
// processed, so a retry that arrives after a restart runs again. A
// production service keeps all of it in durable storage, and records its
// dedupe key in the same transaction as the work the delivery caused.
//
// Deliveries are deduplicated on the event_id of the verified signed body, which
// is the same on every retry and differs between events. A body without one
// (an older Mesh) falls back to its SHA-256: with the v1 signature only the
// timestamp and the body are signed, X-Mesh-Delivery is not. That fallback is a
// limit of the example, not an exactly-once guarantee: events of one workspace
// whose bodies are byte-identical count as one within seenFor, so a handler
// that must tell such events apart should not rely on it.
type Server struct {
	// ExtensionID is the manifest id. Install and token requests for another id
	// are refused.
	ExtensionID string
	// MaxSkew bounds X-Mesh-Timestamp on events (default DefaultMaxSkew).
	MaxSkew time.Duration
	// OnEvent handles a verified, not yet processed delivery. An error answers
	// 500, so Mesh retries and the delivery is processed again.
	OnEvent func(ctx context.Context, ev Event) error
	// UI verifies the iframe context token; nil serves no screens.
	UI *ContextVerifier
	// ProjectAllowed, when set, is the service's own list of projects it works
	// in. An event of another project is acknowledged and dropped. Mesh limits
	// what a connection sees on its side too; this keeps a receiver correct
	// against a Mesh that does not yet, and against its own configuration.
	ProjectAllowed func(workspaceID, projectID string) bool

	now func() time.Time

	mu         sync.Mutex
	workspaces map[string]*workspaceState
	// codes are the binding codes issued and not yet used (IssueInstallCode).
	codes    map[string]bool
	codeMode bool
}

// IssueInstallCode registers a one-time binding code. Once any code is issued,
// a workspace binds, or re-keys to a new signing secret, only with an unused
// code, and the code is spent by it: a code that leaked after use binds
// nothing. Mesh resends the code it holds on every handshake, so rotating the
// signing secret takes a fresh code stored in Mesh (a new code also makes
// Mesh repeat the handshake). Without any code the first install binds the
// workspace for good and its secret can never be rotated: fine for a local
// demo, not for a service other people can reach.
func (s *Server) IssueInstallCode(code string) {
	if code == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes == nil {
		s.codes = map[string]bool{}
	}
	s.codes[code], s.codeMode = true, true
}

// spendCodeLocked consumes code if it is an issued, unused one. Every issued
// code is compared in constant time.
func (s *Server) spendCodeLocked(code string) bool {
	match := ""
	for c := range s.codes {
		if subtle.ConstantTimeCompare([]byte(code), []byte(c)) == 1 {
			match = c
		}
	}
	if match == "" {
		return false
	}
	delete(s.codes, match)
	return true
}

type workspaceState struct {
	secret      string
	token       string
	lastTokenTS int64
	seen        map[string]time.Time
	// inflight holds deliveries being processed right now, so a concurrent
	// copy of one (a retry racing a slow handler) is not run twice.
	inflight map[string]bool
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Token returns the extension token Mesh delivered for a workspace, "" when
// none has arrived yet.
func (s *Server) Token(workspaceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.workspaces[workspaceID]; w != nil {
		return w.token
	}
	return ""
}

// Installed reports whether a workspace has completed the install handshake.
func (s *Server) Installed(workspaceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaces[workspaceID] != nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == PathHealth && r.Method == http.MethodGet:
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == PathInstall && r.Method == http.MethodPost:
		s.install(w, r)
	case r.URL.Path == PathToken && r.Method == http.MethodPost:
		s.receiveToken(w, r)
	case r.URL.Path == PathEvents && r.Method == http.MethodPost:
		s.receiveEvent(w, r)
	case (r.URL.Path == PathUIHello || r.URL.Path == PathUITask) && r.Method == http.MethodGet && s.UI != nil:
		s.screen(w, r)
	default:
		http.NotFound(w, r)
	}
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

// install binds a workspace: Mesh sends the signing secret every later call is
// signed with. The request is signed with that same secret, which proves the
// body arrived whole, not who sent it; the install code is what proves that.
// A repeat with the same secret is a no-op (Mesh repeats the handshake on
// restarts and health checks).
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in InstallRequest
	if err := json.Unmarshal(body, &in); err != nil || in.WorkspaceID == "" || in.SigningSecret == "" {
		http.Error(w, "bad install request", http.StatusBadRequest)
		return
	}
	if in.ExtensionID != s.ExtensionID {
		http.Error(w, "unknown extension", http.StatusNotFound)
		return
	}
	if _, err := Verify(r.Header, body, in.SigningSecret, s.clock(), 0); err != nil {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.workspaces[in.WorkspaceID]
	if cur != nil && cur.secret == in.SigningSecret {
		w.WriteHeader(http.StatusOK)
		return
	}
	// The install signature uses the secret the request carries, so it cannot
	// tell Mesh from anyone else. Without codes, a bound workspace therefore
	// never changes its secret: that would let any caller take it over.
	switch {
	case s.codeMode && !s.spendCodeLocked(in.InstallCode):
		http.Error(w, "unused install code required", http.StatusForbidden)
		return
	case !s.codeMode && cur != nil:
		http.Error(w, "workspace already bound; re-keying needs an install code", http.StatusForbidden)
		return
	}
	if s.workspaces == nil {
		s.workspaces = map[string]*workspaceState{}
	}
	if cur == nil {
		cur = &workspaceState{seen: map[string]time.Time{}, inflight: map[string]bool{}}
		s.workspaces[in.WorkspaceID] = cur
	}
	// A re-key keeps the token: Mesh delivers a new one only when it rotates it.
	cur.secret = in.SigningSecret
	w.WriteHeader(http.StatusOK)
}

// receiveToken stores the extension token Mesh issues on install and on every
// rotation. Only 200 {"ok":true} counts as delivered on the Mesh side; any
// other answer keeps the previous token working there.
func (s *Server) receiveToken(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in TokenRequest
	if err := json.Unmarshal(body, &in); err != nil || in.ExtensionToken == "" {
		http.Error(w, "bad token request", http.StatusBadRequest)
		return
	}
	if in.ExtensionID != s.ExtensionID {
		http.Error(w, "unknown extension", http.StatusNotFound)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws := s.workspaces[in.WorkspaceID]
	if ws == nil {
		http.Error(w, "workspace not installed", http.StatusUnauthorized)
		return
	}
	ts, err := Verify(r.Header, body, ws.secret, s.clock(), s.skew())
	if err != nil {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	// Mesh stamps every delivery with a strictly newer timestamp: an older or
	// equal one is a replay of a token that may already be superseded.
	if ts <= ws.lastTokenTS {
		http.Error(w, "stale token delivery", http.StatusConflict)
		return
	}
	ws.lastTokenTS, ws.token = ts, in.ExtensionToken
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// inScope reports whether an event belongs to a project the service works in.
// Events without a project (workspace members, agent keys) are always in scope.
func (s *Server) inScope(ev Event) bool {
	if s.ProjectAllowed == nil {
		return true
	}
	var projectID string
	if raw, ok := ev.Data["project_id"]; !ok || json.Unmarshal(raw, &projectID) != nil || projectID == "" {
		return true
	}
	return s.ProjectAllowed(ev.WorkspaceID, projectID)
}

func (s *Server) skew() time.Duration {
	if s.MaxSkew > 0 {
		return s.MaxSkew
	}
	return DefaultMaxSkew
}

// receiveEvent verifies one event delivery and hands it to OnEvent unless an
// event with the same event_id (or, without one, the same body) was already
// processed in this process within seenFor (see Server for what that does and does not guarantee). The
// workspace is read from the body before the signature is checked, because the
// workspace names the secret; nothing else in the body is trusted until then.
func (s *Server) receiveEvent(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body, &data); err != nil {
		http.Error(w, "body is not a JSON object", http.StatusBadRequest)
		return
	}
	var wsID string
	if raw, ok := data["workspace_id"]; !ok || json.Unmarshal(raw, &wsID) != nil || wsID == "" {
		http.Error(w, "workspace_id missing", http.StatusBadRequest)
		return
	}
	ev := Event{
		DeliveryID: r.Header.Get(HeaderDelivery), Name: r.Header.Get(HeaderEvent),
		WorkspaceID: wsID, Data: data,
	}
	if !headerToken.MatchString(ev.DeliveryID) || !headerToken.MatchString(ev.Name) {
		http.Error(w, "delivery id or event name missing or malformed", http.StatusBadRequest)
		return
	}

	now := s.clock()
	s.mu.Lock()
	ws := s.workspaces[wsID]
	var secret string
	if ws != nil {
		secret = ws.secret
	}
	s.mu.Unlock()
	if ws == nil {
		http.Error(w, "workspace not installed", http.StatusUnauthorized)
		return
	}
	if _, err := Verify(r.Header, body, secret, now, s.skew()); err != nil {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	// X-Mesh-Delivery is not covered by the signature, so a replayed request
	// could carry a fresh one. Deliveries are told apart by what the signature
	// does cover: the event_id in the body, the same on every retry (only the
	// timestamp is re-signed), else the body itself.
	key := dedupeKey(body, data)

	// A retry of a delivery already processed is acknowledged, not re-run. A
	// copy arriving while the first is still being processed is answered 409:
	// not 2xx, so Mesh retries it later and it meets the outcome of the first.
	s.mu.Lock()
	// A key older than seenFor is forgotten even if no event has pruned it yet.
	at, dup := ws.seen[key]
	dup = dup && now.Sub(at) <= seenFor
	busy := ws.inflight[key]
	if !dup && !busy {
		ws.inflight[key] = true
	}
	s.mu.Unlock()
	switch {
	case dup:
		w.WriteHeader(http.StatusOK)
		return
	case busy:
		http.Error(w, "delivery is being processed", http.StatusConflict)
		return
	}
	// Release the reservation however OnEvent ends, a panic included: it would
	// otherwise stay in flight and answer every retry 409 until a restart.
	processed := false
	defer func() {
		if !processed {
			s.mu.Lock()
			delete(ws.inflight, key)
			s.mu.Unlock()
		}
	}()
	if s.OnEvent != nil && s.inScope(ev) {
		if err := s.OnEvent(r.Context(), ev); err != nil {
			log.Printf("[refext] event %q delivery %q failed: %v", ev.Name, ev.DeliveryID, err)
			http.Error(w, "processing failed", http.StatusInternalServerError)
			return
		}
	}
	processed = true
	s.mu.Lock()
	delete(ws.inflight, key)
	for id, at := range ws.seen {
		if now.Sub(at) > seenFor {
			delete(ws.seen, id)
		}
	}
	ws.seen[key] = now
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// dedupeKey identifies one event by the signed body: its event_id when it
// carries one, else the SHA-256 of the whole body.
func dedupeKey(body []byte, data map[string]json.RawMessage) string {
	var id string
	if raw, ok := data["event_id"]; ok && json.Unmarshal(raw, &id) == nil && headerToken.MatchString(id) {
		return "event:" + id
	}
	sum := sha256.Sum256(body)
	return "body:" + hex.EncodeToString(sum[:])
}
