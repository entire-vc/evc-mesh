package handler

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// MockWorkspaceService implements service.WorkspaceService for testing.
type MockWorkspaceService struct {
	CreateFunc      func(ctx context.Context, workspace *domain.Workspace) error
	GetByIDFunc     func(ctx context.Context, id uuid.UUID) (*domain.Workspace, error)
	GetBySlugFunc   func(ctx context.Context, slug string) (*domain.Workspace, error)
	UpdateFunc      func(ctx context.Context, workspace *domain.Workspace) error
	DeleteFunc      func(ctx context.Context, id uuid.UUID) error
	ListByOwnerFunc func(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error)
}

func (m *MockWorkspaceService) Create(ctx context.Context, workspace *domain.Workspace) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, workspace)
	}
	return nil
}

func (m *MockWorkspaceService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *MockWorkspaceService) GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error) {
	if m.GetBySlugFunc != nil {
		return m.GetBySlugFunc(ctx, slug)
	}
	return nil, nil
}

func (m *MockWorkspaceService) Update(ctx context.Context, workspace *domain.Workspace) error {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, workspace)
	}
	return nil
}

func (m *MockWorkspaceService) Delete(ctx context.Context, id uuid.UUID) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *MockWorkspaceService) ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
	if m.ListByOwnerFunc != nil {
		return m.ListByOwnerFunc(ctx, ownerID)
	}
	return nil, nil
}

// MockProjectService implements service.ProjectService for testing.
type MockProjectService struct {
	CreateFunc    func(ctx context.Context, project *domain.Project) error
	GetByIDFunc   func(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	UpdateFunc    func(ctx context.Context, project *domain.Project) error
	ArchiveFunc   func(ctx context.Context, id uuid.UUID) error
	UnarchiveFunc func(ctx context.Context, id uuid.UUID) error
	ListFunc      func(ctx context.Context, workspaceID uuid.UUID, filter repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error)
}

func (m *MockProjectService) Create(ctx context.Context, project *domain.Project) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, project)
	}
	return nil
}

func (m *MockProjectService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *MockProjectService) Update(ctx context.Context, project *domain.Project) error {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, project)
	}
	return nil
}

func (m *MockProjectService) Archive(ctx context.Context, id uuid.UUID) error {
	if m.ArchiveFunc != nil {
		return m.ArchiveFunc(ctx, id)
	}
	return nil
}

func (m *MockProjectService) Unarchive(ctx context.Context, id uuid.UUID) error {
	if m.UnarchiveFunc != nil {
		return m.UnarchiveFunc(ctx, id)
	}
	return nil
}

func (m *MockProjectService) List(ctx context.Context, workspaceID uuid.UUID, filter repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, workspaceID, filter, pg)
	}
	return nil, nil
}

// MockTaskStatusService implements service.TaskStatusService for testing.
type MockTaskStatusService struct {
	CreateFunc        func(ctx context.Context, status *domain.TaskStatus) error
	UpdateFunc        func(ctx context.Context, status *domain.TaskStatus) error
	DeleteFunc        func(ctx context.Context, id uuid.UUID) error
	ListByProjectFunc func(ctx context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error)
	ReorderFunc       func(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error
}

func (m *MockTaskStatusService) Create(ctx context.Context, status *domain.TaskStatus) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, status)
	}
	return nil
}

func (m *MockTaskStatusService) Update(ctx context.Context, status *domain.TaskStatus) error {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, status)
	}
	return nil
}

func (m *MockTaskStatusService) Delete(ctx context.Context, id uuid.UUID) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *MockTaskStatusService) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error) {
	if m.ListByProjectFunc != nil {
		return m.ListByProjectFunc(ctx, projectID)
	}
	return nil, nil
}

func (m *MockTaskStatusService) Reorder(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error {
	if m.ReorderFunc != nil {
		return m.ReorderFunc(ctx, projectID, statusIDs)
	}
	return nil
}

// MockCommentService implements service.CommentService for testing.
type MockCommentService struct {
	CreateFunc                func(ctx context.Context, comment *domain.Comment) error
	UpdateFunc                func(ctx context.Context, comment *domain.Comment) error
	DeleteFunc                func(ctx context.Context, id uuid.UUID) error
	ListByTaskFunc            func(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error)
	ListByAuthorFunc          func(ctx context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error)
	ListRecentByWorkspaceFunc func(ctx context.Context, wsID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error)
}

func (m *MockCommentService) Create(ctx context.Context, comment *domain.Comment) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, comment)
	}
	return nil
}

