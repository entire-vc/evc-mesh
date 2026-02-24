package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// --- Mock Services ---

type mockProjectService struct {
	projects   []domain.Project
	projectMap map[uuid.UUID]*domain.Project
}

func (m *mockProjectService) Create(_ context.Context, p *domain.Project) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	m.projects = append(m.projects, *p)
	if m.projectMap == nil {
		m.projectMap = make(map[uuid.UUID]*domain.Project)
	}
	m.projectMap[p.ID] = p
	return nil
}

func (m *mockProjectService) GetByID(_ context.Context, id uuid.UUID) (*domain.Project, error) {
	if p, ok := m.projectMap[id]; ok {
		return p, nil
	}
	return nil, nil
}

func (m *mockProjectService) Update(_ context.Context, _ *domain.Project) error { return nil }
func (m *mockProjectService) Archive(_ context.Context, _ uuid.UUID) error      { return nil }
func (m *mockProjectService) Unarchive(_ context.Context, _ uuid.UUID) error    { return nil }

func (m *mockProjectService) List(_ context.Context, _ uuid.UUID, _ repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error) {
	return pagination.NewPage(m.projects, len(m.projects), pg), nil
}

type mockTaskService struct {
	tasks   []domain.Task
	taskMap map[uuid.UUID]*domain.Task
}

func newMockTaskService() *mockTaskService {
	return &mockTaskService{
		taskMap: make(map[uuid.UUID]*domain.Task),
	}
}

func (m *mockTaskService) Create(_ context.Context, t *domain.Task) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	t.CreatedAt = time.Now()
	t.UpdatedAt = time.Now()
	m.tasks = append(m.tasks, *t)
	m.taskMap[t.ID] = t
	return nil
}

func (m *mockTaskService) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	if t, ok := m.taskMap[id]; ok {
		return t, nil
	}
	return nil, nil
}

func (m *mockTaskService) Update(_ context.Context, t *domain.Task) error {
	m.taskMap[t.ID] = t
	return nil
}

func (m *mockTaskService) Delete(_ context.Context, _ uuid.UUID) error { return nil }

func (m *mockTaskService) List(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	return pagination.NewPage(m.tasks, len(m.tasks), pg), nil
}

func (m *mockTaskService) MoveTask(_ context.Context, _ uuid.UUID, _ service.MoveTaskInput) error {
	return nil
}

func (m *mockTaskService) AssignTask(_ context.Context, taskID uuid.UUID, input service.AssignTaskInput) error {
	if t, ok := m.taskMap[taskID]; ok {
		t.AssigneeID = input.AssigneeID
		t.AssigneeType = input.AssigneeType
	}
	return nil
}

func (m *mockTaskService) CreateSubtask(_ context.Context, parentTaskID uuid.UUID, input service.CreateSubtaskInput) (*domain.Task, error) {
	task := &domain.Task{
		ID:           uuid.New(),
		ParentTaskID: &parentTaskID,
		Title:        input.Title,
		Description:  input.Description,
		Priority:     input.Priority,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	m.taskMap[task.ID] = task
	return task, nil
}

func (m *mockTaskService) ListSubtasks(_ context.Context, _ uuid.UUID) ([]domain.Task, error) {
	return nil, nil
}

func (m *mockTaskService) GetMyTasks(_ context.Context, assigneeID uuid.UUID, _ domain.AssigneeType) ([]domain.Task, error) {
	var result []domain.Task
	for _, t := range m.tasks {
		if t.AssigneeID != nil && *t.AssigneeID == assigneeID {
			result = append(result, t)
		}
	}
	return result, nil
}

type mockTaskStatusService struct {
	statuses map[uuid.UUID][]domain.TaskStatus
}

func newMockTaskStatusService() *mockTaskStatusService {
	return &mockTaskStatusService{
		statuses: make(map[uuid.UUID][]domain.TaskStatus),
	}
}

func (m *mockTaskStatusService) Create(_ context.Context, _ *domain.TaskStatus) error { return nil }
func (m *mockTaskStatusService) Update(_ context.Context, _ *domain.TaskStatus) error { return nil }
func (m *mockTaskStatusService) Delete(_ context.Context, _ uuid.UUID) error          { return nil }
func (m *mockTaskStatusService) Reorder(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}

func (m *mockTaskStatusService) ListByProject(_ context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error) {
	return m.statuses[projectID], nil
}

func (m *mockTaskStatusService) addStatuses(projectID uuid.UUID, statuses []domain.TaskStatus) {
	m.statuses[projectID] = statuses
}

type mockTaskDependencyService struct{}

func (m *mockTaskDependencyService) Create(_ context.Context, dep *domain.TaskDependency) error {
	if dep.ID == uuid.Nil {
		dep.ID = uuid.New()
	}
	return nil
}

func (m *mockTaskDependencyService) Delete(_ context.Context, _ uuid.UUID) error { return nil }
func (m *mockTaskDependencyService) ListByTask(_ context.Context, _ uuid.UUID) ([]domain.TaskDependency, error) {
	return nil, nil
}
func (m *mockTaskDependencyService) CheckCycle(_ context.Context, _, _ uuid.UUID) (bool, error) {
	return false, nil
}

type mockCommentService struct {
	comments []domain.Comment
}

func (m *mockCommentService) Create(_ context.Context, c *domain.Comment) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	m.comments = append(m.comments, *c)
	return nil
}

