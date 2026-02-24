package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
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

	wsID := session.WorkspaceID
	if wsIDStr != "" {
		parsed, err := parseUUID(wsIDStr)
		if err != nil {
			return errResult("invalid workspace_id: %v", err)
		}
		wsID = parsed
	}

	filter := repository.ProjectFilter{}
	if !includeArchived {
		f := false
		filter.IsArchived = &f
	}

	pg := defaultPagination(0)
	pg.SortDir = "asc"
	pg.SortBy = "name"

	page, err := s.projectService.List(ctx, wsID, filter, pg)
	if err != nil {
		return errResult("failed to list projects: %v", err)
	}

	return jsonResult(page)
}

// ============================================================================
// 2. get_project
// ============================================================================

type getProjectResponse struct {
	Project      *domain.Project               `json:"project"`
	Statuses     []domain.TaskStatus           `json:"statuses"`
	CustomFields []domain.CustomFieldDefinition `json:"custom_fields"`
}

func (s *Server) handleGetProject(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID, err := parseUUID(mcpsdk.ParseString(request, "project_id", ""))
	if err != nil {
		return errResult("invalid project_id: %v", err)
	}

	project, err := s.projectService.GetByID(ctx, projectID)
	if err != nil {
		return errResult("failed to get project: %v", err)
	}

	statuses, err := s.taskStatusService.ListByProject(ctx, projectID)
	if err != nil {
		return errResult("failed to list statuses: %v", err)
	}

	fields, err := s.customFieldService.ListVisibleToAgents(ctx, projectID)
	if err != nil {
		return errResult("failed to list custom fields: %v", err)
	}

	return jsonResult(getProjectResponse{
		Project:      project,
		Statuses:     statuses,
		CustomFields: fields,
	})
}

// ============================================================================
// 3. list_tasks
// ============================================================================

func (s *Server) handleListTasks(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID, err := parseUUID(mcpsdk.ParseString(request, "project_id", ""))
	if err != nil {
		return errResult("invalid project_id: %v", err)
	}

	filter := repository.TaskFilter{
		Search: mcpsdk.ParseString(request, "search", ""),
		Labels: parseStringSlice(request, "labels"),
	}

	// Status category filter: resolve to status IDs.
	if cat := mcpsdk.ParseString(request, "status_category", ""); cat != "" {
		ids, err := s.resolveStatusesByCategory(ctx, projectID, cat)
		if err != nil {
			return errResult("failed to resolve status category: %v", err)
		}
		filter.StatusIDs = ids
	}

	if at := mcpsdk.ParseString(request, "assignee_type", ""); at != "" {
		assigneeType := domain.AssigneeType(at)
		filter.AssigneeType = &assigneeType
	}

	if p := mcpsdk.ParseString(request, "priority", ""); p != "" {
		priority := domain.Priority(p)
		filter.Priority = &priority
	}

	limit := mcpsdk.ParseInt(request, "limit", pagination.DefaultPageSize)
	pg := defaultPagination(limit)
	if sort := mcpsdk.ParseString(request, "sort", ""); sort != "" {
		pg.SortBy = sort
	}

	page, err := s.taskService.List(ctx, projectID, filter, pg)
	if err != nil {
		return errResult("failed to list tasks: %v", err)
	}

	return jsonResult(page)
}

// ============================================================================
// 4. get_task
// ============================================================================

type getTaskResponse struct {
	Task         *domain.Task             `json:"task"`
	Comments     []domain.Comment         `json:"comments,omitempty"`
	Artifacts    []domain.Artifact        `json:"artifacts,omitempty"`
	Dependencies []domain.TaskDependency  `json:"dependencies,omitempty"`
}

