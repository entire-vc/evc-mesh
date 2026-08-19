package service

import (
	"context"
	"encoding/json"
	"fmt"
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
	// ListForUser returns workspaces visible to the user: those they are a
	// member of plus those they own.
	ListForUser(ctx context.Context, userID uuid.UUID) ([]domain.Workspace, error)
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
	StatusID     *uuid.UUID          `json:"status_id"`
	Position     *float64            `json:"position"`
	AssigneeID   *uuid.UUID          `json:"assignee_id,omitempty"`   // explicit reassign (skips auto-reassign)
	AssigneeType domain.AssigneeType `json:"assignee_type,omitempty"` // required if AssigneeID is set

	// CAS preconditions — optional, ignored if nil. Either or both can be set.
	// If the current task state does not match, MoveTask returns CASConflictError.
	ExpectedStatusID  *uuid.UUID `json:"expected_status_id,omitempty"`
	ExpectedUpdatedAt *time.Time `json:"expected_updated_at,omitempty"`

	// Source identifies the call origin for audit log: "mcp", "api", "ui".
	// Not bound from the JSON body directly; set by the handler layer.
	Source string `json:"-"`
}

// AssignTaskInput holds parameters for assigning a task.
type AssignTaskInput struct {
	AssigneeID   *uuid.UUID          `json:"assignee_id"`
	AssigneeType domain.AssigneeType `json:"assignee_type"`
	// Source indicates who or what is making the assignment.
	// When Source == AssignmentSourceHuman the pin is set (or updated by another human).
	// When Source == AssignmentSourceRule or AssignmentSourceSystem and the task is
	// already pinned by a human, AssignTask returns AssignmentPinnedError (422).
	// Defaults to AssignmentSourceSystem when omitted.
	Source domain.AssignmentSource `json:"source"`
}

// CreateSubtaskInput holds parameters for creating a subtask.
type CreateSubtaskInput struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Priority    domain.Priority `json:"priority"`
	// StatusID pins the subtask's initial status. When nil the project's
	// default status is used — never the parent's status.
	StatusID *uuid.UUID `json:"status_id,omitempty"`
	// AssigneeID/AssigneeType mirror CreateTask's field-level contract: when
	// AssigneeID is set and AssigneeType is empty, the caller (handler) infers
	// "agent" so the explicit assignment is not silently clobbered by
	// applyAutoAssign, which only fires when AssigneeType is unassigned.
	AssigneeID     *uuid.UUID          `json:"assignee_id,omitempty"`
	AssigneeType   domain.AssigneeType `json:"assignee_type,omitempty"`
	Labels         []string            `json:"labels,omitempty"`
	CustomFields   json.RawMessage     `json:"custom_fields,omitempty"`
	DueDate        *time.Time          `json:"due_date,omitempty"`
	EstimatedHours *float64            `json:"estimated_hours,omitempty"`
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

// CheckoutResult is returned when a checkout is successfully acquired or extended.
type CheckoutResult struct {
	TaskID          uuid.UUID              `json:"task_id"`
	CheckoutToken   uuid.UUID              `json:"checkout_token"`
	CheckedOutBy    uuid.UUID              `json:"checked_out_by"`
	ExpiresAt       time.Time              `json:"expires_at"`
	DelegationLevel domain.DelegationLevel `json:"delegation_level"`
	ProjectID       uuid.UUID              `json:"project_id"`
}

// CheckoutConflictError is returned when CheckoutTask finds the task locked by
// a different non-expired agent. CheckedOutByName and CheckedOutByKind are
// best-effort: empty when the holder lookup fails (e.g. agent record deleted).
// AcquiredAt is nil when the task has no checkout_acquired_at record (locks
// created before the migration will lack this field).
type CheckoutConflictError struct {
	CheckedOutBy     uuid.UUID
	CheckedOutByName string
	CheckedOutByKind string
	ExpiresAt        time.Time
	AcquiredAt       *time.Time
}