func (m *mockCommentService) Update(_ context.Context, _ *domain.Comment) error { return nil }
func (m *mockCommentService) Delete(_ context.Context, _ uuid.UUID) error       { return nil }

func (m *mockCommentService) ListByTask(_ context.Context, _ uuid.UUID, _ repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	return pagination.NewPage(m.comments, len(m.comments), pg), nil
}

type mockArtifactService struct{}

func (m *mockArtifactService) Upload(_ context.Context, input service.UploadArtifactInput) (*domain.Artifact, error) {
	return &domain.Artifact{
		ID:             uuid.New(),
		TaskID:         input.TaskID,
		Name:           input.Name,
		ArtifactType:   input.ArtifactType,
		MimeType:       input.MimeType,
		SizeBytes:      input.Size,
		UploadedBy:     input.UploadedBy,
		UploadedByType: input.UploadedByType,
		CreatedAt:      time.Now(),
	}, nil
}

func (m *mockArtifactService) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return &domain.Artifact{
		ID:        id,
		Name:      "test.txt",
		MimeType:  "text/plain",
		SizeBytes: 42,
		CreatedAt: time.Now(),
	}, nil
}

func (m *mockArtifactService) GetDownloadURL(_ context.Context, _ uuid.UUID) (string, error) {
	return "https://example.com/download/test.txt", nil
}

func (m *mockArtifactService) Delete(_ context.Context, _ uuid.UUID) error { return nil }

func (m *mockArtifactService) ListByTask(_ context.Context, _ uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error) {
	return pagination.NewPage([]domain.Artifact{}, 0, pg), nil
}

type mockAgentService struct {
	agents map[uuid.UUID]*domain.Agent
}

func newMockAgentService() *mockAgentService {
	return &mockAgentService{agents: make(map[uuid.UUID]*domain.Agent)}
}

func (m *mockAgentService) Register(_ context.Context, _ service.RegisterAgentInput) (*service.RegisterAgentOutput, error) {
	return nil, nil
}

func (m *mockAgentService) GetByID(_ context.Context, id uuid.UUID) (*domain.Agent, error) {
	if a, ok := m.agents[id]; ok {
		return a, nil
	}
	return nil, nil
}

func (m *mockAgentService) Update(_ context.Context, a *domain.Agent) error {
	m.agents[a.ID] = a
	return nil
}

func (m *mockAgentService) Delete(_ context.Context, _ uuid.UUID) error { return nil }

func (m *mockAgentService) List(_ context.Context, _ uuid.UUID, _ repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error) {
	return pagination.NewPage([]domain.Agent{}, 0, pg), nil
}

func (m *mockAgentService) Heartbeat(_ context.Context, _ uuid.UUID) error { return nil }

func (m *mockAgentService) Authenticate(_ context.Context, _, _ string) (*domain.Agent, error) {
	return nil, nil
}

func (m *mockAgentService) RotateAPIKey(_ context.Context, _ uuid.UUID) (string, error) {
	return "", nil
}

type mockEventBusService struct {
	events []domain.EventBusMessage
}

func (m *mockEventBusService) Publish(_ context.Context, input service.PublishEventInput) (*domain.EventBusMessage, error) {
	payloadBytes, _ := json.Marshal(input.Payload)
	msg := &domain.EventBusMessage{
		ID:          uuid.New(),
		WorkspaceID: input.WorkspaceID,
		ProjectID:   input.ProjectID,
		TaskID:      input.TaskID,
		AgentID:     input.AgentID,
		EventType:   input.EventType,
		Subject:     input.Subject,
		Payload:     json.RawMessage(payloadBytes),
		Tags:        pq.StringArray(input.Tags),
		CreatedAt:   time.Now(),
	}
	m.events = append(m.events, *msg)
	return msg, nil
}

