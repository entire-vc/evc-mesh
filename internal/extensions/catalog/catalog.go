// Package catalog reads the signed Mesh extensions catalog published by Spark
// (GET /api/v1/mesh-extensions/catalog.json). The body is trusted only when
// X-Catalog-Signature, an Ed25519 signature over the exact response bytes,
// verifies against a key the instance already holds. A failed fetch or check
// never replaces the last verified copy.
package catalog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PinnedPublicKey is the production catalog key (raw 32 bytes, standard
// base64), handed over in the E4.6 contract. An instance setting can replace
// it; the key is never taken from the catalog response itself.
const PinnedPublicKey = "N4QB0lrXXelVPEqvx9oEWxOgtMamKoTQh5yRXQZKUhg="

const (
	// SignatureHeader carries the base64 Ed25519 signature of the body.
	SignatureHeader = "X-Catalog-Signature"
	// RefreshInterval is how often the catalog is re-read.
	RefreshInterval = time.Hour
	maxBodyBytes    = 8 << 20
	fetchTimeout    = 15 * time.Second
	catalogPath     = "/api/v1/mesh-extensions/catalog.json"
)

// Errors a caller can tell apart.
var (
	ErrBadKey       = errors.New("catalog public key is not a base64 ed25519 key")
	ErrNoSignature  = errors.New("catalog response has no signature")
	ErrBadSignature = errors.New("catalog signature does not verify")
	ErrBadSchema    = errors.New("catalog schema is not supported")
	ErrNoCatalog    = errors.New("no verified catalog copy yet")
)

// Release is one published version of an extension.
type Release struct {
	Version        string  `json:"version"`
	MeshAPI        string  `json:"mesh_api"`
	CommitSHA      string  `json:"commit_sha"`
	ArtifactSHA256 *string `json:"artifact_sha256"`
	// Yanked marks a release Spark withdrew. Spark's catalog normally leaves
	// such releases out; a release that still carries the flag is never offered.
	Yanked bool `json:"yanked,omitempty"`
	// Capabilities is Spark's JSON object of declared permissions (api,
	// events, webhooks_in, ui, secrets), kept raw: Flatten reads it.
	Capabilities json.RawMessage `json:"capabilities"`
}

// Flatten renders the declared permissions as "<group>:<value>" strings, the
// shape repoinstall.Capabilities uses, so an update can be compared with what
// is installed. Only the permission groups are read: other keys Spark adds
// (such as the "network" object) are skipped. Anything that is not an object,
// or a permission group that is not a list of strings, yields nil. A null
// group is an empty list: it grants nothing and must not hide the other groups.
func (r Release) Flatten() []string {
	var groups map[string]json.RawMessage
	if len(r.Capabilities) == 0 || json.Unmarshal(r.Capabilities, &groups) != nil {
		return nil
	}
	var out []string
	for _, g := range []string{"api", "events", "webhooks_in", "ui", "secrets"} {
		raw, ok := groups[g]
		if !ok {
			continue
		}
		var vals []string
		if json.Unmarshal(raw, &vals) != nil {
			return nil
		}
		for _, v := range vals {
			out = append(out, g+":"+v)
		}
	}
	return out
}

// Entry is one extension in the catalog.
type Entry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Runtime   string    `json:"runtime"`
	Category  string    `json:"category"`
	Trust     string    `json:"trust"`
	Publisher string    `json:"publisher"`
	Latest    Release   `json:"latest"`
	Releases  []Release `json:"releases"`
	PageURL   string    `json:"page_url"`
}

// Block is a blocklist record. An empty Version blocks every version.
type Block struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason"`
	Since   string `json:"since"`
}

// Catalog is the decoded, signature-verified body.
type Catalog struct {
	Schema      int     `json:"schema"`
	GeneratedAt string  `json:"generated_at"`
	Extensions  []Entry `json:"extensions"`
	Blocklist   []Block `json:"blocklist"`
}

// Blocks reports whether id@version is on the blocklist.
func (c *Catalog) Blocks(id, version string) (Block, bool) {
	if c == nil {
		return Block{}, false
	}
	for _, b := range c.Blocklist {
		if b.ID == id && (b.Version == "" || b.Version == version) {
			return b, true
		}
	}
	return Block{}, false
}

