package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// backlogPassiveWaitLabels mirrors PASSIVE_WAIT_LABELS in <ops-repo>/scripts/mesh-intake-sweep.py
// (an internal ticket, this suban internal ticket) — the label vocabulary that means "this
// backlog card is deliberately parked, do not auto-promote it". Kept as ONE flat set,
// unlike the Python script's ABSOLUTE_NO_PROMOTE_LABELS ⊂ PASSIVE_WAIT_LABELS split:
// that split exists to let a passed due_date override the non-absolute half
// (due_wake_overrides_park), and this rule does not implement due_date/wake:<type>
// overrides at all (see the package doc comment on BacklogPromotionAdvisoryService for
// the full list of known gaps) — so every label below parks unconditionally here.
var backlogPassiveWaitLabels = map[string]struct{}{
	"kind:monitor": {}, "kind:verify": {}, "awaiting-window": {},
	"no-alex-triage": {}, "backlog-candidate": {},
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
// <ops-repo>/scripts/mesh-intake-sweep.py's backlog→todo promotion decision (an internal ticket,
// suban internal ticket — unit 1 of 4: ship advisory here, THEN a 7-day parallel-run diff
// against the live Python sweep, THEN a go/no-go on that log,
// THEN cutover + sweep retirement).
//
// SweepAdvisory NEVER calls MoveTask and NEVER posts a comment — counts+logs only, per
// an internal ticket's own description ("deployed in advisory mode only ... never moves a
// task"). It reproduces the sweep's two central, previously-incident-causing
// distinctions:
//
//   - a task DEMOTED into backlog from a working status, with NO dependencies, is a
//     deliberate park and must not be promoted just because all-deps-cleared is
//     vacuously true for an empty dependency set (an internal ticket: the sweep once undid such
//     a park in 26 minutes);
//   - that demotion-park guard must NOT fire on a task that HAS dependencies — with
//     real deps, "promote once they clear" is itself the informative event, and
//     mesh-intake-sweep.py's was_deliberately_parked() deliberately returns false the
//     moment dep_ids is non-empty, before ever looking at the activity log (an internal ticket:
//     a demoted, parented, dependency-bearing card was re-promoted twice over 7.5h by
//     the live sweep — the label route is what protects that class, not the
//     demotion-detector). A naive port that checks "last transition = demotion" WITHOUT
//     this dep_ids-empty gate would silently diverge from the sweep by OVER-protecting
//     exactly this class of card — see the parented+deps test case in
//     backlog_promotion_advisory_test.go, which exists specifically to catch that
//     mistake before it ships.
//
// Parity with the sweep (an internal ticket): umbrella-with-open-children, human-gate (cheap path), parent-gate, eval-fixture
// parent, ABSOLUTE_NO_PROMOTE labels and the due_date / wake:<type> overrides live in
// backlog_promotion_gates.go, ported function-for-function. Still NOT covered:
//
// INTAKE brakes (an internal ticket; source of truth is now fleet-balancer.py INTAKE, since the
// sweep is retired — an internal ticket): start_after, lease, fleet-paused assignee lane
// (WithLaneAvailability), blocking-edge dep_freeze, per-assignee capacity incl. this
// tick's promotions (WithCapacityBrake). NOT ported, server would PROMOTE where the
// balancer holds: a lane switched off in fiddler.json, operator_paused config, the
// turn_waits / parked-WAIT ledger, the ownerless-card team-slot check, MAX_MOVES and
// the per-project FLEET_INTAKE_LIMIT. Measure these as 0 before enforcing.
//
// server holds where the sweep promotes (safe direction, costs a delay):
//   - the comment-aware release tiers of is_human_gated (Alex answered → un-freeze);
//   - the QUEUE-BEHIND lift of a passive-wait label; wake-condition comment types.
//
// server would PROMOTE where the sweep holds (UNSAFE — enforcing must not flip until
// each is ported or measured at 0 in the divergence journal):
//   - epic-candidate-assigned-to-a-user skip (is_epic_candidate);
//   - Alex backlog-freeze-via-comment-phrase (has_user_backlog_freeze) — this rule
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
	projectRepo  repository.ProjectRepository
	now          func() time.Time
	// agentRepo, when set, lets the rule hold a card whose assignee lane is paused
	// (WithLaneAvailability). nil = lane pauses are not evaluated.
	agentRepo repository.AgentRepository
	// devCap > 0 turns on the per-assignee capacity brake (WithCapacityBrake).
	devCap int
	// liftRules, when set, makes the sweep journal workflow.shadow_lift_blocker for
	// promotable cards of projects with mid_pipeline.lift_backlog_blockers.
	liftRules midPipelineSource
}

// BacklogAdvisoryOption tunes NewBacklogPromotionAdvisoryService.
type BacklogAdvisoryOption func(*backlogPromotionAdvisoryService)

// WithLiftBlockersShadow turns on the lift_backlog_blockers shadow journal: for a
// project that sets the flag, each card the advisory would promote also gets a
// workflow.shadow_lift_blocker activity entry. Nothing is moved either way.
func WithLiftBlockersShadow(rules midPipelineSource) BacklogAdvisoryOption {
	return func(s *backlogPromotionAdvisoryService) { s.liftRules = rules }
}

// WithLaneAvailability holds a card whose agent assignee is fleet-paused (or no longer
// exists). Port of the lane_ok brake in fleet-balancer.py INTAKE, limited to what the
// server can see: Agent.FleetPaused. The balancer's other half — a lane switched off in
// fiddler.json — is config the server does not have (see the type doc).
func WithLaneAvailability(agents repository.AgentRepository) BacklogAdvisoryOption {
	return func(s *backlogPromotionAdvisoryService) { s.agentRepo = agents }
}

// WithCapacityBrake holds a card whose agent assignee already carries devCap cards in
// todo+in_progress+review, counting the cards this tick already advised to promote —
// the balancer's `load[aid] >= DEV_CAP` check with its in-tick `load[aid] += 1`.
// devCap mirrors FLEET_BALANCE_DEV_CAP (default 4); <= 0 leaves the brake off.
func WithCapacityBrake(devCap int) BacklogAdvisoryOption {
	return func(s *backlogPromotionAdvisoryService) { s.devCap = devCap }
}

