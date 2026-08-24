package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/internal/repository"
	pgRepo "github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// timeNow is a package-level variable so tests can override the clock.
var timeNow = time.Now

// uuidPtrChanged reports whether two optional UUIDs differ by value. A raw
// pointer comparison (existing.X != task.X) is wrong here: existing and task
// come from two separate GetByID calls, each allocating its own *uuid.UUID
// for the same underlying value, so it would report "changed" even when the
// value didn't move.
func uuidPtrChanged(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a != b
	}
	return *a != *b
}

type taskService struct {
	taskRepo          repository.TaskRepository
	statusRepo        repository.TaskStatusRepository
	depRepo           repository.TaskDependencyRepository
	activityRepo      repository.ActivityLogRepository
	customFieldSvc    CustomFieldService
	projectRepo       repository.ProjectRepository
	projectMemberRepo repository.ProjectMemberRepository
	agentRepo         repository.AgentRepository
	userRepo          repository.UserRepository
	commentRepo       repository.CommentRepository
	vcsLinkRepo       repository.VCSLinkRepository
	githubPRChecker   githubapi.PullRequestChecker  // legacy static client; used only when vcsIntegrations is nil
	gitlabMRChecker   gitlabapi.MergeRequestChecker // legacy static client; used only when vcsIntegrations is nil
	vcsIntegrations   *VCSIntegrationResolver       // resolves a per-workspace client on every call (§C1) — see WithVCSIntegrationResolver
	autoTransSvc      AutoTransitionService
	ruleSvc           RuleService
	rulesConfigSvc    RulesService
	eventBusSvc       EventBusService
	webhookSvc        WebhookService
	agentNotifySvc    AgentNotifyService
	notifySvc         NotificationService
	ctxCacheInv       ContextCacheInvalidator
	wsMembership      WorkspaceMembershipReader
	listRevisionRepo  repository.TaskListRevisionRepository
}

// NewTaskService returns a new TaskService backed by the given repositories.
// The optional customFieldSvc enables custom field value validation on create/update.
func NewTaskService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	depRepo repository.TaskDependencyRepository,
	activityRepo repository.ActivityLogRepository,
	opts ...TaskServiceOption,
) TaskService {
	s := &taskService{
		taskRepo:     taskRepo,
		statusRepo:   statusRepo,
		depRepo:      depRepo,
		activityRepo: activityRepo,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// TaskServiceOption configures optional dependencies for TaskService.
type TaskServiceOption func(*taskService)

// WithCustomFieldService sets the custom field service for value validation.
func WithCustomFieldService(cfs CustomFieldService) TaskServiceOption {
	return func(s *taskService) {
		s.customFieldSvc = cfs
	}
}

// WithProjectRepo sets the project repository used to resolve workspace_id for activity logging.
func WithProjectRepo(pr repository.ProjectRepository) TaskServiceOption {
	return func(s *taskService) {
		s.projectRepo = pr
	}
}

// WithAutoTransitionService sets the auto-transition service that fires after status changes.
func WithAutoTransitionService(ats AutoTransitionService) TaskServiceOption {
	return func(s *taskService) {
		s.autoTransSvc = ats
	}
}

// WithRuleService sets the optional rule service for governance rule evaluation on task operations.
func WithRuleService(rs RuleService) TaskServiceOption {
	return func(s *taskService) {
		s.ruleSvc = rs
	}
}

// WithEventBusService sets the optional event bus service.
// When set, task mutations (create/update/move/delete) automatically publish events.
func WithEventBusService(ebs EventBusService) TaskServiceOption {
	return func(s *taskService) {
		s.eventBusSvc = ebs
	}
}

// WithWebhookService sets the optional webhook service.
// When set, task lifecycle events (created, assigned, status changed) dispatch outbound webhooks.
func WithWebhookService(ws WebhookService) TaskServiceOption {
	return func(s *taskService) {
		s.webhookSvc = ws
	}
}

// WithAgentNotifyService sets the optional agent notification service.
// When set, task assignments fire push notifications to the assigned agent via
// callback URL and Redis pub/sub (for SSE and long-poll consumers).
func WithAgentNotifyService(ans AgentNotifyService) TaskServiceOption {
	return func(s *taskService) {
		s.agentNotifySvc = ans
	}
}

// WithRulesConfigService sets the optional rules configuration service.
// When set, task creation will apply auto-assign rules from project/workspace config.
func WithRulesConfigService(rcs RulesService) TaskServiceOption {
	return func(s *taskService) {
		s.rulesConfigSvc = rcs
	}
}

// WithContextCacheInvalidator sets an optional cache invalidator that is called
// after every task mutation (create, update, move, delete) so that the
// GET /tasks/:task_id/context cache stays consistent.
func WithContextCacheInvalidator(inv ContextCacheInvalidator) TaskServiceOption {
	return func(s *taskService) {
		s.ctxCacheInv = inv
	}
}

// WithNotificationService sets the optional notification service.
// When set, task lifecycle events dispatch in-app notifications to subscribed users.
func WithNotificationService(ns NotificationService) TaskServiceOption {
	return func(s *taskService) {
		s.notifySvc = ns
	}
}

// WithProjectMemberRepoTask sets the project member repo for agent auto-enrollment.
func WithProjectMemberRepoTask(pmr repository.ProjectMemberRepository) TaskServiceOption {
	return func(s *taskService) {
		s.projectMemberRepo = pmr
	}
}

// WithTaskListRevisionRepo sets the repository used to read the per-project
// task_list_revision counter (ADR-0004). When unset, List behaves exactly as
// it did before this option existed: no staleness check, no ListRevision
// stamped on the returned page — callers who never send list_revision are
// completely unaffected either way.
func WithTaskListRevisionRepo(r repository.TaskListRevisionRepository) TaskServiceOption {
	return func(s *taskService) {
		s.listRevisionRepo = r
	}
}

// WithTaskAgentRepo sets the agent repository used to resolve holder names
// when building CheckoutConflictError responses. When unset, the 409 response
// still includes the UUID — only the human-readable name is omitted.
func WithTaskAgentRepo(ar repository.AgentRepository) TaskServiceOption {
	return func(s *taskService) {
		s.agentRepo = ar
	}
}

// WithUserRepoTask sets the user repository used for assignee_type normalization.
// When set, assigning a human UUID auto-corrects assignee_type to 'user'.
func WithUserRepoTask(ur repository.UserRepository) TaskServiceOption {
	return func(s *taskService) {
		s.userRepo = ur
	}
}

// WithCommentRepoTask enables the review-evidence gate in MoveTask.
// When set, moving a task to review status requires at least one of: artifact,
// VCS link, or comment. Skipped (fails open) if not wired.
func WithCommentRepoTask(cr repository.CommentRepository) TaskServiceOption {
	return func(s *taskService) { s.commentRepo = cr }
}

// WithVCSLinkRepoTask enables the done-evidence gate in MoveTask.
// When set, moving a task to done is blocked if a linked PR is not yet merged.
// Skipped (fails open) if not wired.
func WithVCSLinkRepoTask(vr repository.VCSLinkRepository) TaskServiceOption {
	return func(s *taskService) { s.vcsLinkRepo = vr }
}

// WithGitHubPRChecker enables a LIVE GitHub check in the done-evidence gate:
// when a linked PR's cached status says "not merged", the gate asks GitHub
// directly before blocking, instead of trusting a status that is only ever
// as fresh as the last webhook delivery (#5f7f8c6e — a PR merged 9.5h
// earlier still blocked move_task→done because the webhook that would have
// updated the cached record never fired for this particular link).
// Optional: if unset, the gate falls back to the cached status exactly as
// before (fails open on the live check, not on the gate itself).
func WithGitHubPRChecker(c githubapi.PullRequestChecker) TaskServiceOption {
	return func(s *taskService) { s.githubPRChecker = c }
}

// WithGitLabMRChecker enables a LIVE GitLab check in the done-evidence gate,
// mirroring WithGitHubPRChecker for a linked merge request instead of a pull
// request (#bc39d781). Optional: if unset, the gate falls back to the
// cached status exactly as before.
func WithGitLabMRChecker(c gitlabapi.MergeRequestChecker) TaskServiceOption {
	return func(s *taskService) { s.gitlabMRChecker = c }
}

// WithVCSIntegrationResolver enables PER-WORKSPACE GitHub/GitLab checkers
// (#33a4bb57, §C1 of specsintegration-provider-contract): instead of the one
// process-wide client WithGitHubPRChecker/WithGitLabMRChecker wire at
// construction, the done-evidence gate resolves a fresh client for the
// link's own workspace on every call, honoring that workspace's active
// integration row (or the env fallback, or "disabled" — see
// VCSIntegrationResolver's doc comment). Takes priority over
// WithGitHubPRChecker/WithGitLabMRChecker when both are set; they remain
// the fallback path for callers (mainly tests) that construct a taskService
// without a repository.IntegrationRepository to resolve against.
func WithVCSIntegrationResolver(r *VCSIntegrationResolver) TaskServiceOption {
	return func(s *taskService) { s.vcsIntegrations = r }
}

// SetAutoTransitionService implements TaskServiceAutoTransitionConfigurable,
// allowing the auto-transition service to be wired after construction.
func (s *taskService) SetAutoTransitionService(svc AutoTransitionService) {
	s.autoTransSvc = svc
}

// Create validates and persists a new task.
func (s *taskService) Create(ctx context.Context, task *domain.Task) error {
	if strings.TrimSpace(task.Title) == "" {
		return apierror.ValidationError(map[string]string{
			"title": "title is required",
		})
	}

	// Validate custom field values if a custom field service is available.
	if s.customFieldSvc != nil && len(task.CustomFields) > 0 && string(task.CustomFields) != "{}" && string(task.CustomFields) != "null" {
		var vals map[string]interface{}
		if err := json.Unmarshal(task.CustomFields, &vals); err == nil && len(vals) > 0 {
			if err := s.customFieldSvc.ValidateValues(ctx, task.ProjectID, vals, true); err != nil {
				return err
			}
		}
	}

	// Apply auto-assign rules only when no explicit assignee was given.
	// Guarding on AssigneeID ensures a caller who passes assignee_id without
	// assignee_type is never silently rerouted to the auto-assign default.
	if (task.AssigneeType == domain.AssigneeTypeUnassigned || task.AssigneeType == "") && task.AssigneeID == nil {
		s.applyAutoAssign(ctx, task)
	}
	// Normalize: look up assignee in agent/user directory to correct assignee_type.
	if task.AssigneeID != nil {
		task.AssigneeType = s.resolveAssigneeType(ctx, task.AssigneeID, task.AssigneeType)
	}
	// Normalize: look up reviewer in agent/user directory to correct reviewer_type.
	// Mirrors the assignee normalization above and Update's reviewer handling (:358-365).
	if task.ReviewerID != nil {
		fallback := domain.AssigneeTypeUser
		if task.ReviewerType != nil {
			fallback = *task.ReviewerType
		}
		resolved := s.resolveAssigneeType(ctx, task.ReviewerID, fallback)
		task.ReviewerType = &resolved
	}

	if task.ID == uuid.Nil {
		task.ID = uuid.New()
	}

	now := timeNow()
	task.CreatedAt = now
	task.UpdatedAt = now

	// Auto-enroll creator agent into project members.
	actorID, actorType := actorctx.FromContext(ctx)
	if actorType == domain.ActorTypeAgent && actorID != uuid.Nil {
		s.ensureAgentProjectMember(ctx, task.ProjectID, actorID)
	}
	// Auto-enroll assignee into project members — and refuse a foreign one. This
	// runs before taskRepo.Create, so a refused assignment creates no task at all
	// rather than a task the caller then has to notice is unassigned.
	if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, task.AssigneeID, task.AssigneeType); err != nil {
		return err
	}
	// Auto-enroll reviewer into project members — same contract as the assignee
	// path above (see Update's reviewer comment at :339-357 for why this matters:
	// a reviewer left off project_members follows a notification into a task
	// whose status transitions all 403). Reviewer-at-creation landed on main
	// while this branch was open: it is a write path that names a principal by
	// id, so it carries the same refusal, and the error is propagated for the
	// same reason Update's is — a discarded error here is exactly how the
	// reviewer path stayed unguarded the first time.
	if task.ReviewerID != nil && task.ReviewerType != nil {
		if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, task.ReviewerID, *task.ReviewerType); err != nil {
			return err
		}
	}

	if err := s.taskRepo.Create(ctx, task); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
	s.logActivity(ctx, task.ProjectID, task.ID, "task.created", map[string]interface{}{
		"title":    map[string]interface{}{"old": nil, "new": task.Title},
		"priority": map[string]interface{}{"old": nil, "new": string(task.Priority)},
	})

	// Dispatch webhook for task.created (agent wakeup pipeline).
	if s.webhookSvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			go s.webhookSvc.Dispatch(ctx, proj.WorkspaceID, "task.created", map[string]interface{}{
				"task_id":     task.ID,
				"project_id":  task.ProjectID,
				"title":       task.Title,
				"priority":    string(task.Priority),
				"assignee_id": task.AssigneeID,
				"status_id":   task.StatusID,
			})
		}
	}

	// Notify assigned agent via push mechanisms (callback_url, SSE, long-poll).
	s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
		"assignee_id": map[string]any{"old": nil, "new": task.AssigneeID},
	})

	// Notify the assignee, and only the assignee — see notifyAssignee for why a
	// user-typed assignee gets this and an agent-typed one doesn't.
	s.notifyAssignee(ctx, task, "Task assigned: "+task.Title)

	// Tell the reviewer, and only the reviewer — notifyReviewer no-ops when no
	// reviewer was set, so this stays silent for the (still) common no-reviewer
	// case rather than repeating the task.assigned-always-fires bug this task
	// exists to not reintroduce (see the task description's own warning).
	s.notifyReviewer(ctx, task, "task.reviewer_assigned", "Review requested: "+task.Title)

	return nil
}

// GetDefaultStatus returns the default task status for a project.
func (s *taskService) GetDefaultStatus(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error) {
	return s.statusRepo.GetDefaultForProject(ctx, projectID)
}

// GetStatusByID retrieves a task status by its ID.
func (s *taskService) GetStatusByID(ctx context.Context, id uuid.UUID) (*domain.TaskStatus, error) {
	return s.statusRepo.GetByID(ctx, id)
}

// GetByID retrieves a task by its ID.
func (s *taskService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	task, err := s.taskRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, apierror.NotFound("Task")
	}
	return task, nil
}