func (e *CheckoutConflictError) Error() string {
	if e.CheckedOutByName != "" {
		return fmt.Sprintf("task is already checked out by %s (%s) until %s", e.CheckedOutByName, e.CheckedOutBy, e.ExpiresAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("task is already checked out by %s until %s", e.CheckedOutBy, e.ExpiresAt.Format(time.RFC3339))
}

// TaskService provides business logic for task management.
type TaskService interface {
	Create(ctx context.Context, task *domain.Task) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	// GetByShortID resolves a task by 6–12 char hex UUID prefix.
	GetByShortID(ctx context.Context, prefix string) (*domain.Task, error)
	Update(ctx context.Context, task *domain.Task) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	// Search searches tasks across all projects in a workspace.
	Search(ctx context.Context, workspaceID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	MoveTask(ctx context.Context, taskID uuid.UUID, input MoveTaskInput) error
	AssignTask(ctx context.Context, taskID uuid.UUID, input AssignTaskInput) error
	CreateSubtask(ctx context.Context, parentTaskID uuid.UUID, input CreateSubtaskInput) (*domain.Task, error)
	ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error)
	// GetMyTasks lists the principal's tasks inside one workspace. The caller must
	// pass the workspace of the AUTHENTICATED principal, never one taken from the
	// request, or the scoping is decorative.
	//
	// filter is applied in SQL. The second return value is the total number of
	// matches before filter.Limit was applied, so the handler can report a
	// truncated feed as truncated rather than as "no more work".
	GetMyTasks(ctx context.Context, workspaceID, assigneeID uuid.UUID, assigneeType domain.AssigneeType, filter repository.AssigneeTaskFilter) (tasks []domain.Task, total int, err error)
	// GetUserActiveTasks returns non-done/cancelled tasks for a human user in a workspace.
	GetUserActiveTasks(ctx context.Context, workspaceID, userID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error)
	GetDefaultStatus(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error)
	GetStatusByID(ctx context.Context, id uuid.UUID) (*domain.TaskStatus, error)
	BulkUpdate(ctx context.Context, projectID uuid.UUID, input BulkUpdateTasksInput) BulkUpdateTasksResult
	// CheckoutTask acquires an exclusive application-level lock on the task for the
	// calling agent. Only agents may checkout. Returns CheckoutConflictError when the
	// task is already locked by a different non-expired agent. sessionMetadata is
	// optional forensic context (hostname, pid, branch, etc.) recorded into the
	// activity log entry — pass nil to omit.
	CheckoutTask(ctx context.Context, taskID uuid.UUID, ttlMinutes int, sessionMetadata map[string]interface{}) (*CheckoutResult, error)
	// ReleaseCheckout releases the checkout identified by the given token.
	// Returns an error when the token does not match.
	ReleaseCheckout(ctx context.Context, taskID, token uuid.UUID) error
	// SelfReleaseCheckout releases the checkout held by the calling agent without
	// requiring the checkout_token. The caller's identity (from actorctx) must
	// match the current lock holder; otherwise 403 is returned. No-op when the
	// task is not locked.
	SelfReleaseCheckout(ctx context.Context, taskID uuid.UUID) error
	// ExtendCheckout extends the checkout TTL identified by the given token.
	// Returns an error when the token does not match or the checkout has expired.
	ExtendCheckout(ctx context.Context, taskID, token uuid.UUID, ttlMinutes int) (*CheckoutResult, error)
	// ForceReleaseCheckout clears the checkout without token verification.
	// Intended for admin recovery when the holder cannot release the lock
	// (e.g. crash, lost token). Callers must enforce authorization themselves.
	ForceReleaseCheckout(ctx context.Context, taskID uuid.UUID) error
	// MoveToProject moves a task to a different project, resetting status to the
	// target project's default and recalculating task_number atomically.
	// Returns an error if the task is already in the target project.
	MoveToProject(ctx context.Context, taskID, targetProjectID uuid.UUID) (*domain.Task, error)
	// SupersedeRecurringInstances closes all non-terminal instances of the given
	// recurring schedule (except newTaskID) by moving them to the project's done status.
	// Individual failures are logged and skipped to avoid blocking the new instance.
	SupersedeRecurringInstances(ctx context.Context, scheduleID, newTaskID uuid.UUID) error
	// SetHumanGate arms (value=true) or clears (value=false) the sticky human-gate flag.
	// When armed, only a human actor may move the task to backlog/done/cancelled.
	SetHumanGate(ctx context.Context, taskID uuid.UUID, value bool) error
	// ShipTask marks the task as terminally shipped (is_shipped=true). Once set,
	// MoveTask to any non-done category returns TaskShippedError (422).
	// Pass shipped=false to clear the flag (unship).
	ShipTask(ctx context.Context, taskID uuid.UUID, shipped bool) error
	// SetDodCheck upserts a named gate entry in the task's dod_checks map.
	// status must be "pending", "pass", or "fail".
	// reporter is a free-form string identifying the caller (e.g. "verify-driver").
	// Returns an error if the gate name is not configured on the project's dod_gates.
	SetDodCheck(ctx context.Context, taskID uuid.UUID, gateName, status, reporter string) error
	// ValidateAssigneeForProject resolves assigneeID's true type via directory
	// lookup (the same resolution every task write path applies before
	// enrolling) and refuses it with AssigneeNotInWorkspaceError if it does not
	// belong to the workspace owning projectID — by calling the same tenancy
	// funnel task writes go through, not a re-implementation of it.
	//
	// It exists for callers that persist an assignee_id without ever going
	// through Create/Update/MoveTask/AssignTask themselves: a task template or a
	// recurring schedule stores the assignee in its own row, and that row is not
	// a task, so none of the seven task write-path guards ever see it. Without
	// this call a foreign assignee saved into a template or schedule is accepted
	// silently and only refused later, at materialization time, as an opaque
	// failed task-creation.
	//
	// assigneeID == nil (or the nil UUID) returns (AssigneeTypeUnassigned, nil)
	// without any lookup — there is nothing to validate. Returns the resolved
	// type so the caller can persist it instead of trusting the caller-supplied
	// one, exactly as Create/Update do for tasks.
	ValidateAssigneeForProject(ctx context.Context, projectID uuid.UUID, assigneeID *uuid.UUID, assigneeType domain.AssigneeType) (domain.AssigneeType, error)
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
	// ListByTask returns the statuses of the project owning the given task, so a
	// workspace-gated caller can resolve a status slug for a workspace-gated move.
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskStatus, error)
	Reorder(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error
}

// TaskDependencyService provides business logic for task dependencies.
type TaskDependencyService interface {
	Create(ctx context.Context, dep *domain.TaskDependency) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error)
	// ListByTaskBothDirections returns taskID's dependencies split by direction,
	// each enriched with the related task's title and status. Outgoing = edges
	// where taskID is task_id (unchanged ListByTask semantics). Incoming = edges
	// where taskID is depends_on_task_id — e.g. the is_child_of edges its own
	// subtasks recorded, which ListByTask alone never surfaced because it only
	// ever queried WHERE task_id = $1.
	ListByTaskBothDirections(ctx context.Context, taskID uuid.UUID) (outgoing, incoming []domain.EnrichedTaskDependency, err error)
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
	ListByAuthor(ctx context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error)
	ListRecentByWorkspace(ctx context.Context, wsID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error)
	// GetHumanGateOwner reports who owns taskID's currently-live human_gate
	// ask (task #040cddcf), read-only — see the domain.HumanGateInfo and
	// commentService.scanHumanGateOwnership doc comments for the full rule.
	GetHumanGateOwner(ctx context.Context, taskID uuid.UUID) (*domain.HumanGateInfo, error)
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
	// GetByIDInWorkspace is the workspace-scoped variant: returns 404 when the
	// artifact belongs to a different workspace (defense-in-depth after wsAccess).
	GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Artifact, error)
	// GetDownloadURL generates a presigned URL. inline=true omits Content-Disposition
	// so the browser renders the file (image/PDF/etc.) instead of downloading it.
	GetDownloadURL(ctx context.Context, id uuid.UUID, inline bool) (string, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error)
}

