package repository

import (
	"context"
	"encoding/json"
	"errors"
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
	// ListForUser returns workspaces the user is a member of, plus the ones
	// they own (legacy owners may have no workspace_members row).
	ListForUser(ctx context.Context, userID uuid.UUID) ([]domain.Workspace, error)
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
	StatusIDs      []uuid.UUID
	StatusCategory *domain.StatusCategory // filter by status category via join on task_statuses
	AssigneeID     *uuid.UUID
	AssigneeType   *domain.AssigneeType
	Priority       *domain.Priority
	ParentTaskID   *uuid.UUID
	Labels         []string
	Search         string
	HasDueDate     *bool
	CustomFields   map[string]CustomFieldFilter // key = field slug
}

// AssigneeTaskFilter narrows the principal's own task feed (GET /agents/me/tasks
// and its long-poll twin).
//
// These three used to be applied — or not applied at all — AFTER the whole feed
// had been read out of Postgres and marshalled into Go: the repository returned
// every task carrying the principal's id (836 rows / ~1 MB for one real agent),
// and the handler then dropped the ones whose status category did not match. A
// request that answered with an empty array cost exactly as much as one that
// answered with everything. Putting them in the SQL is the whole point of the
// type; nothing here is a convenience.
type AssigneeTaskFilter struct {
	// StatusCategory keeps only tasks whose status belongs to this category.
	StatusCategory *domain.StatusCategory
	// ProjectID keeps only tasks in one project.
	ProjectID *uuid.UUID
	// Limit caps the number of rows returned. 0 means no limit — the feed is how
	// an agent discovers work, so truncation is opt-in and always reported back
	// via the total the call returns.
	Limit int
}