// Update validates that the task exists and persists changes.
func (s *taskService) Update(ctx context.Context, task *domain.Task) error {
	existing, err := s.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Task")
	}

	// Validate custom field values if a custom field service is available.
	if s.customFieldSvc != nil && len(task.CustomFields) > 0 && string(task.CustomFields) != "{}" && string(task.CustomFields) != "null" {
		var vals map[string]interface{}
		if err := json.Unmarshal(task.CustomFields, &vals); err == nil && len(vals) > 0 {
			if err := s.customFieldSvc.ValidateValues(ctx, task.ProjectID, vals, false); err != nil {
				return err
			}
		}
	}

	// Normalize assignee_type before write.
	if task.AssigneeID != nil {
		task.AssigneeType = s.resolveAssigneeType(ctx, task.AssigneeID, task.AssigneeType)
	}
	// Auto-enroll the assignee — PATCH can change assignee_id just like assign_task.
	if existing.AssigneeID == nil || task.AssigneeID == nil || *existing.AssigneeID != *task.AssigneeID {
		if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, task.AssigneeID, task.AssigneeType); err != nil {
			return err
		}
	}

	// Reviewer is the eighth write path that puts a principal on a task, and it needs
	// the same two guarantees as the assignee ones — see ensureAssigneeProjectMember's
	// contract. reviewer_type arrives caller-supplied from updateTaskRequest (and may be
	// nil), so it is resolved against the directories rather than trusted: enrolment
	// dispatches on the type, and a user UUID typed "agent" is rejected by the
	// project_members.agent_id foreign key and then swallowed, leaving no membership row.
	// Without enrolment the reviewer lands in exactly the dead end the assignee paths
	// were fixed to prevent — worse here, because being made reviewer *sends them a
	// notification*, so they follow it to a task whose status transitions all 403.
	//
	// The workspace gap this comment used to record as KNOWN and deliberately unfixed
	// ("it needs one change covering all eight paths, and a user-side check needs a
	// workspace-membership repo this service does not yet hold") is now closed, by
	// exactly that one change: ensureAssigneeProjectMember calls
	// assertAssigneeInProjectWorkspace first, and the membership repo it was waiting
	// for is WorkspaceMembershipReader. Reviewer stays consistent with assignee, which
	// now means checked rather than unchecked.
	//
	// Propagating the error is the whole point — this call already existed and already
	// went through the funnel, but dropped what the funnel returned, so a refusal here
	// would have been logged into the void and the reviewer written anyway.
	if uuidPtrChanged(existing.ReviewerID, task.ReviewerID) && task.ReviewerID != nil {
		fallback := domain.AssigneeTypeUser
		if task.ReviewerType != nil {
			fallback = *task.ReviewerType
		}
		resolved := s.resolveAssigneeType(ctx, task.ReviewerID, fallback)
		task.ReviewerType = &resolved
		if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, task.ReviewerID, resolved); err != nil {
			return err
		}
	}

	task.UpdatedAt = timeNow()
	if err := s.taskRepo.Update(ctx, task); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}

	// Build diff between existing and updated task.
	changes := map[string]interface{}{}
	if existing.Title != task.Title {
		changes["title"] = map[string]interface{}{"old": existing.Title, "new": task.Title}
	}
	if existing.Description != task.Description {
		changes["description"] = map[string]interface{}{"old": existing.Description, "new": task.Description}
	}
	if existing.Priority != task.Priority {
		changes["priority"] = map[string]interface{}{"old": string(existing.Priority), "new": string(task.Priority)}
	}
	// existing and task come from two separate GetByID calls, so their
	// AssigneeID pointers never compare equal even when unchanged — compare
	// by value. Shares uuidPtrChanged with the reviewer diff above rather than
	// carrying a second, inverted copy of the same three lines.
	assigneeChanged := uuidPtrChanged(existing.AssigneeID, task.AssigneeID)
	if assigneeChanged {
		changes["assignee_id"] = map[string]interface{}{"old": existing.AssigneeID, "new": task.AssigneeID}
	}
	if existing.AssigneeType != task.AssigneeType {
		changes["assignee_type"] = map[string]interface{}{"old": string(existing.AssigneeType), "new": string(task.AssigneeType)}
	}
	if existing.DueDate != task.DueDate {
		changes["due_date"] = map[string]interface{}{"old": existing.DueDate, "new": task.DueDate}
	}
	if existing.EstimatedHours != task.EstimatedHours {
		changes["estimated_hours"] = map[string]interface{}{"old": existing.EstimatedHours, "new": task.EstimatedHours}
	}
	delegationLevelChanged := existing.DelegationLevel != task.DelegationLevel
	if delegationLevelChanged {
		changes["delegation_level"] = map[string]interface{}{
			"old": string(existing.DelegationLevel),
			"new": string(task.DelegationLevel),
		}
	}
	reviewerChanged := uuidPtrChanged(existing.ReviewerID, task.ReviewerID)
	if reviewerChanged {
		changes["reviewer_id"] = map[string]interface{}{"old": existing.ReviewerID, "new": task.ReviewerID}
		changes["reviewer_type"] = map[string]interface{}{"old": existing.ReviewerType, "new": task.ReviewerType}
	}
	s.logActivity(ctx, task.ProjectID, task.ID, "task.updated", changes)

	// Dispatch webhook for task.assigned when the assignee changes (agent wakeup pipeline).
	if assigneeChanged && s.webhookSvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			go s.webhookSvc.Dispatch(ctx, proj.WorkspaceID, "task.assigned", map[string]interface{}{
				"task_id":       task.ID,
				"project_id":    task.ProjectID,
				"assignee_id":   task.AssigneeID,
				"assignee_type": string(task.AssigneeType),
			})
		}
	}

	// Notify newly assigned agent via push mechanisms (callback_url, SSE, long-poll).
	if assigneeChanged {
		s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
			"assignee_id": map[string]any{"old": existing.AssigneeID, "new": task.AssigneeID},
		})

		// Notify the assignee, and only the assignee — Create and AssignTask
		// already do this, Update never did.
		s.notifyAssignee(ctx, task, "Task assigned: "+task.Title)
	}

	// Notify assignee agent when delegation_level changes (e.g. task becomes supervised).
	if delegationLevelChanged {
		s.notifyAssignedAgent(ctx, task, "task.delegation_changed", map[string]any{
			"delegation_level": map[string]any{
				"old": string(existing.DelegationLevel),
				"new": string(task.DelegationLevel),
			},
		})
	}

	// Tell the newly set reviewer, and only the reviewer — this event is about one
	// specific person, not workspace-wide news.
	if reviewerChanged && task.ReviewerID != nil {
		s.notifyReviewer(ctx, task, "task.reviewer_assigned", "Review requested: "+task.Title)
	}

	return nil
}

// Delete removes a task after verifying it exists.
func (s *taskService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.taskRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Task")
	}

	parentID := existing.ParentTaskID

	if err := s.taskRepo.Delete(ctx, id); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, id)
	}
	s.logActivity(ctx, existing.ProjectID, id, "task.deleted", nil)

	// After deleting a subtask, re-check whether the parent's remaining
	// subtasks are all complete and an auto-transition should fire.
	if parentID != nil && s.autoTransSvc != nil {
		if atErr := s.autoTransSvc.CheckSubtaskCompletion(ctx, *parentID); atErr != nil {
			log.Printf("[auto-transition] WARNING: CheckSubtaskCompletion after delete for parent %s failed: %v", *parentID, atErr)
		}
	}

	return nil
}

// List returns a paginated list of tasks for the given project.
func (s *taskService) GetByShortID(ctx context.Context, prefix string) (*domain.Task, error) {
	return s.taskRepo.GetByShortID(ctx, prefix)
}

