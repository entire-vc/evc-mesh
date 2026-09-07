package service

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// This sweeper used to have `kind:monitor` as its ENTRY CONDITION — the label the lease
// reaper's auto-park writes (checkoutLeaseReaper.parkTask, which still writes it as
// `parkMonitorLabel`). That is the defect #559270cf fixed: a due_date only woke a card
// parked by that one mechanism, so every card a human or agent parked with a date and a
// different label — or no label — carried an alarm nothing listened to. The constant
// that encoded it is gone rather than left unused, so nothing here reads as if the
// label still selects anything.

// absoluteNoPromoteLabels mirrors ABSOLUTE_NO_PROMOTE_LABELS in
// bob/scripts/mesh-intake-sweep.py — the labels whose park a passed due_date may NOT
// override. Two groups, for two different reasons:
//
//   - freeze / no-intake-promote / no-promote — an explicit human "stay in backlog"
//     (task 383dd12a, Pavel's «из беклога не доставать»). A date must never outrank a
//     person: the park has no expiry unless the person who set it states one.
//   - golden / eval-harness — Agent-Eval fixtures. Promoting one makes an agent
//     decompose and execute a synthetic fixture and pollutes the eval counts; it has
//     recurred three times at real cost (#9f50def6, #0df1f585, #1fa93c49).
//
// This set matters far more now than it did before #559270cf. While the sweeper only
// looked at kind:monitor cards it could not have reached a frozen card by accident;
// widening the candidate query to every dated backlog card is exactly what puts a
// human freeze in its path, so the guard has to be widened in the same change, not
// after it.
//
// KNOWN GAP, deliberate: mesh-intake-sweep.py licenses ONE escape from this set — a
// `wake:<type>` label other than wake:manual, whose condition is spelled out in a
// "wake-condition audit" comment (docs/wake-condition-schema.md). That is not ported
// here. Porting it half-way is the danger: for types like all_children_closed the
// due_date is only a re-check TRIGGER and the real condition lives in the comment, so
// a server that honoured the label without parsing the comment would promote on the
// date alone — the opposite of what the schema means. Not porting it costs nothing
// today: the Python sweep is still live and still performs that override, so a
// wake-labelled frozen card is promoted by it exactly as before. Closing this belongs
// with the sweep's retirement (#00327dc6 / #56ec28e8), which is where the comment
// parsing has to land anyway.
var absoluteNoPromoteLabels = map[string]struct{}{
	"freeze": {}, "no-intake-promote": {}, "no-promote": {},
	"golden": {}, "eval-harness": {},
}

// hasAbsoluteNoPromoteLabel returns the first freeze-class label found, if any.
func hasAbsoluteNoPromoteLabel(labels []string) (string, bool) {
	for _, l := range labels {
		if _, ok := absoluteNoPromoteLabels[l]; ok {
			return l, true
		}
	}
	return "", false
}

// MonitorPromotionService finds backlog tasks whose due_date has passed and promotes
// the eligible ones back to todo — the time-based counterpart to the event-based
// dependency-unblock auto-transition (auto_transition.go tryUnblockTask).
type MonitorPromotionService interface {
	// SweepDueBacklogTasks promotes every eligible backlog task whose due_date has
	// passed and returns how many moved.
	SweepDueBacklogTasks(ctx context.Context) (int, error)
}

type monitorPromotionService struct {
	taskRepo     repository.TaskRepository
	statusRepo   repository.TaskStatusRepository
	commentRepo  repository.CommentRepository
	depRepo      repository.TaskDependencyRepository
	activityRepo repository.ActivityLogRepository
	taskMover    leaseTaskMover
}

// NewMonitorPromotionService constructs a MonitorPromotionService.
// commentRepo may be nil (audit comment skipped). depRepo may be nil, in which case
// the blocker guard cannot run and NOTHING is promoted — see skipReason. activityRepo
// may be nil, in which case the demotion-park guard fails closed (treats every
// no-blocker card as possibly-demoted and skips it) rather than guessing.
func NewMonitorPromotionService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	commentRepo repository.CommentRepository,
	depRepo repository.TaskDependencyRepository,
	activityRepo repository.ActivityLogRepository,
	taskSvc TaskService,
) MonitorPromotionService {
	return &monitorPromotionService{
		taskRepo:     taskRepo,
		statusRepo:   statusRepo,
		commentRepo:  commentRepo,
		depRepo:      depRepo,
		activityRepo: activityRepo,
		taskMover:    taskSvc,
	}
}

