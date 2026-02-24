package service

import (
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// WorkspaceService provides business logic for workspace management.
type WorkspaceService interface {
	Create(ctx context.Context, workspace *domain.Workspace) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error)
	GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error)
	Update(ctx context.Context, workspace *domain.Workspace) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error)
}

// ProjectService provides business logic for project management.
type ProjectService interface {
	Create(ctx context.Context, project *domain.Project) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	Update(ctx context.Context, project *domain.Project) error
	Archive(ctx context.Context, id uuid.UUID) error
	Unarchive(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, workspaceID uuid.UUID, filter repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error)
}

// MoveTaskInput holds parameters for moving a task to a new status and/or position.
type MoveTaskInput struct {
	StatusID *uuid.UUID `json:"status_id"`
	Position *float64   `json:"position"`
}

// AssignTaskInput holds parameters for assigning a task.
type AssignTaskInput struct {
	AssigneeID   *uuid.UUID          `json:"assignee_id"`
	AssigneeType domain.AssigneeType `json:"assignee_type"`
}

// CreateSubtaskInput holds parameters for creating a subtask.
type CreateSubtaskInput struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Priority    domain.Priority `json:"priority"`
}

// TaskService provides business logic for task management.
type TaskService interface {
	Create(ctx context.Context, task *domain.Task) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	Update(ctx context.Context, task *domain.Task) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	MoveTask(ctx context.Context, taskID uuid.UUID, input MoveTaskInput) error
	AssignTask(ctx context.Context, taskID uuid.UUID, input AssignTaskInput) error
	CreateSubtask(ctx context.Context, parentTaskID uuid.UUID, input CreateSubtaskInput) (*domain.Task, error)
	ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error)
	GetMyTasks(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error)
	GetDefaultStatus(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error)
}

// TaskStatusService provides business logic for task status management.
type TaskStatusService interface {
	Create(ctx context.Context, status *domain.TaskStatus) error
	Update(ctx context.Context, status *domain.TaskStatus) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error)
	Reorder(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error
}

// TaskDependencyService provides business logic for task dependencies.
type TaskDependencyService interface {
	Create(ctx context.Context, dep *domain.TaskDependency) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error)
	// CheckCycle validates that adding a dependency does not create a circular reference.
	CheckCycle(ctx context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error)
}

// CustomFieldService provides business logic for custom field definitions.
type CustomFieldService interface {
	Create(ctx context.Context, field *domain.CustomFieldDefinition) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.CustomFieldDefinition, error)
	Update(ctx context.Context, field *domain.CustomFieldDefinition) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error)
	ListVisibleToAgents(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error)
	Reorder(ctx context.Context, projectID uuid.UUID, fieldIDs []uuid.UUID) error
	// ValidateValues validates custom field values against their definitions.
	// When isCreate is true, required fields missing from values produce errors.
	ValidateValues(ctx context.Context, projectID uuid.UUID, values map[string]interface{}, isCreate bool) error
}

// CommentService provides business logic for comments.
type CommentService interface {
	Create(ctx context.Context, comment *domain.Comment) error
	Update(ctx context.Context, comment *domain.Comment) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error)
}

// UploadArtifactInput holds parameters for uploading an artifact.
type UploadArtifactInput struct {
	TaskID         uuid.UUID           `json:"task_id"`
	Name           string              `json:"name"`
	ArtifactType   domain.ArtifactType `json:"artifact_type"`
	MimeType       string              `json:"mime_type"`
	UploadedBy     uuid.UUID           `json:"uploaded_by"`
	UploadedByType domain.UploaderType `json:"uploaded_by_type"`
	Reader         io.Reader           `json:"-"`
	Size           int64               `json:"size"`
}

// ArtifactService provides business logic for artifact management.
type ArtifactService interface {
	Upload(ctx context.Context, input UploadArtifactInput) (*domain.Artifact, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	GetDownloadURL(ctx context.Context, id uuid.UUID) (string, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error)
}

// RegisterAgentInput holds parameters for registering a new agent.
type RegisterAgentInput struct {
	WorkspaceID  uuid.UUID        `json:"workspace_id"`
	Name         string           `json:"name"`
	AgentType    domain.AgentType `json:"agent_type"`
	Capabilities map[string]any   `json:"capabilities"`
}

// RegisterAgentOutput holds the result of agent registration, including the raw API key.
type RegisterAgentOutput struct {
	Agent  *domain.Agent `json:"agent"`
	APIKey string        `json:"api_key"` // Only returned once at registration time
}

// AgentService provides business logic for agent management.
type AgentService interface {
	Register(ctx context.Context, input RegisterAgentInput) (*RegisterAgentOutput, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
	Update(ctx context.Context, agent *domain.Agent) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error)
	Heartbeat(ctx context.Context, agentID uuid.UUID) error
	Authenticate(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error)
	RotateAPIKey(ctx context.Context, agentID uuid.UUID) (string, error)
}

// PublishEventInput holds parameters for publishing an event to the bus.
type PublishEventInput struct {
	WorkspaceID uuid.UUID        `json:"workspace_id"`
	ProjectID   uuid.UUID        `json:"project_id"`
	TaskID      *uuid.UUID       `json:"task_id"`
	AgentID     *uuid.UUID       `json:"agent_id"`
	EventType   domain.EventType `json:"event_type"`
	Subject     string           `json:"subject"`
	Payload     map[string]any   `json:"payload"`
	Tags        []string         `json:"tags"`
	TTLSeconds  int              `json:"ttl_seconds"`
}

// EventBusService provides business logic for the event bus.
type EventBusService interface {
	Publish(ctx context.Context, input PublishEventInput) (*domain.EventBusMessage, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error)
	List(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error)
	GetContext(ctx context.Context, projectID uuid.UUID, opts GetContextOptions) ([]domain.EventBusMessage, error)
	CleanupExpired(ctx context.Context) (int64, error)
}

// EventBusServiceConfigurable extends EventBusService with the ability
// to wire an optional NATS JetStream event bus publisher at runtime.
type EventBusServiceConfigurable interface {
	EventBusService
	SetEventBus(publisher eventbus.Publisher, workspaceRepo repository.WorkspaceRepository, projectRepo repository.ProjectRepository)
}

// GetContextOptions defines options for retrieving context from the event bus.
type GetContextOptions struct {
	TaskID    *uuid.UUID
	AgentID   *uuid.UUID
	EventType *domain.EventType
	Tags      []string
	Limit     int
}

// ActivityLogService provides business logic for activity log entries.
type ActivityLogService interface {
	Log(ctx context.Context, entry *domain.ActivityLog) error
	List(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error)
	ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error)
}