func (s *taskService) List(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	// Revision validation (ADR-0004): only runs when the repo is wired AND the
	// caller sent a nonzero list_revision (i.e. this isn't page 1 of a fresh
	// walk). No repo wired means no wiring done yet, not "not applicable" —
	// but that's a deploy-ordering concern, not something List can fix, so it
	// degrades to "no check" rather than erroring every call.
	var currentRevision int64
	if s.listRevisionRepo != nil {
		var err error
		currentRevision, err = s.listRevisionRepo.GetRevision(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if pg.ListRevision != 0 && pg.ListRevision != currentRevision {
			return nil, &ListRevisionStaleError{
				Requested: pg.ListRevision,
				Current:   currentRevision,
			}
		}
	}

	page, err := s.taskRepo.List(ctx, projectID, filter, pg)
	if err != nil {
		return nil, err
	}
	if s.listRevisionRepo != nil {
		page.ListRevision = currentRevision
	}
	return page, nil
}

func (s *taskService) Search(ctx context.Context, workspaceID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()
	return s.taskRepo.Search(ctx, workspaceID, filter, pg)
}

// MoveTask transitions a task to a new status and/or position.
// It validates that the status exists and belongs to the same project as the task.
// If the target status category is "done", it sets CompletedAt.
func (s *taskService) MoveTask(ctx context.Context, taskID uuid.UUID, input MoveTaskInput) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	// CAS precondition: fail fast if the caller's snapshot is stale.
	if input.ExpectedStatusID != nil && *input.ExpectedStatusID != task.StatusID {
		return &CASConflictError{
			CurrentStatusID:  task.StatusID,
			CurrentUpdatedAt: task.UpdatedAt,
		}
	}
	if input.ExpectedUpdatedAt != nil && !input.ExpectedUpdatedAt.Equal(task.UpdatedAt) {
		return &CASConflictError{
			CurrentStatusID:  task.StatusID,
			CurrentUpdatedAt: task.UpdatedAt,
		}
	}

	// Tenancy of an explicit assignee is decided BEFORE anything is applied.
	//
	// The move and the assignment are one request but two writes, and the
	// assignment write happens well after the status change has been persisted and
	// logged. Checking it at the point of use would leave a refused assignment
	// sitting on top of an already-committed move — half the request applied, with
	// the failure reported as if the whole of it had failed. Refusing here costs
	// one directory read and keeps the operation atomic from the caller's side.
	if input.AssigneeID != nil {
		assigneeType := s.resolveAssigneeType(ctx, input.AssigneeID, input.AssigneeType)
		if err := s.assertAssigneeInProjectWorkspace(ctx, task.ProjectID, input.AssigneeID, assigneeType); err != nil {
			return err
		}
	}

	oldStatusID := task.StatusID
	oldPosition := task.Position
	oldStatusChangedAt := task.StatusChangedAt

	statusChanged := false
	if input.StatusID != nil && *input.StatusID != oldStatusID {
		status, err := s.statusRepo.GetByID(ctx, *input.StatusID)
		if err != nil {
			return err
		}
		if status == nil {
			return apierror.NotFound("TaskStatus")
		}
		if status.ProjectID != task.ProjectID {
			return apierror.BadRequest("status does not belong to the same project as the task")
		}

		// Supervised signoff gate: only human actors (user) may move supervised tasks
		// to terminal categories. Agents and system actors are blocked unconditionally.
		if task.DelegationLevel == domain.DelegationLevelSupervised {
			_, actorType := actorctx.FromContext(ctx)
			if actorType != domain.ActorTypeUser {
				switch status.Category {
				case domain.StatusCategoryReview, domain.StatusCategoryDone, domain.StatusCategoryCancelled:
					return apierror.ForbiddenWithDetails(
						"supervised_requires_human_signoff",
						"supervised tasks must be signed off by a human: only users may move to review/done/cancelled",
					)
				}
			}
		}

		// Human-gate freeze: when a task is awaiting human sign-off, only a user
		// may move it to backlog/done/cancelled. Prevents SupersedeRecurringInstances,
		// auto_transition, and other system actors from silently closing gated tasks.
		if task.HumanGate {
			_, actorType := actorctx.FromContext(ctx)
			if actorType != domain.ActorTypeUser {
				switch status.Category {
				case domain.StatusCategoryBacklog, domain.StatusCategoryDone, domain.StatusCategoryCancelled:
					return &HumanGateFrozenError{}
				}
			}
		}

		// Shipped flag: a terminally shipped task may not be moved to any non-done category.
		// Clearing the flag (ShipTask with shipped=false) is the only escape hatch.
		if task.IsShipped {
			if status.Category != domain.StatusCategoryDone {
				return &TaskShippedError{}
			}
		}

		// Workflow-rules transition gate: advisory or strict enforcement from ProjectRuleConfig.
		if enfMode, vMsg := s.applyTransitionGate(ctx, task, oldStatusID, status); vMsg != "" {
			if enfMode == domain.RuleConfigEnforcementStrict {
				return apierror.ForbiddenWithDetails("workflow_transition_blocked", vMsg)
			}
			// Advisory: permit the move but record the violation in the activity log.
			s.logActivity(ctx, task.ProjectID, taskID, "task.transition_violation", map[string]interface{}{
				"violation":        vMsg,
				"from_status_id":   oldStatusID.String(),
				"to_status_id":     status.ID.String(),
				"enforcement_mode": "advisory",
			})
		}

		// Review-evidence gate: block evidence-less moves to review.
		// Requires at least one of: artifact, VCS link, or comment.
		// Gate is skipped when commentRepo is not wired (e.g. tests without it).
		// System actors (auto_transition) are exempt, consistent with the done-evidence gate.
		if status.Category == domain.StatusCategoryReview && s.commentRepo != nil {
			_, actorType := actorctx.FromContext(ctx)
			if actorType != domain.ActorTypeSystem {
				if task.ArtifactCount == 0 && task.VCSLinkCount == 0 {
					if hasComment, gateErr := s.commentRepo.HasAnyComment(ctx, taskID); gateErr == nil && !hasComment {
						return &ReviewEvidenceError{}
					}
				}
			}
		}

		// Done-evidence gate: block moves to done when a linked PR is not yet merged,
		// or when the task has no evidence at all (no VCS links, no artifact, no comment).
		// System actors (auto_transition) are exempt; gate fails open if vcsLinkRepo is not wired.
		if status.Category == domain.StatusCategoryDone && s.vcsLinkRepo != nil {
			_, actorType := actorctx.FromContext(ctx)
			if actorType != domain.ActorTypeSystem {
				if task.VCSLinkCount > 0 {
					links, linksErr := s.vcsLinkRepo.ListByTask(ctx, taskID)
					if linksErr == nil {
						// Resolved once per gate check, not per link: every
						// link on this task belongs to the same task, hence
						// the same workspace.
						workspaceID, _ := s.resolveProjectWorkspace(ctx, task.ProjectID)
						for _, l := range links {
							if l.LinkType != domain.VCSLinkTypePR || l.Status == domain.VCSLinkStatusMerged || l.Status == domain.VCSLinkStatusClosed {
								continue
							}
							// Cached status says not-yet-merged (or unknown).
							// Before blocking, ask the provider (GitHub or
							// GitLab) directly — the cache is only ever as
							// fresh as the last webhook delivery, and a
							// delivery can be missed, delayed, or (the
							// reported incident) simply never arrive for a
							// link created after the PR/MR was already
							// merged.
							if merged, live := s.isPRMergedLive(ctx, l, workspaceID); live {
								if merged {
									// Self-heal the cache so the next read —
									// and the next agent hitting this gate —
									// doesn't pay for another live round trip
									// or hit the same stale block.
									s.healVCSLinkStatus(ctx, l)
									continue
								}
								return &DoneEvidenceError{PRURL: l.URL, PRTitle: l.Title, PRStatus: string(l.Status), PRProvider: string(l.Provider), PRStatusCheckedLive: true}
							}
							return &DoneEvidenceError{
								PRURL: l.URL, PRTitle: l.Title, PRStatus: string(l.Status), PRProvider: string(l.Provider),
								PRLinkedAt:                 l.CreatedAt,
								PRProviderAccessConfigured: s.hasLiveChecker(ctx, l.Provider, workspaceID),
							}
						}
					}
				} else if s.commentRepo != nil {
					// No VCS links: require artifact or at least one comment.
					if task.ArtifactCount == 0 {
						if hasComment, gateErr := s.commentRepo.HasAnyComment(ctx, taskID); gateErr == nil && !hasComment {
							return &DoneEvidenceError{}
						}
					}
				}
			}
		}

		// DoD-gate check: block move to done when the project has required gates that have not passed.
		// System actors are exempt to allow auto-close of e.g. cancelled recurring instances.
		if status.Category == domain.StatusCategoryDone && s.projectRepo != nil {
			_, actorType := actorctx.FromContext(ctx)
			if actorType != domain.ActorTypeSystem {
				if proj, projErr := s.projectRepo.GetByID(ctx, task.ProjectID); projErr == nil && proj != nil {
					if blocking := dodBlockingGates(proj.GetSettings(), task.DodChecks); len(blocking) > 0 {
						pkgmetrics.RecordDoDGate(proj.Slug, "dod_required_gates", "fail")
						return &DodGateBlockedError{BlockingGates: blocking}
					}
					pkgmetrics.RecordDoDGate(proj.Slug, "dod_required_gates", "pass")
				}
			}
		}

		// Evaluate governance rules before applying the move.
		if s.ruleSvc != nil {
			if violations, evalErr := s.evaluateRulesForMove(ctx, task, status, input); evalErr != nil {
				log.Printf("[rules] WARNING: rule evaluation failed for task %s: %v", taskID, evalErr)
			} else if len(violations) > 0 {
				// Find blocking violations.
				var blockingViolations []domain.RuleViolation
				for _, v := range violations {
					if v.Enforcement == domain.RuleEnforcementBlock {
						blockingViolations = append(blockingViolations, v)
					}
				}
				if len(blockingViolations) > 0 {
					return &RuleViolationError{Violations: blockingViolations}
				}
			}
		}

		task.StatusID = *input.StatusID
		statusChanged = true

		if status.Category == domain.StatusCategoryDone {
			now := timeNow()
			task.CompletedAt = &now
		} else {
			task.CompletedAt = nil
		}

		// Advance status_changed_at so the next transition can measure dwell time.
		scAt := timeNow()
		task.StatusChangedAt = &scAt
	}

	positionChanged := input.Position != nil && *input.Position != oldPosition
	if positionChanged {
		task.Position = *input.Position
	}

	// Nothing changed — return early without touching the DB.
	if !statusChanged && !positionChanged {
		return nil
	}

	task.UpdatedAt = timeNow()
	if err := s.taskRepo.Update(ctx, task); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, taskID)
	}

	// Auto-release checkout when the task transitions to a terminal category
	// (done, review, cancelled). The current holder is unlikely to call
	// release_task on a task they just handed off, and a stale lock blocks
	// other agents from picking it up after the move.
	if statusChanged && task.CheckedOutBy != nil {
		if newStatus, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && newStatus != nil {
			switch newStatus.Category {
			case domain.StatusCategoryDone, domain.StatusCategoryReview, domain.StatusCategoryCancelled:
				previousHolder := task.CheckedOutBy.String()
				if relErr := s.taskRepo.ForceReleaseCheckout(ctx, taskID); relErr != nil {
					log.Printf("[checkout-auto-release] WARNING: failed to release checkout on task %s: %v", taskID, relErr)
				} else {
					s.logActivity(ctx, task.ProjectID, taskID, "task.checkout_released_auto", map[string]interface{}{
						"reason":          "terminal_status_transition",
						"new_status":      newStatus.Name,
						"new_category":    string(newStatus.Category),
						"previous_holder": previousHolder,
					})
					task.CheckedOutBy = nil
					task.CheckoutToken = nil
					task.CheckoutExpires = nil
				}
			}
		}
	}

	moveChanges := map[string]interface{}{}
	if statusChanged {
		// Resolve status names for human-readable activity log.
		oldName := oldStatusID.String()
		newName := input.StatusID.String()
		if oldStatus, err := s.statusRepo.GetByID(ctx, oldStatusID); err == nil && oldStatus != nil {
			oldName = oldStatus.Name
		}
		if newStatus, err := s.statusRepo.GetByID(ctx, *input.StatusID); err == nil && newStatus != nil {
			newName = newStatus.Name
		}
		moveChanges["status"] = map[string]interface{}{"old": oldName, "new": newName}
		if input.Source != "" {
			moveChanges["source"] = input.Source
		}

		// Emit task-flow Prometheus metrics.
		var dur *time.Duration
		if oldStatusChangedAt != nil {
			d := timeNow().Sub(*oldStatusChangedAt)
			dur = &d
		}
		pkgmetrics.RecordTaskTransition(task.ProjectID.String(), oldName, newName, dur)
	}
	if positionChanged {
		moveChanges["position"] = map[string]interface{}{"old": oldPosition, "new": *input.Position}
	}
	if len(moveChanges) > 0 {
		s.logActivity(ctx, task.ProjectID, taskID, "task.moved", moveChanges)
	}

	// Fire auto-transition checks when the status changed.
	if statusChanged && s.autoTransSvc != nil {
		// Look up the new status category so EvaluateOnTaskMove can decide what to do.
		if newStatus, err := s.statusRepo.GetByID(ctx, *input.StatusID); err == nil && newStatus != nil {
			if atErr := s.autoTransSvc.EvaluateOnTaskMove(ctx, taskID, newStatus.Category); atErr != nil {
				log.Printf("[auto-transition] WARNING: EvaluateOnTaskMove for task %s failed: %v", taskID, atErr)
			}
		}
	}

	// Apply explicit assignee if provided in the move request.
	if input.AssigneeID != nil {
		task.AssigneeID = input.AssigneeID
		task.AssigneeType = s.resolveAssigneeType(ctx, input.AssigneeID, input.AssigneeType)
		// Already vetted at the top of MoveTask; this call is the enrolment, and
		// the error path is unreachable in practice. It is still propagated rather
		// than dropped, so that the guard cannot be silently defeated by a future
		// edit that removes the up-front check.
		if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, task.AssigneeID, task.AssigneeType); err != nil {
			return err
		}
		task.UpdatedAt = timeNow()
		if err := s.taskRepo.Update(ctx, task); err != nil {
			log.Printf("[move-assign] WARNING: failed to assign task %s: %v", taskID, err)
		}
		s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
			"assignee_id": map[string]any{"new": input.AssigneeID.String()},
		})
	}

	// Reviewer assignment when moved to "review" category, and the mirror-image restore
	// when bounced back out of review to todo/in_progress.
	// Consults OnTransition.SetReviewer from the project workflow config; if none is configured
	// the current assignee (the builder) is preserved — no creator bounce.
	// Both are skipped entirely when an explicit assignee_id is provided in the move request.
	if statusChanged && input.AssigneeID == nil {
		if newStatus, err := s.statusRepo.GetByID(ctx, *input.StatusID); err == nil && newStatus != nil {
			switch newStatus.Category {
			case domain.StatusCategoryReview:
				s.applyReviewAssignee(ctx, task, oldStatusID)
			case domain.StatusCategoryTodo, domain.StatusCategoryInProgress:
				s.restorePreReviewAssignee(ctx, task, oldStatusID)
			}
		}
	}

	// Dispatch webhook for task.status_changed (agent wakeup pipeline).
	if statusChanged && s.webhookSvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			go s.webhookSvc.Dispatch(ctx, proj.WorkspaceID, "task.status_changed", map[string]interface{}{
				"task_id":       task.ID,
				"project_id":    task.ProjectID,
				"old_status_id": oldStatusID,
				"new_status_id": *input.StatusID,
			})
		}
	}

	// Notify assigned agent about status change via push mechanisms (SSE, long-poll, callback).
	if statusChanged {
		s.notifyAssignedAgent(ctx, task, "task.status_changed", map[string]any{
			"status_id": map[string]any{"old": oldStatusID.String(), "new": input.StatusID.String()},
		})
		// Dispatch in-app notification to subscribed workspace users.
		s.dispatchUserNotification(ctx, task, "task.status_changed", "Task status changed: "+task.Title)
	}

	// Task carrying a reviewer just landed on a review-category status: tell the
	// reviewer specifically, so they don't have to watch the board to know it's
	// their turn.
	if statusChanged && task.ReviewerID != nil {
		if newStatus, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && newStatus != nil &&
			newStatus.Category == domain.StatusCategoryReview {
			s.notifyReviewer(ctx, task, "task.ready_for_review", "Ready for review: "+task.Title)
		}
	}

	return nil
}

// AssignTask assigns or unassigns a task.
func (s *taskService) AssignTask(ctx context.Context, taskID uuid.UUID, input AssignTaskInput) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	oldAssigneeID := task.AssigneeID
	oldAssigneeType := task.AssigneeType

	// Pin-assignee invariant: human assignments cannot be overridden by rule or system sources.
	source := input.Source
	if source == "" {
		source = domain.AssignmentSourceSystem
	}
	if task.AssignedBy == domain.AssignmentSourceHuman && source != domain.AssignmentSourceHuman {
		return &AssignmentPinnedError{}
	}

	// Normalize: look up assignee in agent/user directory to correct assignee_type.
	resolvedType := s.resolveAssigneeType(ctx, input.AssigneeID, input.AssigneeType)

	// Refuse a foreign principal BEFORE touching the task. The order matters
	// beyond tidiness: taskRepo hands out a pointer to the live task, so mutating
	// it first and refusing afterwards leaves the rejected assignee sitting on the
	// in-memory object for anything else holding that pointer.
	if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, input.AssigneeID, resolvedType); err != nil {
		return err
	}

	task.AssigneeID = input.AssigneeID
	task.AssigneeType = resolvedType
	task.AssignedBy = source
	task.UpdatedAt = timeNow()

	if err := s.taskRepo.Update(ctx, task); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, taskID)
	}
	s.logActivity(ctx, task.ProjectID, taskID, "task.assigned", map[string]interface{}{
		"assignee_id":   map[string]interface{}{"old": oldAssigneeID, "new": input.AssigneeID},
		"assignee_type": map[string]interface{}{"old": string(oldAssigneeType), "new": string(input.AssigneeType)},
	})

	// Notify newly assigned agent via push mechanisms (callback_url, SSE, long-poll).
	s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
		"assignee_id":   map[string]any{"old": oldAssigneeID, "new": input.AssigneeID},
		"assignee_type": map[string]any{"old": string(oldAssigneeType), "new": string(input.AssigneeType)},
	})

	// Notify the assignee, and only the assignee.
	s.notifyAssignee(ctx, task, "Task assigned: "+task.Title)

	return nil
}

// resolveSubtaskStatusID picks the status a new subtask is born into: the caller's
// explicit choice when given, otherwise the project's default status. It mirrors the
// resolution Create() performs for top-level tasks, including the guard against
// birthing work directly in a review status.
func (s *taskService) resolveSubtaskStatusID(ctx context.Context, projectID uuid.UUID, requested *uuid.UUID) (uuid.UUID, error) {
	if requested == nil {
		defaultStatus, err := s.statusRepo.GetDefaultForProject(ctx, projectID)
		if err != nil {
			return uuid.Nil, err
		}
		if defaultStatus == nil {
			return uuid.Nil, apierror.BadRequest("project has no default status; provide status_id")
		}
		return defaultStatus.ID, nil
	}

	status, err := s.statusRepo.GetByID(ctx, *requested)
	if err != nil {
		return uuid.Nil, err
	}
	if status == nil || status.ProjectID != projectID {
		return uuid.Nil, apierror.BadRequest("status_id does not belong to the parent task's project")
	}
	if status.Category == domain.StatusCategoryReview {
		return uuid.Nil, apierror.BadRequest(
			"Cannot create task in review status. Use 'todo' or 'in_progress'. Review is for tasks with completed work awaiting check.")
	}
	return status.ID, nil
}

// CreateSubtask creates a child task under the given parent.
func (s *taskService) CreateSubtask(ctx context.Context, parentTaskID uuid.UUID, input CreateSubtaskInput) (*domain.Task, error) {
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, apierror.NotFound("Task")
	}

	// Resolve the child's status. The parent's status must never leak into the
	// child: a subtask born under an in_progress/done parent is invisible to the
	// todo-only agent feed and silently never gets worked.
	statusID, err := s.resolveSubtaskStatusID(ctx, parent.ProjectID, input.StatusID)
	if err != nil {
		return nil, err
	}

	now := timeNow()
	assigneeType := input.AssigneeType
	if assigneeType == "" {
		assigneeType = domain.AssigneeTypeUnassigned
	}
	child := &domain.Task{
		ID:             uuid.New(),
		ProjectID:      parent.ProjectID,
		StatusID:       statusID,
		Title:          input.Title,
		Priority:       input.Priority,
		Description:    input.Description,
		ParentTaskID:   &parentTaskID,
		AssigneeID:     input.AssigneeID,
		AssigneeType:   assigneeType,
		Labels:         pq.StringArray(input.Labels),
		CustomFields:   input.CustomFields,
		DueDate:        input.DueDate,
		EstimatedHours: input.EstimatedHours,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	// Apply auto-assign rules if the subtask has no assignee.
	if child.AssigneeType == domain.AssigneeTypeUnassigned || child.AssigneeType == "" {
		s.applyAutoAssign(ctx, child)
	}

	// Set creator from context (required: created_by NOT NULL, created_by_type enum).
	creatorID, creatorType := actorctx.FromContext(ctx)
	child.CreatedBy = creatorID
	child.CreatedByType = creatorType
	if child.CreatedByType == "" {
		child.CreatedByType = domain.ActorTypeUser
	}

	// Auto-enroll creator agent into project members.
	if creatorType == domain.ActorTypeAgent && creatorID != uuid.Nil {
		s.ensureAgentProjectMember(ctx, parent.ProjectID, creatorID)
	}
	// Resolve the assignee's real type before enrolling. This is the only enrolment
	// site whose type is caller-supplied and therefore untrustworthy: the HTTP handler
	// defaults an omitted assignee_type to "agent", so a subtask assigned to a human
	// arrived here typed as an agent. ensureAssigneeProjectMember switches on that
	// type, so the enrolment went to ensureAgentProjectMember with a user UUID, the
	// project_members.agent_id foreign key rejected the insert, and the error was
	// logged and swallowed — no enrolment, no signal, and the assignee left holding a
	// task they could not transition. Resolving by directory lookup also stores the
	// correct type on the row instead of persisting the handler's guess.
	//
	// The other six enrolment sites do not need this: Create, Update, MoveTask and
	// AssignTask already resolve, applyReviewAssignee gets its type from
	// resolveSetReviewer's own agent lookup, and restorePreReviewAssignee restores a
	// type that was resolved when it was stashed.
	child.AssigneeType = s.resolveAssigneeType(ctx, child.AssigneeID, child.AssigneeType)

	// ...and the assignee, who is usually NOT the creator here: decomposing an epic
	// means creating subtasks owned by whoever will do the work. Enrolling only the
	// creator leaves that owner able to comment on a task they cannot transition.
	// Placed after applyAutoAssign so a rule-assigned subtask is covered too.
	if err := s.ensureAssigneeProjectMember(ctx, parent.ProjectID, child.AssigneeID, child.AssigneeType); err != nil {
		return nil, err
	}

	if err := s.taskRepo.Create(ctx, child); err != nil {
		return nil, err
	}
	s.logActivity(ctx, child.ProjectID, child.ID, "task.created", map[string]interface{}{
		"title":          map[string]interface{}{"old": nil, "new": child.Title},
		"parent_task_id": map[string]interface{}{"old": nil, "new": parentTaskID.String()},
	})

	// Same two-channel contract as Create: push-wake an agent assignee,
	// targeted in-app notify a user assignee. Both no-op on no assignee or
	// self-assignment — this path had neither, which is the bug this fixes.
	// CreateSubtaskInput carries no reviewer field, so there is no reviewer
	// notification to send here.
	s.notifyAssignedAgent(ctx, child, "task.assigned", map[string]any{
		"assignee_id": map[string]any{"old": nil, "new": child.AssigneeID},
	})
	s.notifyAssignee(ctx, child, "Task assigned: "+child.Title)

	return child, nil
}