// TaskRepository manages persistence for tasks.
type TaskRepository interface {
	Create(ctx context.Context, task *domain.Task) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	// GetByShortID resolves the first task whose UUID starts with the given hex prefix.
	// prefix must be 6–12 hex chars. Returns apierror.NotFound if no match, apierror.BadRequest if ambiguous.
	GetByShortID(ctx context.Context, prefix string) (*domain.Task, error)
	Update(ctx context.Context, task *domain.Task) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, projectID uuid.UUID, filter TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	// Search searches tasks across all projects in a workspace by text query.
	Search(ctx context.Context, workspaceID uuid.UUID, filter TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error)
	// ListByAssignee is workspace-scoped by contract: see the implementation for why
	// the predicate is mandatory rather than a filter.
	//
	// Returns the matching page and the TOTAL number of matches, so a caller that
	// asked for a limit can tell a full answer from a truncated one. With
	// filter.Limit == 0 there is no truncation and total equals len(tasks).
	ListByAssignee(ctx context.Context, workspaceID, assigneeID uuid.UUID, assigneeType domain.AssigneeType, filter AssigneeTaskFilter) (tasks []domain.Task, total int, err error)
	// ListByUserActive returns tasks assigned to a user in a workspace, excluding done/cancelled categories.
	ListByUserActive(ctx context.Context, workspaceID, userID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error)
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
	// ForceReleaseCheckout clears the checkout fields without token verification.
	// Used by the service layer for auto-release on terminal status transitions
	// and for admin force-unlock. Returns nil even when no checkout was held.
	ForceReleaseCheckout(ctx context.Context, taskID uuid.UUID) error
	// ReleaseExpiredCheckouts clears checkout fields on all tasks whose
	// checkout_expires has passed. Returns the number of locks released.
	// Called by the background reaper goroutine in cmd/api/main.go.
	ReleaseExpiredCheckouts(ctx context.Context) (int64, error)
	// FindExpiredInProgressCheckouts returns tasks whose checkout_expires is in the
	// past and whose status category is in_progress. These tasks have an expired
	// lease and are candidates for auto-return to todo by the lease reaper.
	FindExpiredInProgressCheckouts(ctx context.Context) ([]domain.Task, error)
	// FindDueMonitorBacklogTasks returns tasks in "backlog" category, labelled
	// "kind:monitor", whose due_date has passed. These are candidates for
	// auto-promotion back to "todo" by the monitor promotion sweeper.
	FindDueMonitorBacklogTasks(ctx context.Context) ([]domain.Task, error)
	// MoveToProject atomically reassigns a task to a different project, assigning it
	// the given target status and a new task_number within that project.
	// Returns apierror.NotFound("Task") if the task does not exist or is soft-deleted.
	MoveToProject(ctx context.Context, taskID, targetProjectID, targetStatusID uuid.UUID) error
	// ListOpenByRecurringScheduleID returns non-terminal (not done/cancelled/deleted)
	// tasks belonging to the given recurring schedule, excluding exceptTaskID.
	// Used by SupersedeRecurringInstances to close previous open instances.
	ListOpenByRecurringScheduleID(ctx context.Context, scheduleID, exceptTaskID uuid.UUID) ([]domain.Task, error)
	// SetHumanGate atomically sets the human_gate sticky flag without touching other fields.
	// Pass true to arm the gate, false to clear it (human sign-off received).
	SetHumanGate(ctx context.Context, taskID uuid.UUID, value bool) error
	// SetShipped atomically sets the is_shipped flag. Pass true to mark the task as
	// terminally shipped; false to clear the flag (unship).
	SetShipped(ctx context.Context, taskID uuid.UUID, value bool) error
	// SetDodCheck upserts a single named gate entry in the task's dod_checks JSONB column.
	// status must be "pending", "pass", or "fail". reporter identifies the caller.
	SetDodCheck(ctx context.Context, taskID uuid.UUID, gateName, status, reporter string) error
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
	// GetByID returns the dependency, or (nil, nil) when it does not exist.
	// Deleting an is_child_of edge has to undo the parent_task_id it set, and
	// that needs the edge's type and endpoints before the row goes away.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskDependency, error)
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

// CommentViewFilter defines filtering options for enriched comment view queries (activity feed).
type CommentViewFilter struct {
	Limit       int
	Before      *time.Time // cursor: return items created before this timestamp
	BeforeID    *uuid.UUID // cursor tie-breaker, paired with Before: (created_at, id) < (Before, BeforeID). Optional — a lone Before still applies the old strict-timestamp comparison, for backward compat with cursors issued before this field existed.
	WorkspaceID *uuid.UUID // optional workspace scope (used by ListByAuthor)
	ProjectID   *uuid.UUID // optional project scope (used by ListByAuthor)
	// IncludeInternal, when true, drops the default `is_internal = false`
	// predicate on ListRecentByWorkspace — see #a7ae4c76. Not honored by
	// ListByAuthor (a caller's own comments aren't the internal-mentions
	// corpus this exists for). Defaults to false: excluded, same as before
	// this field existed.
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
	ListByAuthor(ctx context.Context, authorID uuid.UUID, filter CommentViewFilter) ([]domain.CommentView, *domain.CommentCursor, error)
	ListRecentByWorkspace(ctx context.Context, wsID uuid.UUID, filter CommentViewFilter) ([]domain.CommentView, *domain.CommentCursor, error)
	// HasAnyComment returns true when the task has at least one comment.
	HasAnyComment(ctx context.Context, taskID uuid.UUID) (bool, error)
	// HasRecentCommentBy returns true when the task has a non-internal comment by authorID
	// created on or after `since` whose body is at least minLength characters long.
	HasRecentCommentBy(ctx context.Context, taskID, authorID uuid.UUID, since time.Time, minLength int) (bool, error)
}

// ArtifactRepository manages persistence for artifacts.
type ArtifactRepository interface {
	Create(ctx context.Context, artifact *domain.Artifact) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	// GetByIDInWorkspace is like GetByID but returns nil when the artifact's task
	// does not belong to workspaceID — used as a defense-in-depth ownership check.
	GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Artifact, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListByTask(ctx context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error)
	// UpdateMetadata overwrites the JSONB metadata column for a single artifact.
	UpdateMetadata(ctx context.Context, id uuid.UUID, metadata json.RawMessage) error
}

// DocumentRepository manages persistence for project documents. Every read here
// hides soft-deleted rows; only Create and Update write.
type DocumentRepository interface {
	Create(ctx context.Context, doc *domain.Document) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Document, error)
	// GetByIDInWorkspace is like GetByID but returns nil when the document's
	// project does not belong to workspaceID — the defense-in-depth ownership
	// check behind the /documents/:doc_id routes.
	GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Document, error)
	// GetByPathInProject resolves a slug path — ["architecture", "adr", "adr-004"]
	// — to the document it names, walking down from the project's top level. It
	// returns the document, or nil with the number of segments that did resolve so
	// the caller can say where the path went wrong instead of only that it did.
	//
	// Live rows only, at every level: a soft-deleted document is not a step you can
	// walk through, and its slug is free for a sibling to take.
	GetByPathInProject(ctx context.Context, projectID uuid.UUID, segments []string) (doc *domain.Document, resolvedDepth int, err error)
	// Update writes title, parent_id, position, updated_at and the updated_by
	// pair. Nothing else on a document is mutable: the body is in object storage,
	// and project_id and slug are what its siblings are unique against.
	//
	// expectedVersion, when non-nil, makes the write conditional: the UPDATE only
	// matches a row still at that version, and a row that has moved on is left
	// untouched and reported as ErrDocumentVersionMismatch. The compare and the
	// write are one statement on purpose — a service that read the version and
	// then wrote would leave open exactly the window the check exists to close.
	//
	// bumpVersion sets version = version + 1 in the same statement. It is passed
	// rather than inferred because only the caller knows whether this write
	// touched the document's content: a move in the tree must not bump.
	//
	// Returns the version the row now carries. On ErrDocumentVersionMismatch that
	// is the version actually stored, which is what the caller has to be told to
	// be able to retry.
	Update(ctx context.Context, doc *domain.Document, expectedVersion *int, bumpVersion bool) (version int, err error)
	// SoftDelete stamps deleted_at, freeing the slug for a new sibling, and
	// records who did it: a delete is a change to the row, and "last updated by"
	// has to survive a restore to be worth anything.
	SoftDelete(ctx context.Context, id uuid.UUID, at time.Time, by uuid.UUID, byType domain.ActorType) error
	ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Document], error)
	// HasAncestor reports whether ancestorID appears above docID in the parent
	// chain. Re-parenting uses it to refuse the cycles that would otherwise
	// detach a subtree from every listing that walks down from the roots.
	HasAncestor(ctx context.Context, docID, ancestorID uuid.UUID) (bool, error)
	// SetSearchText stores the copy of the body that the full-text index is built
	// from. Called after the body reaches object storage, never instead of it —
	// S3 stays canonical and this is only what makes the row findable.
	//
	// Its own method rather than a column on Update: the text can be megabytes,
	// and threading it through the row struct would put it in every read that
	// scans one.
	SetSearchText(ctx context.Context, documentID uuid.UUID, text string) error
	// SearchInProject ranks the project's live documents against a query, over
	// title AND content. workspaceID is the tenancy check, the same one
	// GetByIDInWorkspace applies: a project id alone is not proof of ownership.
	SearchInProject(ctx context.Context, projectID, workspaceID uuid.UUID, query string, limit int) ([]domain.DocumentSearchHit, error)
}

// ErrDocumentVersionMismatch reports that a conditional document write found the
// row at a version other than the one the caller expected, so nothing was
// written. DocumentRepository.Update returns the version actually stored
// alongside it.
//
// A sentinel rather than a typed error carrying the number, because the number is
// already in the other return value and two copies of it could disagree.
var ErrDocumentVersionMismatch = errors.New("document version mismatch")