// CreateDocumentInput holds parameters for creating a project document.
// ProjectID comes from the route, never from the request body.
type CreateDocumentInput struct {
	ProjectID uuid.UUID  `json:"project_id"`
	ParentID  *uuid.UUID `json:"parent_id"`
	// Slug is optional; an empty one is derived from the title.
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Position int    `json:"position"`

	CreatedBy     uuid.UUID        `json:"created_by"`
	CreatedByType domain.ActorType `json:"created_by_type"`
}

// UpdateDocumentInput holds the fields for partially updating a document. A nil
// field is left alone.
type UpdateDocumentInput struct {
	Title    *string    `json:"title"`
	ParentID *uuid.UUID `json:"parent_id"`
	// ClearParent moves the document to the top level. It is a separate flag
	// because an absent parent_id and a null one are the same nil pointer once
	// bound — the same reason updateTaskRequest carries ClearReviewer.
	ClearParent bool    `json:"clear_parent"`
	Position    *int    `json:"position"`
	Body        *string `json:"body"`
}

// DocumentService provides business logic for project documents: metadata in
// Postgres, markdown body in object storage.
type DocumentService interface {
	Create(ctx context.Context, input CreateDocumentInput) (*domain.Document, error)
	// GetByIDInWorkspace returns the document with its body, and only when it
	// belongs to workspaceID (defense-in-depth after wsAccess).
	GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Document, error)
	Update(ctx context.Context, id, workspaceID uuid.UUID, input UpdateDocumentInput) (*domain.Document, error)
	// Delete soft-deletes the document and its descendants; the stored body is kept.
	Delete(ctx context.Context, id, workspaceID uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Document], error)
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

// AgentServiceConfigurable allows optional dependencies to be injected after construction.
type AgentServiceConfigurable interface {
	SetAgentActivityLogRepo(repo repository.AgentActivityLogRepository)
}

// HeartbeatInput holds optional fields for the heartbeat update.
type HeartbeatInput struct {
	Status        string          `json:"status"`
	Message       string          `json:"message"`
	Metadata      json.RawMessage `json:"metadata"`
	CurrentTaskID *uuid.UUID      `json:"current_task_id,omitempty"`
}