func (m *MockCommentService) Update(ctx context.Context, comment *domain.Comment) error {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, comment)
	}
	return nil
}

func (m *MockCommentService) Delete(ctx context.Context, id uuid.UUID) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *MockCommentService) ListByTask(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	if m.ListByTaskFunc != nil {
		return m.ListByTaskFunc(ctx, taskID, filter, pg)
	}
	return nil, nil
}

func (m *MockCommentService) ListByAuthor(ctx context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	if m.ListByAuthorFunc != nil {
		return m.ListByAuthorFunc(ctx, authorID, filter)
	}
	return &domain.CommentViewPage{Items: []domain.CommentView{}}, nil
}

func (m *MockCommentService) ListRecentByWorkspace(ctx context.Context, wsID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	if m.ListRecentByWorkspaceFunc != nil {
		return m.ListRecentByWorkspaceFunc(ctx, wsID, filter)
	}
	return &domain.CommentViewPage{Items: []domain.CommentView{}}, nil
}

// MockTaskDependencyService implements service.TaskDependencyService for testing.
type MockTaskDependencyService struct {
	CreateFunc     func(ctx context.Context, dep *domain.TaskDependency) error
	DeleteFunc     func(ctx context.Context, id uuid.UUID) error
	ListByTaskFunc func(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error)
	CheckCycleFunc func(ctx context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error)
}

func (m *MockTaskDependencyService) Create(ctx context.Context, dep *domain.TaskDependency) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, dep)
	}
	return nil
}

func (m *MockTaskDependencyService) Delete(ctx context.Context, id uuid.UUID) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *MockTaskDependencyService) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	if m.ListByTaskFunc != nil {
		return m.ListByTaskFunc(ctx, taskID)
	}
	return nil, nil
}

func (m *MockTaskDependencyService) CheckCycle(ctx context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error) {
	if m.CheckCycleFunc != nil {
		return m.CheckCycleFunc(ctx, taskID, dependsOnTaskID)
	}
	return false, nil
}

// MockEventBusService implements service.EventBusService for testing.
type MockEventBusService struct {
	PublishFunc        func(ctx context.Context, input service.PublishEventInput) (*domain.EventBusMessage, error)
	GetByIDFunc        func(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error)
	ListFunc           func(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error)
	GetContextFunc     func(ctx context.Context, projectID uuid.UUID, opts service.GetContextOptions) ([]domain.EventBusMessage, error)
	CleanupExpiredFunc func(ctx context.Context) (int64, error)
}

func (m *MockEventBusService) Publish(ctx context.Context, input service.PublishEventInput) (*domain.EventBusMessage, error) {
	if m.PublishFunc != nil {
		return m.PublishFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockEventBusService) GetByID(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *MockEventBusService) List(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, projectID, filter, pg)
	}
	return nil, nil
}

func (m *MockEventBusService) GetContext(ctx context.Context, projectID uuid.UUID, opts service.GetContextOptions) ([]domain.EventBusMessage, error) {
	if m.GetContextFunc != nil {
		return m.GetContextFunc(ctx, projectID, opts)
	}
	return nil, nil
}

func (m *MockEventBusService) CleanupExpired(ctx context.Context) (int64, error) {
	if m.CleanupExpiredFunc != nil {
		return m.CleanupExpiredFunc(ctx)
	}
	return 0, nil
}

// MockActivityLogService implements service.ActivityLogService for testing.
type MockActivityLogService struct {
	LogFunc        func(ctx context.Context, entry *domain.ActivityLog) error
	ListFunc       func(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error)
	ListByTaskFunc func(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error)
}

func (m *MockActivityLogService) Log(ctx context.Context, entry *domain.ActivityLog) error {
	if m.LogFunc != nil {
		return m.LogFunc(ctx, entry)
	}
	return nil
}

