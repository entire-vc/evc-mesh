package secretstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Metadata-only probe, protocol mesh-keep-metadata-v1 (closed metadata contract):
//
//	GET /orgs/{org}/mesh/secrets/{ref}/metadata   (no body, no query)
//	  -> {"version":"<positive decimal int64>","length":<1..32768>,"request_id":"<id>"}
//
// It is signed like every other call of HTTPKeepClient, over the SHA-256 of
// zero bytes, and it never reads or writes a secret value: the answer type has
// no field a value could occupy.

// KeepMetadata is what the probe returns about the current version of a
// secret. There is nothing else in it on purpose.
type KeepMetadata struct {
	Version string
	Length  int
}

// KeepMetadataReader is the one extra capability the probe route needs from the
// Keep client. It is a separate interface so the route can never be handed
// something that also holds GetValue/PutValue by accident of a wider type.
type KeepMetadataReader interface {
	Metadata(ctx context.Context, secretID string) (KeepMetadata, error)
}

var (
	// ErrKeepMetadataUnavailable covers every answer the probe must not tell
	// apart: auth failure, 403, 5xx, redirect, cache, schema, size, timeout.
	ErrKeepMetadataUnavailable = errors.New("keep: metadata unavailable")
	// ErrKeepMetadataSchema is a 200 that is not the closed DTO. It is wrapped
	// by ErrKeepMetadataUnavailable so callers match on one error.
	ErrKeepMetadataSchema = errors.New("keep: metadata answer is not the closed schema")
)

