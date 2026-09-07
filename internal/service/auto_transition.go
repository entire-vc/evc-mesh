package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// AutoTransitionService checks and applies automatic status transitions.
type AutoTransitionService interface {
	// EvaluateOnTaskMove checks and applies any auto-transition rules triggered
	// by a task being moved to a new status category.
	EvaluateOnTaskMove(ctx context.Context, taskID uuid.UUID, newStatusCategory domain.StatusCategory) error
	// CheckSubtaskCompletion checks if all subtasks of a parent task are done.
	// If so, moves the parent to the first "review" status (or "done" if no review exists).
	CheckSubtaskCompletion(ctx context.Context, parentTaskID uuid.UUID) error
	// CheckDependencyResolution checks if all blocking dependencies of dependent tasks
	// are resolved and moves them from "backlog" to "todo" accordingly.
	CheckDependencyResolution(ctx context.Context, resolvedTaskID uuid.UUID) error
	// ListRules returns all auto-transition rules for a project.
	ListRules(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error)
	// CreateRule creates a new auto-transition rule.
	CreateRule(ctx context.Context, rule *domain.AutoTransitionRule) error
	// UpdateRule updates an existing auto-transition rule.
	UpdateRule(ctx context.Context, rule *domain.AutoTransitionRule) error
	// DeleteRule removes an auto-transition rule.
	DeleteRule(ctx context.Context, ruleID uuid.UUID) error
}

// autoTransitionService implements AutoTransitionService.
type autoTransitionService struct {
	taskRepo    repository.TaskRepository
	statusRepo  repository.TaskStatusRepository
	depRepo     repository.TaskDependencyRepository
	taskSvc     TaskService
	ruleRepo    repository.AutoTransitionRuleRepository
	commentRepo repository.CommentRepository
}

// NewAutoTransitionService creates a new AutoTransitionService.
// ruleRepo and commentRepo may both be nil for backwards compatibility (ruleRepo falls
// back to hardcoded category lookup; commentRepo just means the umbrella-close system
// comment in postUmbrellaCloseComment is skipped — never a hard failure, see there).
func NewAutoTransitionService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	depRepo repository.TaskDependencyRepository,
	taskSvc TaskService,
	ruleRepo repository.AutoTransitionRuleRepository,
	commentRepo repository.CommentRepository,
) AutoTransitionService {
	return &autoTransitionService{
		taskRepo:    taskRepo,
		statusRepo:  statusRepo,
		depRepo:     depRepo,
		taskSvc:     taskSvc,
		ruleRepo:    ruleRepo,
		commentRepo: commentRepo,
	}
}

// EvaluateOnTaskMove is the main entry point called after a task status change.
// It checks all relevant auto-transition conditions based on the new status category.
func (s *autoTransitionService) EvaluateOnTaskMove(ctx context.Context, taskID uuid.UUID, newStatusCategory domain.StatusCategory) error {
	// When a task is moved to "done" or "cancelled":
	// 1. Check if its parent should be transitioned (all siblings done).
	// 2. Check if tasks that depend on this task can be unblocked.
	if newStatusCategory == domain.StatusCategoryDone || newStatusCategory == domain.StatusCategoryCancelled {
		task, err := s.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return err
		}
		if task == nil {
			return nil
		}

		// Check parent subtask completion.
		if task.ParentTaskID != nil {
			if err := s.CheckSubtaskCompletion(ctx, *task.ParentTaskID); err != nil {
				log.Printf("[auto-transition] WARNING: CheckSubtaskCompletion for parent %s failed: %v", *task.ParentTaskID, err)
			}
		}

		// Check if tasks that depend on this task can now be unblocked.
		if err := s.CheckDependencyResolution(ctx, taskID); err != nil {
			log.Printf("[auto-transition] WARNING: CheckDependencyResolution for task %s failed: %v", taskID, err)
		}
	}
	return nil
}