func (m *mockEventBusService) GetByID(_ context.Context, id uuid.UUID) (*domain.EventBusMessage, error) {
	for _, e := range m.events {
		if e.ID == id {
			return &e, nil
		}
	}
	return nil, nil
}

func (m *mockEventBusService) List(_ context.Context, _ uuid.UUID, _ repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error) {
	return pagination.NewPage(m.events, len(m.events), pg), nil
}

func (m *mockEventBusService) GetContext(_ context.Context, _ uuid.UUID, _ service.GetContextOptions) ([]domain.EventBusMessage, error) {
	return m.events, nil
}

func (m *mockEventBusService) CleanupExpired(_ context.Context) (int64, error) {
	return 0, nil
}

type mockActivityLogService struct{}

func (m *mockActivityLogService) Log(_ context.Context, _ *domain.ActivityLog) error { return nil }

func (m *mockActivityLogService) List(_ context.Context, _ uuid.UUID, _ repository.ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	return pagination.NewPage([]domain.ActivityLog{}, 0, pg), nil
}

func (m *mockActivityLogService) ListByTask(_ context.Context, _ uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	return pagination.NewPage([]domain.ActivityLog{}, 0, pg), nil
}

type mockWorkspaceService struct{}

func (m *mockWorkspaceService) Create(_ context.Context, _ *domain.Workspace) error   { return nil }
func (m *mockWorkspaceService) GetByID(_ context.Context, _ uuid.UUID) (*domain.Workspace, error) {
	return nil, nil
}
func (m *mockWorkspaceService) GetBySlug(_ context.Context, _ string) (*domain.Workspace, error) {
	return nil, nil
}
func (m *mockWorkspaceService) Update(_ context.Context, _ *domain.Workspace) error { return nil }
func (m *mockWorkspaceService) Delete(_ context.Context, _ uuid.UUID) error         { return nil }
func (m *mockWorkspaceService) ListByOwner(_ context.Context, _ uuid.UUID) ([]domain.Workspace, error) {
	return nil, nil
}

type mockCustomFieldService struct{}

func (m *mockCustomFieldService) Create(_ context.Context, _ *domain.CustomFieldDefinition) error {
	return nil
}
func (m *mockCustomFieldService) GetByID(_ context.Context, _ uuid.UUID) (*domain.CustomFieldDefinition, error) {
	return nil, nil
}
func (m *mockCustomFieldService) Update(_ context.Context, _ *domain.CustomFieldDefinition) error {
	return nil
}
func (m *mockCustomFieldService) Delete(_ context.Context, _ uuid.UUID) error { return nil }
func (m *mockCustomFieldService) ListByProject(_ context.Context, _ uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	return nil, nil
}
func (m *mockCustomFieldService) ListVisibleToAgents(_ context.Context, _ uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	return []domain.CustomFieldDefinition{}, nil
}
func (m *mockCustomFieldService) Reorder(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}
func (m *mockCustomFieldService) ValidateValues(_ context.Context, _ uuid.UUID, _ map[string]interface{}, _ bool) error {
	return nil
}

// --- Helper to create test server ---

