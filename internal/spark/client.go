// Package spark provides a client for the Spark agent catalog API.
// When the Spark API is unreachable, methods return empty results rather than errors
// to allow graceful degradation.
package spark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	defaultLimit   = 20
)

// AgentManifest represents an agent listing from the Spark catalog.
type AgentManifest struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	AgentType    string            `json:"agent_type"` // claude_code, openclaw, cline, aider, custom
	Version      string            `json:"version"`
	Author       string            `json:"author"`
	Capabilities map[string]any    `json:"capabilities"`
	Config       map[string]any    `json:"config"` // template config for local install
	Tags         []string          `json:"tags"`
	Downloads    int               `json:"downloads"`
	Rating       float64           `json:"rating"`
	CreatedAt    string            `json:"created_at"`
}

// Client wraps HTTP calls to the Spark agent catalog API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Spark catalog client with the given base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// Search returns agents matching the query and optional tag filters.
// Returns empty slice (not error) when Spark is unreachable.
func (c *Client) Search(ctx context.Context, query string, tags []string, limit int) ([]AgentManifest, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	if len(tags) > 0 {
		params.Set("tags", strings.Join(tags, ","))
	}
	params.Set("limit", fmt.Sprintf("%d", limit))

	endpoint := fmt.Sprintf("%s/api/v1/agents?%s", c.baseURL, params.Encode())

	var result struct {
		Items []AgentManifest `json:"items"`
	}

	if err := c.get(ctx, endpoint, &result); err != nil {
		// Graceful degradation: log-worthy but not fatal.
		return []AgentManifest{}, nil
	}

	if result.Items == nil {
		return []AgentManifest{}, nil
	}
	return result.Items, nil
}

// GetByID returns a single agent manifest from the Spark catalog.
// Returns nil (not error) when Spark is unreachable.
func (c *Client) GetByID(ctx context.Context, id string) (*AgentManifest, error) {
	endpoint := fmt.Sprintf("%s/api/v1/agents/%s", c.baseURL, url.PathEscape(id))

	var manifest AgentManifest
	if err := c.get(ctx, endpoint, &manifest); err != nil {
		return nil, fmt.Errorf("spark: get agent %s: %w", id, err)
	}

	return &manifest, nil
}

// ListPopular returns the most downloaded agents from the Spark catalog.
// Returns empty slice (not error) when Spark is unreachable.
func (c *Client) ListPopular(ctx context.Context, limit int) ([]AgentManifest, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	params := url.Values{}
	params.Set("sort", "downloads")
	params.Set("limit", fmt.Sprintf("%d", limit))

	endpoint := fmt.Sprintf("%s/api/v1/agents?%s", c.baseURL, params.Encode())

	var result struct {
		Items []AgentManifest `json:"items"`
	}

	if err := c.get(ctx, endpoint, &result); err != nil {
		// Graceful degradation.
		return []AgentManifest{}, nil
	}

	if result.Items == nil {
		return []AgentManifest{}, nil
	}
	return result.Items, nil
}

// get performs an HTTP GET request and decodes the JSON response into dest.
// Returns an error if the request fails, the response is non-2xx, or decoding fails.
func (c *Client) get(ctx context.Context, endpoint string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}