// Entry returns the catalog entry with the given id.
func (c *Catalog) Entry(id string) (*Entry, bool) {
	if c == nil {
		return nil, false
	}
	for i := range c.Extensions {
		if c.Extensions[i].ID == id {
			return &c.Extensions[i], true
		}
	}
	return nil, false
}

// ParseKey decodes a standard-base64 raw Ed25519 public key.
func ParseKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrBadKey
	}
	return ed25519.PublicKey(raw), nil
}

// Verify checks sig (base64) over the exact body bytes and decodes the body.
// Decoding happens only after the signature holds.
func Verify(key ed25519.PublicKey, body []byte, sig string) (*Catalog, error) {
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return nil, ErrNoSignature
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(key, body, raw) {
		return nil, ErrBadSignature
	}
	var c Catalog
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("catalog body: %w", err)
	}
	if c.Schema != 1 {
		return nil, fmt.Errorf("%w: %d", ErrBadSchema, c.Schema)
	}
	return &c, nil
}

// ETagOf is the ETag Spark derives from a body: quoted SHA-256 of its bytes.
func ETagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// normalizeETag strips the weak prefix and the "-gzip" suffix that nginx-style
// proxies add when they compress, leaving the quoted content hash. The body is
// still authenticated by its signature; this only keeps the middlebox check
// from rejecting a legitimate compressed hop.
func normalizeETag(et string) string {
	et = strings.TrimPrefix(et, "W/")
	if strings.HasSuffix(et, `-gzip"`) {
		return strings.TrimSuffix(et, `-gzip"`) + `"`
	}
	return et
}

// Client fetches and caches the catalog. Safe for concurrent use.
type Client struct {
	HTTP    *http.Client
	baseURL func() string
	key     func() string
	// OnRefresh runs after a refresh with the catalog that was just accepted.
	OnRefresh func(ctx context.Context, c *Catalog)

	// refreshMu serializes every refresh, regular and forced.
	refreshMu sync.Mutex

	mu         sync.RWMutex
	notifyOwed bool // a forced refresh accepted a catalog OnRefresh has not seen
	current    *Catalog
	etag       string
	signature  string
	body       []byte
	fetchedAt  time.Time
	lastErr    error
}

// NewClient builds a client. baseURL and key are read on every refresh so an
// instance setting change applies without a restart; an empty key falls back
// to PinnedPublicKey.
func NewClient(baseURL, key func() string) *Client {
	return &Client{HTTP: &http.Client{Timeout: fetchTimeout}, baseURL: baseURL, key: key}
}

// Current is the last verified catalog, or ErrNoCatalog.
func (c *Client) Current() (*Catalog, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current == nil {
		return nil, ErrNoCatalog
	}
	return c.current, nil
}

// Status is the freshness of the cache, for the admin screen.
func (c *Client) Status() (fetchedAt time.Time, lastErr error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fetchedAt, c.lastErr
}

func (c *Client) publicKey() (ed25519.PublicKey, error) {
	k := PinnedPublicKey
	if c.key != nil {
		if v := strings.TrimSpace(c.key()); v != "" {
			k = v
		}
	}
	return ParseKey(k)
}

// Refresh reads the catalog once. On any failure the previous verified copy
// stays in place and the error is returned (and logged).
func (c *Client) Refresh(ctx context.Context, meshAPI int) error {
	return c.refreshAndRecord(ctx, meshAPI, true)
}

func (c *Client) refreshAndRecord(ctx context.Context, meshAPI int, notify bool) error {
	// One refresh at a time: a slow older response must not overwrite the
	// cache after a newer one, and an admin call cannot pile onto Spark.
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	err := c.refresh(ctx, meshAPI, notify)
	c.mu.Lock()
	c.lastErr = err
	c.mu.Unlock()
	if err != nil {
		log.Printf("[extensions] catalog refresh failed, keeping previous copy: %v", err)
	}
	return err
}

