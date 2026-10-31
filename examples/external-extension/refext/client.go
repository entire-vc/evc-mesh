package refext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client calls the Mesh API as the extension actor (ext:<id>@<workspace>) with
// the token Mesh delivered to the token path. Every route it calls must be
// mapped for extensions on the Mesh side and covered by a capability the
// manifest declares; anything else answers 403.
type Client struct {
	BaseURL string // Mesh origin, e.g. https://mesh.example.com
	HTTP    *http.Client
}

// APIRoutes is every Mesh route Client calls, written as Mesh's route table
// writes them ("METHOD /pattern"). Mesh's contract tests check each one is
// open to extensions under a capability the manifest declares.
var APIRoutes = []string{
	"GET /api/v1/ext/me",
	"POST /api/v1/tasks/:task_id/comments",
}

// Me is the answer of GET /api/v1/ext/me.
type Me struct {
	Actor        string              `json:"actor"`
	ActorID      string              `json:"actor_id"`
	ExtensionID  string              `json:"extension_id"`
	ConnectionID string              `json:"connection_id"`
	WorkspaceID  string              `json:"workspace_id"`
	Capabilities map[string][]string `json:"capabilities"`
}

// APIError is a non-2xx answer of Mesh in its standard error envelope.
type APIError struct {
	Status    int    `json:"code"`
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("mesh answered %d %s: %s", e.Status, e.ErrorCode, e.Message)
}

// Me returns who the token belongs to and what it may do.
func (c *Client) Me(ctx context.Context, token string) (*Me, error) {
	var out Me
	if err := c.do(ctx, token, http.MethodGet, "/api/v1/ext/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddComment posts a comment on a task (capability api comments:write).
func (c *Client) AddComment(ctx context.Context, token, taskID, body string) error {
	return c.do(ctx, token, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/comments",
		map[string]any{"body": body}, nil)
}

func (c *Client) do(ctx context.Context, token, method, path string, in, out any) error {
	if token == "" {
		return errors.New("no extension token for this workspace yet")
	}
	var body io.Reader = http.NoBody
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, apiErr)
		apiErr.Status = resp.StatusCode
		return apiErr
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
