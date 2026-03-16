package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// WorkspaceRepository manages persistence for workspaces.
type WorkspaceRepository interface {
	Create(ctx context.Context, workspace *domain.Workspace) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error)
	GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error)
	Update(ctx context.Context, workspace *domain.Workspace) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]domain.Workspace, error)
}

// ProjectFilter defines filtering options for listing projects.
type ProjectFilter struct {
	IsArchived    *bool
	Search        string
	MemberUserID  *uuid.UUID // Filter to projects where this user is a member.
	MemberAgentID *uuid.UUID // Filter to projects where this agent is a member.
}

// ProjectRepository manages persistence for projects.
type ProjectRepository interface {
	Create(ctx context.Context, project *domain.Project) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	GetBySlug(ctx context.Context, workspaceID uuid.UUID, slug string) (*domain.Project, error)
	Update(ctx context.Context, project *domain.Project) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, workspaceID uuid.UUID, filter ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error)
}

// CustomFieldFilter defines filter conditions for a single custom field.
type CustomFieldFilter struct {
	Eq    interface{} // exact equality: custom_fields->>'slug' = value
	Gte   *float64    // numeric >=
	Lte   *float64    // numeric <=
	In    []string    // value in set
	IsSet *bool       // whether the field key exists in the JSONB
}

// TaskFilter defines filtering options for listing tasks.
type TaskFilter struct {
	StatusIDs    []uuid.UUID
	AssigneeID   *uuid.UUID
	AssigneeType *domain.AssigneeType
	Priority     *domain.Priority
	ParentTaskID *uuid.UUID
	Labels       []string
	Search       string
	HasDueDate   *bool
	CustomFields map[string]CustomFieldFilter // key = field slug
}

// TaskRepository manages persistence for tasks.
type TaskRepository interface {
	Create(ctx context.Context, task *domain.Task) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	Update(ctx context.Context, task *domain.Task) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, projectID uuid.UUID, filter TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	ListByAssignee(ctx context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error)
	ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error)
	CountByStatus(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID]int, error)
	CountByStatusCategory(ctx context.Context, projectID uuid.UUID) (map[domain.StatusCategory]int, error)
	ListByStatusCategory(ctx context.Context, workspaceID uuid.UUID, category domain.StatusCategory, pg pagination.Params) (*pagination.Page[domain.Task], error)
	// AtomicCheckout acquires an exclusive application-level lock on the task for the
	// given agent. Returns ErrCheckoutConflict if locked by another non-expired agent.
	AtomicCheckout(ctx context.Context, taskID, agentID, token uuid.UUID, expiresAt time.Time) error
	// ReleaseCheckout clears the checkout fields. Returns ErrInvalidCheckoutToken when
	// the provided token does not match.
	ReleaseCheckout(ctx context.Context, taskID, token uuid.UUID) error
	// ExtendCheckout extends the checkout deadline. Returns ErrInvalidCheckoutToken when
	// the provided token does not match or the checkout has already expired.
	ExtendCheckout(ctx context.Context, taskID, token uuid.UUID, newExpires time.Time) error
	// MoveToProject atomically reassigns a task to a different project, assigning it
	// the given target status and a new task_number within that project.
	// Returns apierror.NotFound("Task") if the task does not exist or is soft-deleted.
	MoveToProject(ctx context.Context, taskID, targetProjectID, targetStatusID uuid.UUID) error
}

// TaskStatusRepository manages persistence for task statuses.
type TaskStatusRepository interface {
	Create(ctx context.Context, status *domain.TaskStatus) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskStatus, error)
	Update(ctx context.Context, status *domain.TaskStatus) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error)
	GetDefaultForProject(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error)
	Reorder(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error
}

// TaskDependencyRepository manages persistence for task dependencies.
type TaskDependencyRepository interface {
	Create(ctx context.Context, dep *domain.TaskDependency) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error)
	ListDependents(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error)
	Exists(ctx context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error)
}

// CustomFieldDefinitionRepository manages persistence for custom field definitions.
type CustomFieldDefinitionRepository interface {
	Create(ctx context.Context, field *domain.CustomFieldDefinition) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.CustomFieldDefinition, error)
	Update(ctx context.Context, field *domain.CustomFieldDefinition) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error)
	ListVisibleToAgents(ctx context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error)
	Reorder(ctx context.Context, projectID uuid.UUID, fieldIDs []uuid.UUID) error
}

