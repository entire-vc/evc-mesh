package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// defaultTimeoutDecisionRecorder is the narrow slice of CommentService the sweep needs —
// same narrowing style as leaseTaskMover in task_lease_reaper.go, so a unit test can
// stand this service up without a full CommentService mock.
type defaultTimeoutDecisionRecorder interface {
	RecordHumanGateDecision(ctx context.Context, input domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error)
}

// defaultTimeoutTaskMover is the narrow slice of TaskService the sweep needs to look up
// a task's current status/project and return it to todo.
type defaultTimeoutTaskMover interface {
	MoveTask(ctx context.Context, taskID uuid.UUID, input MoveTaskInput) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error)
}

// HumanGateDefaultTimeoutService finds gates whose gate_deadline has passed with no
// answer and applies each one's own stated recommended_default (task #060ccaae):
// records it as a human_gate_decisions row (provenance=default_applied), releases the
// gate as a CONSEQUENCE of that write (same contract as every other decision — the gate
// never clears on its own, only as a side effect of a recorded answer), returns the task
// to todo for its assignee, and broadcasts a workspace notification (reaching Pavel, and
// the gate's own author when they are a subscribed user) that the default fired.
//
// Deliberately NOT the same mechanism as HumanGateSoftTimeoutService: that one only
// unfreezes a soft gate after a fixed window, without resolving the underlying question.
// See FindSoftTimedOutGates' doc (task_repo.go) for how the two divide the gate
// population so neither races the other on the same row.
type HumanGateDefaultTimeoutService interface {
	SweepExpiredDefaultGates(ctx context.Context) (int, error)
}

type humanGateDefaultTimeoutService struct {
	taskRepo    repository.TaskRepository
	statusRepo  repository.TaskStatusRepository
	projectRepo repository.ProjectRepository
	decisions   defaultTimeoutDecisionRecorder
	taskMover   defaultTimeoutTaskMover
	notifySvc   NotificationService
}

// NewHumanGateDefaultTimeoutService constructs a HumanGateDefaultTimeoutService.
// projectRepo/statusRepo/notifySvc may be nil — the decision is still recorded and the
// gate still released either way; only the todo-return and/or the broadcast are skipped,
// matching the optional-dependency posture the rest of this package uses (e.g.
// commentService's own notifySvc/projectRepo).
func NewHumanGateDefaultTimeoutService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	projectRepo repository.ProjectRepository,
	decisions defaultTimeoutDecisionRecorder,
	taskMover defaultTimeoutTaskMover,
	notifySvc NotificationService,
) HumanGateDefaultTimeoutService {
	return &humanGateDefaultTimeoutService{
		taskRepo:    taskRepo,
		statusRepo:  statusRepo,
		projectRepo: projectRepo,
		decisions:   decisions,
		taskMover:   taskMover,
		notifySvc:   notifySvc,
	}
}

// SweepExpiredDefaultGates applies every candidate the repository hands it. One
// candidate's failure (logged, not returned) never stops the rest — the same
// best-effort-per-row posture as HumanGateSoftTimeoutService.SweepExpiredSoftGates,
// because a single bad row must not leave every OTHER expired gate un-applied until the
// next tick just because it sits earlier in the result set.
func (s *humanGateDefaultTimeoutService) SweepExpiredDefaultGates(ctx context.Context) (int, error) {
	candidates, err := s.taskRepo.FindExpiredDefaultGates(ctx, timeNow())
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	var applied int
	for _, c := range candidates {
		if s.applyOne(ctx, c) {
			applied++
		}
	}
	return applied, nil
}

