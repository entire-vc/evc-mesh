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

// backlogPassiveWaitLabels mirrors PASSIVE_WAIT_LABELS in bob/scripts/mesh-intake-sweep.py
// (task #00327dc6, this subtask #9f3f4064) — the label vocabulary that means "this
// backlog card is deliberately parked, do not auto-promote it". Kept as ONE flat set,
// unlike the Python script's ABSOLUTE_NO_PROMOTE_LABELS ⊂ PASSIVE_WAIT_LABELS split:
// that split exists to let a passed due_date override the non-absolute half
// (due_wake_overrides_park), and this rule does not implement due_date/wake:<type>
// overrides at all (see the package doc comment on BacklogPromotionAdvisoryService for
// the full list of known gaps) — so every label below parks unconditionally here.
var backlogPassiveWaitLabels = map[string]struct{}{
	"kind:monitor": {}, "kind:verify": {}, "awaiting-window": {},
	"no-pavel-triage": {}, "backlog-candidate": {},
	"freeze": {}, "no-intake-promote": {}, "no-promote": {},
	"golden": {}, "eval-harness": {},
}

// hasBacklogParkLabel returns the first matching label (for the log reason) and
// whether any of task.Labels names a deliberate park.
func hasBacklogParkLabel(labels []string) (string, bool) {
	for _, l := range labels {
		if _, ok := backlogPassiveWaitLabels[l]; ok {
			return l, true
		}
	}
	return "", false
}

// BacklogPromotionDecision is one advisory verdict for one backlog task, produced by
// BacklogPromotionAdvisoryService.SweepAdvisory. It is a log record, never an action.
type BacklogPromotionDecision struct {
	TaskID  uuid.UUID
	Promote bool
	Reason  string
}

// BacklogPromotionAdvisoryService is the server-side, advisory-only mirror of
// bob/scripts/mesh-intake-sweep.py's backlog→todo promotion decision (task #00327dc6,
// subtask #9f3f4064 — unit 1 of 4: ship advisory here, THEN a 7-day parallel-run diff
// against the live Python sweep (#f928e5af), THEN a go/no-go on that log (#e96abbff),
// THEN cutover + sweep retirement (#56ec28e8)).
//
// SweepAdvisory NEVER calls MoveTask and NEVER posts a comment — counts+logs only, per
// #9f3f4064's own description ("deployed in advisory mode only ... never moves a
// task"). It reproduces the sweep's two central, previously-incident-causing
// distinctions:
//
//   - a task DEMOTED into backlog from a working status, with NO dependencies, is a
//     deliberate park and must not be promoted just because all-deps-cleared is
//     vacuously true for an empty dependency set (#b832d451: the sweep once undid such
//     a park in 26 minutes);
//   - that demotion-park guard must NOT fire on a task that HAS dependencies — with
//     real deps, "promote once they clear" is itself the informative event, and
//     mesh-intake-sweep.py's was_deliberately_parked() deliberately returns false the
//     moment dep_ids is non-empty, before ever looking at the activity log (#bbf3db92:
//     a demoted, parented, dependency-bearing card was re-promoted twice over 7.5h by
//     the live sweep — the label route is what protects that class, not the
//     demotion-detector). A naive port that checks "last transition = demotion" WITHOUT
//     this dep_ids-empty gate would silently diverge from the sweep by OVER-protecting
//     exactly this class of card — see the parented+deps test case in
//     backlog_promotion_advisory_test.go, which exists specifically to catch that
//     mistake before it ships.
//
// Parity with the sweep (#c15c503a): human-gate (cheap path), parent-gate, eval-fixture
// parent, ABSOLUTE_NO_PROMOTE labels and the due_date / wake:<type> overrides live in
// backlog_promotion_gates.go, ported function-for-function. Still NOT covered:
//
// server holds where the sweep promotes (safe direction, costs a delay):
//   - the comment-aware release tiers of is_human_gated (Pavel answered → un-freeze);
//   - the QUEUE-BEHIND lift of a passive-wait label; wake-condition comment types.
//
// server would PROMOTE where the sweep holds (UNSAFE — enforcing must not flip until
// each is ported or measured at 0 in the divergence journal):
//   - visible-work-needs-a-`source:`-line gate (needs_source);
//   - epic-candidate-assigned-to-a-user skip (is_epic_candidate);
//   - Pavel backlog-freeze-via-comment-phrase (has_user_backlog_freeze) — this rule
//     reads no comments;
//   - the per-assignee promotion cap within one sweep tick.
type BacklogPromotionAdvisoryService interface {
	SweepAdvisory(ctx context.Context) ([]BacklogPromotionDecision, error)
}

