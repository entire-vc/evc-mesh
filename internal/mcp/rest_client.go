package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// RESTClient wraps HTTP calls to the Mesh REST API on behalf of an agent.
type RESTClient struct {
	baseURL    string
	agentKey   string
	httpClient *http.Client
}

// NewRESTClient creates a new RESTClient for the given API base URL and agent key.
func NewRESTClient(baseURL, agentKey string) *RESTClient {
	return &RESTClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		agentKey: agentKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
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

	req.Header.Set("X-Agent-Key", c.agentKey)
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
	defer resp.Body.Close()

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
func (c *RESTClient) doMultipart(ctx context.Context, path string, fields map[string]string, fileField, fileName string, fileContent []byte, result any) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// Write non-file fields.
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return fmt.Errorf("write field %s: %w", k, err)
		}
	}

	// Write file content as a form file.
	fw, err := mw.CreateFormFile(fileField, fileName)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := fw.Write(fileContent); err != nil {
		return fmt.Errorf("write file content: %w", err)
	}

	if err := mw.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Agent-Key", c.agentKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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
		var parts []string
		for k, v := range params {
			parts = append(parts, k+"="+v)
		}
		path += "?" + strings.Join(parts, "&")
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
		var parts []string
		for k, v := range params {
			parts = append(parts, k+"="+v)
		}
		path += "?" + strings.Join(parts, "&")
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UploadArtifact uploads an artifact to a task using multipart form.
func (c *RESTClient) UploadArtifact(ctx context.Context, taskID, name, artifactType, mimeType string, content []byte) (map[string]any, error) {
	fields := map[string]string{
		"name":          name,
		"artifact_type": artifactType,
	}
	var result map[string]any
	if err := c.doMultipart(ctx, "/api/v1/tasks/"+taskID+"/artifacts", fields, "file", name, content, &result); err != nil {
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

// Heartbeat sends a heartbeat for the agent.
func (c *RESTClient) Heartbeat(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agents/heartbeat", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetAgentTasks returns tasks assigned to the current agent.
func (c *RESTClient) GetAgentTasks(ctx context.Context, params map[string]string) (map[string]any, error) {
	path := "/api/v1/agents/me/tasks"
	if len(params) > 0 {
		var parts []string
		for k, v := range params {
			parts = append(parts, k+"="+v)
		}
		path += "?" + strings.Join(parts, "&")
	}
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
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
		var parts []string
		for k, v := range params {
			parts = append(parts, k+"="+v)
		}
		path += "?" + strings.Join(parts, "&")
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

// GetTaskComments returns comments for a task.
func (c *RESTClient) GetTaskComments(ctx context.Context, taskID string) (map[string]any, error) {
	return c.ListComments(ctx, taskID, map[string]string{"include_internal": "true"})
}

// GetTaskArtifacts returns artifacts for a task.
func (c *RESTClient) GetTaskArtifacts(ctx context.Context, taskID string) (map[string]any, error) {
	return c.ListArtifacts(ctx, taskID)
}

// GetTaskDependencies returns dependencies for a task.
func (c *RESTClient) GetTaskDependencies(ctx context.Context, taskID string) ([]map[string]any, error) {
	var result []map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tasks/"+taskID+"/dependencies", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateAgent updates the current agent.
func (c *RESTClient) UpdateAgent(ctx context.Context, agentID string, body map[string]any) (map[string]any, error) {
	var result map[string]any
	if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/agents/"+agentID, body, &result); err != nil {
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
func (c *RESTClient) doRaw(ctx context.Context, method, path, contentType string, rawBody []byte) ([]byte, int, error) {
	var bodyReader io.Reader
	if rawBody != nil {
		bodyReader = bytes.NewReader(rawBody)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Agent-Key", c.agentKey)
	if rawBody != nil {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return data, resp.StatusCode, nil
}

// ImportWorkspaceConfig imports workspace configuration from YAML content.
func (c *RESTClient) ImportWorkspaceConfig(ctx context.Context, workspaceID string, yamlContent string) (map[string]any, error) {
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
