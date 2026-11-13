package catalog

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Catalogue scopes. A default install follows the catalogue's latest; a scoped
// install came from an exact receipt (one id, one version) and stays on that
// version until an admin chooses another one.
const (
	ScopeDefault = "default"
	ScopeScoped  = "scoped"
)

// Errors of an exact-receipt fetch.
var (
	// ErrSelectionUnavailable is Spark's 404 for an exact selection: the
	// release is missing, incompatible, yanked, blocked, suspended or not
	// public. Spark does not say which.
	ErrSelectionUnavailable = errors.New("the catalog does not offer the selected release")
	// ErrBadSelection is a signed receipt that is not the one asked for.
	ErrBadSelection = errors.New("the catalog receipt does not match the selection")
	// ErrUnexpectedSelection is a selection receipt served where the default
	// catalogue was asked for. Spark signs both with the same key; a receipt
	// replayed in place of the catalogue would narrow its blocklist to one id.
	ErrUnexpectedSelection = errors.New("catalog response is an exact-selection receipt, not the default catalogue")
)

// Selection is the "selection" object of an exact receipt.
type Selection struct {
	Scope   string `json:"scope"`
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

// ExactReceipt is a verified exact receipt: the signed body, its identity and
// the one entry it binds (Latest is the selected release).
type ExactReceipt struct {
	Catalog       *Catalog
	Entry         *Entry
	ContentSHA256 string
	Signature     string
	FetchedAt     time.Time
}

// VerifyExact checks an exact receipt for id@version: the signature with key
// (exactly as Verify does for the default catalogue), the selection it states,
// and that it binds exactly one entry with exactly one release, the selected one.
func VerifyExact(key ed25519.PublicKey, body []byte, sig, id, version string) (*Catalog, *Entry, error) {
	cat, err := Verify(key, body, sig)
	if err != nil {
		return nil, nil, err
	}
	s := cat.Selection
	if s == nil || s.Scope != ScopeScoped || s.Slug != id || s.Version != version {
		return nil, nil, fmt.Errorf("%w: asked %s@%s, got %+v", ErrBadSelection, id, version, s)
	}
	if len(cat.Extensions) != 1 {
		return nil, nil, fmt.Errorf("%w: %d entries", ErrBadSelection, len(cat.Extensions))
	}
	e := &cat.Extensions[0]
	if e.ID != id || len(e.Releases) != 1 {
		return nil, nil, fmt.Errorf("%w: entry %q with %d releases", ErrBadSelection, e.ID, len(e.Releases))
	}
	r, l := e.Releases[0], e.Latest
	if r.Version != version || l.Version != version || r.CommitSHA != l.CommitSHA || r.MeshAPI != l.MeshAPI ||
		r.ArtifactSHA256 == nil || l.ArtifactSHA256 == nil || *r.ArtifactSHA256 != *l.ArtifactSHA256 || r.Yanked || l.Yanked {
		return nil, nil, fmt.Errorf("%w: release is not %s or its records disagree", ErrBadSelection, version)
	}
	return cat, e, nil
}

// FetchExact reads the exact receipt of id@version
// (catalog.json?scope=scoped&slug=&version=&mesh_api=) and verifies it with the
// instance's trusted key. It never touches the cached default catalogue.
// Spark's 404 is ErrSelectionUnavailable; any other failure is an error that
// says nothing about the release.
func (c *Client) FetchExact(ctx context.Context, meshAPI int, id, version string) (*ExactReceipt, error) {
	if !targetVersionPattern.MatchString(version) || strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: id and an exact x.y.z version are required", ErrBadSelection)
	}
	base := strings.TrimRight(strings.TrimSpace(c.baseURL()), "/")
	if base == "" {
		return nil, errors.New("spark url is not configured")
	}
	key, err := c.publicKey()
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("scope", ScopeScoped)
	q.Set("slug", id)
	q.Set("version", version)
	q.Set("mesh_api", strconv.Itoa(meshAPI))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+catalogPath+"?"+q.Encode(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSelectionUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog receipt answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, errors.New("catalog receipt too large")
	}
	sig := resp.Header.Get(SignatureHeader)
	cat, e, err := VerifyExact(key, body, sig, id, version)
	if err != nil {
		return nil, err
	}
	if et := resp.Header.Get("ETag"); et != "" && normalizeETag(et) != ETagOf(body) {
		return nil, fmt.Errorf("catalog receipt etag %s does not match the body", et)
	}
	return &ExactReceipt{Catalog: cat, Entry: e, ContentSHA256: strings.Trim(ETagOf(body), `"`), Signature: sig, FetchedAt: time.Now().UTC()}, nil
}