type backlogPromotionAdvisoryService struct {
	taskRepo     repository.TaskRepository
	statusRepo   repository.TaskStatusRepository
	depRepo      repository.TaskDependencyRepository
	activityRepo repository.ActivityLogRepository
	now          func() time.Time
}

// NewBacklogPromotionAdvisoryService constructs a BacklogPromotionAdvisoryService.
func NewBacklogPromotionAdvisoryService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	depRepo repository.TaskDependencyRepository,
	activityRepo repository.ActivityLogRepository,
) BacklogPromotionAdvisoryService {
	return &backlogPromotionAdvisoryService{
		taskRepo:     taskRepo,
		statusRepo:   statusRepo,
		depRepo:      depRepo,
		activityRepo: activityRepo,
		now:          time.Now,
	}
}

// SweepAdvisory evaluates every backlog task once and logs a decision for each. It
// never mutates anything.
func (s *backlogPromotionAdvisoryService) SweepAdvisory(ctx context.Context) ([]BacklogPromotionDecision, error) {
	tasks, err := s.taskRepo.ListAllBacklogTasks(ctx)
	if err != nil {
		return nil, err
	}

	// projectID -> lowercased-trimmed status name -> category. Built lazily, once per
	// project touched this tick — mirrors mesh-intake-sweep.py's name_categories cache.
	nameCatCache := make(map[uuid.UUID]map[string]domain.StatusCategory)
	// parent_id -> parent verdict, one fetch per parent per tick (mirrors the sweep's
	// parent_human_cache / parent_eval_cache).
	parentCache := make(map[uuid.UUID]parentVerdict)

	decisions := make([]BacklogPromotionDecision, 0, len(tasks))
	for i := range tasks {
		task := &tasks[i]
		promote, reason, err := s.evaluate(ctx, task, nameCatCache, parentCache)
		if err != nil {
			reason = fmt.Sprintf("guard lookup failed, fail-closed no-promote: %v", err)
			promote = false
		}
		decisions = append(decisions, BacklogPromotionDecision{TaskID: task.ID, Promote: promote, Reason: reason})
		log.Printf("[backlog-promotion-advisory] task=%s promote=%v reason=%q", task.ID, promote, reason)
	}
	return decisions, nil
}

