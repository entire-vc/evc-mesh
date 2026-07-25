package service

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// monitorLabelKindMonitor is the label that marks a backlog task as a passive-wait
// "monitor" park (CLAUDE-workflow-reference.md §0m) rather than an ordinary backlog
// item or a freeze/no-promote park. Only tasks carrying this label are eligible for
// due_date-triggered auto-promotion.
const monitorLabelKindMonitor = "kind:monitor"

// MonitorPromotionService finds backlog tasks parked with kind:monitor whose
// due_date has passed and promotes them back to todo, mirroring the existing
// dependency-unblock auto-transition (auto_transition.go tryUnblockTask) but on a
// time-based trigger instead of an event-based one.
type MonitorPromotionService interface {
	SweepDueMonitorTasks(ctx context.Context) (int, error)
}

type monitorPromotionService struct {
	taskRepo    repository.TaskRepository
	statusRepo  repository.TaskStatusRepository
	commentRepo repository.CommentRepository
	taskMover   leaseTaskMover
}

// NewMonitorPromotionService constructs a MonitorPromotionService.
// commentRepo may be nil (audit comment skipped).
func NewMonitorPromotionService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	commentRepo repository.CommentRepository,
	taskSvc TaskService,
) MonitorPromotionService {
	return &monitorPromotionService{
		taskRepo:    taskRepo,
		statusRepo:  statusRepo,
		commentRepo: commentRepo,
		taskMover:   taskSvc,
	}
}

// SweepDueMonitorTasks finds backlog+kind:monitor tasks whose due_date has passed
// and moves each one to the project's first todo-category status. Returns the
// number of tasks promoted.
func (s *monitorPromotionService) SweepDueMonitorTasks(ctx context.Context) (int, error) {
	tasks, err := s.taskRepo.FindDueMonitorBacklogTasks(ctx)
	if err != nil {
		return 0, err
	}
	if len(tasks) == 0 {
		return 0, nil
	}

	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)

	// Cache todo status IDs per project to avoid repeated status list fetches.
	todoStatusCache := make(map[uuid.UUID]*uuid.UUID)

	var promoted int
	for i := range tasks {
		task := &tasks[i]

		// Negative-control double-check: the SQL filter already scopes to
		// kind:monitor, but re-verify in Go so a future query change can't
		// silently widen the blast radius to unrelated backlog parks.
		if !containsInStringArray(task.Labels, monitorLabelKindMonitor) {
			continue
		}

		todoID, ok := todoStatusCache[task.ProjectID]
		if !ok {
			todoID = s.findTodoStatusID(sysCtx, task.ProjectID)
			todoStatusCache[task.ProjectID] = todoID
		}
		if todoID == nil {
			log.Printf("[monitor-promotion] no todo status for project %s, skipping task %s", task.ProjectID, task.ID)
			continue
		}

		if err := s.taskMover.MoveTask(sysCtx, task.ID, MoveTaskInput{StatusID: todoID, Source: "monitor_due_sweep"}); err != nil {
			log.Printf("[monitor-promotion] failed to move task %s to todo: %v", task.ID, err)
			continue
		}

		s.postSystemComment(sysCtx, task)
		promoted++
	}
	return promoted, nil
}

// findTodoStatusID returns the ID of the first todo-category status for the project,
// or nil if no todo status exists.
func (s *monitorPromotionService) findTodoStatusID(ctx context.Context, projectID uuid.UUID) *uuid.UUID {
	statuses, err := s.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		log.Printf("[monitor-promotion] cannot list statuses for project %s: %v", projectID, err)
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

// postSystemComment writes an audit comment on the task explaining the auto-unpark.
func (s *monitorPromotionService) postSystemComment(ctx context.Context, task *domain.Task) {
	if s.commentRepo == nil {
		return
	}
	comment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		Body:       "⏰ auto-unparked: due_date reached — задача возвращена в todo (была backlog + kind:monitor).",
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		CreatedAt:  timeNow(),
	}
	if err := s.commentRepo.Create(ctx, comment); err != nil {
		log.Printf("[monitor-promotion] warning: failed to post comment on task %s: %v", task.ID, err)
	}
}
