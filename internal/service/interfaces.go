package service

import (
	"context"
	"io"
	"time"

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

// BulkUpdateTasksInput holds parameters for a bulk task update operation.
type BulkUpdateTasksInput struct {
	TaskIDs      []uuid.UUID          `json:"task_ids"`
	StatusID     *uuid.UUID           `json:"status_id,omitempty"`
	Priority     *domain.Priority     `json:"priority,omitempty"`
	AssigneeID   *uuid.UUID           `json:"assignee_id,omitempty"`
	AssigneeType *domain.AssigneeType `json:"assignee_type,omitempty"`
	Labels       *[]string            `json:"labels,omitempty"`
}

// BulkUpdateTasksResult holds the outcome of a bulk update operation.
type BulkUpdateTasksResult struct {
	Updated int
	Errors  []string
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
	BulkUpdate(ctx context.Context, projectID uuid.UUID, input BulkUpdateTasksInput) BulkUpdateTasksResult
}

// TaskServiceAutoTransitionConfigurable extends TaskService with the ability
// to wire an optional AutoTransitionService at runtime.
type TaskServiceAutoTransitionConfigurable interface {
	TaskService
	SetAutoTransitionService(svc AutoTransitionService)
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
	WorkspaceID   uuid.UUID        `json:"workspace_id"`
	Name          string           `json:"name"`
	AgentType     domain.AgentType `json:"agent_type"`
	Capabilities  map[string]any   `json:"capabilities"`
	ParentAgentID *uuid.UUID       `json:"parent_agent_id,omitempty"`
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
	// ListSubAgents returns child agents of a parent.
	// When recursive is true, all descendants (up to 10 levels) are returned via a CTE.
	// When recursive is false, only direct children are returned.
	ListSubAgents(ctx context.Context, parentID uuid.UUID, recursive bool) ([]domain.Agent, error)
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
	// Export returns up to limit activity log entries matching the filter, for CSV/JSON export.
	Export(ctx context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, limit int) ([]domain.ActivityLog, error)
}

// SavedViewService provides business logic for saved view management.
type SavedViewService interface {
	Create(ctx context.Context, input domain.CreateSavedViewInput) (*domain.SavedView, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.SavedView, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateSavedViewInput, callerID uuid.UUID) (*domain.SavedView, error)
	Delete(ctx context.Context, id uuid.UUID, callerID uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID, userID uuid.UUID) ([]domain.SavedView, error)
}

// CreateProjectUpdateInput holds the fields for creating a project update.
type CreateProjectUpdateInput struct {
	ProjectID  uuid.UUID       `json:"project_id"`
	Title      string          `json:"title"`
	Status     domain.UpdateStatus `json:"status"`
	Summary    string          `json:"summary"`
	Highlights []domain.TextItem `json:"highlights"`
	Blockers   []domain.TextItem `json:"blockers"`
	NextSteps  []domain.TextItem `json:"next_steps"`
	CreatedBy  uuid.UUID       `json:"created_by"`
}

// ProjectUpdateService provides business logic for project updates.
type ProjectUpdateService interface {
	Create(ctx context.Context, input CreateProjectUpdateInput) (*domain.ProjectUpdate, error)
	List(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ProjectUpdate], error)
	GetLatest(ctx context.Context, projectID uuid.UUID) (*domain.ProjectUpdate, error)
}