// CheckSubtaskCompletion checks if all subtasks of a parent task are done/cancelled.
// If so, and the parent is in "in_progress" category, it moves the parent to "review"
// (or "done" if no "review" status exists in the project).
func (s *autoTransitionService) CheckSubtaskCompletion(ctx context.Context, parentTaskID uuid.UUID) error {
	// 1. Get the parent task.
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil {
		return err
	}
	if parent == nil {
		return nil
	}

	// 2. Get the parent's current status category.
	parentStatus, err := s.statusRepo.GetByID(ctx, parent.StatusID)
	if err != nil {
		return err
	}
	if parentStatus == nil {
		return nil
	}

	// A parent already sitting in "review" only auto-closes here when it carries an
	// explicit kind:epic/kind:umbrella label (see hasUmbrellaLabel) — otherwise this
	// is unreachable ground exactly as before 2026-09-07 (#d1eff1c6). Fail-closed on
	// purpose and deliberately NOT an inferred signal (e.g. "no AC in description"):
	// an unlabeled parent sitting in review may carry its own evidence obligation, and
	// a false-positive close there would silently hide unfinished work behind a status
	// that looks routine — invisible and expensive. A false negative (a real umbrella
	// stays stuck in review because nobody labeled it) just leaves today's problem
	// exactly as visible as it is now — cheap, and fixed by adding the label.
	//
	// Why "review" needs handling at all, separately from "in_progress" below: a
	// captain/epic parent commonly reaches review once (via this same rule, first
	// batch of subtasks done), then gains MORE subtasks later as the sprint continues.
	// Those later subtasks completing re-enters this function, but the old code only
	// ever looked at "in_progress" — a parent already in review was silently exempt
	// forever, which is exactly how #52f407e0 and friends sat in review for 27-85 days.
	isLabeledUmbrella := hasUmbrellaLabel(parent.Labels)
	fromReview := parentStatus.Category == domain.StatusCategoryReview
	switch {
	case fromReview && !isLabeledUmbrella:
		return nil
	case !fromReview && parentStatus.Category != domain.StatusCategoryInProgress:
		return nil
	}

	// Supervised parent: skip auto-transition; a human must manually sign off.
	// Parent stays in in_progress; no error is returned so the subtask's move succeeds.
	if parent.DelegationLevel == domain.DelegationLevelSupervised {
		log.Printf("[auto-transition] Parent task %s is supervised — skipping auto-move to review/done, requires human signoff", parentTaskID)
		return nil
	}

	// 3. Get all subtasks.
	subtasks, err := s.taskRepo.ListSubtasks(ctx, parentTaskID)
	if err != nil {
		return err
	}
	if len(subtasks) == 0 {
		return nil // no subtasks — rule does not apply
	}

	// 4. Build a category map for statuses we encounter.
	categoryByStatusID := make(map[uuid.UUID]domain.StatusCategory)
	var st *domain.TaskStatus
	for _, sub := range subtasks {
		if _, seen := categoryByStatusID[sub.StatusID]; !seen {
			st, err = s.statusRepo.GetByID(ctx, sub.StatusID)
			if err != nil {
				return err
			}
			if st != nil {
				categoryByStatusID[sub.StatusID] = st.Category
			}
		}
	}

	// 5. Check if all subtasks are done or cancelled.
	if !allSubtasksTerminal(subtasks, categoryByStatusID) {
		return nil
	}

	var targetStatusID uuid.UUID
	if fromReview {
		// Labeled umbrella already in review with every (including newly added)
		// subtask now terminal: close it directly. The configured-rule lookup below
		// answers a different question — "where does a freshly in_progress parent
		// land" — and doesn't apply once a parent has already made that trip once.
		targetStatusID, err = s.findTargetStatus(ctx, parent.ProjectID, domain.StatusCategoryDone)
		if err != nil {
			return err
		}
		if targetStatusID == uuid.Nil {
			return nil // no "done" status in this project — nothing to move to
		}
	} else {
		// 6. Check if a configured rule overrides the target status.
		targetStatusID, err = s.resolveTargetFromRule(ctx, parent.ProjectID, domain.TriggerAllSubtasksDone)
		if err != nil {
			return err
		}

		// Fallback: prefer "review", fall back to "done".
		// But if a disabled rule exists, respect the explicit disable — don't auto-transition at all.
		if targetStatusID == uuid.Nil {
			if s.ruleExistsForTrigger(ctx, parent.ProjectID, domain.TriggerAllSubtasksDone) {
				return nil // rule exists but is disabled; honour the user's explicit opt-out
			}
			targetStatusID, err = s.findTargetStatus(ctx, parent.ProjectID, domain.StatusCategoryReview, domain.StatusCategoryDone)
			if err != nil {
				return err
			}
		}
		if targetStatusID == uuid.Nil {
			return nil // no suitable target status found
		}
	}

	log.Printf("[auto-transition] Moving parent task %s to review/done because all subtasks are complete", parentTaskID)
	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)
	if err := s.taskSvc.MoveTask(sysCtx, parentTaskID, MoveTaskInput{StatusID: &targetStatusID}); err != nil {
		return err
	}

	if fromReview {
		s.postUmbrellaCloseComment(ctx, parent, subtasks)
	}
	return nil
}

