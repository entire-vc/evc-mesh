// Package repoinstall installs and updates extensions straight from a GitHub or
// GitLab repository (BRAT mode): it reads mesh-extension.json at a tag or a
// branch through the host's API, validates it and keeps the result at trust
// level custom.
package repoinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ManifestFile is the file the installer reads at the root of the repository.
const ManifestFile = "mesh-extension.json"

const (
	maxManifestBytes = 256 << 10
	maxAPIBytes      = 2 << 20
)

// Provider is the hosting flavour of a repository.
type Provider string

const (
	ProviderGitHub Provider = "github"
	ProviderGitLab Provider = "gitlab"
)

// Errors a caller maps to an answer.
var (
	ErrBadURL       = errors.New("repository url is not supported")
	ErrNotFound     = errors.New("repository, ref or mesh-extension.json not found")
	ErrUnauthorized = errors.New("repository access denied: check the token")
)

// Source is a parsed repository URL.
type Source struct {
	Provider Provider
	Scheme   string
	Host     string
	Path     string // owner/repo (GitHub) or group[/sub]/project (GitLab)
}

// Ref is a tag or a branch with the commit it points at.
type Ref struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// Client talks to the GitHub and GitLab APIs.
type Client struct {
	HTTP *http.Client
	// AllowInsecure permits http:// repository URLs and private addresses.
	// Production leaves it off; tests need it for httptest servers.
	AllowInsecure bool
	// GitHubAPI overrides https://api.github.com (tests).
	GitHubAPI string
}

// NewClient returns a client that refuses to connect to loopback, private and
// link-local addresses: the repository URL is admin input, but the server must
// not become a proxy into its own network.
func NewClient() *Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("address %s is not allowed", host)
		}
		return nil
	}}
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: d.DialContext, Proxy: nil}}}
}

// ParseURL turns a repository URL into a Source. github.com is GitHub, any
// other host is treated as GitLab (gitlab.com or self-hosted).
func (c *Client) ParseURL(raw string) (Source, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Source{}, ErrBadURL
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !c.AllowInsecure) {
		return Source{}, fmt.Errorf("%w: https is required", ErrBadURL)
	}
	p := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	parts := strings.Split(p, "/")
	for _, s := range parts {
		if s == "" || s == "." || s == ".." {
			return Source{}, ErrBadURL
		}
	}
	src := Source{Scheme: u.Scheme, Host: u.Host, Path: p, Provider: ProviderGitLab}
	if strings.EqualFold(u.Hostname(), "github.com") {
		src.Provider = ProviderGitHub
		if len(parts) != 2 {
			return Source{}, fmt.Errorf("%w: expected https://github.com/<owner>/<repo>", ErrBadURL)
		}
	} else if len(parts) < 2 {
		return Source{}, fmt.Errorf("%w: expected <host>/<group>/<project>", ErrBadURL)
	}
	return src, nil
}

func (c *Client) get(ctx context.Context, src Source, token, rawURL, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "evc-mesh-extension-installer")
	if token != "" {
		if src.Provider == ProviderGitHub {
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		} else {
			req.Header.Set("PRIVATE-TOKEN", token)
		}
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	if token != "" {
		// PRIVATE-TOKEN is a custom header that net/http would forward across
		// hosts; never follow a redirect away from the host the token was meant for.
		clone := *hc
		clone.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
			if !strings.EqualFold(next.URL.Host, req.URL.Host) || next.URL.Scheme != req.URL.Scheme {
				return fmt.Errorf("refusing to follow a redirect to another host or scheme")
			}
			return nil
		}
		hc = &clone
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("repository host unreachable: %w", scrub(err, token))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read repository response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("repository response is larger than %d bytes", limit)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		if token == "" {
			return nil, fmt.Errorf("%w (a private repository needs a token)", ErrNotFound)
		}
		return nil, ErrNotFound
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("repository host answered %d", resp.StatusCode)
	}
	return body, nil
}