// DocumentAttachmentRepository manages persistence for the files uploaded into a
// document. Every read here hides soft-deleted rows; only Create writes.
//
// There is no Update: nothing about an attachment is mutable. The name is what was
// uploaded, and the storage key is derived from the immutable id — a rename that
// moved the key would orphan the object it used to name.
type DocumentAttachmentRepository interface {
	Create(ctx context.Context, att *domain.DocumentAttachment) error
	// GetByIDInWorkspace returns nil when the attachment's document belongs to a
	// project outside workspaceID, or when either row is soft-deleted. Callers
	// answer 404 on nil, so a stranger's id and a nonexistent one look the same.
	GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.DocumentAttachment, error)
	ListByDocument(ctx context.Context, documentID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.DocumentAttachment], error)
	// SoftDelete stamps deleted_at. The stored object is deliberately left alone —
	// see documentAttachmentService.Delete.
	SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error
}

// DocumentCommentFilter narrows a document's comment listing.
type DocumentCommentFilter struct {
	// IncludeResolved keeps resolved threads in the result. It defaults to false
	// — a resolved thread is one somebody deliberately put away, and the reading
	// view is the default one — and it filters by THREAD, not by comment: a
	// resolved root takes its replies with it, or a listing would show answers to
	// a question that is no longer there.
	//
	// Same shape and same default as CommentFilter.IncludeInternal.
	IncludeResolved bool
}

// DocumentCommentRepository manages persistence for comments anchored to a
// document's text. Every read here hides soft-deleted rows.
type DocumentCommentRepository interface {
	Create(ctx context.Context, comment *domain.DocumentComment) error
	// GetByID ignores tenancy and is only for the checks that already have a
	// tenant-scoped object in hand — validating that a reply's parent lives on
	// the same document. Never call it with an id straight off the wire.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DocumentComment, error)
	// GetByIDInWorkspace returns nil when the comment's document belongs to a
	// project outside workspaceID, or when any row in that chain is soft-deleted.
	// Callers answer 404 on nil, so a stranger's id and a nonexistent one look
	// the same.
	GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.DocumentComment, error)
	// Update writes body, the resolution triple and updated_at — the whole of
	// what is mutable. The anchor is not: it records what the comment was written
	// about, and rewriting it would relabel the past.
	Update(ctx context.Context, comment *domain.DocumentComment) error
	// SoftDelete stamps deleted_at on the comment and, when it is a thread root,
	// on its replies in the same statement: a reply that outlived the comment it
	// answers is an answer to nothing.
	SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error
	ListByDocument(ctx context.Context, documentID uuid.UUID, filter DocumentCommentFilter, pg pagination.Params) (*pagination.Page[domain.DocumentComment], error)
	// ListAnchorsByDocument returns the anchor of every live comment on the
	// document that has one, resolved threads included. It is the input to the
	// re-anchoring pass that runs after the body is rewritten, so it is not
	// paginated and not filtered: an anchor left out of the pass is an anchor
	// left pointing at whatever now occupies its old offsets, and "resolved"
	// describes a conversation, not whether its offsets may lie.
	ListAnchorsByDocument(ctx context.Context, documentID uuid.UUID) ([]DocumentCommentAnchorRow, error)
	// UpdateAnchorPositions moves each listed comment's offsets to where its
	// quote now sits, or nulls them when it no longer sits anywhere. One
	// statement for the whole document: a per-row loop could be interrupted
	// half-way and leave some rows describing the new body and some the old,
	// which is worse than either, and impossible to tell apart from both.
	UpdateAnchorPositions(ctx context.Context, positions []DocumentCommentAnchorPosition) error
}

// DocumentCommentAnchorRow is one comment's anchor, with the id needed to write
// it back. Just the anchor: the re-anchoring pass has no use for the body, the
// author or the resolution triple, and a document with a hundred comments should
// not pull a hundred bodies through to move five offsets.
type DocumentCommentAnchorRow struct {
	ID     uuid.UUID `db:"id"`
	Exact  string    `db:"anchor_exact"`
	Prefix *string   `db:"anchor_prefix"`
	Suffix *string   `db:"anchor_suffix"`
	Start  *int      `db:"anchor_start"`
	End    *int      `db:"anchor_end"`
}

// DocumentCommentAnchorPosition is where one comment's anchor should now sit.
//
// Start and End are nil together to orphan it. The pair is nil-or-both by the
// same schema check that governs the columns, so a caller cannot express
// "orphaned, and here are the offsets" — the state the flag-shaped design would
// have allowed and this one cannot.
type DocumentCommentAnchorPosition struct {
	ID     uuid.UUID
	Prefix string
	Suffix string
	Start  *int
	End    *int
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
	// SetAPIKeySHA256 fills in the fast-verification digest for an agent whose
	// row still predates it. expectedBcryptHash guards the write: it is the hash
	// the caller just verified the presented key against, so a rotation that
	// landed in between makes the UPDATE match no row rather than stamping a
	// digest of the superseded key onto the new one.
	SetAPIKeySHA256(ctx context.Context, agentID uuid.UUID, digest, expectedBcryptHash string) error
	// GetBySlug returns the agent with the given slug in a workspace, or (nil, nil) if not found.
	GetBySlug(ctx context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error)
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
	// TouchLastSeenBatch bumps last_heartbeat and updated_at for the given agent
	// IDs without changing status. Used by the activity-tracker middleware.
	TouchLastSeenBatch(ctx context.Context, ids []uuid.UUID) error
	// SearchByPrefix returns agents in the workspace whose name or slug contain the prefix (ILIKE), sorted by exact-prefix match first then name, up to limit results.
	SearchByPrefix(ctx context.Context, workspaceID uuid.UUID, prefix string, limit int) ([]domain.Agent, error)
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
	EventType     *domain.EventType
	AgentID       *uuid.UUID
	TaskID        *uuid.UUID
	Tags          []string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	// AgentOnly filters to events where agent_id IS NOT NULL.
	AgentOnly bool
}

