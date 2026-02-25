package handler

import (
	"context"

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
	CreateFunc     func(ctx context.Context, comment *domain.Comment) error
	UpdateFunc     func(ctx context.Context, comment *domain.Comment) error
	DeleteFunc     func(ctx context.Context, id uuid.UUID) error
	ListByTaskFunc func(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error)
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
	CreateFunc           func(ctx context.Context, task *domain.Task) error
	GetByIDFunc          func(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	UpdateFunc           func(ctx context.Context, task *domain.Task) error
	DeleteFunc           func(ctx context.Context, id uuid.UUID) error
	ListFunc             func(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	MoveTaskFunc         func(ctx context.Context, taskID uuid.UUID, input service.MoveTaskInput) error
	AssignTaskFunc       func(ctx context.Context, taskID uuid.UUID, input service.AssignTaskInput) error
	CreateSubtaskFunc    func(ctx context.Context, parentTaskID uuid.UUID, input service.CreateSubtaskInput) (*domain.Task, error)
	ListSubtasksFunc     func(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error)
	GetMyTasksFunc       func(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error)
	GetDefaultStatusFunc func(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error)
	BulkUpdateFunc       func(ctx context.Context, projectID uuid.UUID, input service.BulkUpdateTasksInput) service.BulkUpdateTasksResult
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

func (m *MockTaskService) GetDefaultStatus(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error) {
	if m.GetDefaultStatusFunc != nil {
		return m.GetDefaultStatusFunc(ctx, projectID)
	}
	return &domain.TaskStatus{ID: uuid.New(), Name: "To Do", IsDefault: true}, nil
}

func (m *MockTaskService) BulkUpdate(ctx context.Context, projectID uuid.UUID, input service.BulkUpdateTasksInput) service.BulkUpdateTasksResult {
	if m.BulkUpdateFunc != nil {
		return m.BulkUpdateFunc(ctx, projectID, input)
	}
	return service.BulkUpdateTasksResult{Updated: len(input.TaskIDs)}
}

// MockAgentService implements service.AgentService for testing.
type MockAgentService struct {
	RegisterFunc     func(ctx context.Context, input service.RegisterAgentInput) (*service.RegisterAgentOutput, error)
	GetByIDFunc      func(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
	UpdateFunc       func(ctx context.Context, agent *domain.Agent) error
	DeleteFunc       func(ctx context.Context, id uuid.UUID) error
	ListFunc         func(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error)
	HeartbeatFunc    func(ctx context.Context, agentID uuid.UUID) error
	AuthenticateFunc func(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error)
	RotateAPIKeyFunc func(ctx context.Context, agentID uuid.UUID) (string, error)
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

func (m *MockAgentService) Heartbeat(ctx context.Context, agentID uuid.UUID) error {
	if m.HeartbeatFunc != nil {
		return m.HeartbeatFunc(ctx, agentID)
	}
	return nil
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