// evaluate decides promote/no-promote+reason for one backlog task. Fail-closed: any
// guard lookup error returns an error rather than a guessed decision (backlog is the
// safe resting state; a wrongly-logged "promote" is the entire class of bug this rule
// exists to avoid, even in advisory mode where nothing actually moves).
func (s *backlogPromotionAdvisoryService) evaluate(
	ctx context.Context,
	task *domain.Task,
	nameCatCache map[uuid.UUID]map[string]domain.StatusCategory,
	parentCache map[uuid.UUID]parentVerdict,
) (promote bool, reason string, err error) {
	// 1. Park labels and the wake overrides — cheapest guard (no extra query), checked
	// first, mirroring mesh-intake-sweep.py's own ordering (is_passive_wait() runs
	// before any lookup). See evaluateWake for the wake:<type> / due_date rules.
	hold, woke, why := s.evaluateWake(task)
	if hold {
		return false, why, nil
	}

	// 2. Human-gate (is_human_gated's cheap path — see humanGateReason) and the
	// Agent-Eval fixture / parent-gate skips (parent_is_eval_fixture, parent_awaits_human).
	if why := humanGateReason(task); why != "" {
		return false, why, nil
	}
	if task.ParentTaskID != nil {
		pv, perr := s.parentOf(ctx, *task.ParentTaskID, parentCache)
		if perr != nil {
			return false, "", fmt.Errorf("parent lookup: %w", perr)
		}
		if pv.evalFixture {
			return false, "parent is an eval-harness fixture", nil
		}
		if pv.gatesChildren {
			return false, "parent awaits human sign-off", nil
		}
	}

	// 3. Dependencies — ALL outgoing edges regardless of dependency_type, matching
	// mesh-intake-sweep.py's get_dependencies() (which takes the REST endpoint's
	// "outgoing" list unfiltered) rather than hasUnresolvedBlockers' Blocks-only
	// filter elsewhere in this package. Parity with the sweep is the point here, not
	// the narrower canonical-blocking-dependency definition.
	deps, err := s.depRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return false, "", fmt.Errorf("list dependencies: %w", err)
	}
	depIDs := make([]uuid.UUID, 0, len(deps))
	for _, d := range deps {
		depIDs = append(depIDs, d.DependsOnTaskID)
	}

	// 4. Deliberate-park guard. Gated on depIDs being EMPTY, exactly like
	// was_deliberately_parked()'s own `if dep_ids: return False` short-circuit — a
	// task WITH dependencies is judged by whether those dependencies are cleared
	// (step 6), never by its move history alone. This is the #bbf3db92 trap: a naive
	// "last transition = demotion ⇒ parked" check WITHOUT this gate would wrongly
	// protect a demoted, dependency-bearing card that the live sweep actually
	// promotes once its deps clear.
	// A wake that lifted a park label lifts this guard too (sweep: `parked and not
	// due_wake`), otherwise a demoted monitor card never wakes on its due_date.
	if len(depIDs) == 0 && !woke {
		parked, parkErr := s.wasDeliberatelyParked(ctx, task, nameCatCache)
		if parkErr != nil {
			return false, "", fmt.Errorf("activity lookup: %w", parkErr)
		}
		if parked {
			return false, "parked via demotion into backlog (no dependencies to wait on)", nil
		}
	}

	// 5. Resolve each blocker's current status category.
	cleared, err := s.allDepsCleared(ctx, depIDs)
	if err != nil {
		return false, "", fmt.Errorf("resolve blocker categories: %w", err)
	}
	if !cleared {
		return false, "unresolved dependencies", nil
	}

	if len(depIDs) == 0 {
		return true, "born in backlog, no dependencies", nil
	}
	return true, "all dependencies cleared (done/cancelled)", nil
}

// allDepsCleared mirrors mesh-intake-sweep.py's all_deps_cleared(): true (vacuously)
// when depIDs is empty, otherwise true only when EVERY blocker has reached a terminal
// category (done or cancelled — a cancelled blocker is abandoned work that will never
// complete, so the dependent is free; task #9 canonical dep-clear rule, kept in
// lockstep with auto_transition.go's hasUnresolvedBlockers).
func (s *backlogPromotionAdvisoryService) allDepsCleared(ctx context.Context, depIDs []uuid.UUID) (bool, error) {
	statusCache := make(map[uuid.UUID]domain.StatusCategory)
	for _, depID := range depIDs {
		cat, ok := statusCache[depID]
		if !ok {
			blocker, err := s.taskRepo.GetByID(ctx, depID)
			if err != nil {
				return false, err
			}
			if blocker == nil {
				// The blocker task no longer exists. Fail closed: cannot prove
				// cleared, so treat as unresolved rather than silently skipping it.
				return false, nil
			}
			status, err := s.statusRepo.GetByID(ctx, blocker.StatusID)
			if err != nil {
				return false, err
			}
			if status == nil {
				return false, nil
			}
			cat = status.Category
			statusCache[depID] = cat
		}
		if cat != domain.StatusCategoryDone && cat != domain.StatusCategoryCancelled {
			return false, nil
		}
	}
	return true, nil
}

// wasDeliberatelyParked mirrors mesh-intake-sweep.py's was_deliberately_parked(),
// called ONLY when the task has no dependencies (see the caller). Delegates to the
// shared implementation in deliberate_park_guard.go — also used by
// MonitorPromotionService (#1eb4fd7d) — so the activity-log read and its
// interpretation live in exactly one place.
func (s *backlogPromotionAdvisoryService) wasDeliberatelyParked(
	ctx context.Context,
	task *domain.Task,
	nameCatCache map[uuid.UUID]map[string]domain.StatusCategory,
) (bool, error) {
	return wasDeliberatelyParkedFromBacklog(ctx, s.activityRepo, s.statusRepo, task, nameCatCache)
}