func (s *Server) handleGetTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	task, err := s.taskService.GetByID(ctx, taskID)
	if err != nil {
		return errResult("failed to get task: %v", err)
	}

	resp := getTaskResponse{Task: task}

	if mcpsdk.ParseBoolean(request, "include_comments", false) {
		pg := defaultPagination(100)
		filter := repository.CommentFilter{IncludeInternal: true}
		page, err := s.commentService.ListByTask(ctx, taskID, filter, pg)
		if err != nil {
			return errResult("failed to list comments: %v", err)
		}
		resp.Comments = page.Items
	}

	if mcpsdk.ParseBoolean(request, "include_artifacts", false) {
		pg := defaultPagination(100)
		page, err := s.artifactService.ListByTask(ctx, taskID, pg)
		if err != nil {
			return errResult("failed to list artifacts: %v", err)
		}
		resp.Artifacts = page.Items
	}

	if mcpsdk.ParseBoolean(request, "include_dependencies", false) {
		deps, err := s.taskDependencyService.ListByTask(ctx, taskID)
		if err != nil {
			return errResult("failed to list dependencies: %v", err)
		}
		resp.Dependencies = deps
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

	projectID, err := parseUUID(mcpsdk.ParseString(request, "project_id", ""))
	if err != nil {
		return errResult("invalid project_id: %v", err)
	}

	title := mcpsdk.ParseString(request, "title", "")
	if title == "" {
		return errResult("title is required")
	}

	// Resolve status.
	var statusID uuid.UUID
	if slug := mcpsdk.ParseString(request, "status_slug", ""); slug != "" {
		st, err := s.resolveStatusBySlug(ctx, projectID, slug)
		if err != nil {
			return errResult("invalid status_slug: %v", err)
		}
		statusID = st.ID
	} else {
		st, err := s.defaultStatusForProject(ctx, projectID)
		if err != nil {
			return errResult("failed to resolve default status: %v", err)
		}
		statusID = st.ID
	}

	// Parse optional fields.
	assigneeID, err := optionalUUID(mcpsdk.ParseString(request, "assignee_id", ""))
	if err != nil {
		return errResult("invalid assignee_id: %v", err)
	}

	parentTaskID, err := optionalUUID(mcpsdk.ParseString(request, "parent_task_id", ""))
	if err != nil {
		return errResult("invalid parent_task_id: %v", err)
	}

	var dueDate *time.Time
	if dueDateStr := mcpsdk.ParseString(request, "due_date", ""); dueDateStr != "" {
		t, err := time.Parse(time.RFC3339, dueDateStr)
		if err != nil {
			return errResult("invalid due_date format: %v", err)
		}
		dueDate = &t
	}

	var estimatedHours *float64
	if eh := mcpsdk.ParseFloat64(request, "estimated_hours", 0); eh > 0 {
		estimatedHours = &eh
	}

	// Custom fields.
	var customFields json.RawMessage
	if cfMap := mcpsdk.ParseStringMap(request, "custom_fields", nil); cfMap != nil {
		cfBytes, err := json.Marshal(cfMap)
		if err != nil {
			return errResult("invalid custom_fields: %v", err)
		}
		customFields = cfBytes
	}

	task := &domain.Task{
		ProjectID:      projectID,
		StatusID:       statusID,
		Title:          title,
		Description:    mcpsdk.ParseString(request, "description", ""),
		AssigneeID:     assigneeID,
		AssigneeType:   domain.AssigneeType(mcpsdk.ParseString(request, "assignee_type", "unassigned")),
		Priority:       domain.Priority(mcpsdk.ParseString(request, "priority", "medium")),
		ParentTaskID:   parentTaskID,
		DueDate:        dueDate,
		EstimatedHours: estimatedHours,
		CustomFields:   customFields,
		Labels:         pq.StringArray(parseStringSlice(request, "labels")),
		CreatedBy:      session.AgentID,
		CreatedByType:  domain.ActorTypeAgent,
	}

	if err := s.taskService.Create(ctx, task); err != nil {
		return errResult("failed to create task: %v", err)
	}

	return jsonResult(task)
}

// ============================================================================
// 6. update_task
// ============================================================================