func (c *Client) refresh(ctx context.Context, meshAPI int, notify bool) error {
	base := strings.TrimRight(strings.TrimSpace(c.baseURL()), "/")
	if base == "" {
		return errors.New("spark url is not configured")
	}
	key, err := c.publicKey()
	if err != nil {
		return err
	}
	u := base + catalogPath + "?mesh_api=" + url.QueryEscape(strconv.Itoa(meshAPI))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}
	// Ask for the bytes Spark signed. With Go's implicit gzip the proxy in
	// front of Spark rewrites the ETag to `"<sha>-gzip"` and the transport
	// hands us the decoded body, so the content hash check below would fail.
	req.Header.Set("Accept-Encoding", "identity")
	c.mu.RLock()
	haveCopy := c.current != nil
	if haveCopy && c.etag != "" {
		req.Header.Set("If-None-Match", c.etag)
	}
	c.mu.RUnlock()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if !haveCopy {
			return errors.New("catalog answered 304 without a cached copy")
		}
		c.mu.Lock()
		c.fetchedAt = time.Now()
		cur, owed := c.current, c.notifyOwed
		if notify {
			c.notifyOwed = false
		}
		c.mu.Unlock()
		// A forced refresh accepted a catalog without telling OnRefresh; the
		// next regular refresh delivers it even though the body is unchanged.
		if notify && owed && c.OnRefresh != nil {
			c.OnRefresh(ctx, cur)
		}
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("catalog answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxBodyBytes {
		return errors.New("catalog body too large")
	}
	sig := resp.Header.Get(SignatureHeader)
	cat, err := Verify(key, body, sig)
	if err != nil {
		return err
	}
	// ETag is a content hash; a mismatch means a middlebox changed the body.
	if et := resp.Header.Get("ETag"); et != "" && !bytes.Equal([]byte(normalizeETag(et)), []byte(ETagOf(body))) {
		return fmt.Errorf("catalog etag %s does not match the body", et)
	}
	c.mu.Lock()
	c.current, c.body, c.signature = cat, body, sig
	c.etag = ETagOf(body)
	c.fetchedAt = time.Now()
	c.notifyOwed = !notify
	c.mu.Unlock()
	if notify && c.OnRefresh != nil {
		c.OnRefresh(ctx, cat)
	}
	return nil
}

// ReceiptRelease is the identity of one extension's latest release as the
// accepted catalog states it.
type ReceiptRelease struct {
	ID             string  `json:"id"`
	Version        string  `json:"version"`
	CommitSHA      string  `json:"commit_sha"`
	ArtifactSHA256 *string `json:"artifact_sha256"`
}

// Receipt describes the verified copy the cache holds after a refresh. It is
// read back from the cache, so it reports what Current() serves, not what the
// upstream answered.
type Receipt struct {
	// FetchedAt is when the cache last accepted (or revalidated) the catalog.
	FetchedAt time.Time `json:"fetched_at"`
	// ContentSHA256 is the hex sha256 of the signed body.
	ContentSHA256 string           `json:"content_sha256"`
	Signature     string           `json:"signature"`
	GeneratedAt   string           `json:"generated_at"`
	Extensions    []ReceiptRelease `json:"extensions"`
}

// Receipt is the identity of the cached verified copy, or ErrNoCatalog.
func (c *Client) Receipt() (Receipt, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current == nil {
		return Receipt{}, ErrNoCatalog
	}
	r := Receipt{
		FetchedAt:     c.fetchedAt.UTC(),
		ContentSHA256: strings.Trim(c.etag, `"`),
		Signature:     c.signature,
		GeneratedAt:   c.current.GeneratedAt,
		Extensions:    make([]ReceiptRelease, 0, len(c.current.Extensions)),
	}
	for _, e := range c.current.Extensions {
		r.Extensions = append(r.Extensions, ReceiptRelease{
			ID: e.ID, Version: e.Latest.Version, CommitSHA: e.Latest.CommitSHA, ArtifactSHA256: e.Latest.ArtifactSHA256,
		})
	}
	return r, nil
}

// RefreshNow forces one refresh and returns the receipt of the copy now in the
// cache. It does not call OnRefresh: blocklist enforcement toggles installed
// extensions and syncs the runtime, which an admin read must not do. The next
// regular refresh (hourly tick) delivers the accepted catalog to OnRefresh. When the refresh fails (upstream down, bad signature) the previous
// verified copy stays in place: the receipt then describes that last-good copy
// and the error says why it was not replaced.
func (c *Client) RefreshNow(ctx context.Context, meshAPI int) (Receipt, error) {
	err := c.refreshAndRecord(ctx, meshAPI, false)
	rc, rerr := c.Receipt()
	if err == nil {
		err = rerr
	}
	return rc, err
}

// Run refreshes immediately and then every RefreshInterval until ctx ends.
func (c *Client) Run(ctx context.Context, meshAPI int) {
	_ = c.Refresh(ctx, meshAPI)
	t := time.NewTicker(RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.Refresh(ctx, meshAPI)
		}
	}
}
