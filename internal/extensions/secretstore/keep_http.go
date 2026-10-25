package secretstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Errors a Keep answer maps to. Anything else, and every transport failure,
// is treated as "Keep unavailable" and ends in a degraded Resolve.
var (
	ErrKeepNotFound = errors.New("keep: secret not found")
	// ErrKeepUnauthorized is the uniform 401: bad signature, stale timestamp,
	// replayed nonce, or an installation Keep no longer knows (revoked).
	ErrKeepUnauthorized = errors.New("keep: request not authenticated (bad signature or revoked actor)")
	ErrKeepForbidden    = errors.New("keep: workspace does not belong to this organisation")
	ErrKeepRateLimited  = errors.New("keep: rate limited")
	// ErrKeepCache is a success answer that Keep did not mark no-store.
	ErrKeepCache = errors.New("keep: answer is not Cache-Control: no-store")
	// ErrKeepNotTLS is a base URL that is not https: Keep serves these routes
	// over TLS only and the value must not travel in clear.
	ErrKeepNotTLS = errors.New("keep: base URL must be https")
)

// maxKeepBody bounds what is read back from Keep.
const maxKeepBody = 1 << 20

const (
	keepSigTag  = "keep-mesh-secret-v1"
	keepSigPref = "v1="
)

// HTTPKeepClient talks to Keep's extension API as the installed Mesh
// workspace (Keep K5.9, evc-keep docs/mesh-extension.md):
//
//	PUT  /orgs/{org}/mesh/secrets/{ref}          {value} -> {ref, version, fingerprint}
//	POST /orgs/{org}/mesh/secrets/{ref}/resolve  (empty body) -> {value, version}
//
// There is no bearer. Each request carries X-Mesh-Workspace, X-Mesh-Timestamp,
// a single-use X-Mesh-Nonce and X-Mesh-Signature = "v1=" + hex HMAC-SHA256,
// keyed by the installation's events secret over
// "keep-mesh-secret-v1\n{ts}\n{nonce}\n{method}\n{path}\n{hex sha256(body)}".
// 401 = not authenticated, 403 = workspace not in the org, 404 = no such ref,
// 429 = rate limited, 503 and transport errors = Keep unavailable. The value is
// never logged and never put in an error.
type HTTPKeepClient struct {
	BaseURL     string
	OrgID       uuid.UUID
	WorkspaceID uuid.UUID
	// EventsSecret is the HMAC key Keep issued at install.
	EventsSecret []byte
	HTTP         *http.Client
	// Now and Nonce are test seams; nil means the clock and crypto/rand.
	Now   func() time.Time
	Nonce func() (string, error)
}

// client never follows a redirect: the request carries the secret and the
// signed X-Mesh headers, which must not be replayed to another target. A 3xx
// is surfaced as an error status.
func (c *HTTPKeepClient) client() *http.Client {
	hc := &http.Client{Timeout: 5 * time.Second}
	if c.HTTP != nil {
		cp := *c.HTTP
		hc = &cp
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return hc
}

// hasNoStore reports whether a Cache-Control value carries the no-store
// directive itself, not a substring of another token.
func hasNoStore(v string) bool {
	for _, d := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(d), "no-store") {
			return true
		}
	}
	return false
}

// ErrKeepBadID is a secret id that is not a Keep ref. Ids come from stored
// settings, so one is never trusted into a request path.
var ErrKeepBadID = errors.New("keep: malformed secret id")

// keepRefRe mirrors Keep's closed ref alphabet: no separator, no leading dot
// or dash, no "..".
var keepRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func parseSecretID(id string) (string, error) {
	if !keepRefRe.MatchString(id) || strings.Contains(id, "..") {
		return "", ErrKeepBadID
	}
	return id, nil
}

// refFor names a setting's secret inside the installation's namespace.
func refFor(ref Ref) string {
	name := ref.ExtensionID + "." + ref.Field
	if ref.WorkspaceID != uuid.Nil {
		name += "." + ref.WorkspaceID.String()
	}
	return name
}

func (c *HTTPKeepClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *HTTPKeepClient) nonce() (string, error) {
	if c.Nonce != nil {
		return c.Nonce()
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (c *HTTPKeepClient) do(ctx context.Context, method, path string, in, out any) error {
	var raw []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		raw = b
	}
	u, err := url.JoinPath(c.BaseURL, "orgs", c.OrgID.String(), path)
	if err != nil {
		return err
	}
	pu, err := url.Parse(u)
	if err != nil {
		return err
	}
	if pu.Scheme != "https" {
		return ErrKeepNotTLS
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	nonce, err := c.nonce()
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	ts := strconv.FormatInt(c.now().Unix(), 10)
	sum := sha256.Sum256(raw)
	mac := hmac.New(sha256.New, c.EventsSecret)
	mac.Write([]byte(keepSigTag + "\n" + ts + "\n" + nonce + "\n" + method + "\n" + pu.EscapedPath() + "\n" + hex.EncodeToString(sum[:])))
	req.Header.Set("X-Mesh-Workspace", c.WorkspaceID.String())
	req.Header.Set("X-Mesh-Timestamp", ts)
	req.Header.Set("X-Mesh-Nonce", nonce)
	req.Header.Set("X-Mesh-Signature", keepSigPref+hex.EncodeToString(mac.Sum(nil)))
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxKeepBody))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrKeepUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return ErrKeepNotFound
	case resp.StatusCode == http.StatusForbidden:
		return ErrKeepForbidden
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrKeepRateLimited
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if !hasNoStore(strings.Join(resp.Header.Values("Cache-Control"), ",")) {
		return ErrKeepCache
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// maxKeepValue is the largest value Keep accepts (1 byte to 32 KiB); a larger
// one is refused here instead of sending the secret to be rejected.
const maxKeepValue = 32 << 10

// ErrKeepValueSize is a value outside what Keep stores.
var ErrKeepValueSize = errors.New("keep: value must be 1 byte to 32 KiB")

// PutValue writes plain as a new version of the setting's secret and returns
// its ref; the settings row keeps "keep:<ref>".
func (c *HTTPKeepClient) PutValue(ctx context.Context, ref Ref, plain string) (string, error) {
	id, err := parseSecretID(refFor(ref))
	if err != nil {
		return "", err
	}
	if plain == "" || len(plain) > maxKeepValue {
		return "", ErrKeepValueSize
	}
	var put struct {
		Ref     string `json:"ref"`
		Version int    `json:"version"`
	}
	if err := c.do(ctx, http.MethodPut, "mesh/secrets/"+id, map[string]string{"value": plain}, &put); err != nil {
		return "", err
	}
	if put.Ref != id || put.Version < 1 {
		return "", errors.New("keep: unexpected put answer")
	}
	return id, nil
}

func (c *HTTPKeepClient) GetValue(ctx context.Context, secretID string) (string, error) {
	id, err := parseSecretID(secretID)
	if err != nil {
		return "", err
	}
	var out struct {
		Value string `json:"value"`
	}
	if derr := c.do(ctx, http.MethodPost, "mesh/secrets/"+id+"/resolve", nil, &out); derr != nil {
		return "", derr
	}
	if out.Value == "" {
		return "", errors.New("keep returned an empty value")
	}
	return out.Value, nil
}