// SweepDueBacklogTasks finds backlog tasks whose due_date has passed and moves each
// eligible one to its project's first todo-category status. Returns the number promoted.
func (s *monitorPromotionService) SweepDueBacklogTasks(ctx context.Context) (int, error) {
	tasks, err := s.taskRepo.FindDueBacklogTasks(ctx)
	if err != nil {
		return 0, err
	}
	if len(tasks) == 0 {
		return 0, nil
	}

	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)

	// Cache todo status IDs per project to avoid repeated status list fetches.
	todoStatusCache := make(map[uuid.UUID]*uuid.UUID)

	// projectID -> lowercased-trimmed status name -> category, for the demotion-park
	// guard (wasDeliberatelyParkedFromBacklog). Built lazily, once per project touched
	// this tick — mirrors backlog_promotion_advisory.go's own nameCatCache.
	nameCatCache := make(map[uuid.UUID]map[string]domain.StatusCategory)

	var promoted int
	for i := range tasks {
		task := &tasks[i]

		if reason := s.skipReason(sysCtx, task, nameCatCache); reason != "" {
			log.Printf("[monitor-promotion] task=%s NOT promoted: %s", task.ID, reason)
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

// skipReason returns the reason this due backlog task must NOT be promoted, or "" when
// it is eligible. Fail-CLOSED throughout: any guard that cannot be evaluated returns a
// reason rather than a guess, because backlog is the safe resting state and the whole
// cost of a wrong skip is one more sweep tick, while the cost of a wrong promotion is a
// card pulled out from under a human freeze or an agent that is genuinely blocked.
//
// The three status-flag guards are not defensive padding. A system actor is FORBIDDEN
// to move a human_gated task to backlog/done/cancelled and forbidden to move a shipped
// task anywhere but done (see MoveTask), so selecting one here produces a move that
// fails on every single tick, forever, and crowds the log with one retry per minute —
// measured on prod 2026-09-06 on the lease reaper, where #2921ff07 failed to park 145
// times and was that reaper's only output while it spun. Refusing to select such a card
// is not the same as trying and handling the error: only the former terminates.
func (s *monitorPromotionService) skipReason(
	ctx context.Context,
	task *domain.Task,
	nameCatCache map[uuid.UUID]map[string]domain.StatusCategory,
) string {
	// 1. Explicit human freeze / eval fixture — a date does not outrank a person.
	if label, frozen := hasAbsoluteNoPromoteLabel(task.Labels); frozen {
		return fmt.Sprintf("freeze-class label %q (a passed due_date does not override it)", label)
	}

	// 2. human_gate armed — a system actor may not move this card, and the card's
	// wake-up path IS the human it is waiting on, so the gate already serves as the park.
	if task.HumanGate {
		return "human_gate armed (only a user may move it; the gate is the park)"
	}

	// 3. Shipped — terminal by declaration; a system actor may move it only to done.
	if task.IsShipped {
		return "is_shipped (a system actor may not move a shipped task off done)"
	}

	// 4. Open blockers. `blocks` only — deliberately narrower than the advisory
	// service's unfiltered dependency read, and matching hasUnresolvedBlockers, which
	// is the definition auto_transition.go uses for "still blocked". relates_to and
	// is_child_of are not blocking relationships and must not hold a woken card down.
	blocked, hasBlocksEdge, err := s.blockerStatus(ctx, task.ID)
	if err != nil {
		return fmt.Sprintf("blocker check failed, fail-closed: %v", err)
	}
	if blocked {
		return "has open blocks dependencies"
	}

	// 5. Demotion-into-backlog guard (#559270cf amendment, #1eb4fd7d) — a card whose
	// most recent status move DEMOTED it into backlog is a deliberate park, and
	// "no open blocks dependencies" is vacuously true for one that never had any
	// (#b832d451: the sweep once undid such a park in 26 minutes by reading exactly
	// that vacuous truth as "ready").
	//
	// Gated on hasBlocksEdge being FALSE — mirroring
	// backlog_promotion_advisory.go's own dep_ids-empty gate on this same check
	// (#bbf3db92): a card that HAS (or HAD) a `blocks` edge is judged by whether that
	// edge is now clear (guard 4, above), never by its move history — the edge
	// clearing IS the informative event this sweep exists to catch when the
	// event-triggered auto-transition (auto_transition.go tryUnblockTask) missed it
	// because the edge was added, or the blocker closed, before the card was parked.
	// A demotion-check running unconditionally would silently re-park that class of
	// card forever, exchanging a fixed defect for a differently-shaped one.
	if !hasBlocksEdge {
		if s.activityRepo == nil {
			// Not wired. Same fail-closed reasoning as the depRepo==nil branch above:
			// "cannot look" must not read as "looked and it was never demoted".
			return "activity log repository not wired, fail-closed"
		}
		demoted, err := wasDeliberatelyParkedFromBacklog(ctx, s.activityRepo, s.statusRepo, task, nameCatCache)
		if err != nil {
			return fmt.Sprintf("demotion check failed, fail-closed: %v", err)
		}
		if demoted {
			return "parked via demotion into backlog (no blocks dependencies ever recorded)"
		}
	}

	return ""
}

// blockerStatus reports (a) whether the task has at least one `blocks` dependency
// whose blocker has not reached a terminal (done/cancelled) category, and (b) whether
// the task has any `blocks` dependency edge at all, regardless of the blocker's state
// — the latter is what guard 5 (skipReason) needs to decide whether the
// demotion-into-backlog check applies (see its comment for why).
func (s *monitorPromotionService) blockerStatus(ctx context.Context, taskID uuid.UUID) (blocked, hasBlocksEdge bool, err error) {
	if s.depRepo == nil {
		// Not wired. "Cannot look" is not "looked and it was clear" — the one
		// mistake this whole guard exists to avoid, so it reports blocked (and, to
		// stay on the fail-closed side of guard 5 too, as if a blocks edge exists).
		return true, true, fmt.Errorf("dependency repository not wired")
	}
	deps, err := s.depRepo.ListByTask(ctx, taskID)
	if err != nil {
		return true, true, err
	}

	categories := make(map[uuid.UUID]domain.StatusCategory)
	for _, dep := range deps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		hasBlocksEdge = true
		if _, seen := categories[dep.DependsOnTaskID]; seen {
			continue
		}
		blocker, err := s.taskRepo.GetByID(ctx, dep.DependsOnTaskID)
		if err != nil {
			return true, true, err
		}
		if blocker == nil {
			// The blocker row is gone. Cannot prove it completed, so it counts open.
			return true, true, nil
		}
		status, err := s.statusRepo.GetByID(ctx, blocker.StatusID)
		if err != nil {
			return true, true, err
		}
		if status == nil {
			return true, true, nil
		}
		categories[dep.DependsOnTaskID] = status.Category
	}
	return hasUnresolvedBlockers(deps, categories), hasBlocksEdge, nil
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
// It names the label the card was parked under (or says it had none), because after
// #559270cf that is no longer a constant — the reader can no longer assume kind:monitor.
func (s *monitorPromotionService) postSystemComment(ctx context.Context, task *domain.Task) {
	if s.commentRepo == nil {
		return
	}
	parked := "без passive-wait метки"
	if label, ok := hasParkAlarmLabel(task.Labels); ok {
		parked = "метка " + label
	} else if len(task.Labels) > 0 {
		parked = "метки: " + strings.Join(task.Labels, ", ")
	}
	comment := &domain.Comment{
		ID:     uuid.New(),
		TaskID: task.ID,
		Body: "⏰ auto-unparked: due_date наступил — задача возвращена в todo (была в backlog, " +
			parked + "). Открытых `blocks` нет.",
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		CreatedAt:  timeNow(),
	}
	if err := s.commentRepo.Create(ctx, comment); err != nil {
		log.Printf("[monitor-promotion] warning: failed to post comment on task %s: %v", task.ID, err)
	}
}
