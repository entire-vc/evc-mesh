package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

// leaseTaskMover is the narrow slice of TaskService needed by the reaper.
// Using a narrow interface keeps the reaper testable without a full TaskService mock.
type leaseTaskMover interface {
	MoveTask(ctx context.Context, taskID uuid.UUID, input MoveTaskInput) error
}

// midPipelineSource is the narrow slice of RulesService the reaper needs to find
// out whether a project wants stalled cards parked or returned to todo. Narrow
// for the same reason as leaseTaskMover, and nilable: a reaper built without one
// simply never parks.
type midPipelineSource interface {
	GetProjectWorkflowRules(ctx context.Context, projectID uuid.UUID, callerAgentID *uuid.UUID) (*domain.WorkflowRulesResponse, error)
}

// CheckoutLeaseReaper finds in_progress tasks whose checkout TTL has expired without
// a heartbeat and returns them to todo, freeing their capacity slot.
type CheckoutLeaseReaper interface {
	SweepExpiredLeases(ctx context.Context) (int, error)
	// SweepUnleasedInProgress returns in_progress tasks that hold no checkout at
	// all and have been idle for olderThan. See FindStaleUnleasedInProgress.
	SweepUnleasedInProgress(ctx context.Context, olderThan time.Duration) (int, error)
}

// DefaultUnleasedGrace is how long an in_progress task may sit with no checkout
// before the reaper returns it to todo. Equal to the default checkout TTL: a card
// with no lease and no activity for longer than the system's own holding limit is
// abandoned by that same standard.
const DefaultUnleasedGrace = 120 * time.Minute

type checkoutLeaseReaper struct {
	taskRepo       repository.TaskRepository
	statusRepo     repository.TaskStatusRepository
	commentRepo    repository.CommentRepository
	taskMover      leaseTaskMover
	agentNotifySvc AgentNotifyService
	rulesSvc       midPipelineSource
	// activityRepo/projectRepo back logLeaseActivity (audit §1.2): both may be
	// nil, in which case that write is a no-op, same fail-open convention as
	// postSystemComment's nil commentRepo check above it.
	activityRepo repository.ActivityLogRepository
	projectRepo  repository.ProjectRepository
}

// NewCheckoutLeaseReaper constructs a CheckoutLeaseReaper.
// agentNotifySvc may be nil (notifications skipped).
// taskSvc is accepted as TaskService (which satisfies leaseTaskMover) so callers
// in main.go pass the full service without a cast.
// rulesSvc may be nil, in which case no project ever parks and every sweep hands
// tasks back to todo exactly as before this option existed.
// activityRepo/projectRepo may be nil, in which case logLeaseActivity is a no-op
// (the pre-existing behavior for every caller before this pair existed).
func NewCheckoutLeaseReaper(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	commentRepo repository.CommentRepository,
	taskSvc TaskService,
	agentNotifySvc AgentNotifyService,
	rulesSvc midPipelineSource,
	activityRepo repository.ActivityLogRepository,
	projectRepo repository.ProjectRepository,
) CheckoutLeaseReaper {
	return &checkoutLeaseReaper{
		taskRepo:       taskRepo,
		statusRepo:     statusRepo,
		commentRepo:    commentRepo,
		taskMover:      taskSvc,
		agentNotifySvc: agentNotifySvc,
		rulesSvc:       rulesSvc,
		activityRepo:   activityRepo,
		projectRepo:    projectRepo,
	}
}

// SweepExpiredLeases finds in_progress tasks with expired checkout TTLs and moves
// each one back to the project's first todo status. Returns the number of tasks moved.
func (r *checkoutLeaseReaper) SweepExpiredLeases(ctx context.Context) (int, error) {
	tasks, err := r.taskRepo.FindExpiredInProgressCheckouts(ctx)
	if err != nil {
		return 0, err
	}
	return r.returnToTodo(ctx, tasks, activityCheckoutLeaseExpired), nil
}

