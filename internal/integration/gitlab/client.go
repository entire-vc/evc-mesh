// Package gitlab is a deliberately narrow GitLab REST API v4 client. It
// exists for exactly one call — "is this MR actually merged right now" —
// used by the done-evidence gate (internal/service/task_service.go) to
// verify a linked GitLab merge request's live state instead of trusting
// whatever status Mesh cached the last time a webhook happened to arrive.
// Mirrors internal/integration/github/client.go; the two providers diverge
// only where GitLab's API actually differs (self-hosted base URL instead of
// a fixed host, PRIVATE-TOKEN auth instead of Bearer, "/-/merge_requests/"
// URL grammar, and a numeric project-scoped iid instead of a global number).
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	urlpkg "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MergeRequestState is the live state of a merge request as reported by the
// GitLab REST API — distinct from domain.VCSLinkStatus, which is whatever
// Mesh has cached in vcs_links.status and may be stale.
type MergeRequestState struct {
	// Merged is true only once the MR has actually been merged. GitLab's
	// `state` field has a dedicated "merged" value (unlike GitHub's
	// open/closed-only `state`), so this is a direct read, not a derived one.
	Merged bool
	// State is GitLab's raw `state` value ("opened", "closed", "merged", or
	// "locked" — locked is a transient state while GitLab processes a merge),
	// kept for logging/debugging — callers should branch on Merged, not State.
	State string
}

// MergeRequestChecker resolves the current state of a merge request directly
// from GitLab. Implemented by *Client; tests substitute a fake so the
// done-evidence gate's live-check branch doesn't need network access.
type MergeRequestChecker interface {
	GetMergeRequestState(ctx context.Context, projectPath string, iid int) (MergeRequestState, error)
}

// defaultTimeout bounds a single live-check call so a slow/unreachable
// GitLab instance can't stall a move_task request indefinitely — the gate
// treats a timeout the same as any other failure: fall back to the cached
// status rather than block forever.
const defaultTimeout = 8 * time.Second

// Client is a minimal GitLab REST API v4 client.
type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string // e.g. "https://git.entire.host" — self-hosted, so unlike GitHub there is no fixed default.
}

// NewClient creates a Client for a self-hosted GitLab instance at baseURL
// (e.g. "https://git.entire.host", scheme required, no trailing slash
// needed — one is stripped if present). token authenticates as a GitLab
// personal/project access token; our self-hosted instance has no public
// anonymous-read equivalent to GitHub's 60 req/hour (a private project
// returns 404 — GitLab deliberately does not distinguish "doesn't exist"
// from "not for you" — to an unauthenticated request), so unlike the GitHub
// client an empty token here means every private-project lookup fails, not
// merely a lower rate limit.
func NewClient(baseURL, token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		token:      token,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

type mergeRequestResponse struct {
	State string `json:"state"`
}

// GetMergeRequestState calls GET /api/v4/projects/:id/merge_requests/:iid,
// where :id is the URL-encoded "namespace/project" path (GitLab accepts
// either the numeric project id or this URL-encoded path form; the path
// form is what a merge request's own web URL already gives us, so no extra
// lookup is needed to resolve a numeric id first).
func (c *Client) GetMergeRequestState(ctx context.Context, projectPath string, iid int) (MergeRequestState, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d", c.baseURL, urlpkg.PathEscape(projectPath), iid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return MergeRequestState{}, fmt.Errorf("build gitlab mr request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		// GitLab's documented header for personal/project access tokens.
		// (Authorization: Bearer is also accepted for OAuth2/CI job tokens,
		// but PRIVATE-TOKEN is the canonical form for the PAT this client
		// authenticates with.)
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return MergeRequestState{}, fmt.Errorf("gitlab mr request %s!%d: %w", projectPath, iid, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return MergeRequestState{}, fmt.Errorf("gitlab mr request %s!%d: unexpected status %d", projectPath, iid, resp.StatusCode)
	}

	var body mergeRequestResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return MergeRequestState{}, fmt.Errorf("decode gitlab mr response %s!%d: %w", projectPath, iid, err)
	}
	return MergeRequestState{Merged: body.State == "merged", State: body.State}, nil
}

// mrURLPattern matches a GitLab merge request URL's "/-/merge_requests/N"
// grammar on ANY host (self-hosted, unlike github.com there is no single
// fixed domain to anchor on) and captures the project path — everything
// between the host and the literal "/-/merge_requests/" segment, which may
// itself contain slashes for a namespaced/subgroup project (e.g.
// "entire-vc/evc-mesh", or "group/subgroup/project"). The actual API call
// always targets the ONE configured self-hosted instance (Client.baseURL),
// never a host read out of the URL string — mirrors ParsePullRequestURL's
// stance of trusting only a fixed, operator-configured endpoint.
var mrURLPattern = regexp.MustCompile(`^https?://[^/\s]+/(.+?)/-/merge_requests/(\d+)`)

// ParseMergeRequestURL extracts (projectPath, iid) from a GitLab merge
// request URL such as "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/123".
// Returns ok=false for anything that doesn't match GitLab's "-/merge_requests/"
// URL grammar — GitHub links, commit/branch links, or a malformed URL all
// fall through here rather than erroring, since the caller's fallback (use
// the cached status) is the correct behavior for a link this parser doesn't
// recognize.
func ParseMergeRequestURL(rawURL string) (projectPath string, iid int, ok bool) {
	m := mrURLPattern.FindStringSubmatch(rawURL)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}