func (s *Server) handleUpdateTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	task, err := s.taskService.GetByID(ctx, taskID)
	if err != nil {
		return errResult("failed to get task: %v", err)
	}

	args := request.GetArguments()

	if _, ok := args["title"]; ok {
		task.Title = mcpsdk.ParseString(request, "title", task.Title)
	}
	if _, ok := args["description"]; ok {
		task.Description = mcpsdk.ParseString(request, "description", task.Description)
	}
	if _, ok := args["priority"]; ok {
		task.Priority = domain.Priority(mcpsdk.ParseString(request, "priority", string(task.Priority)))
	}
	if _, ok := args["labels"]; ok {
		task.Labels = pq.StringArray(parseStringSlice(request, "labels"))
	}
	if _, ok := args["custom_fields"]; ok {
		cfMap := mcpsdk.ParseStringMap(request, "custom_fields", nil)
		if cfMap != nil {
			cfBytes, err := json.Marshal(cfMap)
			if err != nil {
				return errResult("invalid custom_fields: %v", err)
			}
			task.CustomFields = cfBytes
		}
	}
	if dueDateStr := mcpsdk.ParseString(request, "due_date", ""); dueDateStr != "" {
		t, err := time.Parse(time.RFC3339, dueDateStr)
		if err != nil {
			return errResult("invalid due_date format: %v", err)
		}
		task.DueDate = &t
	}
	if eh := mcpsdk.ParseFloat64(request, "estimated_hours", 0); eh > 0 {
		task.EstimatedHours = &eh
	}

	if err := s.taskService.Update(ctx, task); err != nil {
		return errResult("failed to update task: %v", err)
	}

	return jsonResult(task)
}

// ============================================================================
// 7. move_task
// ============================================================================

func (s *Server) handleMoveTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	statusSlug := mcpsdk.ParseString(request, "status_slug", "")
	if statusSlug == "" {
		return errResult("status_slug is required")
	}

	// Look up the task to get its project_id.
	task, err := s.taskService.GetByID(ctx, taskID)
	if err != nil {
		return errResult("failed to get task: %v", err)
	}

	// Resolve slug to status ID.
	status, err := s.resolveStatusBySlug(ctx, task.ProjectID, statusSlug)
	if err != nil {
		return errResult("invalid status_slug: %v", err)
	}

	input := service.MoveTaskInput{
		StatusID: &status.ID,
	}

	if err := s.taskService.MoveTask(ctx, taskID, input); err != nil {
		return errResult("failed to move task: %v", err)
	}

	// Optionally add a comment about the move.
	if commentBody := mcpsdk.ParseString(request, "comment", ""); commentBody != "" {
		comment := &domain.Comment{
			TaskID:     taskID,
			AuthorID:   session.AgentID,
			AuthorType: domain.ActorTypeAgent,
			Body:       commentBody,
			IsInternal: false,
		}
		// Best-effort: don't fail the move if comment creation fails.
		_ = s.commentService.Create(ctx, comment)
	}

	// Return updated task.
	updatedTask, err := s.taskService.GetByID(ctx, taskID)
	if err != nil {
		return errResult("task moved but failed to reload: %v", err)
	}

	return jsonResult(map[string]any{
		"task":       updatedTask,
		"new_status": status,
	})
}

// ============================================================================
// 8. create_subtask
// ============================================================================

func (s *Server) handleCreateSubtask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	parentTaskID, err := parseUUID(mcpsdk.ParseString(request, "parent_task_id", ""))
	if err != nil {
		return errResult("invalid parent_task_id: %v", err)
	}

	title := mcpsdk.ParseString(request, "title", "")
	if title == "" {
		return errResult("title is required")
	}

	input := service.CreateSubtaskInput{
		Title:       title,
		Description: mcpsdk.ParseString(request, "description", ""),
		Priority:    domain.Priority(mcpsdk.ParseString(request, "priority", "medium")),
	}

	subtask, err := s.taskService.CreateSubtask(ctx, parentTaskID, input)
	if err != nil {
		return errResult("failed to create subtask: %v", err)
	}

	return jsonResult(subtask)
}

// ============================================================================
// 9. add_dependency
// ============================================================================

func (s *Server) handleAddDependency(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	dependsOnID, err := parseUUID(mcpsdk.ParseString(request, "depends_on_task_id", ""))
	if err != nil {
		return errResult("invalid depends_on_task_id: %v", err)
	}

	dep := &domain.TaskDependency{
		TaskID:          taskID,
		DependsOnTaskID: dependsOnID,
		DependencyType:  domain.DependencyType(mcpsdk.ParseString(request, "dependency_type", "blocks")),
	}

	if err := s.taskDependencyService.Create(ctx, dep); err != nil {
		return errResult("failed to add dependency: %v", err)
	}

	return jsonResult(dep)
}

// ============================================================================
// 10. assign_task
// ============================================================================