// SweepUnleasedInProgress returns in_progress tasks that hold NO checkout at all
// and have been idle for olderThan.
//
// SweepExpiredLeases cannot see these: its query requires a non-null
// checkout_expires, so it only ever finds a lease that EXPIRED, never one that was
// CLEARED. A cleared lease leaves the task in_progress with nothing watching it —
// the agent feed polls status_category=todo, and the dependency auto-promotion
// hook (tryUnblockTask) returns early on anything that is not backlog. Measured on
// prod 2026-08-27: 4 of 7 in_progress tasks held no lease, the oldest idle 245h.
func (r *checkoutLeaseReaper) SweepUnleasedInProgress(ctx context.Context, olderThan time.Duration) (int, error) {
	tasks, err := r.taskRepo.FindStaleUnleasedInProgress(ctx, olderThan)
	if err != nil {
		return 0, err
	}
	return r.handBack(ctx, tasks), nil
}

// Activity-log actions for the two returnToTodo call sites (audit §1.2: these
// were previously posted as task-thread comments — 2,205 of them, 7.1% of the
// fleet's total, pure rotating boilerplate on cards nobody was reading them
// on). notifyAssignee's separate event already tells the holder directly;
// activity_log is the durable structured record for anyone who later needs
// to know WHY, without cluttering the comment thread everyone else reads.
const (
	activityCheckoutLeaseExpired     = "task.checkout_lease_expired"
	activityCheckoutUnleasedReturned = "task.checkout_unleased_returned"
)

const (
	parkedCommentFmt = "🅿️ Задача висела в in_progress **без чекаута** и без признаков работы " +
		"(ни комментария, ни артефакта, ни VCS-link) дольше допустимого — **запаркована в backlog** " +
		"c `due_date` через %d ч и меткой `kind:monitor`.\n\n" +
		"Почему в backlog, а не в todo: возврат в todo кладёт карточку обратно в подачу немедленно, " +
		"и застрявшая карточка кормится тому же лейну снова и снова. Парк разрывает петлю, " +
		"а метка `kind:monitor` — это то, чем карточка проснётся: по ней monitor-promotion поднимет её " +
		"обратно в todo, когда наступит `due_date`. Без метки парк не имеет пути пробуждения вообще.\n\n" +
		"Если работа ещё идёт — сделайте `checkout_task` заново: без чекаута карточка не удерживается."
)

// returnToTodo moves each task to its project's first todo status, logging to
// activity_log and notifying as it goes. Shared by both sweeps so they cannot
// drift in how they hand a task back. A task that cannot be moved is skipped
// and logged, never silently dropped.
func (r *checkoutLeaseReaper) returnToTodo(ctx context.Context, tasks []domain.Task, activityAction string) int {
	if len(tasks) == 0 {
		return 0
	}

	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)

	// Cache todo status IDs per project to avoid repeated status list fetches.
	todoStatusCache := make(map[uuid.UUID]*uuid.UUID)

	var moved int
	for i := range tasks {
		task := &tasks[i]

		todoID, ok := todoStatusCache[task.ProjectID]
		if !ok {
			todoID = r.findTodoStatusID(sysCtx, task.ProjectID)
			todoStatusCache[task.ProjectID] = todoID
		}
		if todoID == nil {
			log.Printf("[lease-reaper] no todo status for project %s, skipping task %s", task.ProjectID, task.ID)
			continue
		}

		if err := r.taskMover.MoveTask(sysCtx, task.ID, MoveTaskInput{StatusID: todoID}); err != nil {
			log.Printf("[lease-reaper] failed to move task %s to todo: %v", task.ID, err)
			continue
		}

		r.logLeaseActivity(sysCtx, task, activityAction)
		r.notifyAssignee(ctx, task)
		pkgmetrics.RecordLeaseRelease(task.ProjectID.String())
		moved++
	}
	return moved
}

// findTodoStatusID returns the ID of the first todo-category status for the project,
// or nil if no todo status exists.
func (r *checkoutLeaseReaper) findTodoStatusID(ctx context.Context, projectID uuid.UUID) *uuid.UUID {
	statuses, err := r.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		log.Printf("[lease-reaper] cannot list statuses for project %s: %v", projectID, err)
		return nil
	}
	for i := range statuses {
		if statuses[i].Category == domain.StatusCategoryTodo {
			id := statuses[i].ID
			return &id
		}
	}
	return nil
}

// postSystemComment writes an audit comment on the task explaining the auto-release.
func (r *checkoutLeaseReaper) postSystemComment(ctx context.Context, task *domain.Task, body string) {
	if r.commentRepo == nil {
		return
	}
	comment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		Body:       body,
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		CreatedAt:  timeNow(),
	}
	if err := r.commentRepo.Create(ctx, comment); err != nil {
		log.Printf("[lease-reaper] warning: failed to post comment on task %s: %v", task.ID, err)
	}
}