// AgentService provides business logic for agent management.
type AgentService interface {
	Register(ctx context.Context, input RegisterAgentInput) (*RegisterAgentOutput, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
	Update(ctx context.Context, agent *domain.Agent) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, workspaceID uuid.UUID, filter repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error)
	Heartbeat(ctx context.Context, agentID uuid.UUID, input *HeartbeatInput) error
	Authenticate(ctx context.Context, workspaceSlug, apiKey string) (*domain.Agent, error)
	RotateAPIKey(ctx context.Context, agentID uuid.UUID) (string, error)
	// ListSubAgents returns child agents of a parent.
	// When recursive is true, all descendants (up to 10 levels) are returned via a CTE.
	// When recursive is false, only direct children are returned.
	ListSubAgents(ctx context.Context, parentID uuid.UUID, recursive bool) ([]domain.Agent, error)
	// Agent activity log
	CreateActivityLog(ctx context.Context, entry *domain.AgentActivityLog) error
	ListActivityLog(ctx context.Context, agentID uuid.UUID, filter repository.AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error)
	// TouchLastSeen bumps the agent's last_heartbeat without changing status.
	// Called when an SSE connection is opened.
	TouchLastSeen(ctx context.Context, agentID uuid.UUID) error
	// GetBySlug returns the agent with the given slug in a workspace, or (nil, nil) if not found.
	GetBySlug(ctx context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error)
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
	// MemoryHint is an optional hint instructing the event pipeline to persist
	// a memory entry from this event. When nil, auto-extraction rules still apply.
	MemoryHint *domain.MemoryHint `json:"memory_hint,omitempty"`
}

// EventBusService provides business logic for the event bus.
type EventBusService interface {
	Publish(ctx context.Context, input PublishEventInput) (*domain.EventBusMessage, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error)
	List(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error)
	ListEnriched(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EnrichedEventBusMessage], error)
	GetContext(ctx context.Context, projectID uuid.UUID, opts GetContextOptions) ([]domain.EventBusMessage, error)
	CleanupExpired(ctx context.Context) (int64, error)
}

// EventBusServiceConfigurable extends EventBusService with the ability
// to wire an optional NATS JetStream event bus publisher at runtime.
type EventBusServiceConfigurable interface {
	EventBusService
	SetEventBus(publisher eventbus.Publisher, workspaceRepo repository.WorkspaceRepository, projectRepo repository.ProjectRepository)
	SetMemoryService(ms MemoryService)
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
	Delete(ctx context.Context, id, callerID uuid.UUID) error
	ListByProject(ctx context.Context, projectID, userID uuid.UUID) ([]domain.SavedView, error)
}

// CreateProjectUpdateInput holds the fields for creating a project update.
type CreateProjectUpdateInput struct {
	ProjectID  uuid.UUID           `json:"project_id"`
	Title      string              `json:"title"`
	Status     domain.UpdateStatus `json:"status"`
	Summary    string              `json:"summary"`
	Highlights []domain.TextItem   `json:"highlights"`
	Blockers   []domain.TextItem   `json:"blockers"`
	NextSteps  []domain.TextItem   `json:"next_steps"`
	CreatedBy  uuid.UUID           `json:"created_by"`
}

// ProjectUpdateService provides business logic for project updates.
type ProjectUpdateService interface {
	Create(ctx context.Context, input CreateProjectUpdateInput) (*domain.ProjectUpdate, error)
	List(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ProjectUpdate], error)
	GetLatest(ctx context.Context, projectID uuid.UUID) (*domain.ProjectUpdate, error)
}