func (s *Server) handleAssignTask(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	assignToSelf := mcpsdk.ParseBoolean(request, "assign_to_self", false)

	var assigneeID *uuid.UUID
	assigneeType := domain.AssigneeType(mcpsdk.ParseString(request, "assignee_type", "agent"))

	if assignToSelf {
		assigneeID = &session.AgentID
		assigneeType = domain.AssigneeTypeAgent
	} else {
		parsed, err := optionalUUID(mcpsdk.ParseString(request, "assignee_id", ""))
		if err != nil {
			return errResult("invalid assignee_id: %v", err)
		}
		assigneeID = parsed
		if assigneeID == nil {
			assigneeType = domain.AssigneeTypeUnassigned
		}
	}

	input := service.AssignTaskInput{
		AssigneeID:   assigneeID,
		AssigneeType: assigneeType,
	}

	if err := s.taskService.AssignTask(ctx, taskID, input); err != nil {
		return errResult("failed to assign task: %v", err)
	}

	// Return updated task.
	updatedTask, err := s.taskService.GetByID(ctx, taskID)
	if err != nil {
		return errResult("task assigned but failed to reload: %v", err)
	}

	return jsonResult(updatedTask)
}

// ============================================================================
// 11. add_comment
// ============================================================================

func (s *Server) handleAddComment(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	body := mcpsdk.ParseString(request, "body", "")
	if body == "" {
		return errResult("body is required")
	}

	parentCommentID, err := optionalUUID(mcpsdk.ParseString(request, "parent_comment_id", ""))
	if err != nil {
		return errResult("invalid parent_comment_id: %v", err)
	}

	var metadata json.RawMessage
	if metaMap := mcpsdk.ParseStringMap(request, "metadata", nil); metaMap != nil {
		metaBytes, err := json.Marshal(metaMap)
		if err != nil {
			return errResult("invalid metadata: %v", err)
		}
		metadata = metaBytes
	}

	comment := &domain.Comment{
		TaskID:          taskID,
		ParentCommentID: parentCommentID,
		AuthorID:        session.AgentID,
		AuthorType:      domain.ActorTypeAgent,
		Body:            body,
		Metadata:        metadata,
		IsInternal:      mcpsdk.ParseBoolean(request, "is_internal", false),
	}

	if err := s.commentService.Create(ctx, comment); err != nil {
		return errResult("failed to create comment: %v", err)
	}

	return jsonResult(comment)
}

// ============================================================================
// 12. list_comments
// ============================================================================

func (s *Server) handleListComments(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	limit := mcpsdk.ParseInt(request, "limit", pagination.DefaultPageSize)
	pg := defaultPagination(limit)

	filter := repository.CommentFilter{
		IncludeInternal: mcpsdk.ParseBoolean(request, "include_internal", true),
	}

	page, err := s.commentService.ListByTask(ctx, taskID, filter, pg)
	if err != nil {
		return errResult("failed to list comments: %v", err)
	}

	return jsonResult(page)
}

// ============================================================================
// 13. upload_artifact
// ============================================================================

func (s *Server) handleUploadArtifact(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
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

	contentBytes := []byte(content)

	input := service.UploadArtifactInput{
		TaskID:         taskID,
		Name:           name,
		ArtifactType:   domain.ArtifactType(mcpsdk.ParseString(request, "artifact_type", "file")),
		MimeType:       mimeType,
		UploadedBy:     session.AgentID,
		UploadedByType: domain.UploaderTypeAgent,
		Reader:         bytes.NewReader(contentBytes),
		Size:           int64(len(contentBytes)),
	}

	artifact, err := s.artifactService.Upload(ctx, input)
	if err != nil {
		return errResult("failed to upload artifact: %v", err)
	}

	return jsonResult(artifact)
}

// ============================================================================
// 14. list_artifacts
// ============================================================================

func (s *Server) handleListArtifacts(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	pg := defaultPagination(100)
	page, err := s.artifactService.ListByTask(ctx, taskID, pg)
	if err != nil {
		return errResult("failed to list artifacts: %v", err)
	}

	return jsonResult(page)
}

// ============================================================================
// 15. get_artifact
// ============================================================================