func (m *MockActivityLogService) List(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, workspaceID, filter, pg)
	}
	return nil, nil
}

func (m *MockActivityLogService) ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	if m.ListByTaskFunc != nil {
		return m.ListByTaskFunc(ctx, taskID, pg)
	}
	return nil, nil
}

// MockTaskService implements service.TaskService for testing.
type MockTaskService struct {
	CreateFunc               func(ctx context.Context, task *domain.Task) error
	GetByIDFunc              func(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	GetByShortIDFunc         func(ctx context.Context, prefix string) (*domain.Task, error)
	UpdateFunc               func(ctx context.Context, task *domain.Task) error
	DeleteFunc               func(ctx context.Context, id uuid.UUID) error
	ListFunc                 func(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	SearchFunc               func(ctx context.Context, workspaceID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	MoveTaskFunc             func(ctx context.Context, taskID uuid.UUID, input service.MoveTaskInput) error
	AssignTaskFunc           func(ctx context.Context, taskID uuid.UUID, input service.AssignTaskInput) error
	CreateSubtaskFunc        func(ctx context.Context, parentTaskID uuid.UUID, input service.CreateSubtaskInput) (*domain.Task, error)
	ListSubtasksFunc         func(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error)
	GetMyTasksFunc           func(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error)
	GetUserActiveTasksFunc   func(ctx context.Context, workspaceID, userID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error)
	GetDefaultStatusFunc     func(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error)
	GetStatusByIDFunc        func(ctx context.Context, id uuid.UUID) (*domain.TaskStatus, error)
	BulkUpdateFunc           func(ctx context.Context, projectID uuid.UUID, input service.BulkUpdateTasksInput) service.BulkUpdateTasksResult
	CheckoutTaskFunc         func(ctx context.Context, taskID uuid.UUID, ttlMinutes int, sessionMetadata map[string]interface{}) (*service.CheckoutResult, error)
	ReleaseCheckoutFunc      func(ctx context.Context, taskID uuid.UUID, token uuid.UUID) error
	ExtendCheckoutFunc       func(ctx context.Context, taskID uuid.UUID, token uuid.UUID, ttlMinutes int) (*service.CheckoutResult, error)
	ForceReleaseCheckoutFunc func(ctx context.Context, taskID uuid.UUID) error
	MoveToProjectFunc        func(ctx context.Context, taskID, targetProjectID uuid.UUID) (*domain.Task, error)
}

func (m *MockTaskService) Create(ctx context.Context, task *domain.Task) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, task)
	}
	return nil
}

func (m *MockTaskService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *MockTaskService) Update(ctx context.Context, task *domain.Task) error {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, task)
	}
	return nil
}

func (m *MockTaskService) Delete(ctx context.Context, id uuid.UUID) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *MockTaskService) List(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, projectID, filter, pg)
	}
	return nil, nil
}

func (m *MockTaskService) MoveTask(ctx context.Context, taskID uuid.UUID, input service.MoveTaskInput) error {
	if m.MoveTaskFunc != nil {
		return m.MoveTaskFunc(ctx, taskID, input)
	}
	return nil
}

func (m *MockTaskService) AssignTask(ctx context.Context, taskID uuid.UUID, input service.AssignTaskInput) error {
	if m.AssignTaskFunc != nil {
		return m.AssignTaskFunc(ctx, taskID, input)
	}
	return nil
}

func (m *MockTaskService) CreateSubtask(ctx context.Context, parentTaskID uuid.UUID, input service.CreateSubtaskInput) (*domain.Task, error) {
	if m.CreateSubtaskFunc != nil {
		return m.CreateSubtaskFunc(ctx, parentTaskID, input)
	}
	return nil, nil
}

func (m *MockTaskService) ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	if m.ListSubtasksFunc != nil {
		return m.ListSubtasksFunc(ctx, parentTaskID)
	}
	return nil, nil
}

func (m *MockTaskService) GetMyTasks(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error) {
	if m.GetMyTasksFunc != nil {
		return m.GetMyTasksFunc(ctx, assigneeID, assigneeType)
	}
	return nil, nil
}