// logLeaseActivity records a checkout/lease outcome to the workspace
// activity log instead of the task's comment thread (audit §1.2). Mirrors
// taskService.logActivity's shape (resolve workspace_id from the project,
// fail closed if it cannot), kept as its own small copy here rather than a
// shared helper because the reaper and taskService have deliberately
// different, narrow dependency sets (see leaseTaskMover/midPipelineSource
// above) and a shared helper would need to widen one of them to match the
// other for no real gain.
func (r *checkoutLeaseReaper) logLeaseActivity(ctx context.Context, task *domain.Task, action string) {
	if r.activityRepo == nil || r.projectRepo == nil {
		return
	}
	var wsID uuid.UUID
	if proj, err := r.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
		wsID = proj.WorkspaceID
	}
	if wsID == uuid.Nil {
		log.Printf("[lease-reaper] WARNING: could not resolve workspace_id for project %s, skipping activity log", task.ProjectID)
		return
	}

	payload := map[string]interface{}{}
	if task.CheckedOutBy != nil {
		payload["previous_holder"] = task.CheckedOutBy.String()
	}
	changesJSON, _ := json.Marshal(payload)
	entry := &domain.ActivityLog{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		EntityType:  "task",
		EntityID:    task.ID,
		Action:      action,
		ActorID:     uuid.Nil,
		ActorType:   domain.ActorTypeSystem,
		Changes:     changesJSON,
		CreatedAt:   timeNow(),
	}
	if err := r.activityRepo.Create(ctx, entry); err != nil {
		log.Printf("[lease-reaper] WARNING: failed to log %s for task %s: %v", action, task.ID, err)
	}
}

// notifyAssignee sends a task.lease_expired event to the agent that held the lease.
func (r *checkoutLeaseReaper) notifyAssignee(ctx context.Context, task *domain.Task) {
	if r.agentNotifySvc == nil || task.AssigneeID == nil || task.AssigneeType != domain.AssigneeTypeAgent {
		return
	}
	r.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
		EventType: "task.lease_expired",
		TaskID:    task.ID,
		Payload: map[string]any{
			"task_id":    task.ID.String(),
			"task_title": task.Title,
			"reason":     "checkout_ttl_expired",
		},
	})
}

// parkMonitorLabel is the label a parked task must carry to be woken again by
// MonitorPromotionService. It is not decoration: that sweeper's query filters on
// this exact string (and re-checks it in Go), so a park without it has no
// wake-up path at all and the task sleeps until a human happens to look.
const parkMonitorLabel = "kind:monitor"

// handBack routes each stale unleased task to the destination its project asks
// for: parked in backlog with a due_date (mid_pipeline.auto_park_stalled), or
// returned to todo as before.
//
// The choice is made per task rather than once per sweep because a single sweep
// spans every project in the workspace, and the flag is per-project. Configs are
// cached for the duration of the sweep so a large batch does not re-read the
// same project's rules once per task.
func (r *checkoutLeaseReaper) handBack(ctx context.Context, tasks []domain.Task) int {
	if len(tasks) == 0 {
		return 0
	}

	cfgCache := make(map[uuid.UUID]*domain.MidPipelineConfig)
	var toPark, toTodo []domain.Task

	for i := range tasks {
		task := tasks[i]
		cfg, ok := cfgCache[task.ProjectID]
		if !ok {
			cfg = r.midPipelineFor(ctx, task.ProjectID)
			cfgCache[task.ProjectID] = cfg
		}
		if cfg.ParkStalled() {
			toPark = append(toPark, task)
			continue
		}
		toTodo = append(toTodo, task)
	}

	moved := r.returnToTodo(ctx, toTodo, activityCheckoutUnleasedReturned)
	for i := range toPark {
		cfg := cfgCache[toPark[i].ProjectID]
		if r.parkTask(ctx, &toPark[i], cfg.AutoParkDue()) {
			moved++
		}
	}
	return moved
}

