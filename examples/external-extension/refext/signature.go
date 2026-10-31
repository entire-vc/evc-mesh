// Package refext is the reference external extension for Mesh: the receiving
// side of every call Mesh makes to an external service (install handshake,
// token delivery, event delivery, iframe screens) and the calls the service
// makes back with its extension token.
//
// It uses the standard library and golang-jwt only and never imports Mesh, so
// it reads like code a partner writes from the public documents. Mesh's own
// tests drive it with the real Mesh senders (handshake, token delivery, event
// delivery, ui-context tokens): if Mesh changes a wire format, those tests turn
// red here before any partner notices.
package refext

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Headers Mesh sets on every signed request (docs/webhooks.md).
const (
	HeaderTimestamp = "X-Mesh-Timestamp"
	HeaderSignature = "X-Mesh-Signature"
	HeaderEvent     = "X-Mesh-Event"
	HeaderDelivery  = "X-Mesh-Delivery"
)

// DefaultMaxSkew is how far X-Mesh-Timestamp may be from the receiver's clock.
// A captured request replayed later than this is refused.
const DefaultMaxSkew = 5 * time.Minute

var (
	errNoSignature  = errors.New("missing signature or timestamp")
	errBadTimestamp = errors.New("timestamp is not Unix seconds")
	errStale        = errors.New("timestamp outside the allowed window")
	errBadSignature = errors.New("signature does not match")
)

// Sign returns the documented signature: hex HMAC-SHA256 of "{timestamp}.{body}".
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks the signature of a Mesh request over its raw body and returns
// the timestamp it carried. maxSkew 0 skips the clock check (the install
// handshake is checked by its secret and install code instead).
func Verify(h http.Header, body []byte, secret string, now time.Time, maxSkew time.Duration) (int64, error) {
	ts, sig := h.Get(HeaderTimestamp), h.Get(HeaderSignature)
	if ts == "" || sig == "" || secret == "" {
		return 0, errNoSignature
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0, errBadTimestamp
	}
	if maxSkew > 0 {
		d := now.Sub(time.Unix(unix, 0))
		if d > maxSkew || d < -maxSkew {
			return 0, errStale
		}
	}
	if !hmac.Equal([]byte(sig), []byte(Sign(secret, ts, body))) {
		return 0, errBadSignature
	}
	return unix, nil
}
