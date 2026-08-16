//go:build integration

// Package integration provides end-to-end integration tests for the evc-mesh API.
//
// These tests require running Docker Compose services (postgres, redis, nats, minio)
// and the API server. Run:
//
//	cd deploy/docker/mesh && docker compose up -d
//	go run ./cmd/api &
//	go test ./tests/integration/ -v -tags=integration
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// TestEnv provides HTTP client, database connection, and auth helpers for
// integration tests. Each test should call NewTestEnv and defer Cleanup.
type TestEnv struct {
	BaseURL    string
	DB         *sqlx.DB
	HTTPClient *http.Client
	AuthToken  string
	UserID     string
	t          *testing.T
	cleanupFns []func()
}

// NewTestEnv creates a new test environment. It skips the test if the database
// or API server is unreachable.
func NewTestEnv(t *testing.T) *TestEnv {
	t.Helper()

	baseURL := envOr("TEST_API_URL", "http://localhost:8005")
	dbDSN := envOr("TEST_DB_DSN", "host=localhost port=5437 user=mesh password=mesh dbname=mesh sslmode=disable")

	// Verify API server is up.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Skipf("API server not available at %s, skipping integration test: %v", baseURL, err)
	}
	_ = resp.Body.Close()

	// Connect to database.
	db, err := sqlx.Connect("postgres", dbDSN)
	if err != nil {
		t.Skipf("Database not available, skipping integration test: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("Database ping failed, skipping integration test: %v", err)
	}

	// The refresh token now travels as an httpOnly cookie instead of a body
	// field — the client needs a cookie jar to carry it across requests the
	// same way a real browser does (Login sets it, Refresh/Logout read it).
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("Failed to create cookie jar: %v", err)
	}

	return &TestEnv{
		BaseURL:    baseURL,
		DB:         db,
		HTTPClient: &http.Client{Timeout: 10 * time.Second, Jar: jar},
		t:          t,
	}
}

// Cleanup closes the database connection and runs any registered cleanup functions.
func (e *TestEnv) Cleanup(t *testing.T) {
	t.Helper()
	for i := len(e.cleanupFns) - 1; i >= 0; i-- {
		e.cleanupFns[i]()
	}
	if e.DB != nil {
		_ = e.DB.Close()
	}
}

// OnCleanup registers a function to be called during Cleanup (LIFO order).
func (e *TestEnv) OnCleanup(fn func()) {
	e.cleanupFns = append(e.cleanupFns, fn)
}

// Register creates a new user via the API and sets the auth token.
func (e *TestEnv) Register(t *testing.T, email, password, name string) map[string]interface{} {
	t.Helper()

	body := map[string]string{
		"email":    email,
		"password": password,
		"name":     name,
	}

	resp := e.Post(t, "/api/v1/auth/register", body)
	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("Register failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode register response: %v", err)
	}
	_ = resp.Body.Close()

	// Extract the access token. The refresh token is no longer in the body —
	// it arrives as an httpOnly cookie the jar picks up automatically.
	tokens, ok := result["tokens"].(map[string]interface{})
	if ok {
		if at, exists := tokens["access_token"]; exists {
			e.AuthToken = at.(string)
		}
	}

	// Extract user ID.
	user, ok := result["user"].(map[string]interface{})
	if ok {
		if id, exists := user["id"]; exists {
			e.UserID = id.(string)
		}
	}

	// Register cleanup to delete test user from DB.
	if e.UserID != "" {
		userID := e.UserID
		e.OnCleanup(func() {
			_, _ = e.DB.ExecContext(context.Background(),
				"DELETE FROM workspace_members WHERE user_id = $1", userID)
			_, _ = e.DB.ExecContext(context.Background(),
				"DELETE FROM refresh_tokens WHERE user_id = $1", userID)
			_, _ = e.DB.ExecContext(context.Background(),
				"DELETE FROM workspaces WHERE owner_id = $1", userID)
			_, _ = e.DB.ExecContext(context.Background(),
				"DELETE FROM users WHERE id = $1", userID)
		})
	}

	return result
}

// Login authenticates a user and sets the auth token.
func (e *TestEnv) Login(t *testing.T, email, password string) map[string]interface{} {
	t.Helper()

	body := map[string]string{
		"email":    email,
		"password": password,
	}

	resp := e.Post(t, "/api/v1/auth/login", body)
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("Login failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode login response: %v", err)
	}
	_ = resp.Body.Close()

	// Extract the access token; the refresh token arrives as a cookie.
	tokens, ok := result["tokens"].(map[string]interface{})
	if ok {
		if at, exists := tokens["access_token"]; exists {
			e.AuthToken = at.(string)
		}
	}

	return result
}

// RefreshCookieValue returns the raw refresh_token cookie value currently
// held in the client's cookie jar, for tests that need to replay a token
// deliberately (e.g. reuse-detection) after it has been rotated out from
// under the jar's own copy.
func (e *TestEnv) RefreshCookieValue(t *testing.T) string {
	t.Helper()
	// Must match the cookie's own Path (/api/v1/auth/refresh) — the jar
	// applies RFC 6265 path-matching, so asking it about the bare base URL
	// ("/") would silently return nothing.
	u, err := neturl.Parse(e.BaseURL + "/api/v1/auth/refresh")
	if err != nil {
		t.Fatalf("Failed to parse base URL: %v", err)
	}
	for _, c := range e.HTTPClient.Jar.Cookies(u) {
		if c.Name == "refresh_token" {
			return c.Value
		}
	}
	t.Fatalf("no refresh_token cookie in jar for %s", e.BaseURL)
	return ""
}