// scrub keeps a token out of an error string.
func scrub(err error, token string) error {
	if token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "***"))
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	return strings.Join(segs, "/")
}

func (c *Client) githubBase() string {
	if c.GitHubAPI != "" {
		return strings.TrimRight(c.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

func (c *Client) gitlabProject(src Source) string {
	return fmt.Sprintf("%s://%s/api/v4/projects/%s", src.Scheme, src.Host, url.PathEscape(src.Path))
}

// Manifest reads mesh-extension.json at ref.
func (c *Client) Manifest(ctx context.Context, src Source, ref, token string) ([]byte, error) {
	if src.Provider == ProviderGitHub {
		u := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", c.githubBase(), escapePath(src.Path), ManifestFile, url.QueryEscape(ref))
		return c.get(ctx, src, token, u, "application/vnd.github.raw+json", maxManifestBytes)
	}
	u := fmt.Sprintf("%s/repository/files/%s/raw?ref=%s", c.gitlabProject(src), url.PathEscape(ManifestFile), url.QueryEscape(ref))
	return c.get(ctx, src, token, u, "text/plain", maxManifestBytes)
}

// Commit resolves ref (tag or branch) to a commit sha.
func (c *Client) Commit(ctx context.Context, src Source, ref, token string) (string, error) {
	if src.Provider == ProviderGitHub {
		u := fmt.Sprintf("%s/repos/%s/commits/%s", c.githubBase(), escapePath(src.Path), escapePath(ref))
		b, err := c.get(ctx, src, token, u, "application/vnd.github.sha", 256)
		return strings.TrimSpace(string(b)), err
	}
	u := fmt.Sprintf("%s/repository/commits/%s", c.gitlabProject(src), url.PathEscape(ref))
	b, err := c.get(ctx, src, token, u, "application/json", maxAPIBytes)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("unexpected commit response from repository host")
	}
	return out.ID, nil
}

type refItem struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
		ID  string `json:"id"`
	} `json:"commit"`
}

const refPageSize = 100

func (c *Client) listRefs(ctx context.Context, src Source, token, kind string) ([]Ref, error) {
	out := []Ref{}
	prevFirst := ""
	// Walk every page until a short one; the request context bounds the loop.
	// A host that ignores the page parameter repeats a page, which is an error
	// rather than a silently partial or endless listing.
	for page := 1; ; page++ {
		var u string
		if src.Provider == ProviderGitHub {
			u = fmt.Sprintf("%s/repos/%s/%s?per_page=%d&page=%d", c.githubBase(), escapePath(src.Path), kind, refPageSize, page)
		} else {
			u = fmt.Sprintf("%s/repository/%s?per_page=%d&page=%d", c.gitlabProject(src), kind, refPageSize, page)
		}
		b, err := c.get(ctx, src, token, u, "application/json", maxAPIBytes)
		if err != nil {
			return nil, err
		}
		var items []refItem
		if err := json.Unmarshal(b, &items); err != nil {
			return nil, fmt.Errorf("unexpected %s response from repository host", kind)
		}
		if len(items) > 0 {
			if page > 1 && items[0].Name == prevFirst {
				return nil, fmt.Errorf("repository host ignored pagination while listing %s", kind)
			}
			prevFirst = items[0].Name
		}
		for _, it := range items {
			sha := it.Commit.SHA
			if sha == "" {
				sha = it.Commit.ID
			}
			out = append(out, Ref{Name: it.Name, SHA: sha})
		}
		if len(items) < refPageSize {
			return out, nil
		}
	}
}

// Tags lists all repository tags (paginated).
func (c *Client) Tags(ctx context.Context, src Source, token string) ([]Ref, error) {
	return c.listRefs(ctx, src, token, "tags")
}

// Branches lists all repository branches (paginated).
func (c *Client) Branches(ctx context.Context, src Source, token string) ([]Ref, error) {
	return c.listRefs(ctx, src, token, "branches")
}