// CommentFilter defines filtering options for listing comments.
type CommentFilter struct {
	IncludeInternal bool
}

// CommentRepository manages persistence for comments.
type CommentRepository interface {
	Create(ctx context.Context, comment *domain.Comment) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Comment, error)
	Update(ctx context.Context, comment *domain.Comment) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID, filter CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error)
	ListReplies(ctx context.Context, parentCommentID uuid.UUID) ([]domain.Comment, error)
}

// ArtifactRepository manages persistence for artifacts.
type ArtifactRepository interface {
	Create(ctx context.Context, artifact *domain.Artifact) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error)
}

// AgentFilter defines filtering options for listing agents.
type AgentFilter struct {
	Status        *domain.AgentStatus
	AgentType     *domain.AgentType
	Search        string
	ParentAgentID *uuid.UUID
}

// AgentWithProjects pairs an agent with its project affiliation names.
type AgentWithProjects struct {
	domain.Agent
	Projects []string
}

// UpdateHeartbeatParams holds optional fields for the heartbeat update.
type UpdateHeartbeatParams struct {
	Status   string
	Message  string
	Metadata json.RawMessage
}

// AgentRepository manages persistence for agents.
type AgentRepository interface {
	Create(ctx context.Context, agent *domain.Agent) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Agent, error)
	GetByAPIKeyPrefix(ctx context.Context, workspaceID uuid.UUID, prefix string) (*domain.Agent, error)
	Update(ctx context.Context, agent *domain.Agent) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, workspaceID uuid.UUID, filter AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error)
	UpdateHeartbeat(ctx context.Context, id uuid.UUID, params *UpdateHeartbeatParams) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.AgentStatus) error
	// GetSubAgentTree returns all agents that are descendants of parentID using a recursive CTE
	// limited to 10 levels of depth, ordered by depth then created_at.
	GetSubAgentTree(ctx context.Context, parentID uuid.UUID) ([]domain.Agent, error)
	// ListWithProjects returns all agents in a workspace together with the project names
	// they are members of (via project_members JOIN projects).
	ListWithProjects(ctx context.Context, workspaceID uuid.UUID) ([]AgentWithProjects, error)
}

// AgentActivityLogFilter defines filtering options for listing agent activity log entries.
type AgentActivityLogFilter struct {
	EventType string
	Since     *time.Time
	Until     *time.Time
}

// AgentActivityLogRepository manages persistence for agent activity log.
type AgentActivityLogRepository interface {
	Create(ctx context.Context, entry *domain.AgentActivityLog) error
	List(ctx context.Context, agentID uuid.UUID, filter AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, filter AgentActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.AgentActivityLog], error)
}

// EventBusMessageFilter defines filtering options for listing event bus messages.
type EventBusMessageFilter struct {
	EventType *domain.EventType
	AgentID   *uuid.UUID
	TaskID    *uuid.UUID
	Tags      []string
}

// EventBusMessageRepository manages persistence for event bus messages.
type EventBusMessageRepository interface {
	Create(ctx context.Context, msg *domain.EventBusMessage) error
	Upsert(ctx context.Context, msg *domain.EventBusMessage) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error)
	List(ctx context.Context, projectID uuid.UUID, filter EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error)
	DeleteExpired(ctx context.Context) (int64, error)
}

// ActivityLogFilter defines filtering options for listing activity log entries.
type ActivityLogFilter struct {
	EntityType *string
	EntityID   *uuid.UUID
	ActorID    *uuid.UUID
	ActorType  *domain.ActorType
	Action     *string
	From       *time.Time
	To         *time.Time
}

// ActivityLogRepository manages persistence for activity log entries.
type ActivityLogRepository interface {
	Create(ctx context.Context, entry *domain.ActivityLog) error
	List(ctx context.Context, workspaceID uuid.UUID, filter ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error)
	ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error)
	// Export returns all matching entries (up to limit) without pagination, used for CSV/JSON export.
	Export(ctx context.Context, workspaceID uuid.UUID, filter ActivityLogFilter, limit int) ([]domain.ActivityLog, error)
}