// EventBusMessageRepository manages persistence for event bus messages.
type EventBusMessageRepository interface {
	Create(ctx context.Context, msg *domain.EventBusMessage) error
	Upsert(ctx context.Context, msg *domain.EventBusMessage) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error)
	List(ctx context.Context, projectID uuid.UUID, filter EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error)
	// ListEnriched is like List but LEFT JOINs tasks, projects, and agents to populate
	// display fields (task title, project name, actor name) for the UI.
	ListEnriched(ctx context.Context, projectID uuid.UUID, filter EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EnrichedEventBusMessage], error)
	DeleteExpired(ctx context.Context) (int64, error)
}

// AgentEventsRepository manages persistence for durable SSE event replay.
type AgentEventsRepository interface {
	// Create persists a new agent event.
	Create(ctx context.Context, event *domain.AgentEvent) error
	// Lookup returns the event with the given ID if it exists and has not expired.
	// Returns (nil, nil) if not found or expired — used for cursor validation (410 path).
	Lookup(ctx context.Context, eventID uuid.UUID) (*domain.AgentEvent, error)
	// ListAfter returns up to limit events for agentID with event_id > lastEventID, ordered ASC.
	ListAfter(ctx context.Context, agentID uuid.UUID, lastEventID uuid.UUID, limit int) ([]domain.AgentEvent, error)
	// DeleteExpired removes all events past their expires_at. Returns rows deleted.
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
	// UsernameExists reports whether any user already holds the given username (case-insensitive, global).
	UsernameExists(ctx context.Context, username string) (bool, error)
	// SearchAddableUsers returns users the caller may be shown while looking for
	// someone to add to a workspace: an exact address match anywhere on the
	// instance, plus loose matches restricted to people the caller already shares
	// a workspace with. Pass uuid.Nil for callerID when the caller is not a user
	// (agent key), which leaves only the exact-address rule. It replaced an
	// instance-wide substring search that let any workspace owner enumerate the
	// whole user directory — see the implementation for the full note.
	SearchAddableUsers(ctx context.Context, callerID uuid.UUID, query string, limit int) ([]domain.User, error)
	// GetByUsername returns the user with the given username in the workspace, or (nil, nil) if not found.
	GetByUsername(ctx context.Context, workspaceID uuid.UUID, username string) (*domain.User, error)
	// SearchInWorkspace returns users who are workspace members and whose display_name, username, or email match the query (ILIKE), up to limit results.
	SearchInWorkspace(ctx context.Context, workspaceID uuid.UUID, query string, limit int) ([]domain.User, error)
	// GetByUsernameGlobal returns the user with the given username across all workspaces, or (nil, nil) if not found.
	// Used for global uniqueness checks (ix_users_username is a global unique index, not workspace-scoped).
	GetByUsernameGlobal(ctx context.Context, username string) (*domain.User, error)
	// Count returns the total number of users on the instance. Used to detect a
	// fresh install (zero users) so the first registration can bypass a closed
	// registration policy — otherwise a closed self-host instance could never
	// be bootstrapped.
	Count(ctx context.Context) (int, error)
}

// MentionFilter holds filtering options for listing mention records.
type MentionFilter struct {
	Seen      *bool
	Since     *time.Time
	ProjectID *uuid.UUID
	Limit     int
}

// CommentMentionRepository manages persistence for comment_mentions rows.
type CommentMentionRepository interface {
	InsertBatch(ctx context.Context, mentions []domain.CommentMention) error
	List(ctx context.Context, mentionedID uuid.UUID, mentionedKind string, filter MentionFilter) ([]domain.CommentMentionView, error)
	MarkSeen(ctx context.Context, commentID, mentionedID uuid.UUID) error
	CountUnseen(ctx context.Context, mentionedID uuid.UUID, mentionedKind string) (int64, error)
}

// CommentDeliveryOutcomeRepository manages persistence for
// comment_delivery_outcomes rows — one verdict per @-addressed handle on a
// comment, including handles that resolved to nobody.
//
// Separate from CommentMentionRepository on purpose. That one is the Mention
// feed and is keyed on a resolved recipient id; this one is the delivery
// record and is keyed on the handle as written, which is the only key that
// survives a handle resolving to nothing.
type CommentDeliveryOutcomeRepository interface {
	InsertBatch(ctx context.Context, rows []domain.CommentDeliveryOutcome) error
	MarkFailed(ctx context.Context, commentID uuid.UUID, slug, reason string) error
	ListByCommentIDs(ctx context.Context, commentIDs []uuid.UUID) (map[uuid.UUID][]domain.CommentDeliveryOutcome, error)
}

// DocumentCommentMentionRepository manages persistence for
// document_comment_mentions rows.
//
// Deliberately the same four operations as CommentMentionRepository, over the
// same MentionFilter: a mention on a document page and a mention on a task are
// the same thing to whoever was named, and the read side should not be two
// different shapes because the write side has two parent tables.
type DocumentCommentMentionRepository interface {
	InsertBatch(ctx context.Context, mentions []domain.DocumentCommentMention) error
	List(ctx context.Context, mentionedID uuid.UUID, mentionedKind string, filter MentionFilter) ([]domain.DocumentCommentMentionView, error)
	MarkSeen(ctx context.Context, commentID, mentionedID uuid.UUID) error
	CountUnseen(ctx context.Context, mentionedID uuid.UUID, mentionedKind string) (int64, error)
}

