package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"
)

// RESTClient wraps HTTP calls to the Mesh REST API on behalf of an agent.
type RESTClient struct {
	baseURL    string
	agentKey   string
	basicUser  string // HTTP basic-auth username (empty = disabled)
	basicPass  string
	httpClient *http.Client
}

// NewRESTClient creates a new RESTClient for the given API base URL and agent key.
//
// A self-hosted Mesh instance may sit behind an HTTP basic-auth gate on its
// reverse proxy (e.g. a partner instance whose UI is password-walled while the
// API authenticates callers itself). Basic auth rides the Authorization header;
// the Mesh app authenticates via X-Agent-Key, so the two never collide. When
// MESH_BASIC_AUTH is set (as "user:pass") every request also carries the basic
// credential, letting one binary reach both an open instance and a gated one.
// Unset — the default for the whole fleet against mesh.entire.host — is a no-op.
func NewRESTClient(baseURL, agentKey string) *RESTClient {
	c := &RESTClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		agentKey: agentKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	if ba := strings.TrimSpace(os.Getenv("MESH_BASIC_AUTH")); ba != "" {
		// SplitN on the FIRST colon only: a bcrypt/htpasswd-style password can
		// itself contain colons, and only the username is delimiter-free.
		if user, pass, ok := strings.Cut(ba, ":"); ok {
			c.basicUser, c.basicPass = user, pass
		}
	}
	return c
}

// applyAuth sets the headers every request needs: the agent key the Mesh app
// reads, plus the optional proxy basic-auth. Centralised so `do` and
// `doMultipart` cannot drift apart on which credentials they attach.
func (c *RESTClient) applyAuth(req *http.Request) {
	req.Header.Set("X-Agent-Key", c.agentKey)
	if c.basicUser != "" {
		req.SetBasicAuth(c.basicUser, c.basicPass)
	}
}

// do executes an HTTP request with the agent key auth header.
func (c *RESTClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
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

	c.applyAuth(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

// doJSON executes an HTTP request and decodes the JSON response into result.
// Returns an error for HTTP 4xx/5xx responses using the API's error message.
func (c *RESTClient) doJSON(ctx context.Context, method, path string, body, result any) error {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		msg := fmt.Sprintf("API error %d", resp.StatusCode)
		if m, ok := errBody["message"].(string); ok {
			msg = m
		} else if m, ok := errBody["error"].(string); ok {
			msg = m
		}
		return fmt.Errorf("%s: %s", http.StatusText(resp.StatusCode), msg)
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// doMultipart executes a multipart/form-data POST and decodes the JSON response into result.
func (c *RESTClient) doMultipart(ctx context.Context, path string, fields map[string]string, fileField, fileName, fileContentType string, fileContent []byte, result any) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// Write non-file fields.
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return fmt.Errorf("write field %s: %w", k, err)
		}
	}

	// Write file content as a form file. CreateFormFile is not used because it
	// hardcodes application/octet-stream, which discards the caller's MIME type
	// and forces the server to fall back to guessing from the filename.
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(fileField), escapeQuotes(fileName)))
	if fileContentType == "" {
		fileContentType = "application/octet-stream"
	}
	partHeader.Set("Content-Type", fileContentType)

	fw, err := mw.CreatePart(partHeader)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err = fw.Write(fileContent); err != nil {
		return fmt.Errorf("write file content: %w", err)
	}

	if err = mw.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	c.applyAuth(req)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		msg := fmt.Sprintf("API error %d", resp.StatusCode)
		if m, ok := errBody["message"].(string); ok {
			msg = m
		}
		return fmt.Errorf("%s: %s", http.StatusText(resp.StatusCode), msg)
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// BaseURL returns the base URL used by this client.
func (c *RESTClient) BaseURL() string {
	return c.baseURL
}

// Ping checks connectivity by calling GET /health.
func (c *RESTClient) Ping(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/health", nil, nil)
}