// ListSubtasks returns all direct child tasks of the given parent.
func (s *taskService) ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	return s.taskRepo.ListSubtasks(ctx, parentTaskID)
}

// BulkUpdate applies a set of field changes to multiple tasks within a project.
// Each task is processed independently: failures are collected and returned without
// aborting the batch. Only tasks that belong to projectID are modified.
func (s *taskService) BulkUpdate(ctx context.Context, projectID uuid.UUID, input BulkUpdateTasksInput) BulkUpdateTasksResult {
	result := BulkUpdateTasksResult{}

	for _, taskID := range input.TaskIDs {
		if err := s.bulkUpdateOne(ctx, projectID, taskID, input); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("task %s: %v", taskID, err))
		} else {
			result.Updated++
		}
	}

	return result
}

// bulkUpdateOne applies updates to a single task, verifying project membership.
func (s *taskService) bulkUpdateOne(ctx context.Context, projectID, taskID uuid.UUID, input BulkUpdateTasksInput) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}
	if task.ProjectID != projectID {
		return apierror.BadRequest("task does not belong to the project")
	}

	// If status_id is provided, delegate to MoveTask which handles CompletedAt,
	// activity logging, and auto-transitions.
	if input.StatusID != nil {
		if err := s.MoveTask(ctx, taskID, MoveTaskInput{StatusID: input.StatusID}); err != nil {
			return err
		}
		// Re-fetch so the subsequent Update call works on the latest state.
		var fetchErr error
		task, fetchErr = s.taskRepo.GetByID(ctx, taskID)
		if fetchErr != nil {
			return fetchErr
		}
		if task == nil {
			return apierror.NotFound("Task")
		}
	}

	// Apply remaining scalar fields (priority, assignee, labels).
	changed := false
	if input.Priority != nil {
		task.Priority = *input.Priority
		changed = true
	}
	if input.AssigneeID != nil {
		task.AssigneeID = input.AssigneeID
		changed = true
	}
	if input.AssigneeType != nil {
		task.AssigneeType = *input.AssigneeType
		changed = true
	}
	if input.Labels != nil {
		task.Labels = *input.Labels
		changed = true
	}

	if changed {
		if err := s.Update(ctx, task); err != nil {
			return err
		}
	}

	return nil
}

// GetMyTasks returns the actor's tasks within workspaceID, narrowed in SQL by
// filter, along with the total number of matches before filter.Limit.
func (s *taskService) GetMyTasks(
	ctx context.Context,
	workspaceID, assigneeID uuid.UUID,
	assigneeType domain.AssigneeType,
	filter repository.AssigneeTaskFilter,
) ([]domain.Task, int, error) {
	return s.taskRepo.ListByAssignee(ctx, workspaceID, assigneeID, assigneeType, filter)
}

// GetUserActiveTasks returns non-done/cancelled tasks for a human user in a workspace.
func (s *taskService) GetUserActiveTasks(ctx context.Context, workspaceID, userID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	return s.taskRepo.ListByUserActive(ctx, workspaceID, userID, pg)
}

// applyAutoAssign applies assignment rules to a task if no assignee is set.
// It checks by_type (labels), by_priority, default_assignee, then fallback_chain in order.
// Failures are logged but never block task creation.
func (s *taskService) applyAutoAssign(ctx context.Context, task *domain.Task) {
	if s.rulesConfigSvc == nil {
		return
	}

	effective, err := s.rulesConfigSvc.GetEffectiveAssignmentRules(ctx, task.ProjectID)
	if err != nil {
		log.Printf("[auto-assign] WARNING: failed to get assignment rules for project %s: %v", task.ProjectID, err)
		return
	}

	// Collect candidate assignee IDs in priority order.
	// The first one that parses as a valid UUID wins.
	var candidates []string

	// 1. Check by_type — match task labels against type rules.
	if len(effective.ByType) > 0 && len(task.Labels) > 0 {
		for _, label := range task.Labels {
			if rule, ok := effective.ByType[label]; ok && rule.Value != "" {
				candidates = append(candidates, rule.Value)
				break // first matching label wins
			}
		}
	}

	// 2. Check by_priority[task.priority]
	if effective.ByPriority != nil {
		if rule, ok := effective.ByPriority[string(task.Priority)]; ok && rule.Value != "" {
			candidates = append(candidates, rule.Value)
		}
	}

	// 3. Fallback to default_assignee
	if effective.DefaultAssignee != nil && effective.DefaultAssignee.Value != "" {
		candidates = append(candidates, effective.DefaultAssignee.Value)
	}

	// 4. Fallback to first in fallback_chain
	if len(effective.FallbackChain) > 0 {
		candidates = append(candidates, effective.FallbackChain[0])
	}

	// Try each candidate until one parses successfully.
	for _, assigneeID := range candidates {
		assigneeType := domain.AssigneeTypeAgent
		rawID := assigneeID
		if strings.HasPrefix(assigneeID, "agent:") {
			rawID = strings.TrimPrefix(assigneeID, "agent:")
			assigneeType = domain.AssigneeTypeAgent
		} else if strings.HasPrefix(assigneeID, "user:") {
			rawID = strings.TrimPrefix(assigneeID, "user:")
			assigneeType = domain.AssigneeTypeUser
		}

		parsed, err := uuid.Parse(rawID)
		if err != nil {
			log.Printf("[auto-assign] WARNING: invalid assignee UUID %q in rules, trying next candidate: %v", assigneeID, err)
			continue
		}

		task.AssigneeID = &parsed
		task.AssigneeType = assigneeType
		log.Printf("[auto-assign] assigned task %q to %s %s via rules", task.Title, assigneeType, rawID)
		return
	}
}

// buildTaskSnapshot creates a map representation of a task for webhook payloads.
// Description is truncated to 500 characters per spec.
func (s *taskService) buildTaskSnapshot(ctx context.Context, task *domain.Task) map[string]any {
	desc := task.Description
	if len(desc) > 500 {
		desc = desc[:500]
	}

	snap := map[string]any{
		"id":            task.ID,
		"project_id":    task.ProjectID,
		"title":         task.Title,
		"priority":      string(task.Priority),
		"description":   desc,
		"assignee_id":   task.AssigneeID,
		"assignee_type": string(task.AssigneeType),
		"labels":        task.Labels,
	}

	// Resolve status info.
	if status, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && status != nil {
		snap["status"] = map[string]any{
			"id":       status.ID,
			"name":     status.Name,
			"category": string(status.Category),
		}
	}

	// Include assignee_name if available from enriched query.
	if task.AssigneeName != nil {
		snap["assignee_name"] = *task.AssigneeName
	}

	// Include recurring context when the task is part of a recurring series.
	if task.RecurringScheduleID != nil {
		snap["recurring_schedule_id"] = task.RecurringScheduleID
		snap["recurring_instance_number"] = task.RecurringInstanceNumber
		snap["recurring_context"] = map[string]any{
			"instance_number": task.RecurringInstanceNumber,
			"history_url":     fmt.Sprintf("/api/v1/recurring/%s/history", task.RecurringScheduleID),
		}
	}

	return snap
}

// notifyAssignedAgent sends a push notification to the assigned agent if it's an agent type.
func (s *taskService) notifyAssignedAgent(ctx context.Context, task *domain.Task, eventType string, changes map[string]any) {
	if s.agentNotifySvc == nil || task.AssigneeType != domain.AssigneeTypeAgent || task.AssigneeID == nil {
		return
	}

	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}

	// Extract actor info from request context (set by auth middleware).
	actorID, actorType := actorctx.FromContext(ctx)
	actorName := actorctx.NameFromContext(ctx)

	s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
		EventType:   eventType,
		Timestamp:   timeNow(),
		WorkspaceID: wsID,
		Task:        s.buildTaskSnapshot(ctx, task),
		AgentID:     *task.AssigneeID,
		ActorID:     actorID,
		ActorType:   string(actorType),
		ActorName:   actorName,
		Changes:     changes,
		TaskID:      task.ID,
		ProjectID:   task.ProjectID,
	})
}

// notifyReviewerAgent sends a push notification to the reviewer if it's an agent type.
// Mirrors notifyAssignedAgent but targets ReviewerID/ReviewerType instead of the assignee.
func (s *taskService) notifyReviewerAgent(ctx context.Context, task *domain.Task, eventType string, changes map[string]any) {
	if s.agentNotifySvc == nil || task.ReviewerType == nil || *task.ReviewerType != domain.AssigneeTypeAgent || task.ReviewerID == nil {
		return
	}

	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}

	actorID, actorType := actorctx.FromContext(ctx)
	actorName := actorctx.NameFromContext(ctx)

	s.agentNotifySvc.NotifyAgent(ctx, *task.ReviewerID, AgentNotification{
		EventType:   eventType,
		Timestamp:   timeNow(),
		WorkspaceID: wsID,
		Task:        s.buildTaskSnapshot(ctx, task),
		AgentID:     *task.ReviewerID,
		ActorID:     actorID,
		ActorType:   string(actorType),
		ActorName:   actorName,
		Changes:     changes,
		TaskID:      task.ID,
		ProjectID:   task.ProjectID,
	})
}

// dispatchTargetedUserNotification is dispatchUserNotification restricted to one
// specific user, for events that are inherently about that one person (e.g. "you
// were made reviewer") rather than workspace-wide news. Broadcasting those via the
// plain dispatchUserNotification would tell every subscribed workspace member
// about someone else's reviewer assignment.
func (s *taskService) dispatchTargetedUserNotification(ctx context.Context, task *domain.Task, eventType, title, body string, targetUserID uuid.UUID, extraMeta map[string]any) {
	if s.notifySvc == nil || s.projectRepo == nil {
		return
	}
	var wsID uuid.UUID
	if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
		wsID = proj.WorkspaceID
	}
	if wsID == uuid.Nil {
		return
	}
	taskIDCopy := task.ID
	projIDCopy := task.ProjectID
	targetCopy := targetUserID
	meta := map[string]any{
		"task_id":    task.ID,
		"task_title": task.Title,
		"project_id": task.ProjectID,
	}
	for k, v := range extraMeta {
		meta[k] = v
	}
	s.notifySvc.Notify(ctx, domain.NotificationEvent{
		WorkspaceID:  wsID,
		TaskID:       &taskIDCopy,
		ProjectID:    &projIDCopy,
		EventType:    eventType,
		Title:        title,
		Body:         body,
		TargetUserID: &targetCopy,
		Labels:       []string(task.Labels),
		Metadata:     meta,
	})
}

// notifyAssignee delivers an in-app notification to the newly assigned user, and
// only that user — the targeted counterpart to notifyReviewer, replacing the
// workspace-wide dispatchUserNotification("task.assigned") broadcast every
// subscriber used to receive regardless of whether the task was theirs.
// No-ops in two cases:
//   - agent assignee: notifyAssignedAgent already woke them via push/SSE/callback,
//     and only user rows exist in the notifications table.
//   - self-assignment: the actor already knows what they just did.
func (s *taskService) notifyAssignee(ctx context.Context, task *domain.Task, title string) {
	if task.AssigneeID == nil || task.AssigneeType != domain.AssigneeTypeUser {
		return
	}
	if actorID, _ := actorctx.FromContext(ctx); actorID != uuid.Nil && actorID == *task.AssigneeID {
		return
	}
	s.dispatchTargetedUserNotification(ctx, task, "task.assigned", title, "", *task.AssigneeID,
		map[string]any{"assignee_id": *task.AssigneeID})
}

// notifyReviewer delivers a reviewer-targeted notification through whichever
// channel matches the reviewer's type: agent push for an agent reviewer,
// targeted in-app notification for a human one. No-op if no reviewer is set.
func (s *taskService) notifyReviewer(ctx context.Context, task *domain.Task, eventType, title string) {
	if task.ReviewerID == nil || task.ReviewerType == nil {
		return
	}
	switch *task.ReviewerType {
	case domain.AssigneeTypeAgent:
		s.notifyReviewerAgent(ctx, task, eventType, nil)
	case domain.AssigneeTypeUser:
		s.dispatchTargetedUserNotification(ctx, task, eventType, title, "", *task.ReviewerID, nil)
	}
}

// logActivity writes an activity log entry and publishes an event bus message.
// Failures are logged but not propagated.
func (s *taskService) logActivity(ctx context.Context, projectID, entityID uuid.UUID, action string, changes map[string]interface{}) {
	if s.activityRepo == nil {
		return
	}

	// Resolve workspace_id from project.
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, projectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}
	if wsID == uuid.Nil {
		log.Printf("[activity] WARNING: could not resolve workspace_id for project %s, skipping", projectID)
		return
	}

	// Extract actor from Go context (set by auth middleware).
	actorID, actorType := actorctx.FromContext(ctx)

	changesJSON, _ := json.Marshal(changes)
	entry := &domain.ActivityLog{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		EntityType:  "task",
		EntityID:    entityID,
		Action:      action,
		ActorID:     actorID,
		ActorType:   actorType,
		Changes:     changesJSON,
		CreatedAt:   timeNow(),
	}
	if err := s.activityRepo.Create(ctx, entry); err != nil {
		log.Printf("[activity] WARNING: failed to log %s for task %s: %v", action, entityID, err)
	}

	// Also publish to the event bus so the Events page shows task activity.
	s.publishTaskEvent(ctx, wsID, projectID, entityID, actorID, actorType, action, changes)
}