// DocumentWatchRepository manages document subscriptions and the pending
// change-notices that make coalescing possible.
//
// The two live behind one interface because they are one feature: a notice is
// only ever created for a document somebody might be watching, and it is only
// ever read in order to find out who.
type DocumentWatchRepository interface {
	// Subscribe records a subscription. An automatic source (author, commenter)
	// must not resurrect a muted row — that is what makes unsubscribing stick —
	// so `force` is set only by the explicit Watch button.
	Subscribe(ctx context.Context, w domain.DocumentWatcher, force bool) error

	// Unsubscribe mutes the caller's subscription, creating the tombstone row if
	// none existed. Reports whether anything is now muted for this principal.
	Unsubscribe(ctx context.Context, documentID, watcherID uuid.UUID, watcherKind string) error

	// GetState answers "am I watching this, and how many others are".
	GetState(ctx context.Context, documentID, watcherID uuid.UUID, watcherKind string) (*domain.DocumentWatchState, error)

	// ListLiveWatchers returns the document's non-muted watchers.
	ListLiveWatchers(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentWatcher, error)

	// RecordChange folds one edit into the open notice for (document, actor),
	// opening one if there is none. This is the write that a hundred autosaves
	// collapse into a hundred UPDATEs of a single row.
	RecordChange(ctx context.Context, n domain.DocumentChangeNotice) error

	// ClaimPendingNotices atomically takes ownership of the notices whose actor
	// has been quiet since `quietBefore` and that nobody else has claimed.
	//
	// Atomic claim rather than select-then-update: two API replicas run this
	// sweeper, and a non-atomic read would let both dispatch the same notice.
	ClaimPendingNotices(ctx context.Context, quietBefore time.Time, limit int) ([]domain.DocumentChangeNotice, error)

	// FinishNotice records the outcome of a dispatch: how many principals were
	// told, and why nobody was if that is the answer.
	FinishNotice(ctx context.Context, id uuid.UUID, recipients int, dispatchErr string) error
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
	// RevokeByHash atomically revokes the token with this hash if and only if it is
	// still un-revoked. It reports whether THIS call performed the revocation, which
	// is the sole authority on who won a concurrent one-shot rotation: exactly one
	// caller can ever observe true for a given token. A false with a nil error means
	// the token was already consumed (by a racing request or an earlier one) — the
	// caller must treat it as reuse, never as success.
	RevokeByHash(ctx context.Context, tokenHash string) (bool, error)
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
	// Upsert inserts a new link or updates status/title/metadata on conflict
	// of (task_id, provider, link_type, external_id). Used by the GitHub
	// webhook orchestrator so repeated deliveries (opened → synchronize →
	// closed) update the same row rather than failing the unique index.
	// On the update branch, id and created_at are NOT touched — they keep the
	// existing row's values, never the caller-supplied link's. Upsert mutates
	// *link in place to those actual persisted values so callers never echo a
	// freshly-generated id/created_at that the database silently discarded
	// (see #b73171fa). Returns true when this call inserted a new row, false
	// when it updated an existing one.
	Upsert(ctx context.Context, link *domain.VCSLink) (created bool, err error)
	// ListByExternalID returns all links matching (provider, link_type,
	// external_id), newest first by created_at. Fallback path when a webhook
	// payload has no MESH-<uuid> ref but the (provider, type, external_id)
	// tuple was previously linked to a task.
	ListByExternalID(ctx context.Context, provider domain.VCSProvider, linkType domain.VCSLinkType, externalID string) ([]domain.VCSLink, error)
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
	// AdvanceNextRun updates only next_run_at (without touching instance_count or last_triggered_at).
	// Used on createInstance failure so the poisoned tick is skipped and the schedule doesn't loop every 60s.
	AdvanceNextRun(ctx context.Context, id uuid.UUID, nextRunAt *time.Time) error
	// RecordFailure advances next_run_at past the failing cycle, increments consecutive_failures,
	// and stores the last error message. Prevents infinite 60s retry on a poisoned INSERT.
	RecordFailure(ctx context.Context, id uuid.UUID, nextRunAt *time.Time, errMsg string) error
	// Quarantine marks a schedule inactive (is_active=false, quarantined_at=NOW()).
	// Called after consecutive_failures reaches the configured threshold.
	Quarantine(ctx context.Context, id uuid.UUID) error
	// ResetConsecutiveFailures clears the failure counter and last_error after a successful instance creation.
	ResetConsecutiveFailures(ctx context.Context, id uuid.UUID) error
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
	// ListActiveByProvider returns every active integration for one provider,
	// across all workspaces. Used at API startup to know which Telegram bots
	// to start long-polling — ListByWorkspace only answers "what does one
	// workspace have configured", not "what needs a poller right now".
	ListActiveByProvider(ctx context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error)
}