// UserRepository manages persistence for users.
type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	Update(ctx context.Context, user *domain.User) error
	// SearchUsers returns users whose email or display_name match the query (ILIKE), up to limit.
	SearchUsers(ctx context.Context, query string, limit int) ([]domain.User, error)
}

// RefreshToken represents a stored refresh token record.
type RefreshToken struct {
	ID        uuid.UUID  `db:"id"`
	UserID    uuid.UUID  `db:"user_id"`
	TokenHash string     `db:"token_hash"`
	ExpiresAt time.Time  `db:"expires_at"`
	CreatedAt time.Time  `db:"created_at"`
	RevokedAt *time.Time `db:"revoked_at"`
}

// RefreshTokenRepository manages persistence for refresh tokens.
type RefreshTokenRepository interface {
	Create(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error
	GetByHash(ctx context.Context, tokenHash string) (*RefreshToken, error)
	RevokeByUserID(ctx context.Context, userID uuid.UUID) error
	RevokeByHash(ctx context.Context, tokenHash string) error
	DeleteExpired(ctx context.Context) error
}

// HumanWithProjects pairs a workspace member with their project affiliation names.
type HumanWithProjects struct {
	domain.WorkspaceMemberWithUser
	Projects []string
}

// WorkspaceMemberRepository manages persistence for workspace members.
type WorkspaceMemberRepository interface {
	Create(ctx context.Context, member *domain.WorkspaceMember) error
	GetByWorkspaceAndUser(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error)
	// GetRole returns the role string for a given workspace + user combination.
	// Returns an error if the membership does not exist.
	GetRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error)
	// List returns all members of a workspace with user details joined.
	List(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error)
	// ListWithProjects returns all workspace members with their project affiliations.
	ListWithProjects(ctx context.Context, workspaceID uuid.UUID) ([]HumanWithProjects, error)
	// UpdateRole changes the role for a given workspace + user.
	UpdateRole(ctx context.Context, workspaceID, userID uuid.UUID, role string) error
	// Delete removes the workspace membership for the given user.
	Delete(ctx context.Context, workspaceID, userID uuid.UUID) error
	// CountOwners returns the number of members with the "owner" role in the workspace.
	CountOwners(ctx context.Context, workspaceID uuid.UUID) (int, error)
}

// ProjectMemberRepository manages persistence for project-level members.
type ProjectMemberRepository interface {
	Create(ctx context.Context, member *domain.ProjectMember) error
	GetByProjectAndUser(ctx context.Context, projectID, userID uuid.UUID) (*domain.ProjectMember, error)
	GetByProjectAndAgent(ctx context.Context, projectID, agentID uuid.UUID) (*domain.ProjectMember, error)
	// List returns all members of a project with user and agent details joined.
	List(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectMemberWithUser, error)
	// UpdateRole changes the role for a given project + user.
	UpdateRole(ctx context.Context, projectID, userID uuid.UUID, role string) error
	// Delete removes the project membership for the given user.
	Delete(ctx context.Context, projectID, userID uuid.UUID) error
	// DeleteAgent removes the project membership for the given agent.
	DeleteAgent(ctx context.Context, projectID, agentID uuid.UUID) error
	// DeleteByWorkspaceAndUser removes all project memberships for a user across a workspace.
	DeleteByWorkspaceAndUser(ctx context.Context, workspaceID, userID uuid.UUID) error
	// ExistsMember returns true if the given user or agent is a member of the project.
	ExistsMember(ctx context.Context, projectID uuid.UUID, userID, agentID *uuid.UUID) (bool, error)
}

// SavedViewRepository manages persistence for saved views.
type SavedViewRepository interface {
	Create(ctx context.Context, view *domain.SavedView) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.SavedView, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateSavedViewInput) (*domain.SavedView, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByProject(ctx context.Context, projectID, userID uuid.UUID) ([]domain.SavedView, error)
}

// ProjectUpdateRepository manages persistence for project status updates.
type ProjectUpdateRepository interface {
	Create(ctx context.Context, update *domain.ProjectUpdate) error
	List(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ProjectUpdate], error)
	GetLatest(ctx context.Context, projectID uuid.UUID) (*domain.ProjectUpdate, error)
}