// umbrellaLabels are the ONLY labels that authorize a parent already sitting in
// "review" to auto-close to "done" once every subtask is terminal. Deliberately
// narrow and exact-match — not the free-form "epic"/"sprint"/"captain" labels
// already in casual use on real umbrellas. Measured live against the 10 stale
// umbrellas #d1eff1c6 was written for (07.09.2026): 0 of 10 carried either of
// these two tags (3 had a bare "epic" label, 2 had "sprint"+"captain", 5 had
// neither) — so adopting the label going forward, not just shipping this switch,
// is what makes the rule fire on the next case. See CheckSubtaskCompletion.
var umbrellaLabels = map[string]bool{
	"kind:epic":     true,
	"kind:umbrella": true,
}

// hasUmbrellaLabel reports whether labels contains kind:epic or kind:umbrella.
func hasUmbrellaLabel(labels []string) bool {
	for _, l := range labels {
		if umbrellaLabels[strings.ToLower(strings.TrimSpace(l))] {
			return true
		}
	}
	return false
}

// postUmbrellaCloseComment records why an umbrella auto-closed and what it closed
// over — the parent had no chance to say so itself, since nothing moved it here by
// hand. Best-effort: a failure here must never unwind the move that already
// happened, only log loudly (same convention as postMarkerDefaultAppliedComment in
// task_service.go). No-ops if commentRepo wasn't wired in (nil-safe, matches
// ruleRepo's backward-compat contract on this same constructor).
func (s *autoTransitionService) postUmbrellaCloseComment(ctx context.Context, parent *domain.Task, subtasks []domain.Task) {
	if s.commentRepo == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🤖 Авто-закрытие: все %d потомков в терминальном статусе (done/cancelled), карточка помечена kind:epic/kind:umbrella — перевожу review → done без ожидания повторного приёма.\n\nПотомки:\n", len(subtasks))
	for _, sub := range subtasks {
		fmt.Fprintf(&b, "- #%s %s\n", sub.ID.String()[:8], sub.Title)
	}
	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     parent.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body:       b.String(),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[auto-transition] WARNING: create umbrella-close comment on task %s failed: %v", parent.ID, err)
	}
}

// CheckDependencyResolution checks if tasks that depend on resolvedTaskID can now be
// unblocked. For each dependent task: if ALL its blocking dependencies are now done,
// and the dependent task is in "backlog" category, it moves it to "todo".
func (s *autoTransitionService) CheckDependencyResolution(ctx context.Context, resolvedTaskID uuid.UUID) error {
	// 1. Get all tasks that depend ON this task (reverse lookup).
	dependents, err := s.depRepo.ListDependents(ctx, resolvedTaskID)
	if err != nil {
		return err
	}

	for _, dep := range dependents {
		// Only handle "blocks" dependency type.
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}

		if err := s.tryUnblockTask(ctx, dep.TaskID); err != nil {
			log.Printf("[auto-transition] WARNING: tryUnblockTask for task %s failed: %v", dep.TaskID, err)
		}
	}
	return nil
}

