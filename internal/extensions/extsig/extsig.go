// Package extsig is the v2 signature of extension event deliveries: the HMAC
// covers the timestamp, the delivery id, the event type and the raw body, so a
// header cannot be swapped under a valid signature. The same code signs on the
// Mesh side and verifies on a receiver (see docs/webhooks.md).
//
// v1 (hex HMAC of "{timestamp}.{body}") is unchanged and lives with the sender.
package extsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Version is the value of X-Mesh-Signature-Version and of the manifest field
	// external.signature_version for this scheme.
	Version = 2
	// Prefix starts X-Mesh-Signature in v2. A v1 receiver compares the bare hex
	// and therefore fails on it instead of accepting a message it cannot check.
	Prefix = "v2="
	// Context separates v2 bytes from any other HMAC made with the same secret.
	Context = "mesh-ext-sig-v2"

	HeaderSignature = "X-Mesh-Signature"
	HeaderVersion   = "X-Mesh-Signature-Version"
	HeaderTimestamp = "X-Mesh-Timestamp"
	HeaderDelivery  = "X-Mesh-Delivery"
	HeaderEvent     = "X-Mesh-Event"
)

// CanonicalBytes is what is signed: the context label, timestamp (Unix
// seconds), delivery id and event type each followed by "\n", then the raw body.
// The first four fields never contain "\n" (Sign and Verify refuse one that
// does) and the body comes last, so no two inputs share the same bytes.
func CanonicalBytes(timestamp, deliveryID, event string, body []byte) []byte {
	var out []byte
	for _, f := range []string{Context, timestamp, deliveryID, event} {
		out = append(out, f...)
		out = append(out, '\n')
	}
	return append(out, body...)
}

// Sign returns the X-Mesh-Signature value: "v2=" + hex HMAC-SHA256 of
// CanonicalBytes. A field with a line break is refused.
func Sign(secret, timestamp, deliveryID, event string, body []byte) (string, error) {
	for _, f := range []string{timestamp, deliveryID, event} {
		if strings.ContainsAny(f, "\r\n") {
			return "", errors.New("extsig: header value contains a line break")
		}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(CanonicalBytes(timestamp, deliveryID, event, body))
	return Prefix + hex.EncodeToString(mac.Sum(nil)), nil
}

// Delivery is a verified request.
type Delivery struct {
	DeliveryID string
	Event      string
	// EventID is body.event_id when the body carries one, else the delivery id:
	// the key to run a handler once per event.
	EventID string
}

// Verifier checks v2 requests. Version pinning is the point: a receiver that
// agreed to v2 at install refuses everything else, so a request stripped of its
// v2 marks or re-signed in v1 form is an error, never a fallback.
type Verifier struct {
	Secret string
	// MaxSkew bounds |now - timestamp|; zero means 5 minutes.
	MaxSkew time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Verify checks headers and raw body of one request.
func (v Verifier) Verify(h http.Header, body []byte) (Delivery, error) {
	if h.Get(HeaderVersion) != strconv.Itoa(Version) {
		return Delivery{}, errors.New("extsig: signature version is not 2")
	}
	got := h.Get(HeaderSignature)
	if !strings.HasPrefix(got, Prefix) {
		return Delivery{}, errors.New("extsig: signature is not a v2 signature")
	}
	ts, id, ev := h.Get(HeaderTimestamp), h.Get(HeaderDelivery), h.Get(HeaderEvent)
	if ts == "" || id == "" || ev == "" {
		return Delivery{}, errors.New("extsig: timestamp, delivery id or event is missing")
	}
	want, err := Sign(v.Secret, ts, id, ev, body)
	if err != nil {
		return Delivery{}, err
	}
	if !hmac.Equal([]byte(got), []byte(want)) {
		return Delivery{}, errors.New("extsig: signature mismatch")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return Delivery{}, errors.New("extsig: timestamp is not Unix seconds")
	}
	now, skew := time.Now, v.MaxSkew
	if v.Now != nil {
		now = v.Now
	}
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	if d := now().Sub(time.Unix(sec, 0)); d > skew || d < -skew {
		return Delivery{}, errors.New("extsig: timestamp is outside the allowed window")
	}
	d := Delivery{DeliveryID: id, Event: ev, EventID: id}
	var b struct {
		EventID string `json:"event_id"`
	}
	if json.Unmarshal(body, &b) == nil && b.EventID != "" {
		d.EventID = b.EventID
	}
	return d, nil
}

// ErrInProgress is returned by Once.Do while another delivery of the same event
// is being handled. Answer 5xx: Mesh retries, and by then the first run has
// finished (the retry is then a no-op) or failed (the retry runs it).
var ErrInProgress = errors.New("extsig: the event is being handled")

// DefaultOnceTTL is how long Once remembers a handled event: far beyond Mesh's
// retry window (a few attempts within about half a minute).
const DefaultOnceTTL = 24 * time.Hour

// Once runs a handler once per event id. Retries of one event carry the same
// event_id, so the handler runs once; another event_id runs it again even when
// the rest of the payload is equal. The claim is atomic: concurrent deliveries
// of one event never both run the handler, and a handler that fails releases
// the claim so the next delivery runs it again. A handled event is remembered
// for TTL and then dropped, so a long-running receiver does not grow without
// bound.
type Once struct {
	// TTL is how long a handled event is remembered; zero means DefaultOnceTTL.
	TTL time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu        sync.Mutex
	handled   map[string]time.Time // event id -> when it was handled
	inflight  map[string]bool
	lastSweep time.Time
}

func (o *Once) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Do runs fn unless the event was handled (ran=false, nil) or is being handled
// (ErrInProgress). An error from fn is returned and the event stays unhandled.
func (o *Once) Do(eventID string, fn func() error) (ran bool, err error) {
	ttl := o.TTL
	if ttl <= 0 {
		ttl = DefaultOnceTTL
	}
	o.mu.Lock()
	now := o.now()
	if o.handled == nil {
		o.handled, o.inflight = map[string]time.Time{}, map[string]bool{}
	}
	if now.Sub(o.lastSweep) >= ttl/10 {
		for id, at := range o.handled {
			if now.Sub(at) >= ttl {
				delete(o.handled, id)
			}
		}
		o.lastSweep = now
	}
	if at, ok := o.handled[eventID]; ok && now.Sub(at) < ttl {
		o.mu.Unlock()
		return false, nil
	}
	if o.inflight[eventID] {
		o.mu.Unlock()
		return false, ErrInProgress
	}
	o.inflight[eventID] = true
	o.mu.Unlock()

	// The claim is released even when fn panics, so the event can be retried.
	ok := false
	defer func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		delete(o.inflight, eventID)
		if ok {
			o.handled[eventID] = o.now()
		}
	}()
	if err := fn(); err != nil {
		return false, err
	}
	ok = true
	return true, nil
}