// InitiativeRepository manages persistence for initiatives.
type InitiativeRepository interface {
	Create(ctx context.Context, initiative *domain.Initiative) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Initiative, error)
	Update(ctx context.Context, initiative *domain.Initiative) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, workspaceID uuid.UUID) ([]domain.Initiative, error)
	LinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error
	UnlinkProject(ctx context.Context, initiativeID, projectID uuid.UUID) error
	ListLinkedProjects(ctx context.Context, initiativeID uuid.UUID) ([]domain.Project, error)
	// GetByProjectID returns all initiatives that have the given project linked.
	GetByProjectID(ctx context.Context, projectID uuid.UUID) ([]domain.Initiative, error)
}

// WebhookRepository manages persistence for webhook configurations and deliveries.
type WebhookRepository interface {
	Create(ctx context.Context, webhook *domain.WebhookConfig) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.WebhookConfig, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateWebhookInput) (*domain.WebhookConfig, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WebhookConfig, error)
	ListActiveByEvent(ctx context.Context, workspaceID uuid.UUID, eventType string) ([]domain.WebhookConfig, error)
	IncrementFailure(ctx context.Context, id uuid.UUID) error
	ResetFailure(ctx context.Context, id uuid.UUID) error
	Deactivate(ctx context.Context, id uuid.UUID) error
	CreateDelivery(ctx context.Context, delivery *domain.WebhookDelivery) error
	ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit int) ([]domain.WebhookDelivery, error)
}

// VCSLinkRepository manages persistence for VCS links.
type VCSLinkRepository interface {
	Create(ctx context.Context, link *domain.VCSLink) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error)
}

// RuleRepository manages persistence for governance rules.
type RuleRepository interface {
	Create(ctx context.Context, rule *domain.Rule) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Rule, error)
	Update(ctx context.Context, rule *domain.Rule) error
	Delete(ctx context.Context, id uuid.UUID) error
	// ListByWorkspace returns rules scoped to the workspace (scope=workspace).
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, includeDisabled bool) ([]domain.Rule, error)
	// ListByProject returns rules scoped to a project (scope=project).
	ListByProject(ctx context.Context, projectID uuid.UUID, includeDisabled bool) ([]domain.Rule, error)
	// ListByAgent returns rules scoped to a specific agent (scope=agent).
	ListByAgent(ctx context.Context, agentID uuid.UUID, includeDisabled bool) ([]domain.Rule, error)
	// GetEffective fetches all candidate rules for inheritance resolution across workspace,
	// project, and agent scopes. The caller filters and resolves inheritance.
	GetEffective(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Rule, error)
	// CountByAssigneeAndStatusCategory counts tasks for an assignee in given status categories.
	// Used by evaluators to check capacity limits without importing taskRepo.
	CountTasksByAssigneeAndCategory(ctx context.Context, workspaceID, assigneeID uuid.UUID, assigneeType string, categories []string) (int, error)
}

// WorkspaceRuleConfigRepository manages persistence for workspace-level rule configs.
type WorkspaceRuleConfigRepository interface {
	Upsert(ctx context.Context, rule *domain.WorkspaceRuleConfig) error
	GetByType(ctx context.Context, workspaceID uuid.UUID, ruleType string) (*domain.WorkspaceRuleConfig, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceRuleConfig, error)
	Delete(ctx context.Context, workspaceID uuid.UUID, ruleType string) error
}

// ProjectRuleConfigRepository manages persistence for project-level rule configs.
type ProjectRuleConfigRepository interface {
	Upsert(ctx context.Context, rule *domain.ProjectRuleConfig) error
	GetByType(ctx context.Context, projectID uuid.UUID, ruleType string) (*domain.ProjectRuleConfig, error)
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectRuleConfig, error)
	Delete(ctx context.Context, projectID uuid.UUID, ruleType string) error
}

// RuleViolationLogRepository manages persistence for rule violation log entries.
type RuleViolationLogRepository interface {
	Create(ctx context.Context, v *domain.RuleViolationLog) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.RuleViolationLog, error)
}