// midPipelineFor reads a project's mid-pipeline config. A missing service, a
// read error and an absent block all answer nil, which every accessor reads as
// "off" — so an unreadable config leaves the pre-existing behaviour in place
// rather than starting to park cards on a guess.
func (r *checkoutLeaseReaper) midPipelineFor(ctx context.Context, projectID uuid.UUID) *domain.MidPipelineConfig {
	if r.rulesSvc == nil {
		return nil
	}
	wfResp, err := r.rulesSvc.GetProjectWorkflowRules(ctx, projectID, nil)
	if err != nil || wfResp == nil {
		if err != nil {
			log.Printf("[lease-reaper] cannot read workflow rules for project %s, not parking: %v", projectID, err)
		}
		return nil
	}
	return wfResp.MidPipeline
}

// parkTask moves a stalled task to backlog with a due_date and the kind:monitor
// label. Returns true when the task was actually parked.
//
// The alarm is armed BEFORE the move, deliberately. Parking first and arming
// second would leave a window — and, on any failure of the second step, a
// permanent state — in which the card sits in backlog with no due_date and no
// label: invisible to the agent feed, invisible to monitor-promotion, and
// therefore asleep until a human finds it. Measured cost of exactly that
// half-park elsewhere: nine days. Arming first is the strictly safer order,
// because a task that is armed but not yet moved is simply a task in_progress
// with a due_date, which harms nothing and will be retried on the next tick.
func (r *checkoutLeaseReaper) parkTask(ctx context.Context, task *domain.Task, dueHours int) bool {
	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)

	backlogID := r.findStatusIDByCategory(sysCtx, task.ProjectID, domain.StatusCategoryBacklog)
	if backlogID == nil {
		// No backlog column in this project — fall back to the old behaviour
		// rather than skipping the task, so a project that opts into parking but
		// has no backlog status still gets its stalled cards handed back instead
		// of left in_progress forever.
		log.Printf("[lease-reaper] project %s has no backlog status, returning task %s to todo instead of parking",
			task.ProjectID, task.ID)
		return r.returnToTodo(ctx, []domain.Task{*task}, activityCheckoutUnleasedReturned) == 1
	}

	// Arm only if not already armed. The alarm is (label + a due_date still in the
	// future); when both already hold, this is a retry of a park whose MoveTask
	// failed, and rewriting the same alarm buys nothing while doing real harm:
	// each rewrite pushes due_date another dueHours out, so the wake-up the park
	// depends on recedes once per tick and can never arrive. Measured on prod
	// 2026-09-06, #2921ff07: 145 rewrites, due_date sliding to now+24h every 60s.
	//
	// A previously-fired alarm (due_date in the past) is deliberately NOT treated
	// as armed, so a card that parked, woke, and stalled again is re-armed with a
	// fresh deadline rather than inheriting a stale one.
	needsLabel := !containsInStringArray(task.Labels, parkMonitorLabel)
	alreadyArmed := !needsLabel && task.DueDate != nil && task.DueDate.After(timeNow())

	if !alreadyArmed {
		due := timeNow().Add(time.Duration(dueHours) * time.Hour)
		labels := task.Labels
		if needsLabel {
			labels = append(append([]string{}, labels...), parkMonitorLabel)
		}

		updated := *task
		updated.DueDate = &due
		updated.Labels = labels
		if err := r.taskRepo.Update(sysCtx, &updated); err != nil {
			log.Printf("[lease-reaper] failed to arm park alarm on task %s, leaving it in_progress: %v", task.ID, err)
			return false
		}
	}

	if err := r.taskMover.MoveTask(sysCtx, task.ID, MoveTaskInput{StatusID: backlogID, Source: "stall_park"}); err != nil {
		log.Printf("[lease-reaper] failed to park task %s in backlog: %v", task.ID, err)
		return false
	}

	r.postSystemComment(sysCtx, task, fmt.Sprintf(parkedCommentFmt, dueHours))
	r.notifyAssignee(ctx, task)
	pkgmetrics.RecordLeaseRelease(task.ProjectID.String())
	return true
}

// findStatusIDByCategory returns the ID of the project's first status in the
// given category, or nil when the project has none.
func (r *checkoutLeaseReaper) findStatusIDByCategory(ctx context.Context, projectID uuid.UUID, cat domain.StatusCategory) *uuid.UUID {
	statuses, err := r.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		log.Printf("[lease-reaper] cannot list statuses for project %s: %v", projectID, err)
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