// publishTaskEvent publishes a task mutation as an event bus message.
// This bridges the gap between activity_log (audit) and event_bus_messages (feed).
func (s *taskService) publishTaskEvent(ctx context.Context, wsID, projectID, taskID, actorID uuid.UUID, actorType domain.ActorType, action string, changes map[string]interface{}) {
	if s.eventBusSvc == nil {
		return
	}

	// Map activity actions to event types.
	eventType := domain.EventTypeCustom
	switch action {
	case "task.created", "task.deleted":
		eventType = domain.EventTypeStatusChange
	case "task.moved", "task.assigned":
		eventType = domain.EventTypeStatusChange
	case "task.updated":
		eventType = domain.EventTypeContextUpdate
	}

	payload := map[string]any{
		"task_id":    taskID.String(),
		"action":     action,
		"actor_id":   actorID,
		"actor_type": actorType,
	}
	// Merge changes into payload.
	for k, v := range changes {
		payload[k] = v
	}

	taskIDPtr := &taskID
	input := PublishEventInput{
		WorkspaceID: wsID,
		ProjectID:   projectID,
		TaskID:      taskIDPtr,
		EventType:   eventType,
		Subject:     action,
		Payload:     payload,
		Tags:        []string{"auto", "task"},
		TTLSeconds:  86400, // 24h
	}

	if _, err := s.eventBusSvc.Publish(ctx, input); err != nil {
		log.Printf("[event_bus] WARNING: failed to publish %s event for task %s: %v", action, taskID, err)
	}
}

// dispatchUserNotification sends an in-app notification to every workspace member
// subscribed to eventType. It resolves workspace_id from the project, then fires
// NotificationService.Notify asynchronously.
//
// It takes no body: all four call sites passed "", and unparam flags a parameter
// that is provably always the same constant. NotificationEvent.Body stays in the
// domain type — dispatchTargetedUserNotification still carries one — so a caller
// that needs a body can pass it there rather than reviving a dead parameter here.
func (s *taskService) dispatchUserNotification(ctx context.Context, task *domain.Task, eventType, title string) {
	if s.notifySvc == nil || s.projectRepo == nil {
		return
	}
	var wsID uuid.UUID
	if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
		wsID = proj.WorkspaceID
	}
	if wsID == uuid.Nil {
		return
	}
	taskIDCopy := task.ID
	projIDCopy := task.ProjectID
	s.notifySvc.Notify(ctx, domain.NotificationEvent{
		WorkspaceID: wsID,
		TaskID:      &taskIDCopy,
		ProjectID:   &projIDCopy,
		EventType:   eventType,
		Title:       title,
		Labels:      []string(task.Labels),
		Metadata: map[string]any{
			"task_id":    task.ID,
			"task_title": task.Title,
			"project_id": task.ProjectID,
		},
	})
}

// CASConflictError is returned by MoveTask when an expected_status_id or
// expected_updated_at precondition does not match the current task state.
// Callers should re-read the task using the returned fields and retry.
type CASConflictError struct {
	CurrentStatusID  uuid.UUID `json:"current_status_id"`
	CurrentUpdatedAt time.Time `json:"current_updated_at"`
}

func (e *CASConflictError) Error() string {
	return fmt.Sprintf("cas_conflict: task status is %s (updated_at=%s)",
		e.CurrentStatusID, e.CurrentUpdatedAt.Format(time.RFC3339))
}

// ListRevisionStaleError is returned by List (ADR-0004,
// dev-docs/adrs/0004-task-list-revision-and-stale-cursor.md) when a caller's
// list_revision no longer matches the project's current task_list_revision —
// meaning tasks/artifacts/vcs_links changed since the page the caller is
// continuing from was issued. The handler maps this to HTTP 410 Gone with
// the exact body shape ADR-0004 Decision 4 specifies: restarting pagination
// from page 1 (no list_revision) is the only correct recovery, never a
// silent fallback to a fresh or stale snapshot under the same page number.
type ListRevisionStaleError struct {
	Requested int64
	Current   int64
}

func (e *ListRevisionStaleError) Error() string {
	return fmt.Sprintf("list_revision_stale: requested %d, current %d", e.Requested, e.Current)
}

// RuleViolationError is returned when a governance rule blocks an action.
type RuleViolationError struct {
	Violations []domain.RuleViolation
}

func (e *RuleViolationError) Error() string {
	return fmt.Sprintf("action blocked by %d governance rule(s)", len(e.Violations))
}

// ReviewEvidenceError is returned when a task is moved to review without any
// evidence: no artifact, no VCS link, and no comment.
type ReviewEvidenceError struct{}

func (e *ReviewEvidenceError) Error() string {
	return "review requires evidence: add a PR/VCS link, artifact upload, or comment with proof before moving to review"
}

// TaskShippedError is returned when a caller attempts to move a shipped task to a
// non-done category. Clear the flag via ShipTask(ctx, id, false) before reopening.
type TaskShippedError struct{}

func (e *TaskShippedError) Error() string {
	return "task is marked as shipped and cannot be moved to a non-done status; clear the shipped flag first (PATCH /tasks/:id/ship with shipped=false)"
}

// AssignmentPinnedError is returned when a rule or system source tries to reassign
// a task that is pinned by a human assignment. Only another human-source call can override.
type AssignmentPinnedError struct{}

func (e *AssignmentPinnedError) Error() string {
	return "task assignee is pinned by a human; only a human-source assignment (source=human) can override it"
}

// HumanGateFrozenError is returned when an agent or system actor attempts to move a
// human-gated task to backlog/done/cancelled without a human sign-off (audit 2026-06-15 P0 #3).
type HumanGateFrozenError struct{}

func (e *HumanGateFrozenError) Error() string {
	return "task is human-gated: awaiting human sign-off; only a user may move it to backlog/done/cancelled"
}

// githubLiveCheckTimeout bounds a single done-evidence-gate GitHub round
// trip so an unreachable/slow API can't stall a move_task call — it falls
// back to the cached status instead (see isPRMergedOnGitHub).
const githubLiveCheckTimeout = 5 * time.Second

// gitlabLiveCheckTimeout is githubLiveCheckTimeout's GitLab counterpart (see
// isMRMergedOnGitLab).
const gitlabLiveCheckTimeout = 5 * time.Second

// isPRMergedLive asks the linked provider (GitHub or GitLab) directly
// whether the linked PR/MR has been merged, dispatching on l.Provider.
// workspaceID is the link's OWN task's project's workspace — resolved once
// by the caller (resolveLinkWorkspace) — used to pick a per-workspace
// client when vcsIntegrations is wired (§C1). Returns live=false when no
// check could be performed at all for this link's provider — callers MUST
// treat that as "couldn't verify", never as "not merged", and fall back to
// the cached status instead.
func (s *taskService) isPRMergedLive(ctx context.Context, l domain.VCSLink, workspaceID uuid.UUID) (merged, live bool) {
	switch l.Provider {
	case domain.VCSProviderGitHub:
		return s.isPRMergedOnGitHub(ctx, l, workspaceID)
	case domain.VCSProviderGitLab:
		return s.isMRMergedOnGitLab(ctx, l, workspaceID)
	default:
		return false, false
	}
}

// hasLiveChecker reports whether a live-check client can be resolved for
// the given provider and workspace, independent of whether any particular
// call to it would succeed. Used to distinguish, in the done-evidence
// gate's refusal message, "we never even tried — no access is configured"
// from "we tried and it failed" (#bc39d781: the former needs a message that
// says how to fix it — configure the token — not one implying a merge
// attempt is being waited on).
func (s *taskService) hasLiveChecker(ctx context.Context, provider domain.VCSProvider, workspaceID uuid.UUID) bool {
	switch provider {
	case domain.VCSProviderGitHub:
		_, ok := s.resolveGitHubChecker(ctx, workspaceID)
		return ok
	case domain.VCSProviderGitLab:
		_, ok := s.resolveGitLabChecker(ctx, workspaceID)
		return ok
	default:
		return false
	}
}

// resolveGitHubChecker returns the GitHub live-check client that governs
// workspaceID right now. When vcsIntegrations is wired (§C1, production),
// it resolves fresh on every call — an active workspace row wins wholly,
// then env, then disabled — exactly mirroring the config.go/main.go
// start-of-process wiring this replaces, just re-evaluated per call instead
// of baked in at boot. When vcsIntegrations is nil (tests, and any caller
// that still only wires WithGitHubPRChecker), the legacy static client is
// used unconditionally — workspaceID is ignored in that path, matching the
// pre-#33a4bb57 behavior exactly.
func (s *taskService) resolveGitHubChecker(ctx context.Context, workspaceID uuid.UUID) (githubapi.PullRequestChecker, bool) {
	if s.vcsIntegrations == nil {
		return s.githubPRChecker, s.githubPRChecker != nil
	}
	cfg, _, ok := s.vcsIntegrations.ResolveGitHub(ctx, workspaceID)
	if !ok || cfg.Token == "" {
		return nil, false
	}
	return githubapi.NewClient(cfg.Token), true
}

// resolveGitLabChecker is resolveGitHubChecker's GitLab counterpart. GitLab
// needs BOTH a base URL and a token before a live check can even be
// attempted (self-hosted, no fixed API host like GitHub's) — same gate the
// pre-#33a4bb57 env-only wiring used.
func (s *taskService) resolveGitLabChecker(ctx context.Context, workspaceID uuid.UUID) (gitlabapi.MergeRequestChecker, bool) {
	if s.vcsIntegrations == nil {
		return s.gitlabMRChecker, s.gitlabMRChecker != nil
	}
	cfg, _, ok := s.vcsIntegrations.ResolveGitLab(ctx, workspaceID)
	if !ok || cfg.BaseURL == "" || cfg.Token == "" {
		return nil, false
	}
	return gitlabapi.NewClient(cfg.BaseURL, cfg.Token), true
}

// resolveProjectWorkspace looks up the workspace that owns projectID —
// the done-evidence gate already has the task's project loaded (task,
// above), so this takes the project id directly rather than re-fetching the
// task. Returns ok=false when the project can't be resolved (e.g.
// projectRepo not wired in a test) so callers fall back to the legacy
// static-client path instead of blocking on an unresolvable workspace.
func (s *taskService) resolveProjectWorkspace(ctx context.Context, projectID uuid.UUID) (uuid.UUID, bool) {
	if s.projectRepo == nil {
		return uuid.Nil, false
	}
	proj, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || proj == nil {
		return uuid.Nil, false
	}
	return proj.WorkspaceID, true
}

// isPRMergedOnGitHub asks GitHub directly whether the linked PR has been
// merged, bypassing the (possibly stale) cached vcs_links.status. Returns
// live=false when the check could not be performed at all — no checker
// resolvable for this workspace, the URL doesn't parse as a GitHub PR, or
// the API call itself failed — callers MUST treat that as "couldn't
// verify", never as "not merged", and fall back to the cached status
// instead.
func (s *taskService) isPRMergedOnGitHub(ctx context.Context, l domain.VCSLink, workspaceID uuid.UUID) (merged, live bool) {
	if l.Provider != domain.VCSProviderGitHub {
		return false, false
	}
	checker, ok := s.resolveGitHubChecker(ctx, workspaceID)
	if !ok {
		return false, false
	}
	owner, repo, number, ok := githubapi.ParsePullRequestURL(l.URL)
	if !ok {
		return false, false
	}
	ghCtx, cancel := context.WithTimeout(ctx, githubLiveCheckTimeout)
	defer cancel()
	state, err := checker.GetPullRequestState(ghCtx, owner, repo, number)
	if err != nil {
		log.Printf("[done-evidence-gate] live github check failed for %s/%s#%d (link=%s task=%s): %v — falling back to cached status %q",
			owner, repo, number, l.ID, l.TaskID, err, l.Status)
		return false, false
	}
	return state.Merged, true
}

// isMRMergedOnGitLab asks GitLab directly whether the linked MR has been
// merged — GitLab counterpart of isPRMergedOnGitHub, see its doc comment
// for the live/merged contract callers must honor.
func (s *taskService) isMRMergedOnGitLab(ctx context.Context, l domain.VCSLink, workspaceID uuid.UUID) (merged, live bool) {
	if l.Provider != domain.VCSProviderGitLab {
		return false, false
	}
	checker, ok := s.resolveGitLabChecker(ctx, workspaceID)
	if !ok {
		return false, false
	}
	projectPath, iid, ok := gitlabapi.ParseMergeRequestURL(l.URL)
	if !ok {
		return false, false
	}
	glCtx, cancel := context.WithTimeout(ctx, gitlabLiveCheckTimeout)
	defer cancel()
	state, err := checker.GetMergeRequestState(glCtx, projectPath, iid)
	if err != nil {
		log.Printf("[done-evidence-gate] live gitlab check failed for %s!%d (link=%s task=%s): %v — falling back to cached status %q",
			projectPath, iid, l.ID, l.TaskID, err, l.Status)
		return false, false
	}
	return state.Merged, true
}

// healVCSLinkStatus persists a live-verified "merged" status so future reads
// don't need another GitHub round trip and don't hit the same stale block.
// Best-effort: a failure here does not block the move — the caller already
// independently verified the PR is merged via the live GitHub check, and
// this is only a cache write, not the source of truth.
func (s *taskService) healVCSLinkStatus(ctx context.Context, l domain.VCSLink) {
	l.Status = domain.VCSLinkStatusMerged
	if _, err := s.vcsLinkRepo.Upsert(ctx, &l); err != nil {
		log.Printf("[done-evidence-gate] failed to self-heal vcs_link %s status to merged: %v", l.ID, err)
	}
}

// DoneEvidenceError is returned when a task is moved to done but evidence is
// missing or a linked PR has not been merged (or explicitly closed) yet.
type DoneEvidenceError struct {
	// PRURL, PRTitle and PRStatus are set when a specific unmerged PR
	// triggered the block. PRStatus is the recorded vcs_links.status value
	// ("open", or "" for a link created before status tracking existed).
	PRURL    string
	PRTitle  string
	PRStatus string
	// PRProvider is the recorded vcs_links.provider value ("github" or
	// "gitlab"). It decides the wording of the "could not verify live"
	// branch below: a GitHub link and a GitLab link that both failed live
	// verification (or never got a checker wired at all) need to name their
	// own system, not each other's — saying "against GitHub" for a GitLab
	// link names the wrong reason and sends the reader looking in the wrong
	// place (#0fbed572).
	PRProvider string
	// PRStatusCheckedLive is true when this block reflects a provider API
	// call made just now (the gate asked GitHub/GitLab directly and it said
	// "not merged") rather than the cached vcs_links.status. This is
	// current truth, not a stale cache — the message must not invite a
	// "maybe it's just stale" re-link when it isn't.
	PRStatusCheckedLive bool
	// PRLinkedAt is when this VCS link was first recorded. Only rendered
	// when PRStatusCheckedLive is false, to make clear the cached status
	// might be stale (e.g. the provider was unreachable, so the gate fell
	// back to this recorded value) rather than currently verified.
	PRLinkedAt time.Time
	// PRProviderAccessConfigured is whether a live-check client resolves for
	// PRProvider (and the link's workspace) at all, independent of whether
	// this specific call succeeded — set from taskService.hasLiveChecker at
	// the moment the gate runs, since DoneEvidenceError itself has no access
	// to the service. Decides
	// which fallback wording unverifiableLiveCheckReason uses: "no access is
	// configured, state the status explicitly" reads very differently from
	// "we asked and couldn't verify" (#bc39d781) — the first tells the
	// reader how to unblock themselves right now, the second implies a
	// transient failure worth retrying.
	PRProviderAccessConfigured bool
}

