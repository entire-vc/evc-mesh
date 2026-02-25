// Package sdk provides a Go client for the Mesh REST API.
// Agents import this package to interact with workspaces, projects, tasks,
// comments, and the event bus without having to manage raw HTTP calls.
//
// Usage:
//
//	client, err := sdk.New("http://localhost:8005", "agk_mywk_xxx")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	tasks, err := client.GetMyTasks(ctx)
package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// APIError is returned when the server responds with a 4xx or 5xx status code.
type APIError struct {
	StatusCode int
	Message    string
	Details    string
	Validation map[string]string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("[%d] %s: %s", e.StatusCode, e.Message, e.Details)
	}
	return fmt.Sprintf("[%d] %s", e.StatusCode, e.Message)
}

// Is allows errors.Is comparisons by status code.
func (e *APIError) Is(target error) bool {
	t, ok := target.(*APIError)
	if !ok {
		return false
	}
	return t.StatusCode == e.StatusCode
}

// ErrNotFound is a sentinel *APIError for 404 responses.
var ErrNotFound = &APIError{StatusCode: http.StatusNotFound, Message: "not found"}

// ErrUnauthorized is a sentinel *APIError for 401 responses.
var ErrUnauthorized = &APIError{StatusCode: http.StatusUnauthorized, Message: "unauthorized"}

// ErrForbidden is a sentinel *APIError for 403 responses.
var ErrForbidden = &APIError{StatusCode: http.StatusForbidden, Message: "forbidden"}

// Client is the Mesh API client for agents.
// It authenticates with an agent key (X-Agent-Key header) and discovers
// the workspace and agent IDs automatically on construction.
type Client struct {
	baseURL    string
	agentKey   string
	httpClient *http.Client

	// Discovered on New() via GET /agents/me.
	wsID    string
	agentID string
}

// Option is a functional option for configuring a Client.
type Option func(*Client)

// WithHTTPClient replaces the default *http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// WithTimeout sets a custom timeout on the default HTTP client.
// Ignored if WithHTTPClient is also provided.
func WithTimeout(d time.Duration) Option {
	return func(cl *Client) {
		cl.httpClient.Timeout = d
	}
}

// New creates a new Mesh client. It validates the connection by calling
// GET /agents/me to discover workspace and agent IDs.
// baseURL must not have a trailing slash (e.g. "http://localhost:8005").
func New(baseURL, agentKey string, opts ...Option) (*Client, error) {
	c := &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		agentKey: agentKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}

	agent, err := c.Me(context.Background())
	if err != nil {
		return nil, fmt.Errorf("sdk.New: authenticate: %w", err)
	}
	c.wsID = agent.WorkspaceID
	c.agentID = agent.ID
	return c, nil
}

// WorkspaceID returns the workspace ID discovered during New().
func (c *Client) WorkspaceID() string { return c.wsID }

// AgentID returns the agent ID discovered during New().
func (c *Client) AgentID() string { return c.agentID }

// --- internal HTTP helpers ---

// do executes an HTTP request with JSON body (or nil) and returns the raw response.
func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Agent-Key", c.agentKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

// request executes an HTTP request and decodes the JSON response into result.
// On 4xx/5xx it returns an *APIError populated from the response body.
func (c *Client) request(ctx context.Context, method, path string, body, result any) error {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return parseAPIError(resp)
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	return c.request(ctx, http.MethodGet, path, nil, result)
}

func (c *Client) post(ctx context.Context, path string, body, result any) error {
	return c.request(ctx, http.MethodPost, path, body, result)
}

func (c *Client) patch(ctx context.Context, path string, body, result any) error {
	return c.request(ctx, http.MethodPatch, path, body, result)
}

func (c *Client) delete(ctx context.Context, path string) error {
	return c.request(ctx, http.MethodDelete, path, nil, nil)
}

// parseAPIError reads the API error envelope from the response body.
func parseAPIError(resp *http.Response) error {
	var envelope struct {
		Code       int               `json:"code"`
		Message    string            `json:"message"`
		Details    string            `json:"details"`
		Validation map[string]string `json:"validation"`
		Error      string            `json:"error"` // fallback field
	}
	_ = json.NewDecoder(resp.Body).Decode(&envelope)

	msg := envelope.Message
	if msg == "" {
		msg = envelope.Error
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}

	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    msg,
		Details:    envelope.Details,
		Validation: envelope.Validation,
	}
}