// CreateInitiativeInput holds the fields for creating an initiative.
type CreateInitiativeInput struct {
	WorkspaceID uuid.UUID               `json:"workspace_id"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Status      domain.InitiativeStatus `json:"status"`
	TargetDate  *time.Time              `json:"target_date"`
	CreatedBy   uuid.UUID               `json:"created_by"`
}

// UpdateInitiativeInput holds the fields for partially updating an initiative.
type UpdateInitiativeInput struct {
	Name        *string                  `json:"name"`
	Description *string                  `json:"description"`
	Status      *domain.InitiativeStatus `json:"status"`
	TargetDate  *time.Time               `json:"target_date"`
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
	// Create creates a new VCS link, or — when input.Status is explicitly set
	// — upserts onto an existing (task_id, provider, link_type, external_id)
	// match. The returned bool is true on insert, false on update; the
	// returned *domain.VCSLink always reflects the actual persisted row
	// (id/created_at included), never a caller-generated value that the
	// database discarded on conflict (#b73171fa).
	Create(ctx context.Context, input domain.CreateVCSLinkInput) (link *domain.VCSLink, created bool, err error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error)
	// HandleGitHubPullRequestEvent resolves a GitHub PR webhook payload to a
	// task, upserts the corresponding vcs_link, and on action=closed applies
	// the auto transition policy: in_progress → review, review → done. When
	// multiple PRs are linked to the same task, the transition only fires
	// once all of them are merged/closed. Returns a non-error result that the
	// caller may log; an error is only returned for DB/RPC failures, never
	// for "no task ref" or "no transition".
	HandleGitHubPullRequestEvent(ctx context.Context, ev GitHubWebhookEvent) (PRHandleResult, error)
	// ResolveTaskRef finds the first task actually named by any recognised
	// reference spelling in the given texts — MESH-<uuid>, a /t/<id> link, a
	// Refs/Closes keyword, a #<short id>, or a branch segment — and verifies it
	// exists. Returns uuid.Nil when nothing resolves. Exposed so the push
	// (commit) path gets the same recognition as the pull_request path.
	ResolveTaskRef(ctx context.Context, sources ...TaskRefSource) (uuid.UUID, TaskRef)
}

// GitHubWebhookEvent is a minimal projection of the fields the orchestrator
// reads from a GitHub pull_request webhook payload. Defined in the service
// layer so the handler wire format can evolve independently.
type GitHubWebhookEvent struct {
	Action     string // "opened" | "closed" | "synchronize" | "reopened" | ...
	PRNumber   int
	PRTitle    string
	PRBody     string
	PRHTMLURL  string
	PRState    string
	PRMerged   bool
	MergeSHA   string // pull_request.merge_commit_sha (empty for non-merge close)
	PRBranch   string // pull_request.head.ref — branches cut from a task often carry its id
	Repository string // owner/name
}

// PRHandleResult describes what happened when processing a pull_request event.
// Transitioned == false with Reason != "" means the orchestrator deliberately
// did not move the task. Reason values are stable identifiers suitable for
// logging: "no_task_ref", "not_closed", "closed_without_merge",
// "awaiting_other_prs", "source_status_not_eligible", "transitioned".
type PRHandleResult struct {
	TaskID       uuid.UUID
	OldStatus    string // status slug; empty when no task resolved
	NewStatus    string // status slug; empty when no transition
	Transitioned bool
	Reason       string
}

// IntegrationService provides business logic for workspace integration configs.
type IntegrationService interface {
	Configure(ctx context.Context, input domain.CreateIntegrationInput) (*domain.IntegrationConfig, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.IntegrationConfig, error)
}

// CreateInviteInput holds parameters for creating a workspace invite.
type CreateInviteInput struct {
	WorkspaceID uuid.UUID
	Email       string
	Role        string
	InvitedBy   uuid.UUID
}

// AcceptInviteInput holds parameters for accepting an invite and registering.
type AcceptInviteInput struct {
	Token    string
	Name     string
	Password string
}

// Invite delivery statuses reported by CreateInvite and ResendInvite.
const (
	// InviteDeliverySent means the invitation email was handed to the SMTP server.
	InviteDeliverySent = "sent"
	// InviteDeliveryNotConfigured means no SMTP host is configured, so no email
	// was attempted. This is the normal state of a fresh self-hosted instance,
	// not an error: the invite exists and its link is usable.
	InviteDeliveryNotConfigured = "not_configured"
	// InviteDeliveryFailed means email is configured but the send failed.
	InviteDeliveryFailed = "failed"
)

// InviteDelivery reports what actually happened to the invitation email, so that
// callers can stop conflating "created" with "delivered".
//
// Creating the invite and mailing it are separate outcomes: the invite row is
// written first and is valid regardless of whether any mail could be sent. URL
// is therefore always populated — it is the fallback delivery channel when
// Status is not InviteDeliverySent, and the inviter is expected to pass it on
// themselves.
type InviteDelivery struct {
	// Status is one of InviteDeliverySent, InviteDeliveryNotConfigured, InviteDeliveryFailed.
	Status string
	// URL is the invite accept link. Always set.
	URL string
	// Error carries the send failure message when Status is InviteDeliveryFailed.
	Error string
}

// Sent reports whether an invitation email actually went out.
func (d InviteDelivery) Sent() bool { return d.Status == InviteDeliverySent }

// CreateInviteResult is the outcome of creating an invite: the stored invite
// plus what became of its email.
type CreateInviteResult struct {
	Invite   *domain.WorkspaceInvite
	Delivery InviteDelivery
}

// WorkspaceInviteService provides business logic for workspace invite management.
type WorkspaceInviteService interface {
	// CreateInvite creates a pending invite and attempts to email it. A failure
	// to send is reported in the returned delivery status, not as an error: the
	// invite is created either way and the caller is handed its link.
	CreateInvite(ctx context.Context, input CreateInviteInput) (*CreateInviteResult, error)
	// ListInvites returns all pending (non-expired, unaccepted) invites for a workspace.
	ListInvites(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceInvite, error)
	// ResendInvite re-sends the invitation email for an existing pending invite,
	// reporting the outcome in the returned delivery status.
	// workspaceID is the workspace named in the route: the invite must be one of
	// its own, or a caller could re-send a stranger's invite from their own
	// workspace and mail that stranger's invitee on demand.
	ResendInvite(ctx context.Context, workspaceID, inviteID uuid.UUID) (InviteDelivery, error)
	// RevokeInvite deletes a pending invite. workspaceID is the workspace named in
	// the route; see ResendInvite for why it is not optional.
	RevokeInvite(ctx context.Context, workspaceID, inviteID uuid.UUID) error
	// GetByToken returns invite info for a given token (used by the accept-invite page).
	// Returns nil when the token does not exist or the invite is expired/accepted.
	GetByToken(ctx context.Context, token string) (*domain.WorkspaceInvite, error)
	// AcceptInvite accepts the invite: creates the user if needed, adds them to the workspace,
	// marks the invite accepted, and returns both access and refresh tokens for the new/existing user.
	AcceptInvite(ctx context.Context, input AcceptInviteInput) (accessToken, refreshToken string, err error)
}

// WorkspaceMemberService provides business logic for workspace member management.
type WorkspaceMemberService interface {
	ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error)
	// GetMember returns one membership with its user details, or (nil, nil) when
	// the user is not a member of this workspace.
	GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMemberWithUser, error)
	AddMember(ctx context.Context, workspaceID uuid.UUID, email, role string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error)
	// AddMemberWithCreate adds an existing user or creates a new one (when password is provided).
	// name sets the display name of a newly created account; it falls back to the address.
	AddMemberWithCreate(ctx context.Context, workspaceID uuid.UUID, email, name, role, password string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error)
	UpdateMemberRole(ctx context.Context, workspaceID, targetUserID uuid.UUID, newRole string) error
	// SetMemberDisplayName fills in a member's display name. Refuses when that
	// member has already set their own — the name is account-wide, so overwriting
	// a chosen one from inside one workspace would change how the person appears
	// in every other workspace they belong to.
	SetMemberDisplayName(ctx context.Context, workspaceID, targetUserID uuid.UUID, name string) error
	RemoveMember(ctx context.Context, workspaceID, targetUserID uuid.UUID) error
	GetMyRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error)
	// SearchUsers finds accounts callerID may add to workspaceID, annotated with
	// membership status. callerID bounds what is visible — see the repository.
	SearchUsers(ctx context.Context, workspaceID, callerID uuid.UUID, query string) ([]domain.UserWithMemberStatus, error)
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
	// GetTeamDirectoryTree returns the team directory in hierarchical (tree) format with project affiliations.
	GetTeamDirectoryTree(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectoryTree, error)
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
	CreateTaskFromTemplate(ctx context.Context, templateID, createdBy uuid.UUID, createdByType domain.ActorType, overrides map[string]any) (*domain.Task, error)
}

// AnalyticsMetrics holds aggregated workspace/project metrics.
type AnalyticsMetrics struct {
	TaskMetrics  TaskMetrics  `json:"task_metrics"`
	AgentMetrics AgentMetrics `json:"agent_metrics"`
	EventMetrics EventMetrics `json:"event_metrics"`
	CostMetrics  CostMetrics  `json:"cost_metrics"`
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

// CostMetrics holds aggregated agent-session cost/token data for a workspace/period,
// sourced from agent_sessions (populated by the fiddler session_report MCP tool).
type CostMetrics struct {
	TotalCost      float64          `json:"total_cost"`
	TotalTokensIn  int64            `json:"total_tokens_in"`
	TotalTokensOut int64            `json:"total_tokens_out"`
	SessionCount   int              `json:"session_count"`
	ByAgent        []AgentCostRow   `json:"by_agent"`
	ByProject      []ProjectCostRow `json:"by_project"`
	ByDay          []DayCostMetric  `json:"by_day"`
	TopTasks       []TaskCostRow    `json:"top_tasks"`
}

// AgentCostRow holds per-agent spend/token stats for the period.
type AgentCostRow struct {
	AgentID   uuid.UUID `json:"agent_id"`
	AgentName string    `json:"agent_name"`
	Cost      float64   `json:"cost"`
	TokensIn  int64     `json:"tokens_in"`
	TokensOut int64     `json:"tokens_out"`
}

// ProjectCostRow holds per-project spend/token stats for the period.
type ProjectCostRow struct {
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Cost        float64   `json:"cost"`
	TokensIn    int64     `json:"tokens_in"`
	TokensOut   int64     `json:"tokens_out"`
}

// DayCostMetric holds the daily spend/token totals.
type DayCostMetric struct {
	Date      string  `json:"date"`
	Cost      float64 `json:"cost"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
}