const (
	// maxKeepMetadataBody is the DTO size bound; one more byte is overflow.
	maxKeepMetadataBody = 4096
	// maxKeepMetadataLength is the largest value Keep stores (32 KiB).
	maxKeepMetadataLength = 32 << 10
	// emptyBodySHA256 is the SHA-256 of zero bytes, the last field of the
	// signed message of an empty-body request (not "{}", not a newline).
	emptyBodySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// keepMetadataTimeout bounds the whole call whatever client is injected: a
// caller-supplied http.Client with Timeout 0 must not lift the 5 s limit.
// It is a variable only so a test can shorten it.
var keepMetadataTimeout = 5 * time.Second

var (
	keepVersionRe   = regexp.MustCompile(`^[1-9]\d*$`)
	keepRequestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	keepLengthRe    = regexp.MustCompile(`^\d{1,5}$`)
)

// Metadata asks Keep for the version and length of the current value of the
// secret named by secretID (the stored keep_ref without its "keep:" prefix).
//
// One signed empty-body GET with a fresh nonce, no retry (a retry is a new call
// with a new nonce), no redirect, 5 s timeout. Errors: ErrKeepNotFound,
// ErrKeepRateLimited, ErrKeepBadID, ErrKeepNotTLS, otherwise
// ErrKeepMetadataUnavailable. Nothing from the upstream body or headers is
// returned except the two validated fields.
func (c *HTTPKeepClient) Metadata(ctx context.Context, secretID string) (KeepMetadata, error) {
	id, err := parseSecretID(secretID)
	if err != nil {
		return KeepMetadata{}, err
	}
	u, err := url.JoinPath(c.BaseURL, "orgs", c.OrgID.String(), "mesh", "secrets", id, "metadata")
	if err != nil {
		return KeepMetadata{}, ErrKeepMetadataUnavailable
	}
	pu, err := url.Parse(u)
	if err != nil {
		return KeepMetadata{}, ErrKeepMetadataUnavailable
	}
	if pu.Scheme != "https" {
		return KeepMetadata{}, ErrKeepNotTLS
	}
	ctx, cancel := context.WithTimeout(ctx, keepMetadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return KeepMetadata{}, ErrKeepMetadataUnavailable
	}
	nonce, err := c.nonce()
	if err != nil {
		return KeepMetadata{}, ErrKeepMetadataUnavailable
	}
	ts := strconv.FormatInt(c.now().Unix(), 10)
	mac := hmac.New(sha256.New, c.EventsSecret)
	mac.Write([]byte(keepSigTag + "\n" + ts + "\n" + nonce + "\n" + http.MethodGet + "\n" + pu.EscapedPath() + "\n" + emptyBodySHA256))
	req.Header.Set("X-Mesh-Workspace", c.WorkspaceID.String())
	req.Header.Set("X-Mesh-Timestamp", ts)
	req.Header.Set("X-Mesh-Nonce", nonce)
	req.Header.Set("X-Mesh-Signature", keepSigPref+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Accept", "application/json")

	resp, err := c.client().Do(req)
	if err != nil {
		return KeepMetadata{}, ErrKeepMetadataUnavailable
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Only Keep's own closed not_found envelope is a missing secret. A bare
		// 404 (old Keep without the metadata route, a proxy, an HTML page) is
		// "metadata unavailable", never a Mesh 404.
		if !isKeepNotFoundEnvelope(resp) {
			return KeepMetadata{}, fmt.Errorf("%w: %w", ErrKeepMetadataUnavailable, ErrKeepMetadataSchema)
		}
		return KeepMetadata{}, ErrKeepNotFound
	case http.StatusTooManyRequests:
		return KeepMetadata{}, ErrKeepRateLimited
	default:
		// 401, 403, 3xx (redirect is never followed), 5xx and anything else.
		return KeepMetadata{}, ErrKeepMetadataUnavailable
	}
	if !hasNoStore(strings.Join(resp.Header.Values("Cache-Control"), ",")) {
		return KeepMetadata{}, fmt.Errorf("%w: %w", ErrKeepMetadataUnavailable, ErrKeepCache)
	}
	if mt, _, merr := mime.ParseMediaType(resp.Header.Get("Content-Type")); merr != nil || mt != "application/json" {
		return KeepMetadata{}, fmt.Errorf("%w: %w", ErrKeepMetadataUnavailable, ErrKeepMetadataSchema)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeepMetadataBody+1))
	if err != nil || len(body) > maxKeepMetadataBody {
		return KeepMetadata{}, fmt.Errorf("%w: %w", ErrKeepMetadataUnavailable, ErrKeepMetadataSchema)
	}
	md, err := decodeKeepMetadata(body)
	if err != nil {
		return KeepMetadata{}, fmt.Errorf("%w: %w", ErrKeepMetadataUnavailable, err)
	}
	return md, nil
}

// decodeKeepMetadata is the closed DTO decoder: exactly the keys version,
// length and request_id, each once, with the exact JSON types, and nothing
// after the object.
func decodeKeepMetadata(body []byte) (KeepMetadata, error) {
	bad := func() (KeepMetadata, error) { return KeepMetadata{}, ErrKeepMetadataSchema }
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return bad()
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return bad()
		}
		key, ok := kt.(string)
		if !ok {
			return bad()
		}
		switch key {
		case "version", "length", "request_id":
		default:
			return bad()
		}
		if _, dup := fields[key]; dup {
			return bad()
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return bad()
		}
		fields[key] = raw
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return bad()
	}
	// Trailing JSON or garbage: the next read must be a clean EOF.
	if _, err := dec.Token(); err != io.EOF {
		return bad()
	}
	if len(fields) != 3 {
		return bad()
	}

	var version, requestID string
	if json.Unmarshal(fields["version"], &version) != nil || !keepVersionRe.MatchString(version) {
		return bad()
	}
	if n, err := strconv.ParseInt(version, 10, 64); err != nil || n <= 0 {
		return bad()
	}
	if json.Unmarshal(fields["request_id"], &requestID) != nil || !keepRequestIDRe.MatchString(requestID) {
		return bad()
	}
	// length: an integer literal only; a float, exponent, sign, string, bool
	// or null never matches.
	rawLen := string(fields["length"])
	if !keepLengthRe.MatchString(rawLen) || (len(rawLen) > 1 && rawLen[0] == '0') {
		return bad()
	}
	length, err := strconv.Atoi(rawLen)
	if err != nil || length < 1 || length > maxKeepMetadataLength {
		return bad()
	}
	return KeepMetadata{Version: version, Length: length}, nil
}

// isKeepNotFoundEnvelope reports whether a 404 is Keep's exact spec §3.2 answer:
// application/json, at most 4096 bytes, exactly error_class="not_found",
// message="not found" and a safe request_id, nothing else, nothing trailing.
func isKeepNotFoundEnvelope(resp *http.Response) bool {
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeepMetadataBody+1))
	if err != nil || len(body) > maxKeepMetadataBody {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	// Token walk, not a struct decode: a struct silently keeps the last of two
	// equal keys, so duplicates and unknown keys are refused here.
	fields := map[string]string{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := kt.(string)
		if !ok {
			return false
		}
		switch key {
		case "error_class", "message", "request_id":
		default:
			return false
		}
		if _, dup := fields[key]; dup {
			return false
		}
		var v string
		if dec.Decode(&v) != nil {
			return false
		}
		fields[key] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		return false
	}
	return len(fields) == 3 && fields["error_class"] == "not_found" &&
		fields["message"] == "not found" && keepRequestIDRe.MatchString(fields["request_id"])
}