func newTestServer() (*Server, *testContext) {
	agentID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()

	session := &AgentSession{
		AgentID:     agentID,
		WorkspaceID: workspaceID,
		AgentName:   "test-agent",
		AgentType:   "claude_code",
	}

	taskSvc := newMockTaskService()
	statusSvc := newMockTaskStatusService()
	agentSvc := newMockAgentService()

	// Set up default project statuses.
	todoStatusID := uuid.New()
	inProgressStatusID := uuid.New()
	doneStatusID := uuid.New()

	statuses := []domain.TaskStatus{
		{ID: todoStatusID, ProjectID: projectID, Name: "To Do", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Position: 0},
		{ID: inProgressStatusID, ProjectID: projectID, Name: "In Progress", Slug: "in_progress", Category: domain.StatusCategoryInProgress, Position: 1},
		{ID: doneStatusID, ProjectID: projectID, Name: "Done", Slug: "done", Category: domain.StatusCategoryDone, Position: 2},
	}
	statusSvc.addStatuses(projectID, statuses)

	// Register agent in mock.
	agentSvc.agents[agentID] = &domain.Agent{
		ID:          agentID,
		WorkspaceID: workspaceID,
		Name:        "test-agent",
		AgentType:   domain.AgentTypeClaudeCode,
		Status:      domain.AgentStatusOnline,
	}

	projectSvc := &mockProjectService{
		projects: []domain.Project{
			{ID: projectID, WorkspaceID: workspaceID, Name: "Test Project", Slug: "test-project"},
		},
		projectMap: map[uuid.UUID]*domain.Project{
			projectID: {ID: projectID, WorkspaceID: workspaceID, Name: "Test Project", Slug: "test-project"},
		},
	}

	srv := NewServer(ServerConfig{
		Session:               session,
		WorkspaceService:      &mockWorkspaceService{},
		ProjectService:        projectSvc,
		TaskService:           taskSvc,
		TaskStatusService:     statusSvc,
		TaskDependencyService: &mockTaskDependencyService{},
		CommentService:        &mockCommentService{},
		ArtifactService:       &mockArtifactService{},
		AgentService:          agentSvc,
		EventBusService:       &mockEventBusService{},
		ActivityLogService:    &mockActivityLogService{},
		CustomFieldService:    &mockCustomFieldService{},
	})

	tc := &testContext{
		agentID:            agentID,
		workspaceID:        workspaceID,
		projectID:          projectID,
		todoStatusID:       todoStatusID,
		inProgressStatusID: inProgressStatusID,
		doneStatusID:       doneStatusID,
	}

	return srv, tc
}

type testContext struct {
	agentID            uuid.UUID
	workspaceID        uuid.UUID
	projectID          uuid.UUID
	todoStatusID       uuid.UUID
	inProgressStatusID uuid.UUID
	doneStatusID       uuid.UUID
}

// makeRequest creates a CallToolRequest with the given arguments.
func makeRequest(args map[string]any) mcpsdk.CallToolRequest {
	return mcpsdk.CallToolRequest{
		Params: struct {
			Name      string     `json:"name"`
			Arguments any        `json:"arguments,omitempty"`
			Meta      *mcpsdk.Meta `json:"_meta,omitempty"`
			Task      *mcpsdk.TaskParams `json:"task,omitempty"`
		}{
			Arguments: args,
		},
	}
}

// --- Tests ---

func TestNewServer(t *testing.T) {
	srv, _ := newTestServer()
	assert.NotNil(t, srv)
	assert.NotNil(t, srv.MCPServer())
}