func (e *DoneEvidenceError) Error() string {
	if e.PRURL == "" {
		return "done requires evidence: add a PR/VCS link, artifact upload, or comment with proof before closing"
	}
	ref := e.PRTitle
	if ref == "" {
		ref = e.PRURL
	}
	// No "justification comment" escape hatch exists on this branch —
	// VCSLinkCount > 0 blocks unconditionally on an unmerged/unrecorded
	// PR link, comments are only consulted when there is NO VCS link at
	// all. A message promising one taught agents to write a comment,
	// hit the same 422 again, and conclude the gate itself was broken
	// (#df734dd9).
	if e.PRStatusCheckedLive {
		// The gate just asked the provider and got a definitive "not
		// merged" — don't suggest "if that's stale", there is no cache
		// involved here. This branch is only reachable when isPRMergedLive
		// returned live=true, i.e. a real API call against the link's own
		// provider just succeeded — name that provider, not a hardcoded one.
		return fmt.Sprintf(
			"PR «%s» is not merged (verified live against %s just now) — merge it first, or if this "+
				"read is wrong, re-link it with an explicit status (add_vcs_link ... status=merged)", ref, liveCheckProviderName(e.PRProvider))
	}
	// Live verification was unavailable (no checker wired, the URL doesn't
	// parse, or the provider API call itself failed) — fall back to the
	// cached record, same as before this fix, but say so explicitly plus
	// WHEN that record was made, so the reader understands "this might be
	// stale" rather than "the PR/MR is genuinely still open".
	linkedAt := e.PRLinkedAt.UTC().Format(time.RFC3339)
	verifyNote := unverifiableLiveCheckReason(e.PRProvider, e.PRProviderAccessConfigured)
	if e.PRStatus == "" {
		// The likely cause: the link was created for a PR that was already
		// merged before linking — no webhook will ever arrive to fix that
		// after the fact.
		return fmt.Sprintf(
			"PR «%s» has no recorded merge status (linked %s; %s) — if it's "+
				"actually merged, re-link it with an explicit status (add_vcs_link ... status=merged); if it "+
				"genuinely isn't merged yet, merge it first", ref, linkedAt, verifyNote)
	}
	return fmt.Sprintf(
		"PR «%s» is not merged (recorded status: %s, linked %s; %s) — if "+
			"that's stale, re-link it with an explicit status (add_vcs_link ... status=merged); otherwise merge it first",
		ref, e.PRStatus, linkedAt, verifyNote)
}

// liveCheckProviderName renders PRProvider as the human-facing name used in
// the "verified live against X" message (DoneEvidenceError.Error(),
// PRStatusCheckedLive branch) — that branch is only reachable after a real
// API call against the link's own provider just succeeded, so the name
// shown must track which provider that was.
func liveCheckProviderName(provider string) string {
	switch domain.VCSProvider(provider) {
	case domain.VCSProviderGitLab:
		return "GitLab"
	case domain.VCSProviderGitHub, "":
		return "GitHub"
	default:
		return provider
	}
}

// unverifiableLiveCheckReason explains, per provider, why the done-evidence
// gate fell back to the cached vcs_links.status instead of a fresh check.
// checkerConfigured distinguishes two different facts that both fall back
// to the cache but call for different reader action (#bc39d781): "no live
// check client is wired for this provider at all" (the reader's actual next
// step is to configure access, or state the status explicitly right now —
// nothing will ever get better on its own) versus "a checker IS wired but
// this particular attempt failed" (transient — unreachable API, unparseable
// URL — worth a retry, and the token/URL don't need looking at). Before this
// distinction existed, an unconfigured GitLab link (the reported case:
// MESH_GITLAB_TOKEN unset entirely) got a message that read exactly like a
// live probe had been attempted and failed, when no attempt was possible at
// all — telling the reader to wait for something that would never resolve
// itself.
func unverifiableLiveCheckReason(provider string, checkerConfigured bool) string {
	switch domain.VCSProvider(provider) {
	case domain.VCSProviderGitLab:
		if !checkerConfigured {
			return "no GitLab access is configured (MESH_GITLAB_URL/MESH_GITLAB_TOKEN unset) — state the status explicitly"
		}
		return "could not verify live against GitLab just now — state the status explicitly if this is stale"
	case domain.VCSProviderGitHub, "":
		return "could not verify live against GitHub"
	default:
		return fmt.Sprintf("could not verify live: no live check exists yet for provider %q", provider)
	}
}

// dodBlockingGates returns the names of required DoD gates that have not yet passed.
// An empty slice means the task is clear to move to done.
func dodBlockingGates(settings domain.ProjectSettings, checks domain.DodChecks) []string {
	var blocking []string
	for _, g := range settings.DodGates {
		if !g.Required {
			continue
		}
		check, ok := checks[g.Name]
		if !ok || check.Status != domain.DodCheckPass {
			blocking = append(blocking, g.Name)
		}
	}
	return blocking
}

// DodGateBlockedError is returned when a task is moved to done but one or more
// required Definition-of-Done gates have not passed.
type DodGateBlockedError struct {
	// BlockingGates lists the names of required gates that are not yet "pass".
	BlockingGates []string
}

func (e *DodGateBlockedError) Error() string {
	return fmt.Sprintf("move to done blocked: required DoD gate(s) not passed: %s", strings.Join(e.BlockingGates, ", "))
}

// SetDodCheck upserts a named gate entry in the task's dod_checks map.
// Returns an error if the gate name is not in the project's dod_gates config.
func (s *taskService) SetDodCheck(ctx context.Context, taskID uuid.UUID, gateName, status, reporter string) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	// Validate gate name against project config when projectRepo is wired.
	if s.projectRepo != nil {
		proj, projErr := s.projectRepo.GetByID(ctx, task.ProjectID)
		if projErr == nil && proj != nil {
			settings := proj.GetSettings()
			if len(settings.DodGates) > 0 {
				found := false
				for _, g := range settings.DodGates {
					if g.Name == gateName {
						found = true
						break
					}
				}
				if !found {
					return apierror.BadRequest(fmt.Sprintf("gate %q is not configured for this project", gateName))
				}
			}
		}
	}

	return s.taskRepo.SetDodCheck(ctx, taskID, gateName, status, reporter)
}

// evaluateRulesForMove evaluates governance rules before a MoveTask operation.
// Returns violations; the caller decides whether to block.
func (s *taskService) evaluateRulesForMove(ctx context.Context, task *domain.Task, targetStatus *domain.TaskStatus, _ MoveTaskInput) ([]domain.RuleViolation, error) {
	if s.ruleSvc == nil {
		return nil, nil
	}

	actorID, actorType := actorctx.FromContext(ctx)

	// Resolve workspace_id.
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}
	if wsID == uuid.Nil {
		return nil, nil
	}

	taskID := task.ID
	projID := task.ProjectID
	statusID := targetStatus.ID

	input := EvaluateInput{
		Action:         "move_task",
		TaskID:         &taskID,
		Task:           task,
		TargetStatusID: &statusID,
		TargetStatus:   targetStatus,
		ActorID:        actorID,
		ActorType:      actorType,
		WorkspaceID:    wsID,
		ProjectID:      &projID,
	}

	return s.ruleSvc.Evaluate(ctx, input)
}

// ensureAssigneeProjectMember auto-enrolls whoever is about to become a task's
// assignee into that task's project, dispatching on assignee type.
//
// It is also the point at which a cross-workspace assignee is REFUSED: it calls
// assertAssigneeInProjectWorkspace first and returns that error without writing
// anything, so a caller that propagates the error cannot persist the assignment.
// Enrolling a principal from another tenant was the most damaging half of that
// defect — it manufactured the project_members row that made the foreign
// principal look native to every later check.
//
// EVERY write path that sets assignee_id must call this AND propagate its error.
// As of this commit that is: Create, Update (and BulkUpdate through it), MoveTask,
// AssignTask, CreateSubtask, applyReviewAssignee and restorePreReviewAssignee —
// seven. applyAutoAssign needs no call of its own: both of its callers enrol after
// it. Task creation from a recurring schedule or a template routes through Create.
// If you add an eighth, add the call — and
// TestEveryAssigneeWritePathIsWorkspaceChecked will fail until you do, which is
// the point: the previous version of this comment was prose, and prose does not
// fail a build.
//
// The caller must pass a TRUE assignee type, because this function dispatches on it
// and a wrong type enrols against the wrong column: an agent-typed user UUID hits the
// project_members.agent_id foreign key, the insert is rejected, and the failure is
// logged and swallowed — indistinguishable from success at the call site. Four of the
// seven resolve it via resolveAssigneeType (Create, Update, MoveTask, AssignTask) and
// CreateSubtask now does too, because its type is caller-supplied. The remaining two
// are safe without a lookup: applyReviewAssignee takes the type from
// resolveSetReviewer's agent lookup, restorePreReviewAssignee restores a type that was
// already resolved when it was stashed. `grep -n 'resolveAssigneeType(ctx' ` next to
// the list above is how that split was checked.
//
// Being the assignee of a
// task in a project you are not a member of is a dead end: task-scoped routes
// (get, comment, assign) are only workspace-gated and succeed, but a status
// transition resolves its status slug through the project-gated
// GET /projects/:proj_id/statuses and 403s — so the assignee can do the work and
// then move the task neither forward nor back. Enrolment on assignment is what
// keeps that state unreachable; a path that assigns without enrolling silently
// manufactures it. A set_reviewer rotation did exactly that in production.
func (s *taskService) ensureAssigneeProjectMember(ctx context.Context, projectID uuid.UUID, assigneeID *uuid.UUID, assigneeType domain.AssigneeType) error {
	if assigneeID == nil {
		return nil
	}
	// Typed first: an id that resolved to no principal keeps the caller's declared
	// type, and when that type is not a principal type the tenancy guard below
	// deliberately skips — which used to let the dangling id be written silently.
	if err := assertAssigneeIsTyped(projectID, assigneeID, assigneeType); err != nil {
		return err
	}
	// Tenancy second: refuse before granting anything.
	if err := s.assertAssigneeInProjectWorkspace(ctx, projectID, assigneeID, assigneeType); err != nil {
		return err
	}
	switch assigneeType {
	case domain.AssigneeTypeAgent:
		s.ensureAgentProjectMember(ctx, projectID, *assigneeID)
	case domain.AssigneeTypeUser:
		s.ensureUserProjectMember(ctx, projectID, *assigneeID)
	}
	return nil
}

// ensureAgentProjectMember auto-enrolls an agent into a project's member list
// if it is not already a member. This is called on task assignment, task creation,
// and subtask creation so that agents can always access the projects they work in.
func (s *taskService) ensureAgentProjectMember(ctx context.Context, projectID, agentID uuid.UUID) {
	if s.projectMemberRepo == nil || agentID == uuid.Nil {
		return
	}
	// Confirm the id really is an agent before writing it into project_members.agent_id.
	//
	// The type this dispatches on is not always trustworthy: resolveAssigneeType falls
	// back to the caller's value when the id is in neither directory, so a deleted or
	// bogus assignee arrives here typed by guess. Without this check the insert is
	// attempted anyway, the agent_id foreign key rejects it, and the error is logged
	// and swallowed — enrolment silently does not happen and nothing distinguishes
	// that from "already a member".
	// Only a POSITIVE absence blocks the write. A lookup error means we could not
	// check, which is not the same as checking and finding nothing: refusing on an
	// unreadable directory would turn a transient blip into a missing enrolment — the
	// dead end this whole mechanism exists to prevent. On error, fall through and let
	// the foreign key remain the backstop.
	if s.agentRepo != nil {
		a, aerr := s.agentRepo.GetByID(ctx, agentID)
		switch {
		case aerr != nil:
			log.Printf("[task-svc] auto-enroll: could not verify agent %s (%v) — attempting enrolment anyway", agentID, aerr)
		case a == nil:
			log.Printf("[task-svc] auto-enroll SKIPPED: %s is not a known agent, refusing to enroll it "+
				"as one in project %s (the assignee type was a caller's guess, not a lookup)", agentID, projectID)
			return
		}
	}

	exists, err := s.projectMemberRepo.ExistsMember(ctx, projectID, nil, &agentID)
	if err != nil || exists {
		return
	}
	member := &domain.ProjectMember{
		ID:        uuid.New(),
		ProjectID: projectID,
		AgentID:   &agentID,
		Role:      domain.ProjectRoleMember,
		CreatedAt: timeNow(),
		UpdatedAt: timeNow(),
	}
	if err := s.projectMemberRepo.Create(ctx, member); err != nil {
		log.Printf("[task-svc] auto-enroll agent %s in project %s failed: %v", agentID, projectID, err)
	}
}

// ensureUserProjectMember auto-enrolls a human user into a project's member list
// if they are not already a member. Analogous to ensureAgentProjectMember.
// Workspace owners are enrolled just like regular users — the middleware already
// bypasses the project membership check for owners, but explicit enrollment
// makes them visible in the project member list and avoids FK-check issues.
func (s *taskService) ensureUserProjectMember(ctx context.Context, projectID, userID uuid.UUID) {
	if s.projectMemberRepo == nil || userID == uuid.Nil {
		return
	}
	// Same guard as the agent path: confirm the id really is a user before writing it
	// into project_members.user_id.
	// Same rule as the agent path: a positive absence blocks, an unreadable directory
	// does not.
	if s.userRepo != nil {
		u, uerr := s.userRepo.GetByID(ctx, userID)
		switch {
		case uerr != nil:
			log.Printf("[task-svc] auto-enroll: could not verify user %s (%v) — attempting enrolment anyway", userID, uerr)
		case u == nil:
			log.Printf("[task-svc] auto-enroll SKIPPED: %s is not a known user, refusing to enroll it "+
				"as one in project %s (the assignee type was a caller's guess, not a lookup)", userID, projectID)
			return
		}
	}

	exists, err := s.projectMemberRepo.ExistsMember(ctx, projectID, &userID, nil)
	if err != nil || exists {
		return
	}
	member := &domain.ProjectMember{
		ID:        uuid.New(),
		ProjectID: projectID,
		UserID:    &userID,
		Role:      domain.ProjectRoleMember,
		CreatedAt: timeNow(),
		UpdatedAt: timeNow(),
	}
	if err := s.projectMemberRepo.Create(ctx, member); err != nil {
		log.Printf("[task-svc] auto-enroll user %s in project %s failed: %v", userID, projectID, err)
	}
}

// resolveAssigneeType determines the correct AssigneeType for the given UUID by
// querying the agent and user directories. Never errors — falls back to the
// provided fallback (preserving caller's intent) and logs a warning when the
// UUID cannot be found in either directory.
func (s *taskService) resolveAssigneeType(ctx context.Context, assigneeID *uuid.UUID, fallback domain.AssigneeType) domain.AssigneeType {
	if assigneeID == nil || *assigneeID == uuid.Nil {
		return domain.AssigneeTypeUnassigned
	}
	if s.agentRepo != nil {
		if a, err := s.agentRepo.GetByID(ctx, *assigneeID); err == nil && a != nil {
			return domain.AssigneeTypeAgent
		}
	}
	if s.userRepo != nil {
		if u, err := s.userRepo.GetByID(ctx, *assigneeID); err == nil && u != nil {
			return domain.AssigneeTypeUser
		}
	}
	if s.agentRepo != nil || s.userRepo != nil {
		log.Printf("[task-svc] WARNING: assignee %s not found in agents or users — using fallback %q", *assigneeID, fallback)
	}
	return fallback
}