// CreateInitiativeInput holds the fields for creating an initiative.
type CreateInitiativeInput struct {
	WorkspaceID uuid.UUID        `json:"workspace_id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Status      domain.InitiativeStatus `json:"status"`
	TargetDate  *time.Time       `json:"target_date"`
	CreatedBy   uuid.UUID        `json:"created_by"`
}

// UpdateInitiativeInput holds the fields for partially updating an initiative.
type UpdateInitiativeInput struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Status      *domain.InitiativeStatus `json:"status"`
	TargetDate  *time.Time       `json:"target_date"`
}

// InitiativeService provides business logic for initiative management.
type InitiativeService interface {
	Create(ctx context.Context, input CreateInitiativeInput) (*domain.Initiative, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Initiative, error)
	Update(ctx context.Context, id uuid.UUID, input UpdateInitiativeInput) (*domain.Initiative, error)
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, workspaceID uuid.UUID) ([]domain.Initiative, error)
	LinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error
	UnlinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error
}

// TriageService provides business logic for the triage inbox.
type TriageService interface {
	ListTriageTasks(ctx context.Context, workspaceID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error)
}

// SlackService sends notifications to a Slack workspace via Incoming Webhooks.
// NotifyTaskEvent is fire-and-forget: it spawns a goroutine and never blocks the caller.
type SlackService interface {
	// SendMessage POSTs a SlackMessage to the given Incoming Webhook URL.
	SendMessage(ctx context.Context, webhookURL string, message SlackMessage) error
	// NotifyTaskEvent looks up the active Slack integration for the workspace and
	// sends a rich notification if the event type is subscribed. Fire-and-forget.
	NotifyTaskEvent(ctx context.Context, workspaceID uuid.UUID, event TaskEvent)
}

// WebhookService provides business logic for outbound webhook management.
type WebhookService interface {
	Create(ctx context.Context, input domain.CreateWebhookInput) (*domain.WebhookConfig, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.WebhookConfig, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateWebhookInput) (*domain.WebhookConfig, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WebhookConfig, error)
	ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit int) ([]domain.WebhookDelivery, error)
	// Dispatch finds active webhooks for the given event type and fires HTTP POSTs
	// asynchronously (fire-and-forget). It never blocks or returns an error to the caller.
	Dispatch(ctx context.Context, workspaceID uuid.UUID, eventType string, payload any)
	// TestDelivery sends a test HTTP POST directly to the webhook's URL, bypassing
	// event subscription filtering. The delivery is recorded in the delivery log.
	// It is asynchronous and always returns immediately.
	TestDelivery(ctx context.Context, webhookID uuid.UUID)
}

// VCSLinkService provides business logic for VCS link management.
type VCSLinkService interface {
	Create(ctx context.Context, input domain.CreateVCSLinkInput) (*domain.VCSLink, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error)
}

// IntegrationService provides business logic for workspace integration configs.
type IntegrationService interface {
	Configure(ctx context.Context, input domain.CreateIntegrationInput) (*domain.IntegrationConfig, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.IntegrationConfig, error)
}

// WorkspaceMemberService provides business logic for workspace member management.
type WorkspaceMemberService interface {
	ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error)
	AddMember(ctx context.Context, workspaceID uuid.UUID, email, role string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error)
	UpdateMemberRole(ctx context.Context, workspaceID, targetUserID uuid.UUID, newRole string) error
	RemoveMember(ctx context.Context, workspaceID, targetUserID uuid.UUID) error
	GetMyRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error)
	// SearchUsers searches for users by email or name and annotates each result with membership status.
	SearchUsers(ctx context.Context, workspaceID uuid.UUID, query string) ([]domain.UserWithMemberStatus, error)
}

// ProjectMemberService provides business logic for project member management.
type ProjectMemberService interface {
	ListMembers(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectMemberWithUser, error)
	AddMember(ctx context.Context, projectID, userID uuid.UUID, role string) (*domain.ProjectMemberWithUser, error)
	AddAgentMember(ctx context.Context, projectID, agentID uuid.UUID, role string) (*domain.ProjectMemberWithUser, error)
	UpdateMemberRole(ctx context.Context, projectID, userID uuid.UUID, newRole string) error
	RemoveMember(ctx context.Context, projectID, userID uuid.UUID) error
	RemoveAgentMember(ctx context.Context, projectID, agentID uuid.UUID) error
}

// RuleService provides business logic for governance rule management.
type RuleService interface {
	Create(ctx context.Context, input CreateRuleInput) (*domain.Rule, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Rule, error)
	Update(ctx context.Context, id uuid.UUID, input UpdateRuleInput) (*domain.Rule, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, includeDisabled bool) ([]domain.Rule, error)
	ListByProject(ctx context.Context, projectID uuid.UUID, includeDisabled bool) ([]domain.Rule, error)
	ListByAgent(ctx context.Context, agentID uuid.UUID, includeDisabled bool) ([]domain.Rule, error)
	// GetEffective resolves inheritance and returns the effective rules for the given context.
	GetEffective(ctx context.Context, ruleCtx RuleContext) ([]domain.Rule, error)
	// Evaluate runs effective rules through evaluators and returns violations.
	Evaluate(ctx context.Context, input EvaluateInput) ([]domain.RuleViolation, error)
}

// RulesService provides business logic for the workspace/project rules system (Sprint 20).
type RulesService interface {
	// Team Directory
	GetTeamDirectory(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectory, error)
	UpdateAgentProfile(ctx context.Context, agentID uuid.UUID, profile domain.AgentProfileUpdate) error

	// Assignment Rules
	GetWorkspaceAssignmentRules(ctx context.Context, workspaceID uuid.UUID) (*domain.AssignmentRulesConfig, error)
	SetWorkspaceAssignmentRules(ctx context.Context, workspaceID uuid.UUID, config domain.AssignmentRulesConfig) error
	GetEffectiveAssignmentRules(ctx context.Context, projectID uuid.UUID) (*domain.EffectiveAssignmentRules, error)
	SetProjectAssignmentRules(ctx context.Context, projectID uuid.UUID, config domain.AssignmentRulesConfig) error

	// Workflow Rules
	GetProjectWorkflowRules(ctx context.Context, projectID uuid.UUID, callerAgentID *uuid.UUID) (*domain.WorkflowRulesResponse, error)
	SetProjectWorkflowRules(ctx context.Context, projectID uuid.UUID, config domain.WorkflowRulesConfig) error

	// Violations
	ListViolations(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.RuleViolationLog, error)
	LogViolation(ctx context.Context, v *domain.RuleViolationLog) error

	// Config Import/Export (Sprint 21)
	ImportConfig(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.ImportResult, error)
	ExportConfig(ctx context.Context, workspaceID uuid.UUID) ([]byte, error)
	ImportTeam(ctx context.Context, workspaceID uuid.UUID, yamlData []byte) (*domain.TeamImportResult, error)

	// Workflow Templates (Sprint 21)
	GetWorkflowTemplates(ctx context.Context, workspaceID uuid.UUID) (map[string]domain.WorkflowRulesConfig, error)
	SetWorkflowTemplates(ctx context.Context, workspaceID uuid.UUID, templates map[string]domain.WorkflowRulesConfig) error
}

// CreateRecurringInput holds parameters for creating a recurring schedule.
type CreateRecurringInput struct {
	WorkspaceID         uuid.UUID
	ProjectID           uuid.UUID
	TitleTemplate       string
	DescriptionTemplate string
	Frequency           domain.RecurringFrequency
	CronExpr            string
	Timezone            string
	AssigneeID          *uuid.UUID
	AssigneeType        domain.AssigneeType
	Priority            domain.Priority
	Labels              []string
	StatusID            *uuid.UUID
	IsActive            bool
	StartsAt            time.Time
	EndsAt              *time.Time
	MaxInstances        *int
	CreatedBy           uuid.UUID
	CreatedByType       domain.ActorType
}

// UpdateRecurringInput holds parameters for updating a recurring schedule.
// All fields are optional (pointer = nil means "don't change").
type UpdateRecurringInput struct {
	TitleTemplate       *string
	DescriptionTemplate *string
	Frequency           *domain.RecurringFrequency
	CronExpr            *string
	Timezone            *string
	AssigneeID          *uuid.UUID
	AssigneeType        *domain.AssigneeType
	Priority            *domain.Priority
	Labels              *[]string
	StatusID            *uuid.UUID
	IsActive            *bool
	EndsAt              *time.Time
	MaxInstances        *int
}

// RecurringService provides business logic for recurring task schedules.
type RecurringService interface {
	Create(ctx context.Context, input CreateRecurringInput) (*domain.RecurringSchedule, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.RecurringSchedule, error)
	Update(ctx context.Context, id uuid.UUID, input UpdateRecurringInput) (*domain.RecurringSchedule, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringSchedule], error)

	// TriggerNow creates the next instance immediately, ignoring next_run_at.
	// Returns the created task instance.
	TriggerNow(ctx context.Context, id uuid.UUID) (*domain.Task, error)

	// GetHistory returns all task instances for a recurring schedule, newest first.
	GetHistory(ctx context.Context, id uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringInstanceSummary], error)

	// RunDue is called by the scheduler goroutine. It finds all active schedules
	// where next_run_at <= now and creates task instances for each.
	// Returns the number of instances created.
	RunDue(ctx context.Context) (int, error)
}

// TaskTemplateService provides business logic for reusable task templates.
type TaskTemplateService interface {
	Create(ctx context.Context, input domain.CreateTemplateInput) (*domain.TaskTemplate, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskTemplate, error)
	List(ctx context.Context, projectID uuid.UUID) ([]domain.TaskTemplate, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateTemplateInput) (*domain.TaskTemplate, error)
	Delete(ctx context.Context, id uuid.UUID) error
	// CreateTaskFromTemplate instantiates a new task from the given template, applying
	// any caller-supplied field overrides (title, description, priority, labels,
	// assignee_id, assignee_type, status_id, estimated_hours).
	CreateTaskFromTemplate(ctx context.Context, templateID uuid.UUID, createdBy uuid.UUID, createdByType domain.ActorType, overrides map[string]any) (*domain.Task, error)
}

// AnalyticsMetrics holds aggregated workspace/project metrics.
type AnalyticsMetrics struct {
	TaskMetrics  TaskMetrics  `json:"task_metrics"`
	AgentMetrics AgentMetrics `json:"agent_metrics"`
	EventMetrics EventMetrics `json:"event_metrics"`
	Timeline     []DayMetric  `json:"timeline"`
}

// TaskMetrics holds task-level aggregated data.
type TaskMetrics struct {
	Total               int            `json:"total"`
	ByStatusCategory    map[string]int `json:"by_status_category"`
	ByPriority          map[string]int `json:"by_priority"`
	CreatedThisPeriod   int            `json:"created_this_period"`
	CompletedThisPeriod int            `json:"completed_this_period"`
}

// AgentMetrics holds agent-level aggregated data.
type AgentMetrics struct {
	TotalAgents  int            `json:"total_agents"`
	ActiveAgents int            `json:"active_agents"`
	TasksByAgent []AgentTaskRow `json:"tasks_by_agent"`
}

// AgentTaskRow holds per-agent task completion stats.
type AgentTaskRow struct {
	AgentID   uuid.UUID `json:"agent_id"`
	AgentName string    `json:"agent_name"`
	Completed int       `json:"completed"`
}

// EventMetrics holds event bus aggregated data.
type EventMetrics struct {
	TotalEvents int            `json:"total_events"`
	ByType      map[string]int `json:"by_type"`
}

// DayMetric holds the daily task creation/completion counts.
type DayMetric struct {
	Date      string `json:"date"`
	Created   int    `json:"created"`
	Completed int    `json:"completed"`
}

// AnalyticsFilter defines the filtering parameters for analytics queries.
type AnalyticsFilter struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	From        time.Time
	To          time.Time
}

// AnalyticsService provides business logic for analytics queries.
type AnalyticsService interface {
	GetMetrics(ctx context.Context, filter AnalyticsFilter) (*AnalyticsMetrics, error)
}