// MemoryRepository manages persistence for agent memories (knowledge base).
type MemoryRepository interface {
	Upsert(ctx context.Context, mem *domain.Memory) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error)
	GetByKey(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID, key string, scope domain.MemoryScope) (*domain.Memory, error)
	// FullTextSearch ranks memories by tsvector relevance (ts_rank_cd). When recencyWeight > 0
	// an exponential recency-decay factor (half-life ~30d) is blended into the ranking and
	// results are reordered by the blended score; recencyWeight == 0 preserves the legacy
	// FTS-only ordering byte-for-byte. Values outside [0,1] are clamped.
	FullTextSearch(ctx context.Context, query string, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, tags []string, limit int, recencyWeight float64) ([]domain.ScoredMemory, error)
	FindByScope(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, scope string, limit int) ([]domain.Memory, error)
	// ListByWorkspaceProject returns non-expired memories for a workspace/project pair.
	// filter.Limit/Offset/MinImportance/TagsAny are applied to the workspace-tier when projectID is nil.
	// Limit=0 means no limit (used for export and project-scoped calls). Returns total count pre-limit.
	ListByWorkspaceProject(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error)
	// List executes a richly-filtered query with pagination, tag filters, ordering, and optional
	// recency-decay scoring. It is used by the extended Recall/List endpoints introduced in
	// the memory API extensions (Phase 2). Total is a separate COUNT query.
	List(ctx context.Context, filter domain.MemoryListFilter) (*domain.MemoryListResult, error)
	Delete(ctx context.Context, id uuid.UUID) error
	BoostRelevance(ctx context.Context, ids []uuid.UUID) error
	// VectorSearch performs application-level cosine similarity search using stored embeddings.
	// It returns up to limit memories ranked by cosine similarity to queryVec.
	// Results are filtered by workspace/project plus the shared scope/tags eligibility
	// filter — the SAME filter FullTextSearchRanked applies, so both arms of Recall draw
	// their candidate pool from the identical eligible set.
	// When no embeddings are stored, an empty slice is returned without error.
	VectorSearch(ctx context.Context, queryVec []float32, workspaceID uuid.UUID, projectID *uuid.UUID, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error)
	// UpdateEmbedding stores the embedding vector (encoded as JSON) for a single memory.
	// Also bumps updated_at — correct on a write path (the caller just changed the memory),
	// wrong on a repair path: see UpdateEmbeddingKeepUpdatedAt.
	UpdateEmbedding(ctx context.Context, id uuid.UUID, vec []float32, model string, dim int) error
	// UpdateEmbeddingKeepUpdatedAt stores the embedding exactly as UpdateEmbedding does but
	// leaves updated_at alone, for jobs that re-embed EXISTING memories nobody edited.
	//
	// updated_at is not a cosmetic column and re-embedding the corpus through UpdateEmbedding
	// would rewrite it irrecoverably for every row: MarkStaleByAge keys staleness off it (the
	// whole corpus would get a fresh staleness window), DecayRelevance keys its 30-day
	// threshold off it (agent-scope relevance decay would stall for a month), and
	// applyRecencyBlend computes Δt from it (the recency term collapses to uniform, though
	// only for callers passing recency_weight > 0). DecayRelevance already writes
	// `updated_at = updated_at` for exactly this reason; this method extends the same rule to
	// the embed path.
	UpdateEmbeddingKeepUpdatedAt(ctx context.Context, id uuid.UUID, vec []float32, model string, dim int) error
	// MarkEmbeddingModel sets embedding_model without touching embedding/embedding_dim.
	// NOT called by the chunked embed path — embedChunked uses UpdateEmbedding (see its doc).
	// An earlier revision used this alone as a watermark, on the belief that
	// ListNeedingEmbedding filtered on embedding_model only. It does not: the real predicate
	// is `embedding IS NULL OR embedding_model IS DISTINCT FROM $model` (note the OR), so
	// setting embedding_model while leaving embedding NULL clears one disjunct and leaves the
	// other true forever — the row is re-selected on every reindex run, and meanwhile it is
	// invisible to VectorSearch, which requires embedding IS NOT NULL. That was a live
	// regression (#84b0694d / #7cf0f3be). Kept as a general repo primitive; a future caller
	// must not repeat the embedding_model-only watermark against that predicate.
	MarkEmbeddingModel(ctx context.Context, id uuid.UUID, model string) error
	// DecayRelevance reduces relevance by 0.05 for agent-scope memories not updated in 30+ days,
	// capped at a floor of 0.1. Workspace and project scope memories are exempt.
	// Returns the number of rows updated.
	DecayRelevance(ctx context.Context) (int64, error)
	// CleanExpired deletes memories that have a non-null expires_at in the past.
	// Returns the number of rows deleted.
	CleanExpired(ctx context.Context) (int64, error)
	// ListNeedingEmbedding returns up to limit memories that need to be (re)embedded with
	// the currently configured model. A memory is excluded (does NOT need embedding) when
	// EITHER of these holds for the given model: (a) memories.embedding is populated and
	// memories.embedding_model matches, or (b) it already has a memory_chunks row whose
	// embedding_model matches — chunk freshness is checked independently of the
	// memories.embedding column so this stays correct even if a future write path stops
	// populating memories.embedding (ADR-0002 read-path, subtask 6/8) without needing a
	// matching change here. A memory with NEITHER is selected — no vector yet, or its
	// vector/chunks came from a DIFFERENT model (switching embedding provider/model is a
	// supported operation: vectors from another model live in another vector space, score 0
	// in cosineSimilarity via the dimension guard, and would otherwise stay invisible to
	// semantic recall forever because nothing re-embeds them).
	ListNeedingEmbedding(ctx context.Context, workspaceID uuid.UUID, model string, limit int) ([]domain.Memory, error)
	// ListNotYetChunked returns up to limit memories in workspaceID that have no memory_chunks
	// rows (ADR-0002 backfill, #b052cdda). Deliberately independent of embedding_model: a memory
	// embedded through the pre-chunking single-vector path already carries the current model's
	// name in embedding_model, so ListNeedingEmbedding's filter would never select it — this
	// query selects on chunk existence instead, which is also what makes repeated calls
	// naturally resumable (a memory chunked by an earlier call is excluded by construction, no
	// separate cursor to track).
	ListNotYetChunked(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.Memory, error)
	// ListNeedingRechunk returns up to limit memories in workspaceID whose stored chunk
	// offsets no longer index their current content — the rows still embedded under the
	// pre-#494 scheme, where the composite `key + " " + content + " " + tags` was chunked as a
	// whole instead of content alone.
	//
	// A THIRD selection query rather than a tweak to either of the two above, because neither
	// can see these rows: they have chunks (ListNotYetChunked excludes them by construction)
	// and those chunks carry the current model's name (ListNeedingEmbedding's mismatch filter
	// never fires). Nothing about the damage is visible in a column the write path maintains,
	// which is why the predicate reads chunk offsets instead — see the implementation's doc
	// for what that proves and the one thing it does not.
	ListNeedingRechunk(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.Memory, error)
	// CountNeedingRechunk returns the FULL remaining count of memories matching
	// ListNeedingRechunk's predicate, uncapped by any batch limit — so convergence can be
	// judged against the damaged population directly instead of against the repair job's own
	// processed-count, which is the number that lies when a selector misses its patients.
	CountNeedingRechunk(ctx context.Context, workspaceID uuid.UUID) (int, error)
	// FindByShortID returns the first memory in workspaceID whose UUID starts with prefix (6–12 lowercase hex chars).
	// Returns nil without error when no match is found.
	FindByShortID(ctx context.Context, workspaceID uuid.UUID, prefix string) (*domain.Memory, error)
	// SetArchived marks a memory as archived (true) or unarchived (false) by ID.
	SetArchived(ctx context.Context, id uuid.UUID, archived bool) error
	// FindByThreadID returns non-archived memories in workspaceID whose thread_id
	// matches, excluding the memory identified by excludeID (the calling memory).
	// Used by Amendment 2 to create same-thread relates_to edges.
	FindByThreadID(ctx context.Context, workspaceID uuid.UUID, threadID string, excludeID uuid.UUID) ([]domain.Memory, error)
	// FindBySourceTaskIDs returns non-archived memories in workspaceID whose
	// source_task_id is one of the given task UUIDs.
	// Used by Amendment 3 to create task-graph derived_from edges.
	FindBySourceTaskIDs(ctx context.Context, workspaceID uuid.UUID, sourceTaskIDs []uuid.UUID) ([]domain.Memory, error)
	// ArchiveStaleWorkspaceCheckpoints sets archived=true for workspace-scoped session-checkpoint
	// memories older than olderThan with importance_score < maxImportance.
	// Never archives entries tagged canonical/pavel-decision/kind:decision/kind:incident.
	// Called from the 6h memory cleanup scheduler.
	ArchiveStaleWorkspaceCheckpoints(ctx context.Context, olderThan time.Duration, maxImportance float64) (int64, error)
	// FindBySimhashProximity returns non-archived memories in workspaceID whose content_simhash
	// differs from simhash by at most maxHamming bits (Hamming distance via bit_count XOR).
	// excludeID is excluded from results (avoids self-match on upsert of an existing key).
	// Returns up to limit results ordered by importance_score DESC.
	FindBySimhashProximity(ctx context.Context, workspaceID uuid.UUID, simhash int64, maxHamming int, excludeID uuid.UUID, limit int) ([]domain.Memory, error)
	// FindPinned returns all non-archived memories tagged kind:pinned in the workspace.
	// If projectID is non-nil, both workspace-scoped and project-scoped pinned memories are returned.
	FindPinned(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]domain.Memory, error)
	// ExpireByValidUntil archives (status='archived', freshness_score=0.0) memories whose
	// valid_until is in the past and whose status is not already archived or superseded.
	// Batch cap: 500 rows per call (idempotent: safe to run multiple times).
	ExpireByValidUntil(ctx context.Context) (int64, error)
	// MarkStaleByAge marks active memories as stale (status='stale', freshness_score=0.25)
	// when they haven't been updated in staleAfter and were created after epoch.
	// The epoch gate prevents a mass-stale avalanche on first deploy.
	// Batch cap: 500 rows per call (idempotent).
	MarkStaleByAge(ctx context.Context, epoch time.Time, staleAfter time.Duration) (int64, error)
	// SetMemoryStatus updates the status, superseded_by, and freshness_score of a single memory.
	// freshness_score is derived from status.StatusFreshnessScore().
	SetMemoryStatus(ctx context.Context, id uuid.UUID, status domain.MemoryStatus, supersededBy *uuid.UUID) error
	// ListCreatedSince returns memories that have a stored embedding and were created at or after
	// since, ordered by created_at DESC, capped at limit. Used by the reconciler linker phase.
	ListCreatedSince(ctx context.Context, since time.Time, limit int) ([]domain.Memory, error)
	// FullTextSearchRanked performs BM25-style full-text search using the 'english' dictionary
	// and ts_rank_cd computed over (content || key). Unlike FullTextSearch (simple dictionary,
	// pre-built search_vector), this arm uses linguistic stemming and stopword removal for
	// higher recall precision. ExcludeSuperseded (status != 'superseded') is always enforced,
	// as is the shared scope/tags eligibility filter — the SAME filter VectorSearch applies.
	// Used as the sparse BM25 arm in the RRF fusion of service.Recall.
	FullTextSearchRanked(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, query string, filter domain.MemorySearchFilter, limit int) ([]domain.ScoredMemory, error)
}

