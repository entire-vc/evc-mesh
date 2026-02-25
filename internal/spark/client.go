// Package spark provides a client for the Spark agent catalog API.
// When the Spark API is unreachable, methods return empty results rather than errors
// to allow graceful degradation.
//
// Spark API lives at /api/v1/assets (not /agents). The client maps Spark's
// AssetListOut / AssetOut response shapes to the AgentManifest struct that
// Mesh understands.
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

// AgentManifest represents an agent listing from the Spark catalog,
// normalised to the shape the Mesh frontend expects.
type AgentManifest struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	AgentType    string         `json:"agent_type"` // claude_code, openclaw, cline, aider, custom
	Version      string         `json:"version"`
	Author       string         `json:"author"`
	Capabilities map[string]any `json:"capabilities"`
	Config       map[string]any `json:"config"` // template config for local install
	Tags         []string       `json:"tags"`
	Downloads    int            `json:"downloads"`
	Rating       float64        `json:"rating"`
	CreatedAt    string         `json:"created_at"`
}

// sparkAssetListItem mirrors Spark's AssetListOut schema (list endpoints).
type sparkAssetListItem struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"` // agent, skill, prompt, etc.
	Title            string   `json:"title"`
	Slug             string   `json:"slug"`
	ShortDescription string   `json:"short_description"`
	AITags           []string `json:"ai_tags"`
	AuthorUserID     string   `json:"author_user_id"`
	PricingType      string   `json:"pricing_type"`
	DownloadsCount   int      `json:"downloads_count"`
	RatingAvg        float64  `json:"rating_avg"`
	RatingCount      int      `json:"rating_count"`
	IsFeatured       bool     `json:"is_featured"`
	IsVerified       bool     `json:"is_verified"`
	CreatedAt        string   `json:"created_at"`
}

// sparkAssetDetail mirrors Spark's AssetOut schema (single-item endpoint).
type sparkAssetDetail struct {
	sparkAssetListItem
	DescriptionMD string         `json:"description_md"`
	Version       string         `json:"version"`
	AuthorName    string         `json:"author_name"`
	InlineContent string         `json:"inline_content"`
	MetadataJSON  map[string]any `json:"metadata_json"`
}

func (a *sparkAssetListItem) toManifest() AgentManifest {
	return AgentManifest{
		ID:          a.ID,
		Name:        a.Title,
		Description: a.ShortDescription,
		AgentType:   a.Type,
		Author:      a.AuthorUserID,
		Tags:        a.AITags,
		Downloads:   a.DownloadsCount,
		Rating:      a.RatingAvg,
		CreatedAt:   a.CreatedAt,
	}
}

func (a *sparkAssetDetail) toManifest() AgentManifest {
	m := a.sparkAssetListItem.toManifest()
	m.Version = a.Version
	if a.AuthorName != "" {
		m.Author = a.AuthorName
	}
	if a.MetadataJSON != nil {
		m.Capabilities = a.MetadataJSON
	}
	return m
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
	params.Set("asset_type", "agent")
	params.Set("page_size", fmt.Sprintf("%d", limit))
	if query != "" {
		params.Set("q", query)
	}
	if len(tags) > 0 {
		params.Set("ai", strings.Join(tags, ","))
	}

	endpoint := fmt.Sprintf("%s/api/v1/assets?%s", c.baseURL, params.Encode())

	var result struct {
		Items []sparkAssetListItem `json:"items"`
	}

	if err := c.get(ctx, endpoint, &result); err != nil {
		// Graceful degradation: log-worthy but not fatal.
		return []AgentManifest{}, nil
	}

	out := make([]AgentManifest, 0, len(result.Items))
	for _, item := range result.Items {
		out = append(out, item.toManifest())
	}
	return out, nil
}

// GetByID returns a single agent manifest from the Spark catalog.
// Accepts a Spark asset ID or slug.
// Returns nil (not error) when Spark is unreachable.
func (c *Client) GetByID(ctx context.Context, id string) (*AgentManifest, error) {
	endpoint := fmt.Sprintf("%s/api/v1/assets/%s", c.baseURL, url.PathEscape(id))

	var detail sparkAssetDetail
	if err := c.get(ctx, endpoint, &detail); err != nil {
		return nil, fmt.Errorf("spark: get agent %s: %w", id, err)
	}

	m := detail.toManifest()
	return &m, nil
}

// ListPopular returns the most downloaded agents from the Spark catalog.
// Returns empty slice (not error) when Spark is unreachable.
func (c *Client) ListPopular(ctx context.Context, limit int) ([]AgentManifest, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	params := url.Values{}
	params.Set("asset_type", "agent")
	params.Set("sort", "popular")
	params.Set("page_size", fmt.Sprintf("%d", limit))

	endpoint := fmt.Sprintf("%s/api/v1/assets?%s", c.baseURL, params.Encode())

	var result struct {
		Items []sparkAssetListItem `json:"items"`
	}

	if err := c.get(ctx, endpoint, &result); err != nil {
		// Graceful degradation.
		return []AgentManifest{}, nil
	}

	out := make([]AgentManifest, 0, len(result.Items))
	for _, item := range result.Items {
		out = append(out, item.toManifest())
	}
	return out, nil
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