// GetAgentMe returns the current agent's profile.
func (c *RESTClient) GetAgentMe(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/agents/me", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListProjects lists projects in a workspace.
func (c *RESTClient) ListProjects(ctx context.Context, workspaceID string, includeArchived bool) (map[string]any, error) {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects", workspaceID)
	if !includeArchived {
		path += "?is_archived=false"
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetProject returns a project by ID.
func (c *RESTClient) GetProject(ctx context.Context, projectID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/projects/"+projectID, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetProjectStatuses returns statuses for a project.
func (c *RESTClient) GetProjectStatuses(ctx context.Context, projectID string) ([]map[string]any, error) {
	var result []map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/projects/"+projectID+"/statuses", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetProjectCustomFields returns custom fields for a project.
func (c *RESTClient) GetProjectCustomFields(ctx context.Context, projectID string) ([]map[string]any, error) {
	var result []map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/projects/"+projectID+"/custom-fields", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListTasks lists tasks with optional filters.
func (c *RESTClient) ListTasks(ctx context.Context, projectID string, params map[string]string) (map[string]any, error) {
	path := "/api/v1/projects/" + projectID + "/tasks"
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		path += "?" + q.Encode()
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetTask returns a task by ID.
func (c *RESTClient) GetTask(ctx context.Context, taskID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tasks/"+taskID, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CreateTask creates a new task in a project.
func (c *RESTClient) CreateTask(ctx context.Context, projectID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/projects/"+projectID+"/tasks", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateTask updates a task.
func (c *RESTClient) UpdateTask(ctx context.Context, taskID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/tasks/"+taskID, body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// MoveTask moves a task to a new status.
func (c *RESTClient) MoveTask(ctx context.Context, taskID string, body map[string]any) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/move", body, nil)
}

// AssignTask assigns a task to an agent or user.
func (c *RESTClient) AssignTask(ctx context.Context, taskID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/assign", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CreateSubtask creates a subtask.
func (c *RESTClient) CreateSubtask(ctx context.Context, parentTaskID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+parentTaskID+"/subtasks", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// AddDependency adds a dependency between tasks.
func (c *RESTClient) AddDependency(ctx context.Context, taskID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/dependencies", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// AddComment adds a comment to a task.
func (c *RESTClient) AddComment(ctx context.Context, taskID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/comments", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListComments lists comments on a task.
func (c *RESTClient) ListComments(ctx context.Context, taskID string, params map[string]string) (map[string]any, error) {
	path := "/api/v1/tasks/" + taskID + "/comments"
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		path += "?" + q.Encode()
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UploadArtifact uploads an artifact to a task using multipart form.
// UploadArtifact posts an artifact to a task as multipart/form-data.
//
// mimeType is attached to the file part rather than discarded (it used to be an
// accepted-but-unused parameter). The server infers an artifact's MIME type from
// the part's Content-Type first and only falls back to guessing from the
// filename, so dropping it meant every MCP upload was typed by its extension —
// which is how a file full of base64 text came to be stored as image/png.
func (c *RESTClient) UploadArtifact(ctx context.Context, taskID, name, artifactType, mimeType string, content []byte, metadata map[string]any) (map[string]any, error) {
	fields := map[string]string{
		"name":          name,
		"artifact_type": artifactType,
	}
	if len(metadata) > 0 {
		metaBytes, err := json.Marshal(metadata)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata: %w", err)
		}
		fields["metadata"] = string(metaBytes)
	}
	var result map[string]any
	if err := c.doMultipart(ctx, "/api/v1/tasks/"+taskID+"/artifacts", fields, "file", name, mimeType, content, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListArtifacts lists artifacts for a task.
func (c *RESTClient) ListArtifacts(ctx context.Context, taskID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tasks/"+taskID+"/artifacts", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetArtifact gets an artifact by ID.
func (c *RESTClient) GetArtifact(ctx context.Context, artifactID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/artifacts/"+artifactID, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetArtifactDownloadURL returns the download URL for an artifact.
// The REST API returns a redirect from /artifacts/:id/download — we return the URL.
func (c *RESTClient) GetArtifactDownloadURL(ctx context.Context, artifactID string) (string, error) {
	// Use the direct redirect URL as the download URL.
	return c.baseURL + "/api/v1/artifacts/" + artifactID + "/download", nil
}

// Heartbeat sends a heartbeat for the agent with optional status/message/metadata.
func (c *RESTClient) Heartbeat(ctx context.Context, body map[string]any) (map[string]any, error) {
	if body == nil {
		body = map[string]any{}
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agents/heartbeat", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetAgentTasks returns tasks assigned to the current agent.
func (c *RESTClient) GetAgentTasks(ctx context.Context, params map[string]string) (map[string]any, error) {
	path := "/api/v1/agents/me/tasks"
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		path += "?" + q.Encode()
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// PollTasks long-polls for new task assignments.
// timeoutSecs controls how long the server blocks before returning (1–120).
func (c *RESTClient) PollTasks(ctx context.Context, timeoutSecs int) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v1/agents/me/tasks/poll?timeout=%d", timeoutSecs), nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// PublishEvent publishes an event to the event bus.
func (c *RESTClient) PublishEvent(ctx context.Context, projectID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/projects/"+projectID+"/events", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetContext returns events from the event bus for a project.
func (c *RESTClient) GetContext(ctx context.Context, projectID string, params map[string]string) (map[string]any, error) {
	path := "/api/v1/projects/" + projectID + "/events"
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		path += "?" + q.Encode()
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetTaskContext returns full context for a task.
func (c *RESTClient) GetTaskContext(ctx context.Context, taskID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tasks/"+taskID+"/context", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetTaskComments returns the most recent DefaultPageSize comments for a
// task, in chronological order (last element = newest).
//
// get_task(include_comments=true) is the call the READ-BEFORE-ACT gate tells
// every agent to trust as "the whole thread, read to the end". For a task
// with more than DefaultPageSize comments, the server's untouched default
// (sort_dir=asc) makes "the end" mean the OLDEST comments — an agent reading
// a long-running task's thread would see it stop at whenever comment 50 was
// posted and never learn the thread kept going. Requesting sort_dir=desc
// gets the newest page instead, then reversing it back to chronological
// order preserves the reading experience while fixing which N comments are
// shown. See task 4222c17d (D1) and its parent diagnosis 9855f866.
func (c *RESTClient) GetTaskComments(ctx context.Context, taskID string) (map[string]any, error) {
	result, err := c.ListComments(ctx, taskID, map[string]string{
		"include_internal": "true",
		"sort_dir":         "desc",
	})
	if err != nil {
		return nil, err
	}
	if items, ok := result["items"].([]any); ok {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
		result["items"] = items
	}
	return result, nil
}

// GetTaskArtifacts returns artifacts for a task.
func (c *RESTClient) GetTaskArtifacts(ctx context.Context, taskID string) (map[string]any, error) {
	return c.ListArtifacts(ctx, taskID)
}

// GetTaskDependencies returns a task's dependencies, normalized to
// {"outgoing": [...], "incoming": [...]} regardless of which server version
// answered.
//
// Two shapes exist in the wild and BOTH must keep working:
//
//   - object, {"outgoing": [], "incoming": []} — since #544. A task's graph has
//     two sides, and the old response carried only one.
//   - bare array — every server older than #544, which includes any self-hosted
//     instance that has not updated yet. This binary is handed out to run
//     against those, so dropping the legacy shape would break them on upgrade
//     of the CLIENT alone.
//
// Decoding straight into []map[string]any is what shipped with #544 and it is
// why get_task(include_dependencies=true) failed outright against an updated
// server: encoding/json cannot unmarshal an object into a slice, the handler
// does not swallow that error, and the WHOLE get_task call returned an error —
// not just the dependencies section.
//
// A legacy array is reported as outgoing, which is exactly what it was: the
// pre-#544 endpoint returned only the edges the task itself declared.
func (c *RESTClient) GetTaskDependencies(ctx context.Context, taskID string) (map[string]any, error) {
	var raw json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tasks/"+taskID+"/dependencies", nil, &raw); err != nil {
		return nil, err
	}

	// Object first: a bare array fails to unmarshal into the struct, so the
	// fallback below is reached only for the legacy shape. Probing in the other
	// order would be ambiguous — `null` decodes happily into a slice.
	var both struct {
		Outgoing []map[string]any `json:"outgoing"`
		Incoming []map[string]any `json:"incoming"`
	}
	if err := json.Unmarshal(raw, &both); err == nil {
		return map[string]any{
			"outgoing": nonNil(both.Outgoing),
			"incoming": nonNil(both.Incoming),
		}, nil
	}

	var legacy []map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, fmt.Errorf("decode dependencies: unrecognized shape: %w", err)
	}
	return map[string]any{
		"outgoing": nonNil(legacy),
		"incoming": []map[string]any{},
	}, nil
}

// nonNil keeps an absent list rendering as [] rather than null, so a caller
// never has to tell "no dependencies" apart from "field missing".
func nonNil(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}

// UpdateAgent updates the current agent.
func (c *RESTClient) UpdateAgent(ctx context.Context, agentID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/agents/"+agentID, body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateMe updates the calling agent's own profile via PATCH /agents/me (no admin RBAC required).
func (c *RESTClient) UpdateMe(ctx context.Context, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/agents/me", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// RegisterSubAgent creates a sub-agent under the given parent agent.
// The parentID is embedded in the request body as parent_agent_id.
func (c *RESTClient) RegisterSubAgent(ctx context.Context, workspaceID, parentID, name, agentType string, capabilities map[string]any) (map[string]any, error) {
	body := map[string]any{
		"name":            name,
		"agent_type":      agentType,
		"parent_agent_id": parentID,
	}
	if capabilities != nil {
		body["capabilities"] = capabilities
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/agents", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListSubAgents returns the sub-agents of a given agent.
// When recursive is true, all descendants (up to 10 levels) are returned.
func (c *RESTClient) ListSubAgents(ctx context.Context, agentID string, recursive bool) ([]map[string]any, error) {
	path := "/api/v1/agents/" + agentID + "/sub-agents"
	if recursive {
		path += "?recursive=true"
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	// Response is {"agents": [...], "count": N}
	agents, _ := result["agents"].([]any)
	out := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		if m, ok := a.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// GetEffectiveRules calls the given rules path and returns the response.
// path should be a full API path like /api/v1/workspaces/{id}/rules/effective.
func (c *RESTClient) GetEffectiveRules(ctx context.Context, path string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetTeamDirectory returns the team directory for a workspace (agents + humans).
func (c *RESTClient) GetTeamDirectory(ctx context.Context, workspaceID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/team", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetAssignmentRules returns effective assignment rules for a project.
func (c *RESTClient) GetAssignmentRules(ctx context.Context, projectID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/projects/"+projectID+"/rules/assignment", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetWorkflowRules returns workflow rules and caller permissions for a project.
func (c *RESTClient) GetWorkflowRules(ctx context.Context, projectID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/projects/"+projectID+"/rules/workflow", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateAgentProfile updates the calling agent's profile fields.
func (c *RESTClient) UpdateAgentProfile(ctx context.Context, agentID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPut, "/api/v1/agents/"+agentID+"/profile", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// doRaw executes an HTTP request with a raw body and given Content-Type, returning the response body.
func (c *RESTClient) doRaw(ctx context.Context, method, path, contentType string, rawBody []byte) (body []byte, statusCode int, err error) {
	var bodyReader io.Reader
	if rawBody != nil {
		bodyReader = bytes.NewReader(rawBody)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	c.applyAuth(req)
	if rawBody != nil {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return data, resp.StatusCode, nil
}

// ImportWorkspaceConfig imports workspace configuration from YAML content.
func (c *RESTClient) ImportWorkspaceConfig(ctx context.Context, workspaceID, yamlContent string) (map[string]any, error) {
	data, statusCode, err := c.doRaw(ctx, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/config/import", "text/yaml", []byte(yamlContent))
	if err != nil {
		return nil, err
	}
	if statusCode >= 400 {
		var errBody map[string]any
		_ = json.Unmarshal(data, &errBody)
		msg := fmt.Sprintf("API error %d", statusCode)
		if m, ok := errBody["message"].(string); ok {
			msg = m
		} else if m, ok := errBody["error"].(string); ok {
			msg = m
		}
		return nil, fmt.Errorf("%s: %s", http.StatusText(statusCode), msg)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

// CreateRecurringSchedule creates a recurring task schedule for a project.
func (c *RESTClient) CreateRecurringSchedule(ctx context.Context, projectID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/projects/"+projectID+"/recurring", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListRecurringSchedules lists recurring schedules for a project.
func (c *RESTClient) ListRecurringSchedules(ctx context.Context, projectID string, params map[string]string) (map[string]any, error) {
	path := "/api/v1/projects/" + projectID + "/recurring"
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		path += "?" + q.Encode()
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetRecurringHistory returns the instance history for a recurring schedule.
func (c *RESTClient) GetRecurringHistory(ctx context.Context, scheduleID string, params map[string]string) (map[string]any, error) {
	path := "/api/v1/recurring/" + scheduleID + "/history"
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		path += "?" + q.Encode()
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// TriggerRecurringNow immediately creates the next instance for a recurring schedule.
func (c *RESTClient) TriggerRecurringNow(ctx context.Context, scheduleID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/recurring/"+scheduleID+"/trigger", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateRecurringSchedule updates a recurring schedule by ID.
func (c *RESTClient) UpdateRecurringSchedule(ctx context.Context, scheduleID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/recurring/"+scheduleID, body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// DeleteRecurringSchedule deletes a recurring schedule by ID.
func (c *RESTClient) DeleteRecurringSchedule(ctx context.Context, scheduleID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/recurring/"+scheduleID, nil, nil)
}

// Remember creates or updates a memory entry (UPSERT by key within scope).
func (c *RESTClient) Remember(ctx context.Context, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/memories", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// RecallMemoriesParams holds all optional parameters for memory search.
type RecallMemoriesParams struct {
	Query             string
	WorkspaceID       string
	ProjectID         string
	Scope             string
	Tags              []string
	TagsAny           []string
	CreatedBy         string
	Since             string
	Until             string
	RelevanceMin      float64
	ApplyRecencyDecay bool
	RecencyWeight     float64
	// HalfLifeDays overrides the server-level half-life for exponential decay.
	// 0 means use the server default (env MEMORY_RECALL_HALF_LIFE_DAYS, fallback 30d).
	// Valid range: 1–365.
	HalfLifeDays   int
	OrderBy        string
	IncludeExpired bool
	// ExcludeSuperseded controls whether status=superseded memories are hidden.
	// When false, explicitly passes exclude_superseded=false to the server to
	// override the server-side default of true.
	ExcludeSuperseded *bool
	Limit             int
	Offset            int
}

// RecallMemories searches memories via full-text search with optional filters.
func (c *RESTClient) RecallMemories(ctx context.Context, p RecallMemoriesParams) (map[string]any, error) {
	params := make(url.Values)
	if p.Query != "" {
		params.Set("q", p.Query)
	}
	if p.WorkspaceID != "" {
		params.Set("workspace_id", p.WorkspaceID)
	}
	if p.ProjectID != "" {
		params.Set("project_id", p.ProjectID)
	}
	if p.Scope != "" {
		params.Set("scope", p.Scope)
	}
	for _, tag := range p.Tags {
		params.Add("tags", tag)
	}
	for _, tag := range p.TagsAny {
		params.Add("tags_any", tag)
	}
	if p.CreatedBy != "" {
		params.Set("created_by", p.CreatedBy)
	}
	if p.Since != "" {
		params.Set("since", p.Since)
	}
	if p.Until != "" {
		params.Set("until", p.Until)
	}
	if p.RelevanceMin > 0 {
		params.Set("relevance_min", fmt.Sprintf("%g", p.RelevanceMin))
	}
	if p.ApplyRecencyDecay {
		params.Set("apply_recency_decay", "true")
	}
	if p.RecencyWeight > 0 {
		params.Set("recency_weight", fmt.Sprintf("%g", p.RecencyWeight))
	}
	if p.HalfLifeDays > 0 {
		params.Set("half_life_days", fmt.Sprintf("%d", p.HalfLifeDays))
	}
	if p.OrderBy != "" {
		params.Set("order_by", p.OrderBy)
	}
	if p.IncludeExpired {
		params.Set("include_expired", "true")
	}
	if p.ExcludeSuperseded != nil && !*p.ExcludeSuperseded {
		params.Set("exclude_superseded", "false")
	}
	if p.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", p.Limit))
	}
	if p.Offset > 0 {
		params.Set("offset", fmt.Sprintf("%d", p.Offset))
	}
	path := "/api/v1/memories/search"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SetProjectKnowledge upserts a project-scoped knowledge entry by key.
func (c *RESTClient) SetProjectKnowledge(ctx context.Context, projectID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/projects/"+projectID+"/knowledge", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetProjectKnowledge returns all accumulated knowledge for a project.
func (c *RESTClient) GetProjectKnowledge(ctx context.Context, projectID string) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/projects/"+projectID+"/knowledge", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ForgetMemory deletes a memory entry by ID.
func (c *RESTClient) ForgetMemory(ctx context.Context, memoryID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/memories/"+memoryID, nil, nil)
}

// CheckoutTask acquires an exclusive TTL-based lock on a task. When ttlMinutes
// is zero the server applies its default (15 min); otherwise it's clamped
// server-side to [1, 240].
func (c *RESTClient) CheckoutTask(ctx context.Context, taskID string, ttlMinutes int) (map[string]any, error) {
	var body map[string]any
	if ttlMinutes > 0 {
		body = map[string]any{"ttl_minutes": ttlMinutes}
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/checkout", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ReleaseTask releases the exclusive lock on a task acquired via CheckoutTask.
// The checkoutToken returned by CheckoutTask is required.
func (c *RESTClient) ReleaseTask(ctx context.Context, taskID, checkoutToken string) error {
	body := map[string]any{"checkout_token": checkoutToken}
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/tasks/"+taskID+"/checkout", body, nil)
}

// ExtendCheckout extends the TTL of an existing checkout. When ttlMinutes is
// zero the server applies its default (15 min).
func (c *RESTClient) ExtendCheckout(ctx context.Context, taskID, checkoutToken string, ttlMinutes int) (map[string]any, error) {
	body := map[string]any{"checkout_token": checkoutToken}
	if ttlMinutes > 0 {
		body["ttl_minutes"] = ttlMinutes
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/tasks/"+taskID+"/checkout", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ExportWorkspaceConfig exports workspace configuration as YAML text.
func (c *RESTClient) ExportWorkspaceConfig(ctx context.Context, workspaceID string) (string, error) {
	data, statusCode, err := c.doRaw(ctx, http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/config/export", "", nil)
	if err != nil {
		return "", err
	}
	if statusCode >= 400 {
		var errBody map[string]any
		_ = json.Unmarshal(data, &errBody)
		msg := fmt.Sprintf("API error %d", statusCode)
		if m, ok := errBody["message"].(string); ok {
			msg = m
		} else if m, ok := errBody["error"].(string); ok {
			msg = m
		}
		return "", fmt.Errorf("%s: %s", http.StatusText(statusCode), msg)
	}
	return string(data), nil
}

// escapeQuotes mirrors mime/multipart's own (unexported) quoting for
// Content-Disposition parameter values.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }
