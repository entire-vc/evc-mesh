package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
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
// Known gaps vs. the Python sweep — deliberately out of scope for this first unit; the
// #f928e5af parallel run is what is expected to surface these as named divergences to
// fix, not something to guess at and pre-emptively port:
//   - wake:<type>/due_date override of a passive-wait label (due_wake_overrides_park);
//   - Agent-Eval golden-fixture child skip (parent_is_eval_fixture);
//   - visible-work-needs-a-`source:`-line gate (needs_source);
//   - parent-awaits-human skip (parent_awaits_human) and the epic-candidate-assigned-
//     to-a-user skip (is_epic_candidate);
//   - Pavel backlog-freeze-via-comment-phrase detection (has_user_backlog_freeze) —
//     this rule relies on the label route only, which is also how the #bbf3db92
//     incident itself was actually remediated (labels added to the card), not a
//     comment-phrase fix;
//   - the per-assignee promotion cap within one sweep tick (assignee_count/cap).
type BacklogPromotionAdvisoryService interface {
	SweepAdvisory(ctx context.Context) ([]BacklogPromotionDecision, error)
}

type backlogPromotionAdvisoryService struct {
	taskRepo     repository.TaskRepository
	statusRepo   repository.TaskStatusRepository
	depRepo      repository.TaskDependencyRepository
	activityRepo repository.ActivityLogRepository
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

	decisions := make([]BacklogPromotionDecision, 0, len(tasks))
	for i := range tasks {
		task := &tasks[i]
		promote, reason, err := s.evaluate(ctx, task, nameCatCache)
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
) (promote bool, reason string, err error) {
	// 1. Label-based park — cheapest guard (no extra query), checked first, mirroring
	// mesh-intake-sweep.py's own ordering (is_passive_wait() runs before any lookup).
	if label, ok := hasBacklogParkLabel(task.Labels); ok {
		return false, fmt.Sprintf("passive-wait label %q", label), nil
	}

	// 2. Human-gate — read directly off the task. Unlike the Python sweep (an external
	// client that also re-scans comment bodies for a marker because it cannot fully
	// trust flag-propagation lag — see is_human_gated's "FULL path" comment in
	// mesh-intake-sweep.py), this rule computed the flag itself: the field is
	// authoritative here, no comment re-scan needed.
	if task.HumanGate {
		return false, "human_gate armed", nil
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
	if len(depIDs) == 0 {
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

// activityStatusChange is the shape of the "status" key inside a task.moved activity
// log entry's Changes JSON — written by task_service.go's MoveTask as
// moveChanges["status"] = map[string]interface{}{"old": oldName, "new": newName}.
type activityStatusChange struct {
	Status *struct {
		Old string `json:"old"`
		New string `json:"new"`
	} `json:"status"`
}

// backlogActivityPageSize bounds how far back wasDeliberatelyParked looks for the most
// recent task.moved entry. mesh-intake-sweep.py fetches the task's full activity log
// (its GET .../activity call is unpaginated server-side); this rule uses a generous
// but bounded window instead — a card demoted long ago with many intervening
// activity-log entries (assignment changes, further moves) since could, in principle,
// fall outside it and read as "never moved" (not parked) where the sweep would still
// see the demotion. Accepted as a known gap: the #f928e5af parallel run is what would
// surface such a case as a named divergence to investigate, not something to guess a
// bigger number against up front.
const backlogActivityPageSize = 50

// wasDeliberatelyParked mirrors mesh-intake-sweep.py's was_deliberately_parked(),
// called ONLY when the task has no dependencies (see the caller). Fails CLOSED on an
// unresolvable status name, exactly like the Python original: backlog is the safe
// resting state, so an ambiguous read treats the task as parked rather than promotable.
func (s *backlogPromotionAdvisoryService) wasDeliberatelyParked(
	ctx context.Context,
	task *domain.Task,
	nameCatCache map[uuid.UUID]map[string]domain.StatusCategory,
) (bool, error) {
	page, err := s.activityRepo.ListByTask(ctx, task.ID, pagination.Params{Page: 1, PageSize: backlogActivityPageSize})
	if err != nil {
		return false, err
	}
	if page == nil {
		return false, nil
	}

	// Scan for the latest move rather than trusting position: the real repo returns
	// this page ORDER BY created_at DESC, but the test double returns map order (same
	// reasoning as commentIsOwnClosingReport in comment_closed_task_followup.go) — a
	// guard correct only under one repository's ordering is a guard whose test cannot
	// see it break.
	var lastMove *domain.ActivityLog
	for i := range page.Items {
		e := &page.Items[i]
		if e.Action != "task.moved" {
			continue
		}
		if lastMove == nil || e.CreatedAt.After(lastMove.CreatedAt) {
			lastMove = e
		}
	}
	if lastMove == nil {
		// Never moved (within the window) → born in backlog → genuine intake, not a park.
		return false, nil
	}

	var changes activityStatusChange
	if unmarshalErr := json.Unmarshal(lastMove.Changes, &changes); unmarshalErr != nil || changes.Status == nil {
		// A task.moved entry with no readable status change (e.g. a pure position
		// reorder) proves nothing about a demotion — fall through as "never resolved
		// a status move" rather than guessing.
		return false, nil
	}

	nameCat, err := s.projectNameCategories(ctx, task.ProjectID, nameCatCache)
	if err != nil {
		return false, err
	}

	oldName := strings.ToLower(strings.TrimSpace(changes.Status.Old))
	newName := strings.ToLower(strings.TrimSpace(changes.Status.New))

	if newCat, ok := nameCat[newName]; ok && newCat != domain.StatusCategoryBacklog {
		// The last move landed somewhere we can positively identify as NOT backlog,
		// so the task's current backlog residency was not decided by it.
		return false, nil
	}

	// Either the move landed in backlog, or the destination name is unresolvable (a
	// status renamed after the move was logged) — judge by the SOURCE status instead.
	// Unknown source name → cannot prove it was NOT a demotion → fail closed (parked).
	oldCat, ok := nameCat[oldName]
	if !ok {
		return true, nil
	}
	return oldCat != domain.StatusCategoryBacklog, nil
}

// projectNameCategories returns (and caches) a project's lowercased-trimmed status
// name → category map, used to interpret the OLD/NEW names an activity log entry
// carries (task_service.go logs status NAMES, not IDs — see moveChanges).
func (s *backlogPromotionAdvisoryService) projectNameCategories(
	ctx context.Context,
	projectID uuid.UUID,
	cache map[uuid.UUID]map[string]domain.StatusCategory,
) (map[string]domain.StatusCategory, error) {
	if m, ok := cache[projectID]; ok {
		return m, nil
	}
	statuses, err := s.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	m := make(map[string]domain.StatusCategory, len(statuses))
	for _, st := range statuses {
		m[strings.ToLower(strings.TrimSpace(st.Name))] = st.Category
	}
	cache[projectID] = m
	return m, nil
}