func (s *Server) handleGetArtifact(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	artifactID, err := parseUUID(mcpsdk.ParseString(request, "artifact_id", ""))
	if err != nil {
		return errResult("invalid artifact_id: %v", err)
	}

	artifact, err := s.artifactService.GetByID(ctx, artifactID)
	if err != nil {
		return errResult("failed to get artifact: %v", err)
	}

	resp := map[string]any{
		"artifact": artifact,
	}

	if mcpsdk.ParseBoolean(request, "include_content", false) {
		downloadURL, err := s.artifactService.GetDownloadURL(ctx, artifactID)
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

	projectID, err := parseUUID(mcpsdk.ParseString(request, "project_id", ""))
	if err != nil {
		return errResult("invalid project_id: %v", err)
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

	taskID, err := optionalUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	ttlHours := mcpsdk.ParseInt(request, "ttl_hours", 24)

	input := service.PublishEventInput{
		WorkspaceID: session.WorkspaceID,
		ProjectID:   projectID,
		TaskID:      taskID,
		AgentID:     &session.AgentID,
		EventType:   domain.EventType(eventType),
		Subject:     subject,
		Payload:     payload,
		Tags:        parseStringSlice(request, "tags"),
		TTLSeconds:  ttlHours * 3600,
	}

	msg, err := s.eventBusService.Publish(ctx, input)
	if err != nil {
		return errResult("failed to publish event: %v", err)
	}

	return jsonResult(msg)
}

// ============================================================================
// 17. publish_summary
// ============================================================================

func (s *Server) handlePublishSummary(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	session := s.getSession(ctx)
	if session == nil {
		return errResult("not authenticated: no agent session")
	}

	projectID, err := parseUUID(mcpsdk.ParseString(request, "project_id", ""))
	if err != nil {
		return errResult("invalid project_id: %v", err)
	}

	summary := mcpsdk.ParseString(request, "summary", "")
	if summary == "" {
		return errResult("summary is required")
	}

	taskID, err := optionalUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
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

	input := service.PublishEventInput{
		WorkspaceID: session.WorkspaceID,
		ProjectID:   projectID,
		TaskID:      taskID,
		AgentID:     &session.AgentID,
		EventType:   domain.EventTypeSummary,
		Subject:     fmt.Sprintf("Work summary from %s", session.AgentName),
		Payload:     payload,
		Tags:        []string{"summary", session.AgentName},
		TTLSeconds:  24 * 3600,
	}

	msg, err := s.eventBusService.Publish(ctx, input)
	if err != nil {
		return errResult("failed to publish summary: %v", err)
	}

	return jsonResult(msg)
}

// ============================================================================
// 18. get_context
// ============================================================================

func (s *Server) handleGetContext(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	projectID, err := parseUUID(mcpsdk.ParseString(request, "project_id", ""))
	if err != nil {
		return errResult("invalid project_id: %v", err)
	}

	opts := service.GetContextOptions{
		Limit: mcpsdk.ParseInt(request, "limit", 50),
		Tags:  parseStringSlice(request, "tags"),
	}

	if eventTypes := parseStringSlice(request, "event_types"); len(eventTypes) > 0 {
		et := domain.EventType(eventTypes[0])
		opts.EventType = &et
	}

	events, err := s.eventBusService.GetContext(ctx, projectID, opts)
	if err != nil {
		return errResult("failed to get context: %v", err)
	}

	return jsonResult(map[string]any{
		"events": events,
		"count":  len(events),
	})
}

// ============================================================================
// 19. get_task_context
// ============================================================================

type taskContextResponse struct {
	Task         *domain.Task              `json:"task"`
	Comments     []domain.Comment          `json:"comments"`
	Artifacts    []domain.Artifact         `json:"artifacts"`
	Dependencies []domain.TaskDependency   `json:"dependencies"`
	Events       []domain.EventBusMessage  `json:"events"`
}

func (s *Server) handleGetTaskContext(ctx context.Context, request mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	taskID, err := parseUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	task, err := s.taskService.GetByID(ctx, taskID)
	if err != nil {
		return errResult("failed to get task: %v", err)
	}

	resp := taskContextResponse{Task: task}

	// Comments.
	commentPg := defaultPagination(100)
	commentFilter := repository.CommentFilter{IncludeInternal: true}
	commentPage, err := s.commentService.ListByTask(ctx, taskID, commentFilter, commentPg)
	if err == nil {
		resp.Comments = commentPage.Items
	}

	// Artifacts.
	artifactPg := defaultPagination(100)
	artifactPage, err := s.artifactService.ListByTask(ctx, taskID, artifactPg)
	if err == nil {
		resp.Artifacts = artifactPage.Items
	}

	// Dependencies.
	deps, err := s.taskDependencyService.ListByTask(ctx, taskID)
	if err == nil {
		resp.Dependencies = deps
	}

	// Events.
	eventOpts := service.GetContextOptions{
		TaskID: &taskID,
		Limit:  50,
	}
	events, err := s.eventBusService.GetContext(ctx, task.ProjectID, eventOpts)
	if err == nil {
		resp.Events = events
	}

	return jsonResult(resp)
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

	return jsonResult(map[string]any{
		"status":      "subscribed",
		"project_id":  projectID,
		"event_types": eventTypes,
		"agent_id":    session.AgentID.String(),
		"message":     "Event subscription registered. Actual event delivery via push notifications will be implemented in a future release. Use get_context to poll for events.",
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

	if err := s.agentService.Heartbeat(ctx, session.AgentID); err != nil {
		return errResult("heartbeat failed: %v", err)
	}

	// Update agent status if provided.
	if status := mcpsdk.ParseString(request, "status", ""); status != "" {
		agent, err := s.agentService.GetByID(ctx, session.AgentID)
		if err == nil && agent != nil {
			agent.Status = domain.AgentStatus(status)
			if taskIDStr := mcpsdk.ParseString(request, "current_task_id", ""); taskIDStr != "" {
				if taskID, err := parseUUID(taskIDStr); err == nil {
					agent.CurrentTaskID = &taskID
				}
			}
			_ = s.agentService.Update(ctx, agent)
		}
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

	tasks, err := s.taskService.GetMyTasks(ctx, session.AgentID, domain.AssigneeTypeAgent)
	if err != nil {
		return errResult("failed to get tasks: %v", err)
	}

	// Filter by project if specified.
	if projIDStr := mcpsdk.ParseString(request, "project_id", ""); projIDStr != "" {
		projID, err := parseUUID(projIDStr)
		if err != nil {
			return errResult("invalid project_id: %v", err)
		}
		filtered := make([]domain.Task, 0)
		for _, t := range tasks {
			if t.ProjectID == projID {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}

	// Apply limit.
	limit := mcpsdk.ParseInt(request, "limit", 50)
	if limit > 0 && len(tasks) > limit {
		tasks = tasks[:limit]
	}

	return jsonResult(map[string]any{
		"tasks": tasks,
		"count": len(tasks),
	})
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

	taskID, err := optionalUUID(mcpsdk.ParseString(request, "task_id", ""))
	if err != nil {
		return errResult("invalid task_id: %v", err)
	}

	severity := mcpsdk.ParseString(request, "severity", "medium")
	recoverable := mcpsdk.ParseBoolean(request, "recoverable", true)

	// Build payload.
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

	// Find a project ID: use the task's project if a task is specified.
	var projectID uuid.UUID
	if taskID != nil {
		task, err := s.taskService.GetByID(ctx, *taskID)
		if err == nil && task != nil {
			projectID = task.ProjectID
		}
	}

	// Publish error event if we have a project context.
	var eventMsg *domain.EventBusMessage
	if projectID != uuid.Nil {
		input := service.PublishEventInput{
			WorkspaceID: session.WorkspaceID,
			ProjectID:   projectID,
			TaskID:      taskID,
			AgentID:     &session.AgentID,
			EventType:   domain.EventTypeError,
			Subject:     fmt.Sprintf("Error from %s: %s", session.AgentName, truncate(errorMessage, 100)),
			Payload:     payload,
			Tags:        []string{"error", severity},
			TTLSeconds:  72 * 3600, // 72 hours for errors
		}

		eventMsg, _ = s.eventBusService.Publish(ctx, input)
	}

	// Update agent error count.
	agent, err := s.agentService.GetByID(ctx, session.AgentID)
	if err == nil && agent != nil {
		agent.TotalErrors++
		if !recoverable {
			agent.Status = domain.AgentStatusError
		}
		_ = s.agentService.Update(ctx, agent)
	}

	resp := map[string]any{
		"status":   "reported",
		"severity": severity,
	}
	if eventMsg != nil {
		resp["event_id"] = eventMsg.ID.String()
	}

	return jsonResult(resp)
}

// truncate shortens a string to at most maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