func (m *MockTaskService) GetUserActiveTasks(ctx context.Context, workspaceID, userID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	if m.GetUserActiveTasksFunc != nil {
		return m.GetUserActiveTasksFunc(ctx, workspaceID, userID, pg)
	}
	return pagination.NewPage([]domain.Task{}, 0, pg), nil
}

func (m *MockTaskService) GetDefaultStatus(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error) {
	if m.GetDefaultStatusFunc != nil {
		return m.GetDefaultStatusFunc(ctx, projectID)
	}
	return &domain.TaskStatus{ID: uuid.New(), Name: "To Do", IsDefault: true}, nil
}

func (m *MockTaskService) GetStatusByID(ctx context.Context, id uuid.UUID) (*domain.TaskStatus, error) {
	if m.GetStatusByIDFunc != nil {
		return m.GetStatusByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *MockTaskService) BulkUpdate(ctx context.Context, projectID uuid.UUID, input service.BulkUpdateTasksInput) service.BulkUpdateTasksResult {
	if m.BulkUpdateFunc != nil {
		return m.BulkUpdateFunc(ctx, projectID, input)
	}
	return service.BulkUpdateTasksResult{Updated: len(input.TaskIDs)}
}

func (m *MockTaskService) CheckoutTask(ctx context.Context, taskID uuid.UUID, ttlMinutes int, sessionMetadata map[string]interface{}) (*service.CheckoutResult, error) {
	if m.CheckoutTaskFunc != nil {
		return m.CheckoutTaskFunc(ctx, taskID, ttlMinutes, sessionMetadata)
	}
	return nil, nil
}

func (m *MockTaskService) ReleaseCheckout(ctx context.Context, taskID, token uuid.UUID) error {
	if m.ReleaseCheckoutFunc != nil {
		return m.ReleaseCheckoutFunc(ctx, taskID, token)
	}
	return nil
}

func (m *MockTaskService) ExtendCheckout(ctx context.Context, taskID, token uuid.UUID, ttlMinutes int) (*service.CheckoutResult, error) {
	if m.ExtendCheckoutFunc != nil {
		return m.ExtendCheckoutFunc(ctx, taskID, token, ttlMinutes)
	}
	return nil, nil
}

func (m *MockTaskService) ForceReleaseCheckout(ctx context.Context, taskID uuid.UUID) error {
	if m.ForceReleaseCheckoutFunc != nil {
		return m.ForceReleaseCheckoutFunc(ctx, taskID)
	}
	return nil
}

func (m *MockTaskService) MoveToProject(ctx context.Context, taskID, targetProjectID uuid.UUID) (*domain.Task, error) {
	if m.MoveToProjectFunc != nil {
		return m.MoveToProjectFunc(ctx, taskID, targetProjectID)
	}
	return nil, nil
}

func (m *MockTaskService) GetByShortID(ctx context.Context, prefix string) (*domain.Task, error) {
	if m.GetByShortIDFunc != nil {
		return m.GetByShortIDFunc(ctx, prefix)
	}
	return nil, nil
}

func (m *MockTaskService) Search(ctx context.Context, workspaceID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	if m.SearchFunc != nil {
		return m.SearchFunc(ctx, workspaceID, filter, pg)
	}
	return nil, nil
}

// MockAgentService implements service.AgentService for testing.
type MockAgentService struct {
	RegisterFunc          func(ctx context.Context, input service.RegisterAgentInput) (*service.RegisterAgentOutput, error)
	GetByIDFunc           func(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
	UpdateFunc            func(ctx context.Context, agent *domain.Agent) error
	DeleteFunc            func(ctx context.Context, id uuid.UUID) error
	ListFunc              func(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error)
	HeartbeatFunc         func(ctx context.Context, agentID uuid.UUID, input *service.HeartbeatInput) error
	CreateActivityLogFunc func(ctx context.Context, entry *domain.AgentActivityLog) error
	ListActivityLogFunc   func(ctx context.Context, agentID uuid.UUID, filter repository.AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error)
	AuthenticateFunc      func(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error)
	RotateAPIKeyFunc      func(ctx context.Context, agentID uuid.UUID) (string, error)
	GetBySlugFunc         func(ctx context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error)
}

func (m *MockAgentService) Register(ctx context.Context, input service.RegisterAgentInput) (*service.RegisterAgentOutput, error) {
	if m.RegisterFunc != nil {
		return m.RegisterFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockAgentService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *MockAgentService) Update(ctx context.Context, agent *domain.Agent) error {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, agent)
	}
	return nil
}

func (m *MockAgentService) Delete(ctx context.Context, id uuid.UUID) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *MockAgentService) List(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, workspaceID, filter, pg)
	}
	return nil, nil
}

