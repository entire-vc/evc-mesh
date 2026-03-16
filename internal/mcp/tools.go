package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
)

// ============================================================================
// 1. list_projects
// ============================================================================

func (s *Server) handleListProjects(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	wsIDStr := mcpsdk.ParseString(request, "workspace_id", "")
	includeArchived := mcpsdk.ParseBoolean(request, "include_archived", false)

	wsID := session.WorkspaceID.String()
	if wsIDStr != "" {
		wsID = wsIDStr
	}

	result, err := s.getRESTClient(ctx).ListProjects(ctx, wsID, includeArchived)
	if err != nil {
		return errResult("failed to list projects: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 2. get_project
// ============================================================================

func (s *Server) handleGetProject(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	project, err := s.getRESTClient(ctx).GetProject(ctx, projectID)
	if err != nil {
		return errResult("failed to get project: %v", err)
	}

	statuses, err := s.getRESTClient(ctx).GetProjectStatuses(ctx, projectID)
	if err != nil {
		return errResult("failed to list statuses: %v", err)
	}

	fields, err := s.getRESTClient(ctx).GetProjectCustomFields(ctx, projectID)
	if err != nil {
		return errResult("failed to list custom fields: %v", err)
	}

	return jsonResult(map[string]any{
		"project":       project,
		"statuses":      statuses,
		"custom_fields": fields,
	})
}

// ============================================================================
// 3. list_tasks
// ============================================================================

func (s *Server) handleListTasks(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	params := map[string]string{}

	if search := mcpsdk.ParseString(request, "search", ""); search != "" {
		params["search"] = search
	}
	if at := mcpsdk.ParseString(request, "assignee_type", ""); at != "" {
		params["assignee_type"] = at
	}
	if p := mcpsdk.ParseString(request, "priority", ""); p != "" {
		params["priority"] = p
	}
	if labels := parseStringSlice(request, "labels"); len(labels) > 0 {
		params["labels"] = labels[0] // API supports single label filter
	}
	if sort := mcpsdk.ParseString(request, "sort", ""); sort != "" {
		params["sort_by"] = sort
	}

	// status_category: we need to resolve it to a status_id via the API.
	// The REST API accepts status= as a UUID. We query statuses and filter.
	if cat := mcpsdk.ParseString(request, "status_category", ""); cat != "" {
		statuses, err := s.getRESTClient(ctx).GetProjectStatuses(ctx, projectID)
		if err != nil {
			return errResult("failed to resolve status category: %v", err)
		}
		// Find first status matching the category and use it.
		// Note: REST API only supports single status_id filter.
		for _, st := range statuses {
			stCat, _ := st["category"].(string)
			if stCat == cat {
				stID, _ := st["id"].(string)
				params["status"] = stID
				break
			}
		}
	}

	limit := mcpsdk.ParseInt(request, "limit", 50)
	if limit > 0 {
		params["page_size"] = strconv.Itoa(limit)
	}

	result, err := s.getRESTClient(ctx).ListTasks(ctx, projectID, params)
	if err != nil {
		return errResult("failed to list tasks: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 4. get_task
// ============================================================================

func (s *Server) handleGetTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	task, err := s.getRESTClient(ctx).GetTask(ctx, taskID)
	if err != nil {
		return errResult("failed to get task: %v", err)
	}

	resp := map[string]any{
		"task": task,
	}

	if mcpsdk.ParseBoolean(request, "include_comments", false) {
		page, err := s.getRESTClient(ctx).GetTaskComments(ctx, taskID)
		if err != nil {
			return errResult("failed to list comments: %v", err)
		}
		if items, ok := page["items"]; ok {
			resp["comments"] = items
		} else {
			resp["comments"] = []any{}
		}
	}

	if mcpsdk.ParseBoolean(request, "include_artifacts", false) {
		page, err := s.getRESTClient(ctx).GetTaskArtifacts(ctx, taskID)
		if err != nil {
			return errResult("failed to list artifacts: %v", err)
		}
		if items, ok := page["items"]; ok {
			resp["artifacts"] = items
		} else {
			resp["artifacts"] = []any{}
		}
	}

	if mcpsdk.ParseBoolean(request, "include_dependencies", false) {
		deps, err := s.getRESTClient(ctx).GetTaskDependencies(ctx, taskID)
		if err != nil {
			return errResult("failed to list dependencies: %v", err)
		}
		resp["dependencies"] = deps
	}

	return jsonResult(resp)
}

// ============================================================================
// 5. create_task
// ============================================================================

func (s *Server) handleCreateTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	title := mcpsdk.ParseString(request, "title", "")
	if title == "" {
		return errResult("title is required")
	}

	body := map[string]any{
		"title":         title,
		"assignee_type": mcpsdk.ParseString(request, "assignee_type", "unassigned"),
		"priority":      mcpsdk.ParseString(request, "priority", "medium"),
	}

	// Resolve status slug to status_id.
	if slug := mcpsdk.ParseString(request, "status_slug", ""); slug != "" {
		stID, _, err := s.resolveStatusSlug(ctx, projectID, slug)
		if err != nil {
			return errResult("invalid status_slug: %v", err)
		}
		body["status_id"] = stID
	}
	// If no status_slug provided, REST API will use project default.

	if desc := mcpsdk.ParseString(request, "description", ""); desc != "" {
		body["description"] = desc
	}
	if assigneeID := mcpsdk.ParseString(request, "assignee_id", ""); assigneeID != "" {
		body["assignee_id"] = assigneeID
	}
	if parentTaskID := mcpsdk.ParseString(request, "parent_task_id", ""); parentTaskID != "" {
		body["parent_task_id"] = parentTaskID
	}
	if dueDateStr := mcpsdk.ParseString(request, "due_date", ""); dueDateStr != "" {
		if _, err := time.Parse(time.RFC3339, dueDateStr); err != nil {
			return errResult("invalid due_date format: %v", err)
		}
		body["due_date"] = dueDateStr
	}
	if eh := mcpsdk.ParseFloat64(request, "estimated_hours", 0); eh > 0 {
		body["estimated_hours"] = eh
	}
	if labels := parseStringSlice(request, "labels"); len(labels) > 0 {
		body["labels"] = labels
	}
	if cfMap := mcpsdk.ParseStringMap(request, "custom_fields", nil); cfMap != nil {
		body["custom_fields"] = cfMap
	}

	result, err := s.getRESTClient(ctx).CreateTask(ctx, projectID, body)
	if err != nil {
		return errResult("failed to create task: %v", err)
	}

	if warn := s.contextWarning(); warn != "" {
		result["_mesh_warning"] = warn
	}

	return jsonResult(result)
}

// ============================================================================
// 6. update_task
// ============================================================================

func (s *Server) handleUpdateTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	args := request.GetArguments()
	body := map[string]any{}

	if _, ok := args["title"]; ok {
		body["title"] = mcpsdk.ParseString(request, "title", "")
	}
	if _, ok := args["description"]; ok {
		body["description"] = mcpsdk.ParseString(request, "description", "")
	}
	if _, ok := args["priority"]; ok {
		body["priority"] = mcpsdk.ParseString(request, "priority", "")
	}
	if _, ok := args["labels"]; ok {
		body["labels"] = parseStringSlice(request, "labels")
	}
	if _, ok := args["custom_fields"]; ok {
		cfMap := mcpsdk.ParseStringMap(request, "custom_fields", nil)
		if cfMap != nil {
			body["custom_fields"] = cfMap
		}
	}
	if dueDateStr := mcpsdk.ParseString(request, "due_date", ""); dueDateStr != "" {
		if _, err := time.Parse(time.RFC3339, dueDateStr); err != nil {
			return errResult("invalid due_date format: %v", err)
		}
		body["due_date"] = dueDateStr
	}
	if _, ok := args["estimated_hours"]; ok {
		eh := mcpsdk.ParseFloat64(request, "estimated_hours", 0)
		body["estimated_hours"] = eh
	}

	if len(body) == 0 {
		return errResult("no fields to update")
	}

	result, err := s.getRESTClient(ctx).UpdateTask(ctx, taskID, body)
	if err != nil {
		return errResult("failed to update task: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 7. move_task
// ============================================================================

func (s *Server) handleMoveTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	statusSlug := mcpsdk.ParseString(request, "status_slug", "")
	if statusSlug == "" {
		return errResult("status_slug is required")
	}

	// We need the project_id to resolve the status slug.
	// Get the task first to find its project_id.
	task, err := s.getRESTClient(ctx).GetTask(ctx, taskID)
	if err != nil {
		return errResult("failed to get task: %v", err)
	}

	projectID, _ := task["project_id"].(string)
	if projectID == "" {
		return errResult("task has no project_id")
	}

	// Resolve slug to status ID.
	stID, stName, err := s.resolveStatusSlug(ctx, projectID, statusSlug)
	if err != nil {
		return errResult("invalid status_slug: %v", err)
	}

	if moveErr := s.getRESTClient(ctx).MoveTask(ctx, taskID, map[string]any{
		"status_id": stID,
	}); moveErr != nil {
		return errResult("failed to move task: %v", moveErr)
	}

	// Add optional comment.
	if commentBody := mcpsdk.ParseString(request, "comment", ""); commentBody != "" {
		// Best-effort: don't fail the move if comment creation fails.
		_, _ = s.getRESTClient(ctx).AddComment(ctx, taskID, map[string]any{
			"body":        commentBody,
			"is_internal": false,
		})
	}

	// Return updated task.
	updatedTask, err := s.getRESTClient(ctx).GetTask(ctx, taskID)
	if err != nil {
		return errResult("task moved but failed to reload: %v", err)
	}

	return jsonResult(map[string]any{
		"task":       updatedTask,
		"new_status": map[string]any{"id": stID, "slug": statusSlug, "name": stName},
	})
}

// ============================================================================
// 8. create_subtask
// ============================================================================

func (s *Server) handleCreateSubtask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	parentTaskID := mcpsdk.ParseString(request, "parent_task_id", "")
	if parentTaskID == "" {
		return errResult("parent_task_id is required")
	}

	title := mcpsdk.ParseString(request, "title", "")
	if title == "" {
		return errResult("title is required")
	}

	body := map[string]any{
		"title":    title,
		"priority": mcpsdk.ParseString(request, "priority", "medium"),
	}
	if desc := mcpsdk.ParseString(request, "description", ""); desc != "" {
		body["description"] = desc
	}

	result, err := s.getRESTClient(ctx).CreateSubtask(ctx, parentTaskID, body)
	if err != nil {
		return errResult("failed to create subtask: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 9. add_dependency
// ============================================================================

func (s *Server) handleAddDependency(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	dependsOnID := mcpsdk.ParseString(request, "depends_on_task_id", "")
	if dependsOnID == "" {
		return errResult("depends_on_task_id is required")
	}

	body := map[string]any{
		"depends_on_task_id": dependsOnID,
		"dependency_type":    mcpsdk.ParseString(request, "dependency_type", "blocks"),
	}

	result, err := s.getRESTClient(ctx).AddDependency(ctx, taskID, body)
	if err != nil {
		return errResult("failed to add dependency: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 10. assign_task
// ============================================================================

func (s *Server) handleAssignTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	body := map[string]any{}
	assignToSelf := mcpsdk.ParseBoolean(request, "assign_to_self", false)

	if assignToSelf {
		body["assignee_id"] = session.AgentID.String()
		body["assignee_type"] = "agent"
	} else {
		assigneeID := mcpsdk.ParseString(request, "assignee_id", "")
		if assigneeID != "" {
			body["assignee_id"] = assigneeID
			body["assignee_type"] = mcpsdk.ParseString(request, "assignee_type", "agent")
		} else {
			body["assignee_type"] = "unassigned"
		}
	}

	result, err := s.getRESTClient(ctx).AssignTask(ctx, taskID, body)
	if err != nil {
		return errResult("failed to assign task: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 11. add_comment
// ============================================================================

func (s *Server) handleAddComment(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	body := mcpsdk.ParseString(request, "body", "")
	if body == "" {
		return errResult("body is required")
	}

	reqBody := map[string]any{
		"body":        body,
		"is_internal": mcpsdk.ParseBoolean(request, "is_internal", false),
	}

	if parentID := mcpsdk.ParseString(request, "parent_comment_id", ""); parentID != "" {
		reqBody["parent_comment_id"] = parentID
	}

	if metaMap := mcpsdk.ParseStringMap(request, "metadata", nil); metaMap != nil {
		metaBytes, err := json.Marshal(metaMap)
		if err == nil {
			reqBody["metadata"] = json.RawMessage(metaBytes)
		}
	}

	result, err := s.getRESTClient(ctx).AddComment(ctx, taskID, reqBody)
	if err != nil {
		return errResult("failed to create comment: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 12. list_comments
// ============================================================================

func (s *Server) handleListComments(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	params := map[string]string{}
	includeInternal := mcpsdk.ParseBoolean(request, "include_internal", true)
	if includeInternal {
		params["include_internal"] = "true"
	}

	limit := mcpsdk.ParseInt(request, "limit", 50)
	if limit > 0 {
		params["page_size"] = strconv.Itoa(limit)
	}

	result, err := s.getRESTClient(ctx).ListComments(ctx, taskID, params)
	if err != nil {
		return errResult("failed to list comments: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 13. upload_artifact
// ============================================================================

func (s *Server) handleUploadArtifact(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	name := mcpsdk.ParseString(request, "name", "")
	if name == "" {
		return errResult("name is required")
	}

	content := mcpsdk.ParseString(request, "content", "")
	if content == "" {
		return errResult("content is required")
	}

	mimeType := mcpsdk.ParseString(request, "mime_type", "")
	if mimeType == "" {
		mimeType = detectMIMEType(name)
	}

	artifactType := mcpsdk.ParseString(request, "artifact_type", "file")

	result, err := s.getRESTClient(ctx).UploadArtifact(ctx, taskID, name, artifactType, mimeType, []byte(content))
	if err != nil {
		return errResult("failed to upload artifact: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 14. list_artifacts
// ============================================================================

func (s *Server) handleListArtifacts(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	result, err := s.getRESTClient(ctx).ListArtifacts(ctx, taskID)
	if err != nil {
		return errResult("failed to list artifacts: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 15. get_artifact
// ============================================================================

func (s *Server) handleGetArtifact(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	artifactID := mcpsdk.ParseString(request, "artifact_id", "")
	if artifactID == "" {
		return errResult("artifact_id is required")
	}

	artifact, err := s.getRESTClient(ctx).GetArtifact(ctx, artifactID)
	if err != nil {
		return errResult("failed to get artifact: %v", err)
	}

	resp := map[string]any{
		"artifact": artifact,
	}

	if mcpsdk.ParseBoolean(request, "include_content", false) {
		downloadURL, err := s.getRESTClient(ctx).GetArtifactDownloadURL(ctx, artifactID)
		if err != nil {
			resp["content_error"] = fmt.Sprintf("failed to get download URL: %v", err)
		} else {
			resp["download_url"] = downloadURL
		}
	}

	return jsonResult(resp)
}

// ============================================================================
// 16. publish_event
// ============================================================================

func (s *Server) handlePublishEvent(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	eventType := mcpsdk.ParseString(request, "event_type", "")
	if eventType == "" {
		return errResult("event_type is required")
	}

	subject := mcpsdk.ParseString(request, "subject", "")
	if subject == "" {
		return errResult("subject is required")
	}

	payload := mcpsdk.ParseStringMap(request, "payload", nil)
	if payload == nil {
		payload = map[string]any{}
	}

	ttlHours := mcpsdk.ParseInt(request, "ttl_hours", 24)

	body := map[string]any{
		"event_type":  eventType,
		"subject":     subject,
		"payload":     payload,
		"ttl_seconds": ttlHours * 3600,
	}

	if taskID := mcpsdk.ParseString(request, "task_id", ""); taskID != "" {
		body["task_id"] = taskID
	}
	if tags := parseStringSlice(request, "tags"); len(tags) > 0 {
		body["tags"] = tags
	}

	// Parse optional memory hint — passed through to the API for persistence.
	if memoryHint := request.GetArguments()["memory"]; memoryHint != nil {
		body["memory_hint"] = memoryHint
	}

	result, err := s.getRESTClient(ctx).PublishEvent(ctx, projectID, body)
	if err != nil {
		return errResult("failed to publish event: %v", err)
	}

	if warn := s.contextWarning(); warn != "" {
		result["_mesh_warning"] = warn
	}

	return jsonResult(result)
}

// ============================================================================
// 17. publish_summary
// ============================================================================

func (s *Server) handlePublishSummary(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	summary := mcpsdk.ParseString(request, "summary", "")
	if summary == "" {
		return errResult("summary is required")
	}

	payload := map[string]any{
		"summary":    summary,
		"agent_name": session.AgentName,
		"agent_type": session.AgentType,
	}

	if kd := parseStringSlice(request, "key_decisions"); len(kd) > 0 {
		payload["key_decisions"] = kd
	}
	if ac := parseStringSlice(request, "artifacts_created"); len(ac) > 0 {
		payload["artifacts_created"] = ac
	}
	if bl := parseStringSlice(request, "blockers"); len(bl) > 0 {
		payload["blockers"] = bl
	}
	if ns := parseStringSlice(request, "next_steps"); len(ns) > 0 {
		payload["next_steps"] = ns
	}
	if metrics := mcpsdk.ParseStringMap(request, "metrics", nil); metrics != nil {
		payload["metrics"] = metrics
	}

	body := map[string]any{
		"event_type":  "summary",
		"subject":     fmt.Sprintf("Work summary from %s", session.AgentName),
		"payload":     payload,
		"tags":        []string{"summary", session.AgentName},
		"ttl_seconds": 24 * 3600,
	}

	if taskID := mcpsdk.ParseString(request, "task_id", ""); taskID != "" {
		body["task_id"] = taskID
	}

	result, err := s.getRESTClient(ctx).PublishEvent(ctx, projectID, body)
	if err != nil {
		return errResult("failed to publish summary: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 18. get_context
// ============================================================================

func (s *Server) handleGetContext(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	params := map[string]string{}

	limit := mcpsdk.ParseInt(request, "limit", 50)
	if limit > 0 {
		params["page_size"] = strconv.Itoa(limit)
	}

	if eventTypes := parseStringSlice(request, "event_types"); len(eventTypes) > 0 {
		params["event_type"] = eventTypes[0]
	}

	if tags := parseStringSlice(request, "tags"); len(tags) > 0 {
		params["tags"] = tags[0]
	}

	result, err := s.getRESTClient(ctx).GetContext(ctx, projectID, params)
	if err != nil {
		return errResult("failed to get context: %v", err)
	}

	// Normalize to match expected format with events + count.
	resp := map[string]any{}
	if items, ok := result["items"]; ok {
		count := 0
		if arr, ok := items.([]any); ok {
			count = len(arr)
		}
		resp["events"] = items
		resp["count"] = count
	} else {
		// Pass through as-is if the response is already in the expected shape.
		for k, v := range result {
			resp[k] = v
		}
	}

	// Also fetch project knowledge and merge it (non-fatal if it fails).
	knowledge, knowledgeErr := s.getRESTClient(ctx).GetProjectKnowledge(ctx, projectID)
	if knowledgeErr == nil {
		// Prefer the "items" slice if present, otherwise embed the full response.
		if items, ok := knowledge["items"]; ok {
			resp["project_knowledge"] = items
		} else {
			resp["project_knowledge"] = knowledge
		}
	}
	// knowledgeErr is intentionally ignored — context events are still useful without it.

	return jsonResult(resp)
}

// ============================================================================
// 19. get_task_context
// ============================================================================

func (s *Server) handleGetTaskContext(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	result, err := s.getRESTClient(ctx).GetTaskContext(ctx, taskID)
	if err != nil {
		return errResult("failed to get task context: %v", err)
	}

	// If the task is part of a recurring series, enrich the response with
	// schedule info and previous instance summary from the history endpoint.
	task, _ := result["task"].(map[string]any)
	if task != nil {
		if scheduleID, ok := task["recurring_schedule_id"].(string); ok && scheduleID != "" {
			// Fetch the most recent instances (page_size=2: current + previous).
			history, histErr := s.getRESTClient(ctx).GetRecurringHistory(ctx, scheduleID, map[string]string{
				"page_size": "2",
			})
			if histErr == nil {
				instanceNumber, _ := task["recurring_instance_number"].(float64)
				recurringBlock := map[string]any{
					"schedule_id":     scheduleID,
					"instance_number": int(instanceNumber),
					"history_url":     fmt.Sprintf("/api/v1/recurring/%s/history", scheduleID),
				}

				// Extract previous_instance from history items (skip current instance).
				if items, ok := history["items"].([]any); ok {
					for _, item := range items {
						inst, ok := item.(map[string]any)
						if !ok {
							continue
						}
						instNum, _ := inst["instance_number"].(float64)
						if int(instNum) < int(instanceNumber) {
							recurringBlock["previous_instance"] = inst
							break
						}
					}
				}

				result["recurring"] = recurringBlock
			}
		}
	}

	return jsonResult(result)
}

// ============================================================================
// 20. subscribe_events
// ============================================================================

func (s *Server) handleSubscribeEvents(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	projectID := mcpsdk.ParseString(request, "project_id", "")
	eventTypes := parseStringSlice(request, "event_types")
	callbackURL := mcpsdk.ParseString(request, "callback_url", "")

	// If callback_url is provided, persist it on the agent via PATCH /agents/me (self-service, no admin RBAC).
	if callbackURL != "" {
		client := s.getRESTClient(ctx)
		_, err := client.UpdateMe(ctx, map[string]any{
			"callback_url": callbackURL,
		})
		if err != nil {
			return errResult("failed to set callback_url: %v", err)
		}
	}

	baseURL := s.getRESTClient(ctx).BaseURL()

	return jsonResult(map[string]any{
		"status":       "configured",
		"agent_id":     session.AgentID.String(),
		"project_id":   projectID,
		"event_types":  eventTypes,
		"callback_url": callbackURL,
		"push_endpoints": map[string]any{
			"sse":       baseURL + "/api/v1/agents/me/events/stream",
			"long_poll": baseURL + "/api/v1/agents/me/tasks/poll?timeout=30",
		},
		"message": "Push notifications configured. Available mechanisms: (1) callback_url — Mesh POSTs events to your URL, (2) SSE — connect to events/stream for real-time, (3) long-poll — call tasks/poll or use the poll_tasks MCP tool to block until new assignment.",
	})
}

// ============================================================================
// 21. heartbeat
// ============================================================================

func (s *Server) handleHeartbeat(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	// Build heartbeat body from tool params.
	body := map[string]any{}
	if status := mcpsdk.ParseString(request, "status", ""); status != "" {
		body["status"] = status
	}
	if message := mcpsdk.ParseString(request, "message", ""); message != "" {
		body["message"] = message
	}
	if currentTaskID := mcpsdk.ParseString(request, "current_task_id", ""); currentTaskID != "" {
		body["current_task_id"] = currentTaskID
	}
	if args := request.GetArguments(); args != nil {
		if md, ok := args["metadata"]; ok && md != nil {
			body["metadata"] = md
		}
	}

	_, err := s.getRESTClient(ctx).Heartbeat(ctx, body)
	if err != nil {
		return errResult("heartbeat failed: %v", err)
	}

	return jsonResult(map[string]any{
		"status":    "ok",
		"agent_id":  session.AgentID.String(),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// ============================================================================
// 22. get_my_tasks
// ============================================================================

func (s *Server) handleGetMyTasks(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	params := map[string]string{}

	if projID := mcpsdk.ParseString(request, "project_id", ""); projID != "" {
		params["project_id"] = projID
	}
	if cat := mcpsdk.ParseString(request, "status_category", ""); cat != "" {
		params["status_category"] = cat
	}

	limit := mcpsdk.ParseInt(request, "limit", 50)
	if limit > 0 {
		params["page_size"] = strconv.Itoa(limit)
	}

	result, err := s.getRESTClient(ctx).GetAgentTasks(ctx, params)
	if err != nil {
		return errResult("failed to get tasks: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 23. report_error
// ============================================================================

func (s *Server) handleReportError(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	errorMessage := mcpsdk.ParseString(request, "error_message", "")
	if errorMessage == "" {
		return errResult("error_message is required")
	}

	severity := mcpsdk.ParseString(request, "severity", "medium")
	recoverable := mcpsdk.ParseBoolean(request, "recoverable", true)

	payload := map[string]any{
		"error_message": errorMessage,
		"severity":      severity,
		"recoverable":   recoverable,
		"agent_name":    session.AgentName,
		"agent_type":    session.AgentType,
	}

	if stackTrace := mcpsdk.ParseString(request, "stack_trace", ""); stackTrace != "" {
		payload["stack_trace"] = stackTrace
	}

	taskID := mcpsdk.ParseString(request, "task_id", "")

	// If we have a task_id, look up its project_id and publish an error event.
	var eventID string
	if taskID != "" {
		task, err := s.getRESTClient(ctx).GetTask(ctx, taskID)
		if err == nil {
			projectID, _ := task["project_id"].(string)
			if projectID != "" {
				eventBody := map[string]any{
					"event_type":  "error",
					"subject":     fmt.Sprintf("Error from %s: %s", session.AgentName, truncate(errorMessage, 100)),
					"payload":     payload,
					"tags":        []string{"error", severity},
					"ttl_seconds": 72 * 3600,
					"task_id":     taskID,
				}
				eventResult, pubErr := s.getRESTClient(ctx).PublishEvent(ctx, projectID, eventBody)
				if pubErr == nil {
					if id, ok := eventResult["id"].(string); ok {
						eventID = id
					}
				}
			}
		}
	}

	// Best-effort: update agent error status.
	if !recoverable {
		_, _ = s.getRESTClient(ctx).UpdateAgent(ctx, session.AgentID.String(), map[string]any{
			"status": "error",
		})
	}

	resp := map[string]any{
		"status":   "reported",
		"severity": severity,
	}
	if eventID != "" {
		resp["event_id"] = eventID
	}

	return jsonResult(resp)
}

// ============================================================================
// 24. register_sub_agent
// ============================================================================

func (s *Server) handleRegisterSubAgent(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	name := mcpsdk.ParseString(request, "name", "")
	if name == "" {
		return errResult("name is required")
	}

	agentType := mcpsdk.ParseString(request, "agent_type", "")
	if agentType == "" {
		return errResult("agent_type is required")
	}

	capabilities := mcpsdk.ParseStringMap(request, "capabilities", nil)

	result, err := s.getRESTClient(ctx).RegisterSubAgent(
		ctx,
		session.WorkspaceID.String(),
		session.AgentID.String(),
		name,
		agentType,
		capabilities,
	)
	if err != nil {
		return errResult("failed to register sub-agent: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 26. get_my_rules
// ============================================================================

func (s *Server) handleGetMyRules(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	wsID := session.WorkspaceID.String()
	projectID := mcpsdk.ParseString(request, "project_id", "")

	var path string
	if projectID != "" {
		path = fmt.Sprintf("/api/v1/projects/%s/rules/effective", projectID)
	} else {
		path = fmt.Sprintf("/api/v1/workspaces/%s/rules/effective", wsID)
	}

	result, err := s.getRESTClient(ctx).GetEffectiveRules(ctx, path)
	if err != nil {
		return errResult("failed to get rules: %v", err)
	}

	rules, _ := result["items"].([]interface{})
	summary := buildRulesSummary(rules)

	return jsonResult(map[string]any{
		"rules":   rules,
		"summary": summary,
		"count":   len(rules),
	})
}

// ============================================================================
// 27. get_project_rules
// ============================================================================

func (s *Server) handleGetProjectRules(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	result, err := s.getRESTClient(ctx).GetEffectiveRules(ctx, fmt.Sprintf("/api/v1/projects/%s/rules", projectID))
	if err != nil {
		return errResult("failed to get project rules: %v", err)
	}

	return jsonResult(result)
}

// buildRulesSummary generates a plain-English summary of effective rules for LLMs.
func buildRulesSummary(rules []interface{}) string {
	if len(rules) == 0 {
		return "No governance rules apply to you in this context."
	}

	summary := fmt.Sprintf("%d rule(s) apply: ", len(rules))
	for i, r := range rules {
		rMap, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := rMap["name"].(string)
		enforcement, _ := rMap["enforcement"].(string)
		if i > 0 {
			summary += "; "
		}
		summary += fmt.Sprintf("%s (%s)", name, enforcement)
	}
	return summary
}

// ============================================================================
// 25. list_sub_agents
// ============================================================================

func (s *Server) handleListSubAgents(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	// agent_id defaults to the calling agent.
	agentID := mcpsdk.ParseString(request, "agent_id", "")
	if agentID == "" {
		agentID = session.AgentID.String()
	}

	recursive := mcpsdk.ParseBoolean(request, "recursive", false)

	agents, err := s.getRESTClient(ctx).ListSubAgents(ctx, agentID, recursive)
	if err != nil {
		return errResult("failed to list sub-agents: %v", err)
	}

	return jsonResult(map[string]any{
		"agents": agents,
		"count":  len(agents),
	})
}

// ============================================================================
// 28. get_team_directory
// ============================================================================

func (s *Server) handleGetTeamDirectory(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	result, err := s.getRESTClient(ctx).GetTeamDirectory(ctx, session.WorkspaceID.String())
	if err != nil {
		return errResult("failed to get team directory: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 29. get_assignment_rules
// ============================================================================

func (s *Server) handleGetAssignmentRules(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	result, err := s.getRESTClient(ctx).GetAssignmentRules(ctx, projectID)
	if err != nil {
		return errResult("failed to get assignment rules: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 30. get_workflow_rules
// ============================================================================

func (s *Server) handleGetWorkflowRules(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	result, err := s.getRESTClient(ctx).GetWorkflowRules(ctx, projectID)
	if err != nil {
		return errResult("failed to get workflow rules: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 31. update_agent_profile
// ============================================================================

func (s *Server) handleUpdateAgentProfile(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	args := request.GetArguments()
	body := map[string]any{}

	if _, ok := args["role"]; ok {
		body["role"] = mcpsdk.ParseString(request, "role", "")
	}
	if caps := parseStringSlice(request, "capabilities"); len(caps) > 0 {
		body["capabilities"] = caps
	}
	if _, ok := args["responsibility_zone"]; ok {
		body["responsibility_zone"] = mcpsdk.ParseString(request, "responsibility_zone", "")
	}
	if _, ok := args["escalation_to"]; ok {
		body["escalation_to"] = mcpsdk.ParseString(request, "escalation_to", "")
	}
	if accepts := parseStringSlice(request, "accepts_from"); len(accepts) > 0 {
		body["accepts_from"] = accepts
	}
	if _, ok := args["max_concurrent_tasks"]; ok {
		body["max_concurrent_tasks"] = mcpsdk.ParseInt(request, "max_concurrent_tasks", 0)
	}
	if _, ok := args["working_hours"]; ok {
		body["working_hours"] = mcpsdk.ParseString(request, "working_hours", "")
	}
	if _, ok := args["description"]; ok {
		body["description"] = mcpsdk.ParseString(request, "description", "")
	}

	// callback_url goes to PATCH /agents/me (self-service), not PUT /agents/:id/profile.
	var callbackURLUpdate bool
	if _, ok := args["callback_url"]; ok {
		callbackURLUpdate = true
	}

	if len(body) == 0 && !callbackURLUpdate {
		return errResult("no profile fields to update")
	}

	var profileResult map[string]any
	if len(body) > 0 {
		var err error
		profileResult, err = s.getRESTClient(ctx).UpdateAgentProfile(ctx, session.AgentID.String(), body)
		if err != nil {
			return errResult("failed to update agent profile: %v", err)
		}
	}

	// Persist callback_url via PATCH /agents/me.
	if callbackURLUpdate {
		cbURL := mcpsdk.ParseString(request, "callback_url", "")
		if _, err := s.getRESTClient(ctx).UpdateMe(ctx, map[string]any{"callback_url": cbURL}); err != nil {
			return errResult("failed to update callback_url: %v", err)
		}
		if profileResult == nil {
			profileResult = map[string]any{}
		}
		profileResult["callback_url"] = cbURL
	}

	return jsonResult(profileResult)
}

// ============================================================================
// 32. import_workspace_config
// ============================================================================

func (s *Server) handleImportWorkspaceConfig(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	yamlContent := mcpsdk.ParseString(request, "yaml_content", "")
	if yamlContent == "" {
		return errResult("yaml_content is required")
	}

	result, err := s.getRESTClient(ctx).ImportWorkspaceConfig(ctx, session.WorkspaceID.String(), yamlContent)
	if err != nil {
		return errResult("failed to import workspace config: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 33. export_workspace_config
// ============================================================================

func (s *Server) handleExportWorkspaceConfig(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	yamlContent, err := s.getRESTClient(ctx).ExportWorkspaceConfig(ctx, session.WorkspaceID.String())
	if err != nil {
		return errResult("failed to export workspace config: %v", err)
	}

	return jsonResult(map[string]any{
		"yaml_content": yamlContent,
	})
}

// ============================================================================
// 34. poll_tasks
// ============================================================================

func (s *Server) handlePollTasks(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	timeout := mcpsdk.ParseInt(request, "timeout", 30)
	if timeout < 1 {
		timeout = 1
	}
	if timeout > 120 {
		timeout = 120
	}

	result, err := s.getRESTClient(ctx).PollTasks(ctx, timeout)
	if err != nil {
		return errResult("poll_tasks failed: %v", err)
	}
	return jsonResult(result)
}

// ============================================================================
// 35. create_recurring_task
// ============================================================================

func (s *Server) handleCreateRecurringTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	titleTemplate := mcpsdk.ParseString(request, "title_template", "")
	if titleTemplate == "" {
		return errResult("title_template is required")
	}

	frequency := mcpsdk.ParseString(request, "frequency", "")
	if frequency == "" {
		return errResult("frequency is required")
	}

	body := map[string]any{
		"title_template": titleTemplate,
		"frequency":      frequency,
	}

	if desc := mcpsdk.ParseString(request, "description_template", ""); desc != "" {
		body["description_template"] = desc
	}
	if cronExpr := mcpsdk.ParseString(request, "cron_expr", ""); cronExpr != "" {
		body["cron_expr"] = cronExpr
	}
	if tz := mcpsdk.ParseString(request, "timezone", ""); tz != "" {
		body["timezone"] = tz
	}
	if assigneeID := mcpsdk.ParseString(request, "assignee_id", ""); assigneeID != "" {
		body["assignee_id"] = assigneeID
	}
	if assigneeType := mcpsdk.ParseString(request, "assignee_type", ""); assigneeType != "" {
		body["assignee_type"] = assigneeType
	}
	if priority := mcpsdk.ParseString(request, "priority", ""); priority != "" {
		body["priority"] = priority
	}
	if labels := parseStringSlice(request, "labels"); len(labels) > 0 {
		body["labels"] = labels
	}
	if startsAt := mcpsdk.ParseString(request, "starts_at", ""); startsAt != "" {
		body["starts_at"] = startsAt
	}
	if endsAt := mcpsdk.ParseString(request, "ends_at", ""); endsAt != "" {
		body["ends_at"] = endsAt
	}

	args := request.GetArguments()
	if _, ok := args["max_instances"]; ok {
		maxInstances := mcpsdk.ParseInt(request, "max_instances", 0)
		if maxInstances > 0 {
			body["max_instances"] = maxInstances
		}
	}

	result, err := s.getRESTClient(ctx).CreateRecurringSchedule(ctx, projectID, body)
	if err != nil {
		return errResult("failed to create recurring schedule: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 36. list_recurring_schedules
// ============================================================================

func (s *Server) handleListRecurringSchedules(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	activeOnly := mcpsdk.ParseBoolean(request, "active_only", true)

	params := map[string]string{}
	if activeOnly {
		params["is_active"] = "true"
	}

	result, err := s.getRESTClient(ctx).ListRecurringSchedules(ctx, projectID, params)
	if err != nil {
		return errResult("failed to list recurring schedules: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 37. get_recurring_history
// ============================================================================

func (s *Server) handleGetRecurringHistory(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	scheduleID := mcpsdk.ParseString(request, "recurring_schedule_id", "")
	if scheduleID == "" {
		return errResult("recurring_schedule_id is required")
	}

	limit := mcpsdk.ParseInt(request, "limit", 5)
	if limit < 1 {
		limit = 5
	}

	params := map[string]string{
		"page_size": strconv.Itoa(limit),
	}

	result, err := s.getRESTClient(ctx).GetRecurringHistory(ctx, scheduleID, params)
	if err != nil {
		return errResult("failed to get recurring history: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// 38. trigger_recurring_now
// ============================================================================

func (s *Server) handleTriggerRecurringNow(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	scheduleID := mcpsdk.ParseString(request, "recurring_schedule_id", "")
	if scheduleID == "" {
		return errResult("recurring_schedule_id is required")
	}

	result, err := s.getRESTClient(ctx).TriggerRecurringNow(ctx, scheduleID)
	if err != nil {
		return errResult("failed to trigger recurring schedule: %v", err)
	}

	return jsonResult(result)
}

// ============================================================================
// Memory tools
// ============================================================================

func (s *Server) handleRecall(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	query := mcpsdk.ParseString(request, "query", "")
	if query == "" {
		return errResult("query is required")
	}

	scope := mcpsdk.ParseString(request, "scope", "")
	projectID := mcpsdk.ParseString(request, "project_id", "")
	tags := parseStringSlice(request, "tags")
	limit := mcpsdk.ParseInt(request, "limit", 10)

	result, err := s.getRESTClient(ctx).RecallMemories(ctx, query, session.WorkspaceID.String(), projectID, scope, tags, limit)
	if err != nil {
		return errResult("recall failed: %v", err)
	}

	return jsonResult(result)
}

func (s *Server) handleRemember(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	key := mcpsdk.ParseString(request, "key", "")
	content := mcpsdk.ParseString(request, "content", "")
	if key == "" || content == "" {
		return errResult("key and content are required")
	}

	scope := mcpsdk.ParseString(request, "scope", "project")
	projectID := mcpsdk.ParseString(request, "project_id", "")
	tags := parseStringSlice(request, "tags")

	body := map[string]any{
		"workspace_id": session.WorkspaceID.String(),
		"key":          key,
		"content":      content,
		"scope":        scope,
		"tags":         tags,
		"source_type":  "agent",
	}
	if projectID != "" {
		body["project_id"] = projectID
	}

	result, err := s.getRESTClient(ctx).Remember(ctx, body)
	if err != nil {
		return errResult("remember failed: %v", err)
	}

	return jsonResult(result)
}

func (s *Server) handleGetProjectKnowledge(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID := mcpsdk.ParseString(request, "project_id", "")
	if projectID == "" {
		return errResult("project_id is required")
	}

	result, err := s.getRESTClient(ctx).GetProjectKnowledge(ctx, projectID)
	if err != nil {
		return errResult("get_project_knowledge failed: %v", err)
	}

	return jsonResult(result)
}

func (s *Server) handleForget(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	memoryID := mcpsdk.ParseString(request, "memory_id", "")
	if memoryID == "" {
		return errResult("memory_id is required")
	}

	if err := s.getRESTClient(ctx).ForgetMemory(ctx, memoryID); err != nil {
		return errResult("forget failed: %v", err)
	}

	return jsonResult(map[string]any{"deleted": true})
}

// ============================================================================
// checkout_task / release_task
// ============================================================================

func (s *Server) handleCheckoutTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	result, err := s.getRESTClient(ctx).CheckoutTask(ctx, taskID)
	if err != nil {
		return errResult("checkout_task failed: %v", err)
	}

	return jsonResult(result)
}

func (s *Server) handleReleaseTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID := mcpsdk.ParseString(request, "task_id", "")
	if taskID == "" {
		return errResult("task_id is required")
	}

	if err := s.getRESTClient(ctx).ReleaseTask(ctx, taskID); err != nil {
		return errResult("release_task failed: %v", err)
	}

	return jsonResult(map[string]any{"released": true, "task_id": taskID})
}

// ============================================================================
// session_report
// ============================================================================

func (s *Server) handleSessionReport(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	model := mcpsdk.ParseString(request, "model", "")
	tokensIn := int(mcpsdk.ParseFloat64(request, "tokens_in", 0))
	tokensOut := int(mcpsdk.ParseFloat64(request, "tokens_out", 0))
	cost := mcpsdk.ParseFloat64(request, "estimated_cost", 0)

	stats := s.tracker.Stats()
	if model != "" {
		stats["model_used"] = model
	}
	if tokensIn > 0 {
		stats["tokens_in"] = tokensIn
	}
	if tokensOut > 0 {
		stats["tokens_out"] = tokensOut
	}
	if cost > 0 {
		stats["estimated_cost"] = cost
	}

	score, detail := s.tracker.ComplianceScore()
	stats["compliance_score"] = score
	stats["compliance_detail"] = detail

	return jsonResult(stats)
}