// MemoryChunkRepository stores and retrieves per-chunk embeddings for long
// memories (ADR-0002) — see domain.MemoryChunk.
type MemoryChunkRepository interface {
	// ReplaceChunks atomically replaces every chunk for memoryID with chunks
	// (delete existing rows + insert the new batch in one transaction). This
	// is what makes re-embedding a memory idempotent: the chunker is
	// deterministic, so re-running it and replacing wholesale is always safe,
	// no content hashing needed. An empty chunks slice just clears the memory's
	// rows (e.g. if it shrank below the chunking threshold).
	ReplaceChunks(ctx context.Context, memoryID uuid.UUID, chunks []domain.MemoryChunk) error
	// ListByMemoryIDs returns every chunk row for the given memory IDs, in no
	// particular order. Never joins to memories.content — callers needing the
	// full memory (e.g. to hydrate top-N results) fetch that separately.
	ListByMemoryIDs(ctx context.Context, memoryIDs []uuid.UUID) ([]domain.MemoryChunk, error)
	// MemoryIDsWithChunks reports which of the given memory IDs have at least
	// one chunk row. Lets the read path fall back to memories.embedding for a
	// memory not yet migrated to chunked storage (mid-backfill, or a memory
	// short enough it was never chunked).
	MemoryIDsWithChunks(ctx context.Context, memoryIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// MemoryEdgeRepository manages directed, typed edges in the memory Knowledge Graph.
type MemoryEdgeRepository interface {
	// UpsertEdge inserts a new edge or updates weight/last_traversed_at on conflict (from, to, type).
	UpsertEdge(ctx context.Context, edge *domain.MemoryEdge) error
	// ReinforceEdge increments weight by 0.1 (capped at 5.0) and sets last_traversed_at=NOW()
	// for the edge identified by (fromID, toID, relType). No-op if the edge does not exist.
	ReinforceEdge(ctx context.Context, fromID, toID uuid.UUID, relType domain.MemoryEdgeRelationshipType) error
	// DecayWeights applies geometric decay (×0.95) to edges not traversed in >30 days.
	DecayWeights(ctx context.Context) (int64, error)
	// PruneDeadEdges deletes edges with weight < 0.1.
	PruneDeadEdges(ctx context.Context) (int64, error)
	// GetNeighbors returns edges connected to any of the given memory IDs
	// (bidirectional: memory_from_id ∈ ids OR memory_to_id ∈ ids) with weight >= weightThreshold,
	// ordered by weight DESC and capped at limit rows (defaults to 200 when <= 0).
	// Confined to workspaceID so BFS expansion cannot hop out of the caller's tenant.
	// Used by RecallGraph BFS expansion across the KG.
	GetNeighbors(ctx context.Context, ids []uuid.UUID, workspaceID uuid.UUID, weightThreshold float64, limit int) ([]domain.MemoryEdge, error)
}

// WorkspaceInviteRepository manages persistence for pending workspace invitations.
type WorkspaceInviteRepository interface {
	Create(ctx context.Context, invite *domain.WorkspaceInvite) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.WorkspaceInvite, error)
	// GetByToken looks up a pending invite by its signed token. Returns nil if not found.
	GetByToken(ctx context.Context, token string) (*domain.WorkspaceInvite, error)
	// ListByWorkspace returns all non-expired, unaccepted invites for a workspace.
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceInvite, error)
	// Accept marks the invite as accepted (sets accepted_at = NOW()).
	Accept(ctx context.Context, id uuid.UUID) error
	Delete(ctx context.Context, id uuid.UUID) error
	// DeleteExpired removes expired unaccepted invites. Returns rows deleted.
	DeleteExpired(ctx context.Context) (int64, error)
}