func (m *MockAgentService) Heartbeat(ctx context.Context, agentID uuid.UUID, input *service.HeartbeatInput) error {
	if m.HeartbeatFunc != nil {
		return m.HeartbeatFunc(ctx, agentID, input)
	}
	return nil
}

func (m *MockAgentService) CreateActivityLog(ctx context.Context, entry *domain.AgentActivityLog) error {
	if m.CreateActivityLogFunc != nil {
		return m.CreateActivityLogFunc(ctx, entry)
	}
	return nil
}

func (m *MockAgentService) ListActivityLog(ctx context.Context, agentID uuid.UUID, filter repository.AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error) {
	if m.ListActivityLogFunc != nil {
		return m.ListActivityLogFunc(ctx, agentID, filter, pg)
	}
	return pagination.NewPage([]domain.AgentActivityLog{}, 0, pg), nil
}

func (m *MockAgentService) Authenticate(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error) {
	if m.AuthenticateFunc != nil {
		return m.AuthenticateFunc(ctx, workspaceSlug, apiKey)
	}
	return nil, nil
}

func (m *MockAgentService) RotateAPIKey(ctx context.Context, agentID uuid.UUID) (string, error) {
	if m.RotateAPIKeyFunc != nil {
		return m.RotateAPIKeyFunc(ctx, agentID)
	}
	return "", nil
}

func (m *MockAgentService) ListSubAgents(ctx context.Context, parentID uuid.UUID, recursive bool) ([]domain.Agent, error) {
	return nil, nil
}

