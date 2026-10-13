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

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
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

// openDependentSource is the narrow slice of the dependency repository the reaper
// needs to tell whether parking a card would strand work that waits on it.
// Nilable: a reaper built without one never asks and parks as before.
type openDependentSource interface {
	// HasOpenBlockedDependents reports whether a `blocks` dependency names
	// blockerID as the blocker of a live task that is not done/cancelled.
	HasOpenBlockedDependents(ctx context.Context, blockerID uuid.UUID) (bool, error)
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
	// dependents may be nil, in which case handBack never consults dependencies
	// and parks exactly as it did before this field existed.
	dependents openDependentSource
}

// NewCheckoutLeaseReaper constructs a CheckoutLeaseReaper.
// agentNotifySvc may be nil (notifications skipped).
// taskSvc is accepted as TaskService (which satisfies leaseTaskMover) so callers
// in main.go pass the full service without a cast.
// rulesSvc may be nil, in which case no project ever parks and every sweep hands
// tasks back to todo exactly as before this option existed.
// activityRepo/projectRepo may be nil, in which case logLeaseActivity is a no-op
// (the pre-existing behavior for every caller before this pair existed).
// dependents may be nil, in which case a card blocking open work can still be
// parked (the pre-existing behaviour).
func NewCheckoutLeaseReaper(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	commentRepo repository.CommentRepository,
	taskSvc TaskService,
	agentNotifySvc AgentNotifyService,
	rulesSvc midPipelineSource,
	activityRepo repository.ActivityLogRepository,
	projectRepo repository.ProjectRepository,
	dependents openDependentSource,
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
		dependents:     dependents,
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
	return r.handBack(ctx, tasks, olderThan), nil
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
	parkedCommentFmt = "🅿️ The task sat in in_progress **without a checkout** and without signs of work " +
		"(no comment, artifact, VCS link or status update within the idle window; the last " +
		"comment has no line starting with `⏳ WAIT`/`↻ NEXT`, or it is older than 48 h; " +
		"no open task depends on it) — **parked in backlog** " +
		"with a `due_date` in %d h and the `kind:monitor` label.\n\n" +
		"Why backlog and not todo: a return to todo puts the card straight back into the feed, " +
		"and a stuck card is fed to the same lane again and again. The park breaks the loop, " +
		"and the `kind:monitor` label is what wakes the card: monitor-promotion lifts it " +
		"back to todo when the `due_date` arrives. Without the label a park has no way to wake at all.\n\n" +
		"If the work is still going on, run `checkout_task` again: without a checkout the card is not held."
)