// TaskCostRow holds spend/token stats for a single task (used for the top-N leaderboard).
type TaskCostRow struct {
	TaskID       uuid.UUID `json:"task_id"`
	TaskTitle    string    `json:"task_title"`
	Cost         float64   `json:"cost"`
	TokensIn     int64     `json:"tokens_in"`
	TokensOut    int64     `json:"tokens_out"`
	SessionCount int       `json:"session_count"`
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

// SetProjectKnowledgeInput holds parameters for writing a project knowledge entry.
type SetProjectKnowledgeInput struct {
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	AgentID     *uuid.UUID
	Key         string
	Value       string   // stored as Content
	Category    string   // optional, stored as "category:{value}" tag
	Tags        []string // additional tags
	SourceType  string   // "agent" | "human"
	SourceURL   *string
}

// WSPublisher publishes JSON-encoded events to a Redis pub/sub channel
// for delivery to connected WebSocket clients.
type WSPublisher interface {
	Publish(ctx context.Context, channel string, event any) error
}

// MentionService provides business logic for comment @-mentions.
type MentionService interface {
	List(ctx context.Context, mentionedID uuid.UUID, mentionedKind string, filter repository.MentionFilter) ([]domain.CommentMentionView, error)
	MarkSeen(ctx context.Context, commentID, mentionedID uuid.UUID) error
	CountUnseen(ctx context.Context, mentionedID uuid.UUID, mentionedKind string) (int64, error)
}

// RelayPublisher is the optional interface for publishing artifacts to Team Relay.
// Publish returns the artifact's public (browser-renderable) URL and the agent key
// that was used to authenticate the upload (empty string for public shares or when
// the relay omits one). Errors are best-effort context.
type RelayPublisher interface {
	Publish(ctx context.Context, taskID uuid.UUID, artifactName string, content []byte, contentType string) (publicURL, agentKey string, err error)
}

// ArtifactServiceConfigurable allows optional relay publisher to be injected after construction.
type ArtifactServiceConfigurable interface {
	SetRelayPublisher(p RelayPublisher)
}

// UpsertProjectIntegrationInput holds data for creating/updating a project integration.
type UpsertProjectIntegrationInput struct {
	Enabled            bool
	ShareID            string // relay share UUID (preferred; works for private sync folders)
	ShareSlug          string
	AgentKey           string // empty = keep existing
	Subfolder          string
	IncludeProjectSlug bool
	CreatedBy          *uuid.UUID
}

// ProjectIntegrationService manages project-level integrations.
type ProjectIntegrationService interface {
	GetTeamRelay(ctx context.Context, projectID uuid.UUID) (*domain.ProjectIntegration, error)
	UpsertTeamRelay(ctx context.Context, projectID uuid.UUID, input UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error)
	DeleteTeamRelay(ctx context.Context, projectID uuid.UUID) error
	List(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectIntegration, error)
	// SearchTR looks up the TR integration by share_slug, calls the relay file-list API,
	// filters in-memory by q (case-insensitive contains on name+path), and returns up to limit results.
	// Returns apierror.NotFound when the share_slug is not configured in any project.
	SearchTR(ctx context.Context, shareSlug, q string, limit int) ([]domain.RelayFileItem, error)
}

// MentionablesService searches for @-mentionable workspace members (agents and users).
type MentionablesService interface {
	Search(ctx context.Context, workspaceID uuid.UUID, query string, limit int) ([]domain.Mentionable, error)
}

// RememberResult is returned by MemoryService.Remember.
type RememberResult struct {
	// Outcome is "created" or "updated".
	Outcome string
	// NearDupKey is set when a near-duplicate memory (Hamming distance ≤ 10 bits) was
	// detected at write time. It holds the key of the closest existing near-duplicate.
	// Empty when no near-duplicate was found.
	NearDupKey string
	// EmbeddingPending is true when Remember fired an async embedding goroutine
	// (embedAndStore) that has not necessarily completed by the time this result
	// is returned. While pending, the row is invisible to the dense/vector recall
	// arm (it fails vectorCandidateIDs's predicate) even though Recall's
	// search_mode still reports "hybrid" — nothing errored, the write just hasn't
	// landed yet. A caller that recalls immediately after remembering should treat
	// EmbeddingPending=true as "expect BM25-only results for this row for a short
	// window", not as a signal to retry or wait. Always false when no embedder is
	// configured (embedding.IsNoop) — there is nothing pending in that case. See #a2e00afd.
	EmbeddingPending bool
}

// MemoryService provides business logic for agent persistent memory.
type MemoryService interface {
	Remember(ctx context.Context, mem *domain.Memory) (RememberResult, error)
	// Recall runs a hybrid (BM25 + vector) search. It FAILS OPEN when the embedder
	// is down: results are still returned, served by the BM25 arm alone. The
	// returned domain.SearchMode says which mode the call was actually served in
	// ("hybrid" | "bm25-only") so that degradation is visible to callers instead
	// of hiding in a log line.
	Recall(ctx context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error)
	// RecallWithStats is Recall plus the per-arm row counts (domain.RecallStats).
	// Additive on purpose: SearchMode says the dense arm RAN, not that it
	// returned anything, so "hybrid" is compatible with a vector arm that matched
	// zero rows corpus-wide. Callers that need to tell those apart — the REST
	// envelope, and through it the CI recall gate — use this one.
	RecallWithStats(ctx context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.RecallStats, error)
	// ListMemories executes a richly-filtered, paginated list query backed by the repository.
	ListMemories(ctx context.Context, filter domain.MemoryListFilter) (*RecallResult, error)
	// GetProjectKnowledge returns memories for a project (project-scoped) or workspace tier.
	// filter.Limit/Offset/MinImportance/TagsAny apply to the workspace-tier (projectID=nil).
	// Returns memories and the total count before pagination.
	GetProjectKnowledge(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error)
	// SetProjectKnowledge upserts a project-scoped knowledge entry. Returns "created" or "updated".
	SetProjectKnowledge(ctx context.Context, input SetProjectKnowledgeInput) (*domain.Memory, string, error)
	Forget(ctx context.Context, id uuid.UUID, actorAgentID *uuid.UUID, isAdmin bool) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error)
	// ExportMemories returns YAML-encoded memories for the given workspace (optionally filtered by project).
	ExportMemories(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]byte, error)
	// ImportMemories parses a YAML export and upserts each memory. Returns the count imported.
	ImportMemories(ctx context.Context, workspaceID uuid.UUID, data []byte) (int, error)
	// BatchEmbed finds all memories without an embedding and embeds them using the configured embedder.
	// Returns the count of memories that were successfully embedded.
	BatchEmbed(ctx context.Context, workspaceID uuid.UUID) (int, error)
	// BackfillChunks finds up to limit memories in workspaceID with no memory_chunks rows yet
	// and embeds them through the chunked path (ADR-0002). Unlike BatchEmbed, selection is NOT
	// based on embedding_model — a memory already carrying the current model's watermark from
	// before chunking existed would be invisible to that filter, which is exactly why this is a
	// separate method rather than a BatchEmbed variant. Idempotent and resumable by construction:
	// call repeatedly (limit<=0 defaults to 100) until it returns 0 — a memory that got chunks
	// this call is excluded from the next call's selection automatically, no cursor needed.
	// Returns 0, nil (not an error) when chunked embedding is not configured, matching BatchEmbed's
	// no-op-on-noop-embedder convention.
	BackfillChunks(ctx context.Context, workspaceID uuid.UUID, limit int) (int, error)
	// RechunkStale re-embeds up to limit memories whose chunk offsets no longer index their
	// content — the corpus still chunked as the composite `key + " " + content + " " + tags`
	// before #494 moved chunking onto content alone with key+tags prefixed per chunk.
	//
	// Returns (processed, remaining): how many this call re-embedded, and how many still match
	// the predicate afterwards. `remaining` is a direct count of the damaged population, not a
	// derivative of `processed`, so the two disagreeing is the signal that the selector and the
	// damage have come apart. Drive it from `remaining == 0`; a repair job's own processed-count
	// cannot distinguish "healed everything" from "never selected the sick rows".
	//
	// Idempotent and resumable with no cursor: a repaired row's offsets index content and it
	// leaves the population. Preserves updated_at on every row it touches (see
	// UpdateEmbeddingKeepUpdatedAt) — nobody edited these memories, and the column drives
	// staleness and relevance decay. Returns (0, remaining, nil) when chunked embedding is not
	// configured or the embedder is a noop, matching BackfillChunks' convention.
	RechunkStale(ctx context.Context, workspaceID uuid.UUID, limit int) (processed, remaining int, err error)
	// FindRelated returns memories related to the given memory ID via full-text search on its key+tags.
	// The source memory itself is excluded from results.
	FindRelated(ctx context.Context, memoryID uuid.UUID, limit int) ([]domain.ScoredMemory, error)
	ExtractFromEvent(ctx context.Context, event *domain.EventBusMessage, hint *domain.MemoryHint) error
	// Supersede creates a 'supersedes' edge from newID → oldID and marks oldID as archived.
	// Both memories must exist in the same workspace. Returns NotFound if either is missing.
	Supersede(ctx context.Context, oldID, newID uuid.UUID) error
	// RecallGraph performs a multi-hop knowledge-graph traversal starting from hybrid recall seeds.
	// Seeds are the top-k memories from hybrid recall; BFS then expands along memory_edges with
	// weight >= opts.WeightThreshold for up to opts.Hops levels. Results are ranked by composite
	// score (seed_score × product of edge weights along the chain). Graph-expanded memories with
	// importance_score < 0.4 are dropped. Results are cached in-process for 5 minutes keyed by
	// (TaskID, queryHash).
	RecallGraph(ctx context.Context, opts domain.RecallGraphOpts) ([]domain.RecallGraphResult, error)
}