func (m *MockAgentService) TouchLastSeen(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (m *MockAgentService) GetBySlug(ctx context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error) {
	if m.GetBySlugFunc != nil {
		return m.GetBySlugFunc(ctx, workspaceID, slug)
	}
	return nil, nil
}

// MockRulesService implements service.RulesService for testing.
type MockRulesService struct {
	GetTeamDirectoryFunc            func(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectory, error)
	GetTeamDirectoryTreeFunc        func(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectoryTree, error)
	UpdateAgentProfileFunc          func(ctx context.Context, agentID uuid.UUID, profile domain.AgentProfileUpdate) error
	GetWorkspaceAssignmentRulesFunc func(ctx context.Context, workspaceID uuid.UUID) (*domain.AssignmentRulesConfig, error)
	SetWorkspaceAssignmentRulesFunc func(ctx context.Context, workspaceID uuid.UUID, config domain.AssignmentRulesConfig) error
	GetEffectiveAssignmentRulesFunc func(ctx context.Context, projectID uuid.UUID) (*domain.EffectiveAssignmentRules, error)
	SetProjectAssignmentRulesFunc   func(ctx context.Context, projectID uuid.UUID, config domain.AssignmentRulesConfig) error
	GetProjectWorkflowRulesFunc     func(ctx context.Context, projectID uuid.UUID, callerAgentID *uuid.UUID) (*domain.WorkflowRulesResponse, error)
	SetProjectWorkflowRulesFunc     func(ctx context.Context, projectID uuid.UUID, config domain.WorkflowRulesConfig) error
	ListViolationsFunc              func(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.RuleViolationLog, error)
	LogViolationFunc                func(ctx context.Context, v *domain.RuleViolationLog) error
	ImportConfigFunc                func(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.ImportResult, error)
	ExportConfigFunc                func(ctx context.Context, workspaceID uuid.UUID) ([]byte, error)
	ImportTeamFunc                  func(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.TeamImportResult, error)
	GetWorkflowTemplatesFunc        func(ctx context.Context, workspaceID uuid.UUID) (map[string]domain.WorkflowRulesConfig, error)
	SetWorkflowTemplatesFunc        func(ctx context.Context, workspaceID uuid.UUID, templates map[string]domain.WorkflowRulesConfig) error
}

func (m *MockRulesService) GetTeamDirectory(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectory, error) {
	if m.GetTeamDirectoryFunc != nil {
		return m.GetTeamDirectoryFunc(ctx, workspaceID)
	}
	return &domain.TeamDirectory{}, nil
}
func (m *MockRulesService) GetTeamDirectoryTree(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectoryTree, error) {
	if m.GetTeamDirectoryTreeFunc != nil {
		return m.GetTeamDirectoryTreeFunc(ctx, workspaceID)
	}
	return &domain.TeamDirectoryTree{}, nil
}
func (m *MockRulesService) UpdateAgentProfile(ctx context.Context, agentID uuid.UUID, profile domain.AgentProfileUpdate) error {
	if m.UpdateAgentProfileFunc != nil {
		return m.UpdateAgentProfileFunc(ctx, agentID, profile)
	}
	return nil
}
func (m *MockRulesService) GetWorkspaceAssignmentRules(ctx context.Context, workspaceID uuid.UUID) (*domain.AssignmentRulesConfig, error) {
	if m.GetWorkspaceAssignmentRulesFunc != nil {
		return m.GetWorkspaceAssignmentRulesFunc(ctx, workspaceID)
	}
	return nil, nil
}
func (m *MockRulesService) SetWorkspaceAssignmentRules(ctx context.Context, workspaceID uuid.UUID, config domain.AssignmentRulesConfig) error {
	if m.SetWorkspaceAssignmentRulesFunc != nil {
		return m.SetWorkspaceAssignmentRulesFunc(ctx, workspaceID, config)
	}
	return nil
}
func (m *MockRulesService) GetEffectiveAssignmentRules(ctx context.Context, projectID uuid.UUID) (*domain.EffectiveAssignmentRules, error) {
	if m.GetEffectiveAssignmentRulesFunc != nil {
		return m.GetEffectiveAssignmentRulesFunc(ctx, projectID)
	}
	return nil, nil
}
func (m *MockRulesService) SetProjectAssignmentRules(ctx context.Context, projectID uuid.UUID, config domain.AssignmentRulesConfig) error {
	if m.SetProjectAssignmentRulesFunc != nil {
		return m.SetProjectAssignmentRulesFunc(ctx, projectID, config)
	}
	return nil
}
func (m *MockRulesService) GetProjectWorkflowRules(ctx context.Context, projectID uuid.UUID, callerAgentID *uuid.UUID) (*domain.WorkflowRulesResponse, error) {
	if m.GetProjectWorkflowRulesFunc != nil {
		return m.GetProjectWorkflowRulesFunc(ctx, projectID, callerAgentID)
	}
	return nil, nil
}
func (m *MockRulesService) SetProjectWorkflowRules(ctx context.Context, projectID uuid.UUID, config domain.WorkflowRulesConfig) error {
	if m.SetProjectWorkflowRulesFunc != nil {
		return m.SetProjectWorkflowRulesFunc(ctx, projectID, config)
	}
	return nil
}
func (m *MockRulesService) ListViolations(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.RuleViolationLog, error) {
	if m.ListViolationsFunc != nil {
		return m.ListViolationsFunc(ctx, workspaceID, limit)
	}
	return nil, nil
}
func (m *MockRulesService) LogViolation(ctx context.Context, v *domain.RuleViolationLog) error {
	if m.LogViolationFunc != nil {
		return m.LogViolationFunc(ctx, v)
	}
	return nil
}
func (m *MockRulesService) ImportConfig(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.ImportResult, error) {
	if m.ImportConfigFunc != nil {
		return m.ImportConfigFunc(ctx, workspaceID, yamlData)
	}
	return nil, nil
}
func (m *MockRulesService) ExportConfig(ctx context.Context, workspaceID uuid.UUID) ([]byte, error) {
	if m.ExportConfigFunc != nil {
		return m.ExportConfigFunc(ctx, workspaceID)
	}
	return nil, nil
}
func (m *MockRulesService) ImportTeam(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.TeamImportResult, error) {
	if m.ImportTeamFunc != nil {
		return m.ImportTeamFunc(ctx, workspaceID, yamlData)
	}
	return nil, nil
}
func (m *MockRulesService) GetWorkflowTemplates(ctx context.Context, workspaceID uuid.UUID) (map[string]domain.WorkflowRulesConfig, error) {
	if m.GetWorkflowTemplatesFunc != nil {
		return m.GetWorkflowTemplatesFunc(ctx, workspaceID)
	}
	return nil, nil
}
func (m *MockRulesService) SetWorkflowTemplates(ctx context.Context, workspaceID uuid.UUID, templates map[string]domain.WorkflowRulesConfig) error {
	if m.SetWorkflowTemplatesFunc != nil {
		return m.SetWorkflowTemplatesFunc(ctx, workspaceID, templates)
	}
	return nil
}

// MockMemoryService implements service.MemoryService for testing.
// Only the methods needed by CanonicalUpdatesHandler are fully wired;
// all others are no-ops.
type MockMemoryService struct {
	ListMemoriesFunc func(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error)
}

func (m *MockMemoryService) ListMemories(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error) {
	if m.ListMemoriesFunc != nil {
		return m.ListMemoriesFunc(ctx, filter)
	}
	return &service.RecallResult{Items: []domain.ScoredMemory{}}, nil
}

func (m *MockMemoryService) Remember(ctx context.Context, mem *domain.Memory) (string, error) {
	return "created", nil
}
func (m *MockMemoryService) Recall(ctx context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, error) {
	return nil, nil
}
func (m *MockMemoryService) GetProjectKnowledge(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]domain.Memory, error) {
	return nil, nil
}
func (m *MockMemoryService) SetProjectKnowledge(ctx context.Context, input service.SetProjectKnowledgeInput) (*domain.Memory, string, error) {
	return nil, "", nil
}
func (m *MockMemoryService) Forget(ctx context.Context, id uuid.UUID, actorAgentID *uuid.UUID, isAdmin bool) error {
	return nil
}
func (m *MockMemoryService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error) {
	return nil, nil
}
func (m *MockMemoryService) ExportMemories(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]byte, error) {
	return nil, nil
}
func (m *MockMemoryService) ImportMemories(ctx context.Context, workspaceID uuid.UUID, data []byte) (int, error) {
	return 0, nil
}
func (m *MockMemoryService) BatchEmbed(ctx context.Context, workspaceID uuid.UUID) (int, error) {
	return 0, nil
}
func (m *MockMemoryService) FindRelated(ctx context.Context, memoryID uuid.UUID, limit int) ([]domain.ScoredMemory, error) {
	return nil, nil
}
func (m *MockMemoryService) ExtractFromEvent(ctx context.Context, event *domain.EventBusMessage, hint *domain.MemoryHint) error {
	return nil
}
func (m *MockMemoryService) Supersede(ctx context.Context, oldID, newID uuid.UUID) error {
	return nil
}

// MockAgentSessionRepository implements repository.AgentSessionRepository for testing.
type MockAgentSessionRepository struct {
	GetPreviousStartedAtFunc func(ctx context.Context, agentID uuid.UUID) (*time.Time, error)
}

func (m *MockAgentSessionRepository) Create(ctx context.Context, session *domain.AgentSession) error {
	return nil
}
func (m *MockAgentSessionRepository) Update(ctx context.Context, session *domain.AgentSession) error {
	return nil
}
func (m *MockAgentSessionRepository) GetActive(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error) {
	return nil, nil
}
func (m *MockAgentSessionRepository) EndStale(ctx context.Context, timeout time.Duration) (int, error) {
	return 0, nil
}
func (m *MockAgentSessionRepository) GetPreviousStartedAt(ctx context.Context, agentID uuid.UUID) (*time.Time, error) {
	if m.GetPreviousStartedAtFunc != nil {
		return m.GetPreviousStartedAtFunc(ctx, agentID)
	}
	return nil, nil
}