func TestListProjects(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handleListProjects(ctx, makeRequest(map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	// Parse the result.
	text := mcpsdk.GetTextFromContent(result.Content[0])
	var page pagination.Page[domain.Project]
	require.NoError(t, json.Unmarshal([]byte(text), &page))
	assert.Equal(t, 1, page.TotalCount)
	assert.Equal(t, tc.projectID, page.Items[0].ID)
}

func TestGetProject(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handleGetProject(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
	}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var resp getProjectResponse
	require.NoError(t, json.Unmarshal([]byte(text), &resp))
	assert.Equal(t, "Test Project", resp.Project.Name)
	assert.Len(t, resp.Statuses, 3)
}

func TestCreateTask(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id":  tc.projectID.String(),
		"title":       "Test Task",
		"description": "A test task description",
		"priority":    "high",
		"status_slug": "todo",
	}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var task domain.Task
	require.NoError(t, json.Unmarshal([]byte(text), &task))
	assert.Equal(t, "Test Task", task.Title)
	assert.Equal(t, domain.PriorityHigh, task.Priority)
	assert.Equal(t, tc.todoStatusID, task.StatusID)
	assert.Equal(t, domain.ActorTypeAgent, task.CreatedByType)
}

func TestCreateTask_DefaultStatus(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
		"title":      "Task With Default Status",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var task domain.Task
	require.NoError(t, json.Unmarshal([]byte(text), &task))
	assert.Equal(t, tc.todoStatusID, task.StatusID, "should use default status (todo)")
}

func TestCreateTask_MissingTitle(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestCreateTask_InvalidProjectID(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	result, err := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": "not-a-uuid",
		"title":      "Test",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestListTasks(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	// Create a task first.
	_, _ = srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
		"title":      "Listed Task",
	}))

	result, err := srv.handleListTasks(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestMoveTask(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	// Create a task.
	createResult, _ := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
		"title":      "Task To Move",
	}))
	text := mcpsdk.GetTextFromContent(createResult.Content[0])
	var task domain.Task
	require.NoError(t, json.Unmarshal([]byte(text), &task))

	// Move the task.
	result, err := srv.handleMoveTask(ctx, makeRequest(map[string]any{
		"task_id":     task.ID.String(),
		"status_slug": "in_progress",
		"comment":     "Starting work",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestMoveTask_InvalidSlug(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	// Create a task.
	createResult, _ := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
		"title":      "Task To Move",
	}))
	text := mcpsdk.GetTextFromContent(createResult.Content[0])
	var task domain.Task
	require.NoError(t, json.Unmarshal([]byte(text), &task))

	result, err := srv.handleMoveTask(ctx, makeRequest(map[string]any{
		"task_id":     task.ID.String(),
		"status_slug": "nonexistent_status",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestCreateSubtask(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	// Create parent task.
	createResult, _ := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
		"title":      "Parent Task",
	}))
	text := mcpsdk.GetTextFromContent(createResult.Content[0])
	var parent domain.Task
	require.NoError(t, json.Unmarshal([]byte(text), &parent))

	// Create subtask.
	result, err := srv.handleCreateSubtask(ctx, makeRequest(map[string]any{
		"parent_task_id": parent.ID.String(),
		"title":          "Child Task",
		"priority":       "low",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text = mcpsdk.GetTextFromContent(result.Content[0])
	var subtask domain.Task
	require.NoError(t, json.Unmarshal([]byte(text), &subtask))
	assert.Equal(t, "Child Task", subtask.Title)
	assert.Equal(t, &parent.ID, subtask.ParentTaskID)
}

func TestAssignTask_SelfAssign(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	// Create a task.
	createResult, _ := srv.handleCreateTask(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
		"title":      "Task To Assign",
	}))
	text := mcpsdk.GetTextFromContent(createResult.Content[0])
	var task domain.Task
	require.NoError(t, json.Unmarshal([]byte(text), &task))

	// Self-assign.
	result, err := srv.handleAssignTask(ctx, makeRequest(map[string]any{
		"task_id":        task.ID.String(),
		"assign_to_self": true,
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestAddComment(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	taskID := uuid.New()
	// Seed mock task service.
	srv.taskService.(*mockTaskService).taskMap[taskID] = &domain.Task{
		ID:        taskID,
		ProjectID: tc.projectID,
		Title:     "Commented Task",
	}

	result, err := srv.handleAddComment(ctx, makeRequest(map[string]any{
		"task_id":     taskID.String(),
		"body":        "This is a test comment",
		"is_internal": true,
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var comment domain.Comment
	require.NoError(t, json.Unmarshal([]byte(text), &comment))
	assert.Equal(t, "This is a test comment", comment.Body)
	assert.True(t, comment.IsInternal)
	assert.Equal(t, domain.ActorTypeAgent, comment.AuthorType)
}

func TestAddComment_MissingBody(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	result, err := srv.handleAddComment(ctx, makeRequest(map[string]any{
		"task_id": uuid.New().String(),
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestUploadArtifact(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	taskID := uuid.New()
	result, err := srv.handleUploadArtifact(ctx, makeRequest(map[string]any{
		"task_id":       taskID.String(),
		"name":          "output.json",
		"content":       `{"result": "success"}`,
		"artifact_type": "data",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var artifact domain.Artifact
	require.NoError(t, json.Unmarshal([]byte(text), &artifact))
	assert.Equal(t, "output.json", artifact.Name)
	assert.Equal(t, "application/json", artifact.MimeType)
}

func TestPublishEvent(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handlePublishEvent(ctx, makeRequest(map[string]any{
		"project_id": tc.projectID.String(),
		"event_type": "context_update",
		"subject":    "Test event",
		"payload":    map[string]any{"key": "value"},
		"tags":       []any{"test"},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var msg domain.EventBusMessage
	require.NoError(t, json.Unmarshal([]byte(text), &msg))
	assert.Equal(t, domain.EventTypeContextUpdate, msg.EventType)
	assert.Equal(t, "Test event", msg.Subject)
}

func TestPublishSummary(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handlePublishSummary(ctx, makeRequest(map[string]any{
		"project_id":    tc.projectID.String(),
		"summary":       "Completed feature X",
		"key_decisions": []any{"Used strategy A", "Avoided pattern B"},
		"next_steps":    []any{"Write tests", "Update docs"},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var msg domain.EventBusMessage
	require.NoError(t, json.Unmarshal([]byte(text), &msg))
	assert.Equal(t, domain.EventTypeSummary, msg.EventType)
}

func TestHeartbeat(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	result, err := srv.handleHeartbeat(ctx, makeRequest(map[string]any{
		"status": "busy",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestGetMyTasks(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	// Create and assign a task to the agent.
	agentID := tc.agentID
	srv.taskService.(*mockTaskService).tasks = []domain.Task{
		{
			ID:           uuid.New(),
			ProjectID:    tc.projectID,
			Title:        "My Task",
			AssigneeID:   &agentID,
			AssigneeType: domain.AssigneeTypeAgent,
		},
	}

	result, err := srv.handleGetMyTasks(ctx, makeRequest(map[string]any{}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &resp))
	count := resp["count"].(float64)
	assert.Equal(t, float64(1), count)
}

func TestReportError(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	// Need a task in the mock for project lookup.
	taskID := uuid.New()
	srv.taskService.(*mockTaskService).taskMap[taskID] = &domain.Task{
		ID:        taskID,
		ProjectID: tc.projectID,
		Title:     "Task with error",
	}

	result, err := srv.handleReportError(ctx, makeRequest(map[string]any{
		"task_id":       taskID.String(),
		"error_message": "Something went wrong",
		"severity":      "high",
		"recoverable":   false,
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &resp))
	assert.Equal(t, "reported", resp["status"])
}

func TestSubscribeEvents(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	result, err := srv.handleSubscribeEvents(ctx, makeRequest(map[string]any{
		"project_id":  tc.projectID.String(),
		"event_types": []any{"summary", "error"},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &resp))
	assert.Equal(t, "subscribed", resp["status"])
}

func TestGetTaskContext(t *testing.T) {
	srv, tc := newTestServer()
	ctx := context.Background()

	taskID := uuid.New()
	srv.taskService.(*mockTaskService).taskMap[taskID] = &domain.Task{
		ID:        taskID,
		ProjectID: tc.projectID,
		Title:     "Context Task",
	}

	result, err := srv.handleGetTaskContext(ctx, makeRequest(map[string]any{
		"task_id": taskID.String(),
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var resp taskContextResponse
	require.NoError(t, json.Unmarshal([]byte(text), &resp))
	assert.Equal(t, "Context Task", resp.Task.Title)
}

func TestAddDependency(t *testing.T) {
	srv, _ := newTestServer()
	ctx := context.Background()

	result, err := srv.handleAddDependency(ctx, makeRequest(map[string]any{
		"task_id":            uuid.New().String(),
		"depends_on_task_id": uuid.New().String(),
		"dependency_type":    "relates_to",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := mcpsdk.GetTextFromContent(result.Content[0])
	var dep domain.TaskDependency
	require.NoError(t, json.Unmarshal([]byte(text), &dep))
	assert.Equal(t, domain.DependencyTypeRelatesTo, dep.DependencyType)
}

// --- Helper function tests ---

func TestDetectMIMEType(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"file.json", "application/json"},
		{"file.yaml", "application/x-yaml"},
		{"file.yml", "application/x-yaml"},
		{"file.go", "text/x-go"},
		{"file.py", "text/x-python"},
		{"file.md", "text/markdown"},
		{"file.txt", "text/plain"},
		{"file.png", "image/png"},
		{"file.unknown", "application/octet-stream"},
		{"FILE.JSON", "application/json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, detectMIMEType(tt.name))
		})
	}
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hel...", truncate("hello world", 3))
	assert.Equal(t, "", truncate("", 5))
}

func TestParseUUID(t *testing.T) {
	// Valid UUID.
	id := uuid.New()
	parsed, err := parseUUID(id.String())
	assert.NoError(t, err)
	assert.Equal(t, id, parsed)

	// Empty string.
	_, err = parseUUID("")
	assert.Error(t, err)

	// Invalid string.
	_, err = parseUUID("not-a-uuid")
	assert.Error(t, err)
}

func TestOptionalUUID(t *testing.T) {
	// Empty returns nil.
	result, err := optionalUUID("")
	assert.NoError(t, err)
	assert.Nil(t, result)

	// Valid UUID.
	id := uuid.New()
	result, err = optionalUUID(id.String())
	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, id, *result)

	// Invalid.
	_, err = optionalUUID("bad")
	assert.Error(t, err)
}