// ValidateAssigneeForProject is the funnel non-task write paths call into the
// tenancy guard through. See the TaskService interface doc for why it exists.
//
// It runs the exact two steps Create/Update/MoveTask/AssignTask run before
// enrolling an assignee — resolveAssigneeType then assertAssigneeInProjectWorkspace
// — as calls into those functions, not a second copy of either. A re-implemented
// predicate is exactly how a check like this drifts from the one it was meant to
// match without either side failing a build.
func (s *taskService) ValidateAssigneeForProject(ctx context.Context, projectID uuid.UUID, assigneeID *uuid.UUID, assigneeType domain.AssigneeType) (domain.AssigneeType, error) {
	if assigneeID == nil || *assigneeID == uuid.Nil {
		return domain.AssigneeTypeUnassigned, nil
	}
	resolved := s.resolveAssigneeType(ctx, assigneeID, assigneeType)
	// Same order as ensureAssigneeProjectMember, and for the same reason: when the id
	// resolves to no principal, resolveAssigneeType hands back the caller's declared
	// type, and a non-principal type makes the tenancy check below skip. Without this
	// line a template or schedule would store an assignee_id that names nobody and
	// report success — the defect this pair of guards exists to close, arriving
	// through the second funnel instead of the first.
	if err := assertAssigneeIsTyped(projectID, assigneeID, resolved); err != nil {
		return resolved, err
	}
	if err := s.assertAssigneeInProjectWorkspace(ctx, projectID, assigneeID, resolved); err != nil {
		return resolved, err
	}
	return resolved, nil
}

// MoveToProject moves a task to a different project. It finds the default status
// for the target project, validates the task is not already there, calls the
// repository to atomically update project_id/status_id/task_number, logs activity,
// invalidates the context cache, and returns the freshly fetched task.
func (s *taskService) MoveToProject(ctx context.Context, taskID, targetProjectID uuid.UUID) (*domain.Task, error) {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, apierror.NotFound("Task")
	}

	if task.ProjectID == targetProjectID {
		return nil, apierror.ValidationError(map[string]string{
			"project_id": "task is already in the target project",
		})
	}

	// The target project has to be in the same workspace as the task.
	//
	// project_id arrives in the request body, so no route parameter names it and
	// the workspace guard cannot see it — it checks :task_id, which is the
	// caller's own task. Until this check existed, a member of any workspace could
	// move their task (and, by cascade, its whole subtree) into a stranger's
	// project, where it showed up on that tenant's board and pulled their status
	// ids into it.
	if s.projectRepo == nil {
		// Fail closed. Without the project repository there is no way to tell the
		// two workspaces apart, and "cannot check" must not read as "allowed" on
		// the one code path whose whole job is to decide where a task lands.
		return nil, apierror.InternalError("project lookup unavailable")
	}
	source, err := s.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil {
		return nil, err
	}
	target, err := s.projectRepo.GetByID(ctx, targetProjectID)
	if err != nil {
		return nil, err
	}
	if source == nil || target == nil {
		return nil, apierror.NotFound("Project")
	}
	if source.WorkspaceID != target.WorkspaceID {
		return nil, apierror.BadRequest("target project belongs to a different workspace")
	}
	// Capture sourceProjectID before the repo call mutates the task in-memory.
	sourceProjectID := task.ProjectID

	// Find the default status for the target project.
	statuses, err := s.statusRepo.ListByProject(ctx, targetProjectID)
	if err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, apierror.BadRequest("target project has no statuses")
	}
	// Pick the status with the lowest position (first column on the board).
	defaultStatus := statuses[0]
	for _, st := range statuses[1:] {
		if st.Position < defaultStatus.Position {
			defaultStatus = st
		}
	}

	if err = s.taskRepo.MoveToProject(ctx, taskID, targetProjectID, defaultStatus.ID); err != nil { //nolint:gocritic // using = to avoid govet shadow
		return nil, err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, taskID)
	}
	s.logActivity(ctx, targetProjectID, taskID, "task.moved_to_project", map[string]interface{}{
		"from_project_id": map[string]interface{}{"old": sourceProjectID.String(), "new": targetProjectID.String()},
	})

	// Build category→status map for the target project (used by subtask cascade).
	targetCatMap := make(map[domain.StatusCategory]uuid.UUID)
	for _, st := range statuses {
		if _, exists := targetCatMap[st.Category]; !exists {
			targetCatMap[st.Category] = st.ID
		}
	}

	// Fetch source project statuses to map each subtask's statusID → category.
	sourceStatuses, err := s.statusRepo.ListByProject(ctx, sourceProjectID)
	if err != nil {
		return nil, err
	}
	sourceCatMap := make(map[uuid.UUID]domain.StatusCategory)
	for _, st := range sourceStatuses {
		sourceCatMap[st.ID] = st.Category
	}

	// Cascade move to all direct and nested subtasks.
	cascadeErr := s.moveSubtasksToProject(ctx, taskID, sourceProjectID, targetProjectID, sourceCatMap, targetCatMap, defaultStatus.ID)
	if cascadeErr != nil {
		return nil, cascadeErr
	}

	// Re-fetch the updated task so the caller has the new project_id/status_id/task_number.
	updated, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, apierror.NotFound("Task")
	}
	return updated, nil
}

// moveSubtasksToProject recursively moves all subtasks of parentID to targetProjectID,
// remapping each subtask's status by semantic category. Falls back to fallbackStatusID
// when the target project has no status with the matching category.
func (s *taskService) moveSubtasksToProject(
	ctx context.Context,
	parentID, sourceProjectID, targetProjectID uuid.UUID,
	sourceCatMap map[uuid.UUID]domain.StatusCategory,
	targetCatMap map[domain.StatusCategory]uuid.UUID,
	fallbackStatusID uuid.UUID,
) error {
	subtasks, err := s.taskRepo.ListSubtasks(ctx, parentID)
	if err != nil {
		return err
	}
	for _, sub := range subtasks {
		targetStatusID := fallbackStatusID
		if cat, ok := sourceCatMap[sub.StatusID]; ok {
			if tgtID, ok := targetCatMap[cat]; ok {
				targetStatusID = tgtID
			}
		}
		if err := s.taskRepo.MoveToProject(ctx, sub.ID, targetProjectID, targetStatusID); err != nil {
			return err
		}
		if s.ctxCacheInv != nil {
			s.ctxCacheInv.Invalidate(ctx, sub.ID)
		}
		s.logActivity(ctx, targetProjectID, sub.ID, "task.moved_to_project", map[string]interface{}{
			"from_project_id":      map[string]interface{}{"old": sourceProjectID.String(), "new": targetProjectID.String()},
			"cascaded_from_parent": parentID.String(),
		})
		// Recurse for deeper nesting.
		if err := s.moveSubtasksToProject(ctx, sub.ID, sourceProjectID, targetProjectID, sourceCatMap, targetCatMap, fallbackStatusID); err != nil {
			return err
		}
	}
	return nil
}

// CheckoutTask acquires an exclusive application-level lock on the task for the
// calling agent. The TTL is clamped to [1, 240] minutes; default is 15.
// Only agents may checkout — users should assign tasks instead.
// sessionMetadata is recorded into the activity log entry (forensics) and is
// never persisted on the task row, so the schema is unchanged.
func (s *taskService) CheckoutTask(ctx context.Context, taskID uuid.UUID, ttlMinutes int, sessionMetadata map[string]interface{}) (*CheckoutResult, error) {
	actorID, actorType := actorctx.FromContext(ctx)
	if actorType != domain.ActorTypeAgent || actorID == uuid.Nil {
		return nil, apierror.BadRequest("only agents can checkout tasks")
	}

	// Pre-fetch the task to validate existence and status category before locking.
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, apierror.NotFound("Task")
	}
	wasTodo := false
	if status, sErr := s.statusRepo.GetByID(ctx, task.StatusID); sErr == nil && status != nil {
		switch status.Category {
		case domain.StatusCategoryDone, domain.StatusCategoryCancelled:
			return nil, apierror.BadRequest("cannot checkout a task in " + string(status.Category) + " status")
		case domain.StatusCategoryTodo:
			if task.DelegationLevel == domain.DelegationLevelSupervised {
				return nil, apierror.ForbiddenWithDetails(
					"supervised_mode_requires_manual_start",
					"task must be moved to in_progress by a human before agent checkout",
				)
			}
			wasTodo = true
		}
	}

	if ttlMinutes <= 0 {
		ttlMinutes = 15
	}
	if ttlMinutes > 240 {
		ttlMinutes = 240
	}

	token := uuid.New()
	expiresAt := timeNow().Add(time.Duration(ttlMinutes) * time.Minute)

	if err = s.taskRepo.AtomicCheckout(ctx, taskID, actorID, token, expiresAt); err != nil {
		if errors.Is(err, pgRepo.ErrCheckoutConflict) {
			// Fetch the task to surface the current holder's info in the error.
			latest, fetchErr := s.taskRepo.GetByID(ctx, taskID)
			if fetchErr == nil && latest != nil && latest.CheckedOutBy != nil && latest.CheckoutExpires != nil {
				conflict := &CheckoutConflictError{
					CheckedOutBy:     *latest.CheckedOutBy,
					CheckedOutByKind: "agent",
					ExpiresAt:        *latest.CheckoutExpires,
					AcquiredAt:       latest.CheckoutAcquiredAt,
				}
				if s.agentRepo != nil {
					if agent, agentErr := s.agentRepo.GetByID(ctx, *latest.CheckedOutBy); agentErr == nil && agent != nil {
						conflict.CheckedOutByName = agent.Name
					}
				}
				return nil, conflict
			}
			// Concurrent lock/release race: return a clean 409 instead of leaking
			// the raw sentinel (which handleError cannot map and would return 500).
			return nil, apierror.Conflict("task is already checked out")
		}
		return nil, err
	}

	payload := map[string]interface{}{
		"expires_at": expiresAt.Format(time.RFC3339),
		"ttl_min":    ttlMinutes,
	}
	if len(sessionMetadata) > 0 {
		payload["session_metadata"] = sessionMetadata
	}
	s.logActivity(ctx, task.ProjectID, taskID, "task.checkout_acquired", payload)

	// Reflect the checkout on the board: a card in an agent's hands belongs in
	// In Progress, not in Todo.
	if wasTodo {
		s.reflectCheckoutInStatus(ctx, task, actorID)
	}

	return &CheckoutResult{
		TaskID:          taskID,
		CheckoutToken:   token,
		CheckedOutBy:    actorID,
		ExpiresAt:       expiresAt,
		DelegationLevel: task.DelegationLevel,
		ProjectID:       task.ProjectID,
	}, nil
}

// reflectCheckoutInStatus moves a just-checked-out todo task into the project's
// first in_progress status, so that work in an agent's hands is visible on the
// board instead of being indistinguishable from untouched todo.
//
// Why the move is made as SYSTEM and never fails the checkout:
//
// The lock is already held when this runs — the work IS in someone's hands, and
// that is a fact, not a request. The status change reports the fact; it does not
// grant permission. Refusing it (a capacity rule, a workflow gate, a transient
// status-repo error) would not stop the agent working the card, it would only
// hide the card again — which is precisely the bug this exists to fix. So every
// failure here is logged and swallowed: the caller still gets its lock.
//
// Concretely, the workspace runs an active block-enforcement
// `capacity_limit.max_in_progress` rule with limit 2, counted per ACTOR. That rule
// was authored when nothing ever entered in_progress, so it has been dormant, and
// under an agent actor it would begin refusing the 3rd concurrent card of a
// sanctioned fan-out (the fleet convention allows one own card plus up to three
// delegated). Moving as SYSTEM — the same actor the lease reaper uses for the
// mirror-image transition in task_lease_reaper.go — keeps this path a report
// rather than a second, accidental throttle. The WIP throttle that actually binds
// is the checkout itself and the fan-out cap, neither of which is weakened here.
// Re-tuning or retiring that rule is a policy decision, deliberately left to its
// owner rather than made silently as a side effect of a visibility fix.
func (s *taskService) reflectCheckoutInStatus(ctx context.Context, task *domain.Task, actorID uuid.UUID) {
	inProgressID := s.findStatusIDByCategory(ctx, task.ProjectID, domain.StatusCategoryInProgress)
	if inProgressID == nil {
		log.Printf("[checkout-auto-progress] no in_progress status for project %s, task %s stays in todo",
			task.ProjectID, task.ID)
		return
	}

	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)
	if err := s.MoveTask(sysCtx, task.ID, MoveTaskInput{StatusID: inProgressID, Source: "checkout"}); err != nil {
		// Fail open: the checkout stands, only the board display is degraded.
		log.Printf("[checkout-auto-progress] WARNING: task %s stays in todo after checkout by %s: %v",
			task.ID, actorID, err)
		s.logActivity(ctx, task.ProjectID, task.ID, "task.checkout_auto_progress_failed", map[string]interface{}{
			"reason":         err.Error(),
			"checked_out_by": actorID.String(),
		})
		return
	}
	s.logActivity(ctx, task.ProjectID, task.ID, "task.checkout_auto_progress", map[string]interface{}{
		"checked_out_by": actorID.String(),
	})
}

// findStatusIDByCategory returns the ID of the project's first status in the given
// category, or nil when the project has none.
func (s *taskService) findStatusIDByCategory(ctx context.Context, projectID uuid.UUID, cat domain.StatusCategory) *uuid.UUID {
	statuses, err := s.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		log.Printf("[checkout-auto-progress] cannot list statuses for project %s: %v", projectID, err)
		return nil
	}
	for i := range statuses {
		if statuses[i].Category == cat {
			id := statuses[i].ID
			return &id
		}
	}
	return nil
}

// ReleaseCheckout clears the checkout on a task. The token must match.
func (s *taskService) ReleaseCheckout(ctx context.Context, taskID, token uuid.UUID) error {
	err := s.taskRepo.ReleaseCheckout(ctx, taskID, token)
	if err != nil {
		if errors.Is(err, pgRepo.ErrInvalidCheckoutToken) {
			return apierror.Forbidden("invalid checkout token")
		}
		return err
	}
	return nil
}