func (s *humanGateDefaultTimeoutService) applyOne(ctx context.Context, c domain.HumanGateDefaultTimeoutCandidate) bool {
	// canonical_key is scoped to this specific arm (task + armed_at), not just the task:
	// a task can be gated, defaulted, and gated again later on a genuinely new question,
	// and each arm's application must be its own distinct decision row rather than
	// colliding with — or being read as a duplicate of — an earlier one on the same task.
	canonicalKey := fmt.Sprintf("default-timeout:%s:%s", c.TaskID, c.ArmedAt.UTC().Format(time.RFC3339Nano))
	quote := c.RecommendedDefault
	_, err := s.decisions.RecordHumanGateDecision(ctx, domain.RecordHumanGateDecisionInput{
		TaskID:       c.TaskID,
		CanonicalKey: &canonicalKey,
		// DecidedBy is the gate's own author, not a system sentinel: applying the
		// default is mechanically enacting what THEY already declared they would do,
		// not a new decision the system made on their behalf.
		DecidedBy:  c.GateAuthor,
		Provenance: domain.HumanGateProvenanceDefaultApplied,
		Channel:    domain.HumanGateChannelMesh,
		Quote:      &quote,
	})
	if err != nil {
		log.Printf("[human-gate-default-timeout] WARNING: RecordHumanGateDecision on task %s failed: %v", c.TaskID, err)
		return false
	}

	task := s.returnToTodo(ctx, c.TaskID)
	s.notifyDefaultApplied(ctx, task, c)
	return true
}

// returnToTodo moves the task back to its project's todo-category status, per task
// #060ccaae's "возврат в todo исполнителю" — applying a default means the executor
// should just proceed on that basis, not keep waiting in whatever status the ask froze
// it in (often triage, from enforceBlockingTriage's own move). Returns the fetched task
// (possibly nil on any lookup failure) so the caller can reuse it for notification
// without a second round trip.
//
// A task already in a terminal or todo category is left alone: todo needs no move, and
// applying a default is not a reason to reopen work that finished or was cancelled by a
// human in the meantime (the gate does not block those transitions for a USER, only for
// an agent — see task_handler.go's PATCH {human_gate:false} guard).
func (s *humanGateDefaultTimeoutService) returnToTodo(ctx context.Context, taskID uuid.UUID) *domain.Task {
	if s.taskMover == nil {
		return nil
	}
	task, err := s.taskMover.GetByID(ctx, taskID)
	if err != nil || task == nil {
		log.Printf("[human-gate-default-timeout] WARNING: task %s lookup before todo-return failed: %v", taskID, err)
		return nil
	}
	if s.statusRepo == nil {
		return task
	}
	curStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || curStatus == nil {
		return task
	}
	switch curStatus.Category {
	case domain.StatusCategoryTodo, domain.StatusCategoryDone, domain.StatusCategoryCancelled:
		return task
	}
	todoID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryTodo)
	if err != nil || todoID == uuid.Nil {
		return task
	}
	if err := s.taskMover.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &todoID}); err != nil {
		log.Printf("[human-gate-default-timeout] WARNING: move task %s to todo failed: %v", taskID, err)
	}
	return task
}

// notifyDefaultApplied broadcasts the "default fired" event workspace-wide (no
// TargetUserID/RelevantUserIDs — the same shape enforceBlockingTriage's
// task.blocking_triage event uses), which reaches Pavel through his own subscription
// without this service needing to know his user id, and reaches the gate's author too
// when they are a subscribed user. RecordHumanGateDecision has already woken the task's
// assignee agent as a side effect of releasing the gate; this is the separate,
// human-facing half of "уведомление автору гейта и Павлу" the task asks for.
func (s *humanGateDefaultTimeoutService) notifyDefaultApplied(ctx context.Context, task *domain.Task, c domain.HumanGateDefaultTimeoutCandidate) {
	if s.notifySvc == nil || task == nil {
		return
	}
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}
	taskIDCopy := c.TaskID
	projIDCopy := task.ProjectID
	s.notifySvc.Notify(ctx, domain.NotificationEvent{
		WorkspaceID: wsID,
		TaskID:      &taskIDCopy,
		ProjectID:   &projIDCopy,
		EventType:   "task.human_gate_default_applied",
		Title:       "Default applied: " + task.Title,
		Body: fmt.Sprintf(
			"Gate deadline passed with no answer — recommended default applied: %s",
			c.RecommendedDefault,
		),
		Labels: []string(task.Labels),
		Metadata: map[string]any{
			"task_id":             c.TaskID,
			"task_title":          task.Title,
			"project_id":          task.ProjectID,
			"gate_author":         c.GateAuthor,
			"gate_author_type":    string(c.GateAuthorType),
			"recommended_default": c.RecommendedDefault,
			"gate_deadline":       c.Deadline,
		},
	})
}