// NewBacklogPromotionAdvisoryService constructs a BacklogPromotionAdvisoryService.
func NewBacklogPromotionAdvisoryService(
	taskRepo repository.TaskRepository,
	statusRepo repository.TaskStatusRepository,
	depRepo repository.TaskDependencyRepository,
	activityRepo repository.ActivityLogRepository,
	projectRepo repository.ProjectRepository,
	opts ...BacklogAdvisoryOption,
) BacklogPromotionAdvisoryService {
	svc := &backlogPromotionAdvisoryService{
		taskRepo:     taskRepo,
		statusRepo:   statusRepo,
		depRepo:      depRepo,
		activityRepo: activityRepo,
		projectRepo:  projectRepo,
		now:          time.Now,
	}
	for _, o := range opts {
		o(svc)
	}
	return svc
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
	// project_id -> workspace_id, one fetch per project per tick (§workspace scope guard).
	projectWSCache := make(map[uuid.UUID]uuid.UUID)

	liftEnabled := make(map[uuid.UUID]bool) // project -> mid_pipeline.lift_backlog_blockers
	// assignee -> cards already advised to promote this tick, added to the DB load.
	tickLoad := make(map[uuid.UUID]int)
	decisions := make([]BacklogPromotionDecision, 0, len(tasks))
	for i := range tasks {
		task := &tasks[i]
		promote, reason, err := s.evaluate(ctx, task, nameCatCache, parentCache, projectWSCache)
		if err != nil {
			reason = fmt.Sprintf("guard lookup failed, fail-closed no-promote: %v", err)
			promote = false
		}
		if promote {
			if why, cerr := s.capacityHold(ctx, task, projectWSCache, tickLoad); cerr != nil {
				promote, reason = false, fmt.Sprintf("guard lookup failed, fail-closed no-promote: capacity: %v", cerr)
			} else if why != "" {
				promote, reason = false, why
			} else if task.AssigneeID != nil {
				tickLoad[*task.AssigneeID]++
			}
		}
		decisions = append(decisions, BacklogPromotionDecision{TaskID: task.ID, Promote: promote, Reason: reason})
		log.Printf("[backlog-promotion-advisory] task=%s promote=%v reason=%q", task.ID, promote, reason)
		if promote {
			s.journalLift(ctx, task, reason, liftEnabled, projectWSCache)
		}
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
	projectWSCache map[uuid.UUID]uuid.UUID,
) (promote bool, reason string, err error) {
	// 0. Workspace scope. mesh-intake-sweep.py only ever polls ONE workspace
	// (WORKSPACE_ID in <ops-repo>/scripts/mesh-intake-sweep.py) — a task whose project belongs
	// to any OTHER workspace (Sandbox, Alex's personal workspace under Codex;
	// Editorial) is invisible to the sweep, which never promotes it because it never
	// sees it. Promoting such a card server-side would be NEW behaviour, not parity
	// (Robin, an internal ticket, 2026-09-21T16:16Z: "enforcing только на workspace EVC ...
	// паритет, а не новое поведение"). Checked first: it decides which rule set even
	// applies, and a task outside scope should never accrue an in-scope reason string.
	ws, wsErr := s.projectWorkspaceOf(ctx, task.ProjectID, projectWSCache)
	if wsErr != nil {
		return false, "", fmt.Errorf("resolve project workspace: %w", wsErr)
	}
	if ws != backlogInScopeWorkspaceID {
		return false, "workspace outside EVC scope (not covered by mesh-intake-sweep.py)", nil
	}

	// 1. Park labels and the wake overrides — cheapest guard (no extra query), checked
	// first, mirroring mesh-intake-sweep.py's own ordering (is_passive_wait() runs
	// before any lookup). See evaluateWake for the wake:<type> / due_date rules.
	hold, woke, why := s.evaluateWake(task)
	if hold {
		return false, why, nil
	}

	// 1a. Umbrella with open children (sweep branch 1a-bis, umbrella_with_open_children):
	// a parent is fed only on closure, when every child is done/cancelled, so it is
	// never promoted itself while one is open. Sits after the park/wake hold and
	// before anything that could promote — a woken card is still an umbrella.
	umbrella, umbErr := s.hasOpenChildren(ctx, task.ID)
	if umbErr != nil {
		return false, "", fmt.Errorf("list subtasks: %w", umbErr)
	}
	if umbrella {
		return false, "umbrella with open children", nil
	}

	// 2. Human-gate (is_human_gated's cheap path — see humanGateReason) and the
	// Agent-Eval fixture / parent-gate skips (parent_is_eval_fixture, parent_awaits_human).
	if why := humanGateReason(task); why != "" {
		return false, why, nil
	}
	if why := needsSourceReason(task); why != "" {
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

	// 2a. Scheduling, lease and lane brakes — fleet_intake.evaluate: start_after not
	// reached, card leased, assignee lane unavailable.
	if why, lerr := s.laneHold(ctx, task); lerr != nil {
		return false, "", fmt.Errorf("lane lookup: %w", lerr)
	} else if why != "" {
		return false, why, nil
	}

	// 3. Dependencies — blocking edges only (dependency_type == blocks), the
	// DEP_BLOCKING_TYPES set of human_gate.dep_freeze_reason that fleet-balancer INTAKE
	// uses (the single promoter after an internal ticket). Cleared = done OR cancelled.
	deps, err := s.depRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return false, "", fmt.Errorf("list dependencies: %w", err)
	}
	depIDs := make([]uuid.UUID, 0, len(deps))
	for _, d := range deps {
		// fleet-balancer INTAKE freezes on human_gate.dep_freeze_reason, which looks at
		// DEP_BLOCKING_TYPES = {"blocks"} only; a relates_to / is_child_of edge is not a
		// blocker there. (The sweep this rule was first ported from took every edge.)
		if d.DependencyType != domain.DependencyTypeBlocks {
			continue
		}
		depIDs = append(depIDs, d.DependsOnTaskID)
	}

	// 4. Deliberate-park guard. Gated on depIDs being EMPTY, exactly like
	// was_deliberately_parked()'s own `if dep_ids: return False` short-circuit — a
	// task WITH dependencies is judged by whether those dependencies are cleared
	// (step 6), never by its move history alone. This is the an internal ticket trap: a naive
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

// projectWorkspaceOf resolves a project's workspace once per tick, cached the same
// way as nameCatCache/parentCache above. A project that cannot be read (error or
// gone) is an error, which the caller turns into fail-closed no-promote — we cannot
// prove the task is in scope, so it is treated as out of scope's safe side (hold).
func (s *backlogPromotionAdvisoryService) projectWorkspaceOf(
	ctx context.Context, projectID uuid.UUID, cache map[uuid.UUID]uuid.UUID,
) (uuid.UUID, error) {
	if ws, ok := cache[projectID]; ok {
		return ws, nil
	}
	p, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return uuid.Nil, err
	}
	if p == nil {
		return uuid.Nil, fmt.Errorf("project %s not found", projectID)
	}
	cache[projectID] = p.WorkspaceID
	return p.WorkspaceID, nil
}

// wasDeliberatelyParked mirrors mesh-intake-sweep.py's was_deliberately_parked(),
// called ONLY when the task has no dependencies (see the caller). Delegates to the
// shared implementation in deliberate_park_guard.go — also used by
// MonitorPromotionService — so the activity-log read and its
// interpretation live in exactly one place.
func (s *backlogPromotionAdvisoryService) wasDeliberatelyParked(
	ctx context.Context,
	task *domain.Task,
	nameCatCache map[uuid.UUID]map[string]domain.StatusCategory,
) (bool, error) {
	return wasDeliberatelyParkedFromBacklog(ctx, s.activityRepo, s.statusRepo, task, nameCatCache)
}

// hasOpenChildren reports whether the task has at least one child that is not
// done/cancelled. A child whose status cannot be resolved counts as open, like the
// sweep's missing-closure-signal case (fail closed). Children may live in another
// project; status ids are globally unique, so a lookup by StatusID is enough.
func (s *backlogPromotionAdvisoryService) hasOpenChildren(ctx context.Context, taskID uuid.UUID) (bool, error) {
	subtasks, err := s.taskRepo.ListSubtasks(ctx, taskID)
	if err != nil {
		return false, err
	}
	if len(subtasks) == 0 {
		return false, nil
	}
	categories := make(map[uuid.UUID]domain.StatusCategory, len(subtasks))
	for _, sub := range subtasks {
		if _, ok := categories[sub.StatusID]; ok {
			continue
		}
		st, err := s.statusRepo.GetByID(ctx, sub.StatusID)
		if err != nil {
			return false, err
		}
		if st != nil {
			categories[sub.StatusID] = st.Category
		}
	}
	return !allSubtasksTerminal(subtasks, categories), nil
}

// journalLift writes workflow.shadow_lift_blocker for a card the advisory would
// promote, when its project opted in. It never moves the card; a rules or
// journal failure is logged and skipped so the advisory sweep is unaffected.
func (s *backlogPromotionAdvisoryService) journalLift(
	ctx context.Context, task *domain.Task, reason string,
	enabled map[uuid.UUID]bool, wsCache map[uuid.UUID]uuid.UUID,
) {
	if s.liftRules == nil || s.activityRepo == nil {
		return
	}
	on, seen := enabled[task.ProjectID]
	if !seen {
		wf, err := s.liftRules.GetProjectWorkflowRules(ctx, task.ProjectID, nil)
		on = err == nil && wf != nil && wf.MidPipeline != nil && wf.MidPipeline.LiftBacklogBlockers
		enabled[task.ProjectID] = on
	}
	if !on {
		return
	}
	ws, err := s.projectWorkspaceOf(ctx, task.ProjectID, wsCache)
	if err != nil {
		log.Printf("[lift-blockers-shadow] task %s: %v", task.ID, err)
		return
	}
	changes, _ := json.Marshal(map[string]interface{}{"mode": "shadow", "would": "promote_to_todo", "reason": reason})
	entry := &domain.ActivityLog{
		ID: uuid.New(), WorkspaceID: ws, EntityType: "task", EntityID: task.ID,
		Action: "workflow.shadow_lift_blocker", ActorID: uuid.Nil, ActorType: domain.ActorTypeSystem,
		Changes: changes, CreatedAt: timeNow(),
	}
	if err := s.activityRepo.Create(ctx, entry); err != nil {
		log.Printf("[lift-blockers-shadow] task %s: %v", task.ID, err)
	}
}

// laneHold ports the cheap pre-comment brakes of fleet_intake.evaluate that are plain
// task fields: start_after (schedule_ready), a live checkout ("leased"), and the
// assignee lane. A start_after in the future holds; so does a held checkout. The lane
// check applies only to an agent assignee and only when WithLaneAvailability is wired:
// a missing agent row or Agent.FleetPaused() means no promotion.
func (s *backlogPromotionAdvisoryService) laneHold(ctx context.Context, t *domain.Task) (string, error) {
	if t.StartAfter != nil && t.StartAfter.After(s.now()) {
		return fmt.Sprintf("start_after %s not reached", t.StartAfter.UTC().Format(time.RFC3339)), nil
	}
	// An expired checkout is no lease (same rule as ResolveRuntimeState).
	if t.CheckedOutBy != nil && (t.CheckoutExpires == nil || t.CheckoutExpires.After(s.now())) {
		return "leased (checked out)", nil
	}
	if s.agentRepo == nil || t.AssigneeID == nil || t.AssigneeType != domain.AssigneeTypeAgent {
		return "", nil
	}
	a, err := s.agentRepo.GetByID(ctx, *t.AssigneeID)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "assignee lane unavailable (agent not found)", nil
	}
	if a.FleetPaused() {
		return "assignee lane unavailable (fleet paused)", nil
	}
	return "", nil
}

