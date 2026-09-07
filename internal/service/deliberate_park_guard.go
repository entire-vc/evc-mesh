package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// This file holds the "was this backlog card deliberately parked by a status
// demotion?" check shared by BacklogPromotionAdvisoryService (#9f3f4064) and
// MonitorPromotionService (#1eb4fd7d). Extracted rather than duplicated: both callers
// need the identical activity-log read + interpretation, and a guard correct in one
// copy and stale in the other is exactly the kind of drift this package already warns
// about elsewhere (see monitor_promotion.go's absoluteNoPromoteLabels doc comment on
// the two label-set copies that diverged in bob/).

// activityStatusChange is the shape of the "status" key inside a task.moved activity
// log entry's Changes JSON — written by task_service.go's MoveTask as
// moveChanges["status"] = map[string]interface{}{"old": oldName, "new": newName}.
type activityStatusChange struct {
	Status *struct {
		Old string `json:"old"`
		New string `json:"new"`
	} `json:"status"`
}

// deliberateParkActivityPageSize bounds how far back wasDeliberatelyParkedFromBacklog
// looks for the most recent task.moved entry. mesh-intake-sweep.py fetches the task's
// full activity log (its GET .../activity call is unpaginated server-side); this rule
// uses a generous but bounded window instead — a card demoted long ago with many
// intervening activity-log entries (assignment changes, further moves) since could, in
// principle, fall outside it and read as "never moved" (not parked) where the sweep
// would still see the demotion. Accepted as a known gap: the #f928e5af parallel run is
// what would surface such a case as a named divergence to investigate, not something to
// guess a bigger number against up front.
const deliberateParkActivityPageSize = 50

// wasDeliberatelyParkedFromBacklog reports whether task's most recent status move (if
// any, within the lookback window) DEMOTED it into a backlog-category status from a
// non-backlog one — i.e. it is resting in backlog because something moved it back
// there, not because it was born there.
//
// Callers MUST gate this on "the task has no live blocking dependency" before calling
// it — see monitor_promotion.go's skipReason and backlog_promotion_advisory.go's
// evaluate(), both of which check this ONLY when the relevant dependency set is empty.
// mesh-intake-sweep.py's was_deliberately_parked() deliberately returns false the
// moment dep_ids is non-empty, before ever looking at the activity log (#bbf3db92: a
// demoted, parented, dependency-bearing card was re-promoted twice over 7.5h by the
// live sweep once its dependency cleared — the live sweep's "all deps cleared, and
// deps existed" IS the informative event for that class of card, and a demotion-check
// that ran unconditionally would silently re-park it forever). This function itself has
// no way to enforce that precondition — the two callers have different notions of
// "dependency" (all types unfiltered vs. blocks-type only) — so it is the caller's job.
//
// Fails CLOSED on an unresolvable status name, exactly like the Python original:
// backlog is the safe resting state, so an ambiguous read treats the task as parked
// rather than promotable.
func wasDeliberatelyParkedFromBacklog(
	ctx context.Context,
	activityRepo repository.ActivityLogRepository,
	statusRepo repository.TaskStatusRepository,
	task *domain.Task,
	nameCatCache map[uuid.UUID]map[string]domain.StatusCategory,
) (bool, error) {
	page, err := activityRepo.ListByTask(ctx, task.ID, pagination.Params{Page: 1, PageSize: deliberateParkActivityPageSize})
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

	nameCat, err := projectStatusNameCategories(ctx, statusRepo, task.ProjectID, nameCatCache)
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

// projectStatusNameCategories returns (and caches) a project's lowercased-trimmed
// status name → category map, used to interpret the OLD/NEW names an activity log
// entry carries (task_service.go logs status NAMES, not IDs — see moveChanges).
func projectStatusNameCategories(
	ctx context.Context,
	statusRepo repository.TaskStatusRepository,
	projectID uuid.UUID,
	cache map[uuid.UUID]map[string]domain.StatusCategory,
) (map[string]domain.StatusCategory, error) {
	if m, ok := cache[projectID]; ok {
		return m, nil
	}
	statuses, err := statusRepo.ListByProject(ctx, projectID)
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