// tryUnblockTask checks if a specific task can be moved from "backlog" to "todo".
func (s *autoTransitionService) tryUnblockTask(ctx context.Context, taskID uuid.UUID) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}

	// Only unblock tasks that are in "backlog".
	currentStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil {
		return err
	}
	if currentStatus == nil || currentStatus.Category != domain.StatusCategoryBacklog {
		return nil
	}

	// Check if ALL blocking dependencies are now done.
	allDeps, err := s.depRepo.ListByTask(ctx, taskID)
	if err != nil {
		return err
	}

	// Build category map for blocker tasks.
	categoryByTaskID := make(map[uuid.UUID]domain.StatusCategory)
	var blocker *domain.Task
	var blockerStatus *domain.TaskStatus
	for _, dep := range allDeps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		if _, seen := categoryByTaskID[dep.DependsOnTaskID]; !seen {
			blocker, err = s.taskRepo.GetByID(ctx, dep.DependsOnTaskID)
			if err != nil {
				return err
			}
			if blocker == nil {
				continue
			}
			blockerStatus, err = s.statusRepo.GetByID(ctx, blocker.StatusID)
			if err != nil {
				return err
			}
			if blockerStatus != nil {
				categoryByTaskID[dep.DependsOnTaskID] = blockerStatus.Category
			}
		}
	}

	if hasUnresolvedBlockers(allDeps, categoryByTaskID) {
		return nil // still blocked
	}

	// Check if a configured rule overrides the target status.
	targetStatusID, err := s.resolveTargetFromRule(ctx, task.ProjectID, domain.TriggerBlockingDepResolved)
	if err != nil {
		return err
	}

	// Fallback: move to "todo".
	if targetStatusID == uuid.Nil {
		targetStatusID, err = s.findTargetStatus(ctx, task.ProjectID, domain.StatusCategoryTodo)
		if err != nil {
			return err
		}
	}
	if targetStatusID == uuid.Nil {
		return nil
	}

	log.Printf("[auto-transition] Unblocking task %s (all blocking deps resolved) → moving to todo", taskID)
	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)
	return s.taskSvc.MoveTask(sysCtx, taskID, MoveTaskInput{StatusID: &targetStatusID})
}

// resolveTargetFromRule looks up a configured, enabled rule for the given trigger and
// returns its target_status_id. Returns uuid.Nil if ruleRepo is nil or no matching
// enabled rule exists.
func (s *autoTransitionService) resolveTargetFromRule(ctx context.Context, projectID uuid.UUID, trigger domain.AutoTransitionTrigger) (uuid.UUID, error) {
	if s.ruleRepo == nil {
		return uuid.Nil, nil
	}
	rules, err := s.ruleRepo.List(ctx, projectID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, r := range rules {
		if r.Trigger == trigger && r.IsEnabled {
			return r.TargetStatusID, nil
		}
	}
	return uuid.Nil, nil
}

// ruleExistsForTrigger returns true if any rule (enabled or disabled) exists for
// the given trigger in the project. Used to distinguish "disabled rule" (do nothing)
// from "no rule at all" (fall back to category lookup).
func (s *autoTransitionService) ruleExistsForTrigger(ctx context.Context, projectID uuid.UUID, trigger domain.AutoTransitionTrigger) bool {
	if s.ruleRepo == nil {
		return false
	}
	rules, err := s.ruleRepo.List(ctx, projectID)
	if err != nil {
		return false
	}
	for _, r := range rules {
		if r.Trigger == trigger {
			return true
		}
	}
	return false
}

// findTargetStatus returns the first status in a project matching any of the given
// categories (in priority order). Returns uuid.Nil if none found.
func (s *autoTransitionService) findTargetStatus(ctx context.Context, projectID uuid.UUID, categories ...domain.StatusCategory) (uuid.UUID, error) {
	return findStatusIDByCategory(ctx, s.statusRepo, projectID, categories...)
}

// findStatusIDByCategory returns the first status in a project matching any of the
// given categories (in priority order). Returns uuid.Nil if none found. Shared by
// the auto-transition and comment-triage enforcement paths.
func findStatusIDByCategory(ctx context.Context, statusRepo repository.TaskStatusRepository, projectID uuid.UUID, categories ...domain.StatusCategory) (uuid.UUID, error) {
	statuses, err := statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		return uuid.Nil, err
	}

	// Build a map from category to first matching status ID.
	categoryToStatus := make(map[domain.StatusCategory]uuid.UUID)
	for _, st := range statuses {
		if _, exists := categoryToStatus[st.Category]; !exists {
			categoryToStatus[st.Category] = st.ID
		}
	}

	for _, cat := range categories {
		if id, ok := categoryToStatus[cat]; ok {
			return id, nil
		}
	}
	return uuid.Nil, nil
}

