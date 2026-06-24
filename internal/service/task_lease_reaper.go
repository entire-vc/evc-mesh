package service

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
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
}

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
	if len(tasks) == 0 {
		return 0, nil
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

		r.postSystemComment(sysCtx, task)
		r.notifyAssignee(ctx, task)
		moved++
	}
	return moved, nil
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
func (r *checkoutLeaseReaper) postSystemComment(ctx context.Context, task *domain.Task) {
	if r.commentRepo == nil {
		return
	}
	comment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		Body:       "🔄 Checkout lease expired without heartbeat — задача возвращена в todo, слот ёмкости освобождён.",
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