// SelfReleaseCheckout releases the checkout held by the calling agent without
// requiring the checkout_token. The caller's identity (from actorctx) must
// match the current lock holder; otherwise 403 is returned. No-op when the
// task is not locked.
func (s *taskService) SelfReleaseCheckout(ctx context.Context, taskID uuid.UUID) error {
	callerID, _ := actorctx.FromContext(ctx)
	if callerID == uuid.Nil {
		return apierror.Forbidden("authenticated identity required to self-release")
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || task.CheckedOutBy == nil {
		return nil // not locked — idempotent no-op
	}
	if *task.CheckedOutBy != callerID {
		return apierror.Forbidden("cannot release a lock held by another agent")
	}
	return s.ForceReleaseCheckout(ctx, taskID)
}

// ExtendCheckout extends the TTL of an existing checkout identified by token.
// The TTL is clamped to [1, 240] minutes; default is 15.
func (s *taskService) ExtendCheckout(ctx context.Context, taskID, token uuid.UUID, ttlMinutes int) (*CheckoutResult, error) {
	if ttlMinutes <= 0 {
		ttlMinutes = 15
	}
	if ttlMinutes > 240 {
		ttlMinutes = 240
	}

	newExpires := timeNow().Add(time.Duration(ttlMinutes) * time.Minute)
	err := s.taskRepo.ExtendCheckout(ctx, taskID, token, newExpires)
	if err != nil {
		if errors.Is(err, pgRepo.ErrInvalidCheckoutToken) {
			return nil, apierror.Forbidden("invalid or expired checkout token")
		}
		return nil, err
	}

	// Fetch the task to get the agent ID for the response.
	task, fetchErr := s.taskRepo.GetByID(ctx, taskID)
	if fetchErr != nil || task == nil || task.CheckedOutBy == nil {
		// Token was valid (no error from ExtendCheckout), just return what we know.
		actorID, _ := actorctx.FromContext(ctx)
		return &CheckoutResult{
			TaskID:        taskID,
			CheckoutToken: token,
			CheckedOutBy:  actorID,
			ExpiresAt:     newExpires,
		}, nil
	}

	return &CheckoutResult{
		TaskID:          taskID,
		CheckoutToken:   token,
		CheckedOutBy:    *task.CheckedOutBy,
		ExpiresAt:       newExpires,
		DelegationLevel: task.DelegationLevel,
		ProjectID:       task.ProjectID,
	}, nil
}

// ForceReleaseCheckout clears the checkout without token verification.
// The handler layer must enforce authorization before calling this — the
// service trusts its caller and performs the release unconditionally.
func (s *taskService) ForceReleaseCheckout(ctx context.Context, taskID uuid.UUID) error {
	if err := s.taskRepo.ForceReleaseCheckout(ctx, taskID); err != nil {
		return err
	}
	actorID, _ := actorctx.FromContext(ctx)
	if task, err := s.taskRepo.GetByID(ctx, taskID); err == nil && task != nil {
		s.logActivity(ctx, task.ProjectID, taskID, "task.checkout_force_released", map[string]interface{}{
			"actor_id": actorID.String(),
		})
	}
	return nil
}

// applyReviewAssignee assigns a configured reviewer when a task transitions to review category.
// Consults WorkflowRulesConfig.Transitions[fromStatus].OnTransition.SetReviewer; if not set,
// the current assignee is preserved (no bounce to creator).
func (s *taskService) applyReviewAssignee(ctx context.Context, task *domain.Task, oldStatusID uuid.UUID) {
	if s.rulesConfigSvc == nil {
		return
	}

	var oldStatusName string
	if oldStatus, err := s.statusRepo.GetByID(ctx, oldStatusID); err == nil && oldStatus != nil {
		oldStatusName = oldStatus.Name
	}
	if oldStatusName == "" {
		return
	}

	wfResp, err := s.rulesConfigSvc.GetProjectWorkflowRules(ctx, task.ProjectID, nil)
	if err != nil || wfResp == nil {
		return
	}

	tr, ok := wfResp.Transitions[oldStatusName]
	if !ok || tr.OnTransition == nil || tr.OnTransition.SetReviewer == "" {
		return
	}

	reviewerID, assigneeType, err := s.resolveSetReviewer(ctx, task.ProjectID, tr.OnTransition.SetReviewer)
	if err != nil || reviewerID == nil {
		log.Printf("[review-assign] WARNING: cannot resolve set_reviewer=%q for task %s: %v", tr.OnTransition.SetReviewer, task.ID, err)
		return
	}

	if task.AssigneeID != nil && *task.AssigneeID == *reviewerID {
		return
	}

	// Tenancy of the configured reviewer is decided before the in-memory task is
	// touched. This function mutates the caller's task and only then persists it,
	// so a refusal discovered mid-way would have to be unwound — and MoveTask goes
	// on to use the same pointer. Checking first means there is nothing to unwind.
	//
	// A workflow rule naming a reviewer from another workspace is a misconfigured
	// rule, not a caller's request: it cannot be answered with a 4xx to anyone, so
	// it is logged loudly and the rotation is skipped. The task stays with its
	// current assignee, which is the safe outcome — the alternative is handing the
	// card, and its callback delivery, to a foreign principal.
	if err := s.assertAssigneeInProjectWorkspace(ctx, task.ProjectID, reviewerID, assigneeType); err != nil {
		log.Printf("[review-assign] REFUSED: set_reviewer=%q for task %s resolves to a principal outside "+
			"the task's workspace, leaving the assignee unchanged: %v", tr.OnTransition.SetReviewer, task.ID, err)
		return
	}

	// Capture the current holder before it is overwritten below — this is the real
	// "old" for the activity entry and comment, not the caller's request (there is
	// no caller here; the server is moving the task on its own).
	oldAssigneeID := task.AssigneeID
	oldAssigneeType := task.AssigneeType

	// Stash the current assignee so a later bounce out of review (MoveTask's
	// restorePreReviewAssignee) can return the task to whoever was doing the work,
	// instead of stranding it on the reviewer.
	task.PreReviewAssigneeID = oldAssigneeID
	task.PreReviewAssigneeType = &oldAssigneeType

	task.AssigneeID = reviewerID
	task.AssigneeType = assigneeType
	if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, task.AssigneeID, task.AssigneeType); err != nil {
		log.Printf("[review-assign] WARNING: enrolment refused for task %s after the tenancy check passed: %v", task.ID, err)
		return
	}
	task.UpdatedAt = timeNow()
	if err := s.taskRepo.Update(ctx, task); err != nil {
		log.Printf("[review-assign] WARNING: failed to assign set_reviewer for task %s: %v", task.ID, err)
		return
	}
	log.Printf("[review-assign] task %s assigned to set_reviewer=%q on review", task.ID, tr.OnTransition.SetReviewer)
	const reason = "set_reviewer on review transition"
	s.logActivity(ctx, task.ProjectID, task.ID, "task.assigned", map[string]interface{}{
		"assignee_id": map[string]interface{}{"old": oldAssigneeID, "new": reviewerID.String()},
		"reason":      reason,
	})
	s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
		"assignee_id": map[string]any{"old": oldAssigneeID, "new": reviewerID.String()},
		"reason":      reason,
	})
	s.postAssigneeChangeComment(ctx, task, oldAssigneeID, reviewerID, reason)
}

// restorePreReviewAssignee returns the task to whoever held it before applyReviewAssignee
// bounced it to the configured reviewer, when the task transitions back out of a
// review-category status without an explicit assignee_id in the move request. Without
// this, a plain reviewer bounce (move_task review -> todo with no assignee_id) silently
// strands the task on the reviewer instead of returning it to the executor.
func (s *taskService) restorePreReviewAssignee(ctx context.Context, task *domain.Task, oldStatusID uuid.UUID) {
	oldStatus, err := s.statusRepo.GetByID(ctx, oldStatusID)
	if err != nil || oldStatus == nil || oldStatus.Category != domain.StatusCategoryReview {
		return
	}
	if task.PreReviewAssigneeType == nil {
		// Nothing stashed — task didn't enter review via a SetReviewer bounce, or was
		// already restored on a prior transition.
		return
	}

	prevAssigneeID := task.PreReviewAssigneeID
	prevAssigneeType := *task.PreReviewAssigneeType

	// The stashed assignee was in-workspace when it was stashed, but the stash can
	// outlive that fact — an agent can be moved or deleted while the card sits in
	// review. Re-check before restoring rather than trusting the stash's age.
	if err := s.assertAssigneeInProjectWorkspace(ctx, task.ProjectID, prevAssigneeID, prevAssigneeType); err != nil {
		log.Printf("[review-assign] REFUSED: pre-review assignee of task %s is no longer valid for its "+
			"workspace, leaving the task with the reviewer: %v", task.ID, err)
		return
	}

	// Capture the reviewer (the current holder, about to be overwritten) as the real
	// "old" for the activity entry and comment.
	oldAssigneeID := task.AssigneeID

	task.AssigneeID = prevAssigneeID
	task.AssigneeType = prevAssigneeType
	if err := s.ensureAssigneeProjectMember(ctx, task.ProjectID, task.AssigneeID, task.AssigneeType); err != nil {
		log.Printf("[review-assign] WARNING: enrolment refused restoring pre-review assignee of task %s: %v", task.ID, err)
		return
	}
	task.PreReviewAssigneeID = nil
	task.PreReviewAssigneeType = nil
	task.UpdatedAt = timeNow()
	if err := s.taskRepo.Update(ctx, task); err != nil {
		log.Printf("[review-assign] WARNING: failed to restore pre-review assignee for task %s: %v", task.ID, err)
		return
	}
	var restoredID interface{}
	if prevAssigneeID != nil {
		restoredID = prevAssigneeID.String()
	}
	log.Printf("[review-assign] task %s assignee restored to pre-review holder on bounce out of review", task.ID)
	const reason = "restored pre-review assignee on bounce out of review"
	s.logActivity(ctx, task.ProjectID, task.ID, "task.assigned", map[string]interface{}{
		"assignee_id": map[string]interface{}{"old": oldAssigneeID, "new": restoredID},
		"reason":      reason,
	})
	s.notifyAssignedAgent(ctx, task, "task.assigned", map[string]any{
		"assignee_id": map[string]any{"old": oldAssigneeID, "new": restoredID},
		"reason":      reason,
	})
	s.postAssigneeChangeComment(ctx, task, oldAssigneeID, prevAssigneeID, reason)
}

// postAssigneeChangeComment writes an audit comment on the card explaining a
// server-initiated (not caller-requested) assignee change — applyReviewAssignee's
// reviewer bounce and restorePreReviewAssignee's restore are the only two write
// paths that move assignee_id without the caller asking for it, and both were
// previously silent: the activity log recorded "old": nil, and no comment was
// posted at all, so an agent reading the thread had no way to tell the card had
// moved out from under them (source: task f06ebeb7). A comment-post failure must
// not unwind the assignee change itself — it is logged loudly and the change stands.
func (s *taskService) postAssigneeChangeComment(ctx context.Context, task *domain.Task, oldID, newID *uuid.UUID, reason string) {
	if s.commentRepo == nil {
		return
	}
	describe := func(id *uuid.UUID) string {
		if id == nil {
			return "(unassigned)"
		}
		return id.String()
	}
	now := timeNow()
	comment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🔄 Авто-смена исполнителя: %s → %s (%s).",
			describe(oldID), describe(newID), reason,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, comment); err != nil {
		log.Printf("[review-assign] WARNING: failed to post assignee-change comment on task %s: %v", task.ID, err)
	}
}

// resolveSetReviewer resolves a SetReviewer config value to an agent UUID.
// "lead" maps to the project's default_assignee from assignment rules;
// any other value is treated as an agent slug.
func (s *taskService) resolveSetReviewer(ctx context.Context, projectID uuid.UUID, reviewer string) (*uuid.UUID, domain.AssigneeType, error) {
	slug := reviewer
	if reviewer == "lead" {
		rules, err := s.rulesConfigSvc.GetEffectiveAssignmentRules(ctx, projectID)
		if err != nil {
			return nil, "", fmt.Errorf("get assignment rules: %w", err)
		}
		if rules == nil || rules.DefaultAssignee == nil || rules.DefaultAssignee.Value == "" {
			return nil, "", fmt.Errorf("no default_assignee configured for project")
		}
		slug = rules.DefaultAssignee.Value
	}

	if s.agentRepo == nil || s.projectRepo == nil {
		return nil, "", fmt.Errorf("agent or project repo not wired")
	}
	proj, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || proj == nil {
		return nil, "", fmt.Errorf("project %s not found: %w", projectID, err)
	}
	agent, err := s.agentRepo.GetBySlug(ctx, proj.WorkspaceID, slug)
	if err != nil || agent == nil {
		return nil, "", fmt.Errorf("agent slug=%q not found in workspace: %w", slug, err)
	}
	id := agent.ID
	return &id, domain.AssigneeTypeAgent, nil
}

// SupersedeRecurringInstances closes all non-terminal instances of scheduleID
// (excluding newTaskID) by moving them to the project's done status.
// Errors on individual tasks are logged and skipped — fail-open.
func (s *taskService) SupersedeRecurringInstances(ctx context.Context, scheduleID, newTaskID uuid.UUID) error {
	openTasks, err := s.taskRepo.ListOpenByRecurringScheduleID(ctx, scheduleID, newTaskID)
	if err != nil {
		return fmt.Errorf("SupersedeRecurringInstances: %w", err)
	}
	if len(openTasks) == 0 {
		return nil
	}
	statuses, err := s.statusRepo.ListByProject(ctx, openTasks[0].ProjectID)
	if err != nil {
		return fmt.Errorf("SupersedeRecurringInstances ListByProject: %w", err)
	}
	var doneStatusID uuid.UUID
	for _, st := range statuses {
		if st.Category == domain.StatusCategoryDone {
			doneStatusID = st.ID
			break
		}
	}
	if doneStatusID == uuid.Nil {
		log.Printf("[recurring] WARNING: SupersedeRecurringInstances: no done status for project %s", openTasks[0].ProjectID)
		return nil
	}
	for _, task := range openTasks {
		if err := s.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &doneStatusID}); err != nil {
			log.Printf("[recurring] WARNING: SupersedeRecurringInstances: failed to close task %s: %v", task.ID, err)
		}
	}
	return nil
}

// SetHumanGate arms (value=true) or clears (value=false) the sticky human-gate flag.
func (s *taskService) SetHumanGate(ctx context.Context, taskID uuid.UUID, value bool) error {
	return s.taskRepo.SetHumanGate(ctx, taskID, value)
}

// SetHumanGateClass classifies the task's human_gate as hard or soft. See
// domain.HumanGateClass.
func (s *taskService) SetHumanGateClass(ctx context.Context, taskID uuid.UUID, class domain.HumanGateClass) error {
	return s.taskRepo.SetHumanGateClass(ctx, taskID, class)
}

// ShipTask marks the task as terminally shipped when shipped=true. Once shipped,
// MoveTask to any non-done category is rejected with TaskShippedError.
// Pass shipped=false to clear the flag (unship).
func (s *taskService) ShipTask(ctx context.Context, taskID uuid.UUID, shipped bool) error {
	if err := s.taskRepo.SetShipped(ctx, taskID, shipped); err != nil {
		return err
	}
	if shipped {
		if task, err := s.taskRepo.GetByID(ctx, taskID); err == nil && task != nil {
			pkgmetrics.RecordTaskShipped(task.ProjectID.String())
		}
	}
	return nil
}
