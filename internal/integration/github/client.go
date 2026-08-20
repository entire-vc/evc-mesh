// Package github is a deliberately narrow GitHub REST API client. It exists
// for exactly one call — "is this PR actually merged right now" — used by
// the done-evidence gate (internal/service/task_service.go) to verify a
// linked PR's live state instead of trusting whatever status Mesh cached the
// last time a webhook happened to arrive.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// PullRequestState is the live state of a pull request as reported by the
// GitHub REST API — distinct from domain.VCSLinkStatus, which is whatever
// Mesh has cached in vcs_links.status and may be stale.
type PullRequestState struct {
	// Merged is true only once the PR has actually been merged. GitHub's API
	// has no separate "merged" value for the `state` field (only open/closed),
	// so Merged is the field that matters here.
	Merged bool
	// State is GitHub's raw `state` value ("open" or "closed"), kept for
	// logging/debugging — callers should branch on Merged, not State.
	State string
}

// PullRequestChecker resolves the current state of a pull request directly
// from GitHub. Implemented by *Client; tests substitute a fake so the
// done-evidence gate's live-check branch doesn't need network access.
type PullRequestChecker interface {
	GetPullRequestState(ctx context.Context, owner, repo string, number int) (PullRequestState, error)
}

// defaultTimeout bounds a single live-check call so a slow/unreachable
// GitHub can't stall a move_task request indefinitely — the gate treats a
// timeout the same as any other failure: fall back to the cached status
// rather than block forever.
const defaultTimeout = 8 * time.Second

// Client is a minimal GitHub REST API v3 (v2022-11-28) client.
type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string // overridable in tests; defaults to https://api.github.com
}

// NewClient creates a Client. token may be empty — GitHub allows anonymous
// reads of public repos (rate-limited to 60 req/hour); pass a token (a
// classic PAT or fine-grained token with read access to Pull requests) to
// raise the limit and to read private repos, which is the common case here
// since entire-vc repos are private.
func NewClient(token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		token:      token,
		baseURL:    "https://api.github.com",
	}
}

type pullRequestResponse struct {
	State  string `json:"state"`
	Merged bool   `json:"merged"`
}

// GetPullRequestState calls GET /repos/{owner}/{repo}/pulls/{number}.
func (c *Client) GetPullRequestState(ctx context.Context, owner, repo string, number int) (PullRequestState, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.baseURL, owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return PullRequestState{}, fmt.Errorf("build github pr request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return PullRequestState{}, fmt.Errorf("github pr request %s/%s#%d: %w", owner, repo, number, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PullRequestState{}, fmt.Errorf("github pr request %s/%s#%d: unexpected status %d", owner, repo, number, resp.StatusCode)
	}

	var body pullRequestResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return PullRequestState{}, fmt.Errorf("decode github pr response %s/%s#%d: %w", owner, repo, number, err)
	}
	return PullRequestState{Merged: body.Merged, State: body.State}, nil
}

// prURLPattern matches a GitHub pull request URL, tolerating a trailing
// slash or any suffix after the number (e.g. "#discussion_r123", "/files")
// so a URL copy-pasted with a fragment still parses.
var prURLPattern = regexp.MustCompile(`^https?://github\.com/([^/\s]+)/([^/\s]+)/pull/(\d+)`)

// ParsePullRequestURL extracts (owner, repo, number) from a GitHub PR URL
// such as "https://github.com/entire-vc/evc-mesh/pull/123". Returns
// ok=false for anything that isn't a github.com PR URL — GitLab links,
// commit/branch links, or a malformed URL all fall through here rather than
// erroring, since the caller's fallback (use the cached status) is the
// correct behavior for a link this parser doesn't recognize.
func ParsePullRequestURL(rawURL string) (owner, repo string, number int, ok bool) {
	m := prURLPattern.FindStringSubmatch(rawURL)
	if m == nil {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, false
	}
	return m[1], m[2], n, true
}
