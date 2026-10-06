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
			if err := s.checkSubtaskCompletion(ctx, *task.ParentTaskID, taskID); err != nil {
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
	return s.checkSubtaskCompletion(ctx, parentTaskID, parentTaskID)
}

func (s *autoTransitionService) checkSubtaskCompletion(ctx context.Context, parentTaskID, triggerTaskID uuid.UUID) error {
	candidate := func() (*domain.Task, uuid.UUID, error) { return s.parentTransitionCandidate(ctx, parentTaskID) }
	return s.moveAutomatic(ctx, parentTaskID, triggerTaskID, domain.TriggerAllSubtasksDone, candidate)
}

func (s *autoTransitionService) parentTransitionCandidate(ctx context.Context, taskID uuid.UUID) (*domain.Task, uuid.UUID, error) {
	parent, err := s.taskSnapshot(ctx, taskID)
	if err != nil || parent == nil {
		return nil, uuid.Nil, err
	}
	if reason := autoTransitionGate(parent, timeNow()); reason != "" {
		log.Printf("[auto-transition] holding parent %s: %s", taskID, reason)
		return nil, uuid.Nil, nil
	}
	status, err := s.statusRepo.GetByID(ctx, parent.StatusID)
	if err != nil || status == nil {
		return nil, uuid.Nil, err
	}
	fromReview := status.Category == domain.StatusCategoryReview
	if (fromReview && !hasUmbrellaLabel(parent.Labels)) || (!fromReview && status.Category != domain.StatusCategoryInProgress) {
		return nil, uuid.Nil, nil
	}
	subtasks, err := s.taskRepo.ListSubtasks(ctx, taskID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	categories := make(map[uuid.UUID]domain.StatusCategory)
	for _, sub := range subtasks {
		st, statusErr := s.statusRepo.GetByID(ctx, sub.StatusID)
		if statusErr != nil {
			return nil, uuid.Nil, statusErr
		}
		if st != nil {
			categories[sub.StatusID] = st.Category
		}
	}
	if !allSubtasksTerminal(subtasks, categories) {
		return nil, uuid.Nil, nil
	}
	target, exists, err := s.resolveTargetFromRule(ctx, parent.ProjectID, domain.TriggerAllSubtasksDone)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if exists && target == uuid.Nil {
		return nil, uuid.Nil, nil
	}
	if fromReview {
		// Only explicitly labelled umbrellas may finish a prior review cycle.
		target, err = s.findTargetStatus(ctx, parent.ProjectID, domain.StatusCategoryDone)
	} else if !exists {
		target, err = s.findTargetStatus(ctx, parent.ProjectID, domain.StatusCategoryReview, domain.StatusCategoryDone)
	}
	return parent, target, err
}

// taskSnapshot prevents mutable repository test doubles/caches from changing the
// preconditions after eligibility has been evaluated.
func (s *autoTransitionService) taskSnapshot(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	task, err := s.taskRepo.GetByID(ctx, id)
	if err != nil || task == nil {
		return nil, err
	}
	snapshot := *task
	snapshot.Labels = append([]string(nil), task.Labels...)
	snapshot.CustomFields = append([]byte(nil), task.CustomFields...)
	return &snapshot, nil
}

// moveAutomatic owns a fresh read immediately before moving. Re-evaluating the
// full candidate also catches reblocks, new children and rule/gate changes that
// do not necessarily update the candidate task's timestamp.
func (s *autoTransitionService) moveAutomatic(ctx context.Context, taskID, triggerTaskID uuid.UUID, reason domain.AutoTransitionTrigger, candidate func() (*domain.Task, uuid.UUID, error)) error {
	initial, target, err := candidate()
	if err != nil || initial == nil || target == uuid.Nil {
		return err
	}
	fresh, freshTarget, err := candidate()
	if err != nil {
		return err
	}
	if fresh == nil || freshTarget != target || fresh.StatusID != initial.StatusID || !fresh.UpdatedAt.Equal(initial.UpdatedAt) {
		log.Printf("[auto-transition] holding task %s: candidate changed before move", taskID)
		return nil
	}
	sysCtx := actorctx.WithActor(ctx, uuid.Nil, domain.ActorTypeSystem)
	if err := s.taskSvc.MoveTask(sysCtx, taskID, MoveTaskInput{
		StatusID: &target, ExpectedStatusID: &fresh.StatusID, ExpectedUpdatedAt: &fresh.UpdatedAt,
		Source: "auto_transition", Reason: string(reason), TriggerTaskID: &triggerTaskID,
	}); err != nil {
		return err
	}
	if reason == domain.TriggerAllSubtasksDone {
		oldStatus, err := s.statusRepo.GetByID(ctx, fresh.StatusID)
		if err == nil && oldStatus != nil && oldStatus.Category == domain.StatusCategoryReview {
			subtasks, err := s.taskRepo.ListSubtasks(ctx, taskID)
			if err == nil {
				s.postUmbrellaCloseComment(ctx, fresh, subtasks)
			}
		}
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

		if err := s.tryUnblockTask(ctx, dep.TaskID, resolvedTaskID); err != nil {
			log.Printf("[auto-transition] WARNING: tryUnblockTask for task %s failed: %v", dep.TaskID, err)
		}
	}
	return nil
}

// tryUnblockTask checks if a specific task can be moved from "backlog" to "todo".
func (s *autoTransitionService) tryUnblockTask(ctx context.Context, taskID, triggerTaskID uuid.UUID) error {
	candidate := func() (*domain.Task, uuid.UUID, error) { return s.dependencyTransitionCandidate(ctx, taskID) }
	return s.moveAutomatic(ctx, taskID, triggerTaskID, domain.TriggerBlockingDepResolved, candidate)
}

func (s *autoTransitionService) dependencyTransitionCandidate(ctx context.Context, taskID uuid.UUID) (*domain.Task, uuid.UUID, error) {
	task, err := s.taskSnapshot(ctx, taskID)
	if err != nil || task == nil {
		return nil, uuid.Nil, err
	}
	status, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || status == nil {
		return nil, uuid.Nil, err
	}
	if status.Category != domain.StatusCategoryBacklog {
		return nil, uuid.Nil, nil
	}
	if reason := dependencyParkIneligibility(task, timeNow()); reason != "" {
		log.Printf("[auto-transition] holding task %s: %s", taskID, reason)
		return nil, uuid.Nil, nil
	}
	deps, err := s.depRepo.ListByTask(ctx, taskID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	categories := make(map[uuid.UUID]domain.StatusCategory)
	blockers := 0
	for _, dep := range deps {
		if dep.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		blockers++
		blocker, blockerErr := s.taskRepo.GetByID(ctx, dep.DependsOnTaskID)
		if blockerErr != nil {
			return nil, uuid.Nil, blockerErr
		}
		if blocker == nil {
			continue
		}
		st, statusErr := s.statusRepo.GetByID(ctx, blocker.StatusID)
		if statusErr != nil {
			return nil, uuid.Nil, statusErr
		}
		if st != nil {
			categories[blocker.ID] = st.Category
		}
	}
	// An event whose edge has since been deleted is not proof of resolution.
	if blockers == 0 || hasUnresolvedBlockers(deps, categories) {
		return nil, uuid.Nil, nil
	}
	target, exists, err := s.resolveTargetFromRule(ctx, task.ProjectID, domain.TriggerBlockingDepResolved)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if !exists {
		target, err = s.findTargetStatus(ctx, task.ProjectID, domain.StatusCategoryTodo)
	}
	return task, target, err
}

// One configuration read distinguishes absent (fallback allowed) from disabled
// (explicit opt-out). Errors and conflicting duplicate rules fail closed.
func (s *autoTransitionService) resolveTargetFromRule(ctx context.Context, projectID uuid.UUID, trigger domain.AutoTransitionTrigger) (uuid.UUID, bool, error) {
	if s.ruleRepo == nil {
		return uuid.Nil, false, nil
	}
	rules, err := s.ruleRepo.List(ctx, projectID)
	if err != nil {
		return uuid.Nil, false, err
	}
	var matching *domain.AutoTransitionRule
	for i := range rules {
		if rules[i].Trigger != trigger {
			continue
		}
		if matching != nil {
			return uuid.Nil, true, fmt.Errorf("conflicting auto-transition rules for %s", trigger)
		}
		matching = &rules[i]
	}
	if matching == nil {
		return uuid.Nil, false, nil
	}
	if !matching.IsEnabled {
		return uuid.Nil, true, nil
	}
	if matching.TargetStatusID == uuid.Nil {
		return uuid.Nil, true, fmt.Errorf("auto-transition rule %s has no target", matching.ID)
	}
	return matching.TargetStatusID, true, nil
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