// allSubtasksTerminal returns true if every subtask has a "done" or "cancelled" category.
func allSubtasksTerminal(subtasks []domain.Task, categoryByStatusID map[uuid.UUID]domain.StatusCategory) bool {
	if len(subtasks) == 0 {
		return false
	}
	for _, sub := range subtasks {
		cat, ok := categoryByStatusID[sub.StatusID]
		if !ok {
			return false // unknown status — treat as not done
		}
		if cat != domain.StatusCategoryDone && cat != domain.StatusCategoryCancelled {
			return false
		}
	}
	return true
}

// hasUnresolvedBlockers returns true if any "blocks" dependency points to a task
// that has NOT reached a terminal category.
//
// Canonical dep-clear rule (P3 #9, 2026-06-16): a blocking dependency is CLEARED
// when its blocker reaches a TERMINAL status — `done` OR `cancelled`. A cancelled
// blocker is abandoned/obsoleted work that will never complete, so keeping the
// dependent blocked on it forever is wrong; the dependent is free to proceed.
// This brings hasUnresolvedBlockers in line with the rest of the system, which
// already treated both terminal categories as clearing:
//   - EvaluateOnTaskMove fires CheckDependencyResolution on done OR cancelled.
//   - allSubtasksTerminal counts a subtask terminal on done OR cancelled.
//   - mesh-intake-sweep all_deps_cleared promotes on done OR cancelled.
//
// Previously this function alone accepted only `done`, so a cancelled blocker
// left the dependent stuck in backlog server-side while intake-sweep promoted it
// — an engine-dependent split. Now both engines agree.
func hasUnresolvedBlockers(deps []domain.TaskDependency, categoryByTaskID map[uuid.UUID]domain.StatusCategory) bool {
	for _, dep := range deps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		cat, ok := categoryByTaskID[dep.DependsOnTaskID]
		if !ok || (cat != domain.StatusCategoryDone && cat != domain.StatusCategoryCancelled) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Rule management — backed by AutoTransitionRuleRepository
// ---------------------------------------------------------------------------

// ListRules returns all auto-transition rules for a project.
func (s *autoTransitionService) ListRules(ctx context.Context, projectID uuid.UUID) ([]domain.AutoTransitionRule, error) {
	if s.ruleRepo == nil {
		return []domain.AutoTransitionRule{}, nil
	}
	return s.ruleRepo.List(ctx, projectID)
}

// CreateRule creates a new auto-transition rule.
func (s *autoTransitionService) CreateRule(ctx context.Context, rule *domain.AutoTransitionRule) error {
	if rule.ID == uuid.Nil {
		rule.ID = uuid.New()
	}
	now := time.Now()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	if rule.UpdatedAt.IsZero() {
		rule.UpdatedAt = now
	}
	if s.ruleRepo == nil {
		return nil // no-op for backward compat
	}
	return s.ruleRepo.Create(ctx, rule)
}

// UpdateRule persists changes to an existing auto-transition rule.
func (s *autoTransitionService) UpdateRule(ctx context.Context, rule *domain.AutoTransitionRule) error {
	if s.ruleRepo == nil {
		return nil
	}
	return s.ruleRepo.Update(ctx, rule)
}

// DeleteRule removes an auto-transition rule.
func (s *autoTransitionService) DeleteRule(ctx context.Context, ruleID uuid.UUID) error {
	if s.ruleRepo == nil {
		return nil
	}
	return s.ruleRepo.Delete(ctx, ruleID)
}