// Get performs an authenticated GET request to the API.
func (e *TestEnv) Get(t *testing.T, path string) *http.Response {
	t.Helper()
	return e.doRequest(t, http.MethodGet, path, nil)
}

// Post performs an authenticated POST request to the API.
func (e *TestEnv) Post(t *testing.T, path string, body interface{}) *http.Response {
	t.Helper()
	return e.doRequest(t, http.MethodPost, path, body)
}

// Patch performs an authenticated PATCH request to the API.
func (e *TestEnv) Patch(t *testing.T, path string, body interface{}) *http.Response {
	t.Helper()
	return e.doRequest(t, http.MethodPatch, path, body)
}

// Put performs an authenticated PUT request to the API.
func (e *TestEnv) Put(t *testing.T, path string, body interface{}) *http.Response {
	t.Helper()
	return e.doRequest(t, http.MethodPut, path, body)
}

// Delete performs an authenticated DELETE request to the API.
func (e *TestEnv) Delete(t *testing.T, path string) *http.Response {
	t.Helper()
	return e.doRequest(t, http.MethodDelete, path, nil)
}

// ReadBody reads and returns the response body as bytes, then closes it.
func (e *TestEnv) ReadBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	return data
}

// DecodeJSON reads the response body into the target struct.
func (e *TestEnv) DecodeJSON(t *testing.T, resp *http.Response, target interface{}) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("Failed to decode JSON response: %v", err)
	}
}

// doRequest creates and executes an HTTP request with optional JSON body and auth header.
func (e *TestEnv) doRequest(t *testing.T, method, path string, body interface{}) *http.Response {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Failed to marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	url := e.BaseURL + path
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+e.AuthToken)
	}

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %s %s: %v", method, path, err)
	}

	return resp
}

// GetWithAgentKey performs a GET authenticated as an agent (X-Agent-Key), not as a
// user. Agent auth is a distinct path through DualAuth/WorkspaceRLS, so tests about
// agent-facing gates must not be written with a user JWT.
func (e *TestEnv) GetWithAgentKey(t *testing.T, path, agentKey string) *http.Response {
	t.Helper()
	return e.doRequestWithAgentKey(t, http.MethodGet, path, agentKey, nil)
}

// PostWithAgentKey performs a POST authenticated as an agent (X-Agent-Key).
func (e *TestEnv) PostWithAgentKey(t *testing.T, path, agentKey string, body interface{}) *http.Response {
	t.Helper()
	return e.doRequestWithAgentKey(t, http.MethodPost, path, agentKey, body)
}

// doRequestWithAgentKey issues a request carrying only the agent key — the user
// bearer token is deliberately omitted so the agent's own gates are what is tested.
func (e *TestEnv) doRequestWithAgentKey(t *testing.T, method, path, agentKey string, body interface{}) *http.Response {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Failed to marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(method, e.BaseURL+path, bodyReader)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %s %s: %v", method, path, err)
	}
	return resp
}

// CreateAgent registers an agent in the workspace and returns its id and API key.
// The agent is NOT added to any project — project membership is a separate grant.
func (e *TestEnv) CreateAgent(t *testing.T, wsID, name string) (agentID, apiKey string) {
	t.Helper()

	resp := e.Post(t, fmt.Sprintf("/api/v1/workspaces/%s/agents", wsID), map[string]interface{}{
		"name":       name,
		"agent_type": "claude_code",
	})
	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("CreateAgent failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	e.DecodeJSON(t, resp, &result)

	if agent, ok := result["agent"].(map[string]interface{}); ok {
		agentID, _ = agent["id"].(string)
	}
	apiKey, _ = result["api_key"].(string)

	if agentID == "" || apiKey == "" {
		t.Fatalf("CreateAgent returned no id/api_key: %v", result)
	}

	e.OnCleanup(func() {
		_, _ = e.DB.ExecContext(context.Background(),
			"DELETE FROM project_members WHERE agent_id = $1", agentID)
		_, _ = e.DB.ExecContext(context.Background(),
			"DELETE FROM agents WHERE id = $1", agentID)
	})

	return agentID, apiKey
}

// uniqueEmail generates a collision-free email for test isolation.
func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@test.mesh.local", prefix, time.Now().UnixNano())
}

// AddComment posts a comment on a task. Used to satisfy the done-evidence gate
// before moving a task to the done category.
func (e *TestEnv) AddComment(t *testing.T, taskID, body string) {
	t.Helper()
	resp := e.Post(t, fmt.Sprintf("/api/v1/tasks/%s/comments", taskID), map[string]string{"body": body})
	resp.Body.Close()
}

// MoveTask moves a task to the given statusID. For done-category statuses,
// call AddComment first to satisfy the done-evidence gate.
func (e *TestEnv) MoveTask(t *testing.T, taskID, statusID string) *http.Response {
	t.Helper()
	return e.Post(t, fmt.Sprintf("/api/v1/tasks/%s/move", taskID), map[string]string{"status_id": statusID})
}

// MoveTaskToDone adds a comment (evidence) then moves the task to the done status.
func (e *TestEnv) MoveTaskToDone(t *testing.T, taskID, doneStatusID string) *http.Response {
	t.Helper()
	e.AddComment(t, taskID, "Integration test evidence — task completed.")
	return e.MoveTask(t, taskID, doneStatusID)
}

// envOr returns the environment variable value or a fallback default.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