// capacityHold ports `load[aid] >= DEV_CAP` of intake_team: an agent assignee already
// holding devCap cards in todo/in_progress/review (plus those advised this tick) gets no
// further promotion. Unassigned cards are not evaluated — the balancer's team-slot
// check for ownerless cards needs the project roster, which the server does not hold.
func (s *backlogPromotionAdvisoryService) capacityHold(
	ctx context.Context, t *domain.Task, wsCache map[uuid.UUID]uuid.UUID, tickLoad map[uuid.UUID]int,
) (string, error) {
	if s.devCap <= 0 || t.AssigneeID == nil || t.AssigneeType != domain.AssigneeTypeAgent {
		return "", nil
	}
	ws, err := s.projectWorkspaceOf(ctx, t.ProjectID, wsCache)
	if err != nil {
		return "", err
	}
	load := tickLoad[*t.AssigneeID]
	for _, cat := range []domain.StatusCategory{
		domain.StatusCategoryTodo, domain.StatusCategoryInProgress, domain.StatusCategoryReview,
	} {
		c := cat
		_, total, err := s.taskRepo.ListByAssignee(ctx, ws, *t.AssigneeID, domain.AssigneeTypeAgent,
			repository.AssigneeTaskFilter{StatusCategory: &c, Limit: 1})
		if err != nil {
			return "", err
		}
		load += total
	}
	if load >= s.devCap {
		return fmt.Sprintf("capacity %d>=%d for assignee", load, s.devCap), nil
	}
	return "", nil
}
