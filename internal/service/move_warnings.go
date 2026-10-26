package service

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// MoveWarningOpenChildren is the code of the warning a move to a done-category
// status carries when the task still has open children.
const MoveWarningOpenChildren = "open_children"

// maxWarnedChildIDs bounds how many child ids one warning lists.
const maxWarnedChildIDs = 20

// MoveWarning is advisory information about a move that succeeded. It never
// blocks the move.
type MoveWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// OpenChildren is the number of children that are not done/cancelled.
	OpenChildren int `json:"open_children,omitempty"`
	// ChildIDs lists up to maxWarnedChildIDs of them.
	ChildIDs []uuid.UUID `json:"child_ids,omitempty"`
}

// MoveWarnings collects the warnings of one MoveTask call. It travels in the
// context so that TaskService.MoveTask keeps its error-only signature (the
// interface has many implementers) and only a caller that asked for warnings
// pays for computing them.
type MoveWarnings struct {
	mu    sync.Mutex
	items []MoveWarning
}

type moveWarningsKey struct{}

// ContextWithMoveWarnings returns a context whose MoveTask call records its
// advisory warnings into the returned collector.
func ContextWithMoveWarnings(ctx context.Context) (context.Context, *MoveWarnings) {
	w := &MoveWarnings{}
	return context.WithValue(ctx, moveWarningsKey{}, w), w
}

// MoveWarningsFromContext returns the collector ContextWithMoveWarnings put in
// ctx, or nil.
func MoveWarningsFromContext(ctx context.Context) *MoveWarnings {
	w, _ := ctx.Value(moveWarningsKey{}).(*MoveWarnings)
	return w
}

// Add records a warning.
func (w *MoveWarnings) Add(m MoveWarning) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.items = append(w.items, m)
}

// Items returns a copy of the collected warnings; nil-safe.
func (w *MoveWarnings) Items() []MoveWarning {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]MoveWarning(nil), w.items...)
}

// collectParentCloseWarning records an open_children warning when task has just
// been moved to a done-category status while some of its children (parent_task_id
// subtasks or is_child_of edges pointing at it) are not done/cancelled. The
// move already succeeded; any lookup failure only drops the warning.
func (s *taskService) collectParentCloseWarning(ctx context.Context, task *domain.Task) {
	w := MoveWarningsFromContext(ctx)
	if w == nil || task == nil {
		return
	}
	status, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || status == nil || status.Category != domain.StatusCategoryDone {
		return
	}

	children := map[uuid.UUID]domain.Task{}
	if subs, err := s.taskRepo.ListSubtasks(ctx, task.ID); err == nil {
		for _, c := range subs {
			children[c.ID] = c
		}
	}
	if s.depRepo != nil {
		if deps, err := s.depRepo.ListDependents(ctx, task.ID); err == nil {
			for _, d := range deps {
				if d.DependencyType != domain.DependencyTypeIsChildOf || d.TaskID == task.ID {
					continue
				}
				if _, ok := children[d.TaskID]; ok {
					continue
				}
				if c, gerr := s.taskRepo.GetByID(ctx, d.TaskID); gerr == nil && c != nil {
					children[c.ID] = *c
				}
			}
		}
	}

	categories := map[uuid.UUID]domain.StatusCategory{}
	var open []uuid.UUID
	for id, c := range children {
		cat, ok := categories[c.StatusID]
		if !ok {
			st, serr := s.statusRepo.GetByID(ctx, c.StatusID)
			if serr == nil && st != nil {
				cat = st.Category
			}
			categories[c.StatusID] = cat
		}
		// An unresolvable status counts as open: warn rather than stay silent.
		if cat != domain.StatusCategoryDone && cat != domain.StatusCategoryCancelled {
			open = append(open, id)
		}
	}
	if len(open) == 0 {
		return
	}
	sort.Slice(open, func(i, j int) bool { return open[i].String() < open[j].String() })
	n := len(open)
	if len(open) > maxWarnedChildIDs {
		open = open[:maxWarnedChildIDs]
	}
	w.Add(MoveWarning{
		Code:         MoveWarningOpenChildren,
		Message:      "task moved to done but it still has open children; they were not closed",
		OpenChildren: n,
		ChildIDs:     open,
	})
}