// RecurringRepository manages persistence for recurring task schedules.
type RecurringRepository interface {
	Create(ctx context.Context, schedule *domain.RecurringSchedule) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.RecurringSchedule, error)
	Update(ctx context.Context, schedule *domain.RecurringSchedule) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringSchedule], error)
	// FindDue returns active schedules where next_run_at <= now and (last_triggered_at IS NULL OR last_triggered_at < next_run_at),
	// using SELECT FOR UPDATE SKIP LOCKED for safe concurrent access.
	FindDue(ctx context.Context) ([]domain.RecurringSchedule, error)
	// IncrementInstance atomically sets instance_count, last_triggered_at, and next_run_at in one UPDATE.
	IncrementInstance(ctx context.Context, id uuid.UUID, nextRunAt *time.Time) error
	// GetInstanceHistory returns lightweight summaries for all task instances of a schedule.
	GetInstanceHistory(ctx context.Context, scheduleID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringInstanceSummary], error)
}

// TaskTemplateRepository manages persistence for reusable task templates.
type TaskTemplateRepository interface {
	Create(ctx context.Context, tmpl *domain.TaskTemplate) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskTemplate, error)
	List(ctx context.Context, projectID uuid.UUID) ([]domain.TaskTemplate, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateTemplateInput) (*domain.TaskTemplate, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// AutoTransitionRuleRepository manages persistence for auto-transition rules.
type AutoTransitionRuleRepository interface {
	List(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.AutoTransitionRule, error)
	Create(ctx context.Context, rule *domain.AutoTransitionRule) error
	Update(ctx context.Context, rule *domain.AutoTransitionRule) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// IntegrationRepository manages persistence for workspace integration configurations.
type IntegrationRepository interface {
	Upsert(ctx context.Context, cfg *domain.IntegrationConfig) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error)
	GetByProvider(ctx context.Context, workspaceID uuid.UUID, provider domain.IntegrationProvider) (*domain.IntegrationConfig, error)
	Update(ctx context.Context, id uuid.UUID, input domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.IntegrationConfig, error)
}

// MemoryRepository manages persistence for agent memories (knowledge base).
type MemoryRepository interface {
	Upsert(ctx context.Context, mem *domain.Memory) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error)
	GetByKey(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error)
	FullTextSearch(ctx context.Context, query string, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, tags []string, limit int) ([]domain.ScoredMemory, error)
	FindByScope(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, limit int) ([]domain.Memory, error)
	ListByWorkspaceProject(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]domain.Memory, error)
	Delete(ctx context.Context, id uuid.UUID) error
	BoostRelevance(ctx context.Context, ids []uuid.UUID) error
	// VectorSearch performs application-level cosine similarity search using stored embeddings.
	// It returns up to limit memories ranked by cosine similarity to queryVec.
	// Results are filtered by the same workspace/project/scope/tags criteria as FullTextSearch.
	// When no embeddings are stored, an empty slice is returned without error.
	VectorSearch(ctx context.Context, queryVec []float32, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, tags []string, limit int) ([]domain.ScoredMemory, error)
	// UpdateEmbedding stores the embedding vector (encoded as JSON) for a single memory.
	UpdateEmbedding(ctx context.Context, id uuid.UUID, vec []float32, model string, dim int) error
	// DecayRelevance reduces relevance by 0.05 for agent-scope memories not updated in 30+ days,
	// capped at a floor of 0.1. Workspace and project scope memories are exempt.
	// Returns the number of rows updated.
	DecayRelevance(ctx context.Context) (int64, error)
	// CleanExpired deletes memories that have a non-null expires_at in the past.
	// Returns the number of rows deleted.
	CleanExpired(ctx context.Context) (int64, error)
	// ListWithNullEmbedding returns up to limit memories whose embedding column is NULL.
	// Used by the batch embedding job.
	ListWithNullEmbedding(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.Memory, error)
}

// AgentSessionRepository manages persistence for agent session tracking.
type AgentSessionRepository interface {
	Create(ctx context.Context, session *domain.AgentSession) error
	Update(ctx context.Context, session *domain.AgentSession) error
	GetActive(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error)
	EndStale(ctx context.Context, timeout time.Duration) (int, error)
}
