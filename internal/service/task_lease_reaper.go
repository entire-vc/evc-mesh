package service

import (
	"context"
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
}

// NewCheckoutLeaseReaper constructs a CheckoutLeaseReaper.
// agentNotifySvc may be nil (notifications skipped).
// taskSvc is accepted as TaskService (which satisfies leaseTaskMover) so callers
// in main.go pass the full service without a cast.
func NewCheckoutLeaseReaper(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	commentRepo repository.CommentRepository,
	taskSvc TaskService,
	agentNotifySvc AgentNotifyService,
) CheckoutLeaseReaper {
	return &checkoutLeaseReaper{
		taskRepo:       taskRepo,
		statusRepo:     statusRepo,
		commentRepo:    commentRepo,
		taskMover:      taskSvc,
		agentNotifySvc: agentNotifySvc,
	}
}

// SweepExpiredLeases finds in_progress tasks with expired checkout TTLs and moves
// each one back to the project's first todo status. Returns the number of tasks moved.
func (r *checkoutLeaseReaper) SweepExpiredLeases(ctx context.Context) (int, error) {
	tasks, err := r.taskRepo.FindExpiredInProgressCheckouts(ctx)
	if err != nil {
		return 0, err
	}
	return r.returnToTodo(ctx, tasks, expiredLeaseComment), nil
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
	return r.returnToTodo(ctx, tasks, unleasedComment), nil
}

const (
	expiredLeaseComment = "🔄 Checkout TTL истёк — задача возвращена в todo, слот ёмкости освобождён. " +
		"Чтобы удержать задачу дольше, зовите extend_checkout до истечения TTL (heartbeat на это не влияет)."

	unleasedComment = "🔄 Задача висела в in_progress **без чекаута** и без активности дольше допустимого — " +
		"возвращена в todo, чтобы снова попадать в подачу. Такую карточку не видит ни lease-reaper " +
		"(он ищет истёкший чекаут, а не отсутствующий), ни поллер агента (он смотрит только todo). " +
		"Если работа ещё идёт — сделайте checkout_task заново: без чекаута карточка не удерживается."
)

// returnToTodo moves each task to its project's first todo status, commenting and
// notifying as it goes. Shared by both sweeps so they cannot drift in how they
// hand a task back. A task that cannot be moved is skipped and logged, never
// silently dropped.
func (r *checkoutLeaseReaper) returnToTodo(ctx context.Context, tasks []domain.Task, comment string) int {
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

		r.postSystemComment(sysCtx, task, comment)
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