// AgentSessionRepository manages persistence for agent session tracking.
type AgentSessionRepository interface {
	Create(ctx context.Context, session *domain.AgentSession) error
	Update(ctx context.Context, session *domain.AgentSession) error
	GetActive(ctx context.Context, agentID uuid.UUID) (*domain.AgentSession, error)
	// GetActiveForTask returns the agent's active session for a specific task, or nil.
	// Scopes spend per-task so a busy agent completing multiple tasks within one
	// EndStale window doesn't pile every task's cost onto the first task's session.
	GetActiveForTask(ctx context.Context, agentID, taskID uuid.UUID) (*domain.AgentSession, error)
	EndStale(ctx context.Context, timeout time.Duration) (int, error)
	// GetPreviousStartedAt returns the started_at of the most recent non-active session
	// for the agent. Returns nil when no prior session exists.
	GetPreviousStartedAt(ctx context.Context, agentID uuid.UUID) (*time.Time, error)
	// GetTaskCostSummary aggregates session metrics for a task and computes rework count
	// from the activity_log. Returns a zero-value summary when no sessions exist.
	GetTaskCostSummary(ctx context.Context, taskID uuid.UUID) (*domain.TaskCostSummary, error)
}

// PushSubscriptionRepository manages persistence for Web Push subscriptions.
type PushSubscriptionRepository interface {
	Upsert(ctx context.Context, sub *domain.PushSubscription) error
	Delete(ctx context.Context, id uuid.UUID) error
	DeleteByEndpoint(ctx context.Context, userID uuid.UUID, endpoint string) error
	ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.PushSubscription, error)
	GetByEndpoint(ctx context.Context, endpoint string) (*domain.PushSubscription, error)
}

// ProjectIntegrationRepository manages project-level integration settings.
type ProjectIntegrationRepository interface {
	Get(ctx context.Context, projectID uuid.UUID, intType string) (*domain.ProjectIntegration, error)
	// GetByShareSlug finds the first enabled team_relay integration whose settings.share_slug matches.
	// Returns nil if not found.
	GetByShareSlug(ctx context.Context, shareSlug string) (*domain.ProjectIntegration, error)
	Upsert(ctx context.Context, pi *domain.ProjectIntegration) error
	Delete(ctx context.Context, projectID uuid.UUID, intType string) error
	ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectIntegration, error)
}

// SecretRepository manages the write-only secrets store (task #64e84eb1).
// Every method here returns or accepts domain.Secret, which has no field
// capable of carrying a value — that is what makes "no read path returns
// plaintext or ciphertext" a property the compiler can help enforce, not
// just something a test happens to check.
type SecretRepository interface {
	// Create encrypts input.Value and inserts a new current row. Returns
	// apierror.Conflict if a current (unrotated) secret already exists for
	// the same workspace/scope/name — callers must Rotate instead.
	Create(ctx context.Context, input domain.CreateSecretInput) (domain.Secret, error)
	// Rotate stamps the current row's rotated_at and inserts a new current
	// row with the new value, in one transaction. Returns apierror.NotFound
	// if there is no current secret to rotate.
	Rotate(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, input domain.CreateSecretInput) (domain.Secret, error)
	// Delete stamps rotated_at on the current row without inserting a
	// replacement, ending materialization for that name. History is kept,
	// not hard-deleted — same audit-trail posture as activity_log.
	Delete(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, deletedBy uuid.UUID, deletedByType domain.ActorType) error
	// ListCurrent returns the masked metadata (never a value) for every
	// current secret in scope for the given resolution: all workspace-scope
	// secrets in workspaceID, plus project-scope ones for projectID (if
	// given), plus agent-scope ones for agentID (if given).
	ListCurrent(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Secret, error)
}

// SecretMaterializer decrypts current secret values for spawn-time env-file
// materialization (task #64e84eb1, S4). This is a SEPARATE interface from
// SecretRepository on purpose: SecretRepository is what the public secrets
// API (S3) is built on, and it has no method that can return a value at all.
// Wire this interface into exactly one caller — the spawn-hook endpoint —
// never into anything a browser session or a general agent tool reaches.
type SecretMaterializer interface {
	// ResolveCurrentValues decrypts every CURRENT secret in scope for the
	// given resolution and reports each one's expiry state explicitly — an
	// expired entry is returned WITH Expired=true and an empty Value, so the
	// caller can name it in a loud spawn error rather than silently omit the
	// variable (the task explicitly rejects a silent empty var as worse than
	// a loud failure).
	ResolveCurrentValues(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.MaterializedSecret, error)
}