// returnToTodo moves each task to its project's first todo status, logging to
// activity_log and notifying as it goes. Shared by both sweeps so they cannot
// drift in how they hand a task back. A task that cannot be moved is skipped
// and logged, never silently dropped.
func (r *checkoutLeaseReaper) returnToTodo(ctx context.Context, tasks []domain.Task, activityAction string, grace ...time.Duration) int {
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

		if err := r.taskMover.MoveTask(sysCtx, task.ID, reaperMoveInput(task, todoID, activityAction, grace...)); err != nil {
			var conflict *domain.TaskConflict
			if errors.As(err, &conflict) {
				continue // renewed/changed candidates are normal skips
			}
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
func (r *checkoutLeaseReaper) postSystemComment(ctx context.Context, task *domain.Task, code string, params map[string]any, body string) {
	if r.commentRepo == nil {
		return
	}
	comment := &domain.Comment{
		ID:           uuid.New(),
		TaskID:       task.ID,
		Body:         body,
		AuthorID:     uuid.Nil,
		AuthorType:   domain.ActorTypeSystem,
		SystemCode:   SysCodePtr(code),
		SystemParams: SysParamsJSON(params),
		CreatedAt:    timeNow(),
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
	if mover, ok := r.taskMover.(interface{ DurableTaskTransitions() bool }); ok && mover.DurableTaskTransitions() {
		return
	}
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

	mode := "unleased"
	if action == activityCheckoutLeaseExpired {
		mode = "expired"
	}
	payload := map[string]interface{}{"source": "lease_reaper." + mode, "reason": action, "old_version": task.Version, "new_version": task.Version + 1, "lease_generation": task.CheckoutGeneration}
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
func (r *checkoutLeaseReaper) handBack(ctx context.Context, tasks []domain.Task, grace ...time.Duration) int {
	if len(tasks) == 0 {
		return 0
	}

	cfgCache := make(map[uuid.UUID]*domain.MidPipelineConfig)
	var toPark, toTodo []domain.Task
	var moved int

	for i := range tasks {
		task := tasks[i]
		cfg, ok := cfgCache[task.ProjectID]
		if !ok {
			cfg = r.midPipelineFor(ctx, task.ProjectID)
			cfgCache[task.ProjectID] = cfg
		}
		if cfg.ParkStalled() {
			// A move handed to the card's own executor by someone else is not an
			// abandoned card: parking it strands the executor's next step in
			// backlog where nothing feeds it. Give it back to them instead.
			if handoff := r.handoffComment(ctx, &task); handoff != nil {
				if r.returnToTodo(ctx, []domain.Task{task}, activityCheckoutUnleasedReturned, grace...) == 1 {
					r.postSystemComment(actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem), &task,
						SysCodeLeaseHandoff, map[string]any{"comment_id": handoff.ID.String()},
						fmt.Sprintf(handoffCommentFmt, handoff.ID))
					moved++
				}
				continue
			}
			// A card that open work formally waits on is not parked either:
			// backlog takes it out of every feed, and the dependent then waits on
			// a card nobody is working (#53f30091 blocked #14580276 for ~16 h on
			// 11.10). Hand it back to its own assignee's todo instead.
			//
			// Unreadable dependency state decides nothing: the card stays where it
			// is and the next tick (60 s) asks again. Parking on a guess may strand
			// a dependent for a day; returning on a guess turns a DB outage into a
			// workspace-wide re-feed of every stale card.
			blocking, err := r.blocksOpenDependent(ctx, &task)
			if err != nil {
				log.Printf("[lease-reaper] cannot read dependents of task %s, leaving it for the next tick: %v", task.ID, err)
				continue
			}
			if blocking {
				if r.returnToTodo(ctx, []domain.Task{task}, activityCheckoutUnleasedReturned, grace...) == 1 {
					r.postSystemComment(actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem), &task, SysCodeLeaseBlockerReturned, nil, blockerReturnedComment)
					moved++
				}
				continue
			}
			toPark = append(toPark, task)
			continue
		}
		toTodo = append(toTodo, task)
	}

	moved += r.returnToTodo(ctx, toTodo, activityCheckoutUnleasedReturned, grace...)
	for i := range toPark {
		cfg := cfgCache[toPark[i].ProjectID]
		if r.parkTask(ctx, &toPark[i], cfg.AutoParkDue(), grace...) {
			moved++
		}
	}
	return moved
}

// blockerReturnedComment explains, on the card itself, why a stalled card went
// back to todo instead of backlog. Without it the lane and its lead see a card
// bounce back into the feed with no reason. A repeat is bounded by the feed's
// own per-card refeed cap, which escalates the card to the lead.
const blockerReturnedComment = "↩️ The card sat in in_progress without a checkout and without signs of work, " +
	"but is **not parked**: an open task waits on it through a `blocks` dependency, and nobody feeds " +
	"a card from backlog. Returned to todo for the same assignee. If you are waiting for an external event, " +
	"leave a `⏳ WAIT …` line in the last comment."

// blocksOpenDependent reports whether parking task would strand an open task
// that has it as a `blocks` dependency. With no source wired it answers false
// (park as before).
func (r *checkoutLeaseReaper) blocksOpenDependent(ctx context.Context, task *domain.Task) (bool, error) {
	if r.dependents == nil {
		return false, nil
	}
	return r.dependents.HasOpenBlockedDependents(ctx, task.ID)
}

// handoffScanLimit bounds how many recent comments the handoff decision reads.
const handoffScanLimit = 30

// handoffCommentFmt is the system note left when a handoff is returned to todo.
// Its `handoff:<comment id>` token is the loop guard: the same handoff is never
// returned twice, the second time the card parks like any abandoned one.
const handoffCommentFmt = "↩️ The turn was handed to the card's assignee (the last live comment is not from the assignee): " +
	"the card is back in todo for the same assignee, not parked in backlog. The same handoff is not returned to todo twice. [handoff:%s]"

// handoffComment returns the newest live comment when it was written by someone
// other than the task's assignee, i.e. the turn now belongs to the assignee.
// System and informational comments are skipped. It answers nil (park as an
// abandoned card) when the assignee had the last word, there is no assignee or no
// such comment, the comment list cannot be read, or this very handoff was already
// returned once (a system note carrying its id exists).
func (r *checkoutLeaseReaper) handoffComment(ctx context.Context, task *domain.Task) *domain.Comment {
	if r.commentRepo == nil || task.AssigneeID == nil {
		return nil
	}
	// ListByTask always returns oldest-first whatever SortDir says, so walk the
	// pages from the last one backwards, newest comment first, until the live
	// comment is found. Internal comments count: a handoff can be internal.
	// A handoff note is always newer than the comment it answers, so every note
	// that could match is seen before the walk reaches that comment.
	filter := repository.CommentFilter{IncludeInternal: true}
	params := pagination.Params{Page: 1, PageSize: handoffScanLimit}
	page, err := r.commentRepo.ListByTask(ctx, task.ID, filter, params)
	if err != nil || page == nil {
		return nil
	}
	notes := make([]string, 0, 1)
	for pageNo := max(page.TotalPages, 1); pageNo >= 1; pageNo-- {
		if pageNo != page.Page {
			params.Page = pageNo
			if page, err = r.commentRepo.ListByTask(ctx, task.ID, filter, params); err != nil || page == nil {
				return nil
			}
		}
		for i := len(page.Items) - 1; i >= 0; i-- {
			c := &page.Items[i]
			if c.AuthorType == domain.ActorTypeSystem {
				notes = append(notes, c.Body)
				// Code first: a note written with system_code carries the
				// handled comment id in params; the body token above stays as
				// the reader of notes written before the code existed.
				if id := leaseHandoffParamID(c); id != "" {
					notes = append(notes, "[handoff:"+id+"]")
				}
				continue
			}
			if commentIsInformational(c.Metadata) {
				continue
			}
			if c.AuthorID == *task.AssigneeID {
				return nil
			}
			token := "[handoff:" + c.ID.String() + "]"
			for _, body := range notes {
				if strings.Contains(body, token) {
					return nil
				}
			}
			return c
		}
	}
	return nil
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
// Alarm, status and guarded lease decision are committed together.
func (r *checkoutLeaseReaper) parkTask(ctx context.Context, task *domain.Task, dueHours int, grace ...time.Duration) bool {
	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)

	backlogID := r.findStatusIDByCategory(sysCtx, task.ProjectID, domain.StatusCategoryBacklog)
	if backlogID == nil {
		// No backlog column in this project — fall back to the old behaviour
		// rather than skipping the task, so a project that opts into parking but
		// has no backlog status still gets its stalled cards handed back instead
		// of left in_progress forever.
		log.Printf("[lease-reaper] project %s has no backlog status, returning task %s to todo instead of parking",
			task.ProjectID, task.ID)
		return r.returnToTodo(ctx, []domain.Task{*task}, activityCheckoutUnleasedReturned, grace...) == 1
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

	input := reaperMoveInput(task, backlogID, activityCheckoutUnleasedReturned, grace...)
	if !alreadyArmed {
		due := timeNow().Add(time.Duration(dueHours) * time.Hour)
		labels := task.Labels
		if needsLabel {
			labels = append(append([]string{}, labels...), parkMonitorLabel)
		}

		input.AlarmDue, input.AlarmLabels = &due, labels
	}

	if err := r.taskMover.MoveTask(sysCtx, task.ID, input); err != nil {
		var conflict *domain.TaskConflict
		if errors.As(err, &conflict) {
			return false
		}
		log.Printf("[lease-reaper] failed to park task %s in backlog: %v", task.ID, err)
		return false
	}

	r.logLeaseActivity(sysCtx, task, activityCheckoutUnleasedReturned)
	r.postSystemComment(sysCtx, task, SysCodeLeaseParked, map[string]any{"due_hours": dueHours}, fmt.Sprintf(parkedCommentFmt, dueHours))
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

// Candidate snapshots authorize nothing: the repository repeats all guards at
// the write. The version also catches comments/artifacts committed since SELECT.
func reaperMoveInput(task *domain.Task, status *uuid.UUID, action string, grace ...time.Duration) MoveTaskInput {
	mode := "unleased"
	if action == activityCheckoutLeaseExpired {
		mode = "expired"
	}
	quiet := DefaultUnleasedGrace
	if len(grace) > 0 {
		quiet = grace[0]
	}
	return MoveTaskInput{StatusID: status, ExpectedVersion: &task.Version, ExpectedStatusID: &task.StatusID,
		Source: "lease_reaper." + mode, Reason: action,
		Reaper: &domain.ReaperExpectation{Mode: mode, Generation: task.CheckoutGeneration, Token: task.CheckoutToken, ExpiresAt: task.CheckoutExpires, QuietGrace: quiet}}
}

// leaseHandoffParamID returns system_params.comment_id of a lease_handoff note.
func leaseHandoffParamID(c *domain.Comment) string {
	if c.SystemCode == nil || *c.SystemCode != SysCodeLeaseHandoff || len(c.SystemParams) == 0 {
		return ""
	}
	var p struct {
		CommentID string `json:"comment_id"`
	}
	if err := json.Unmarshal(c.SystemParams, &p); err != nil {
		return ""
	}
	return p.CommentID
}
