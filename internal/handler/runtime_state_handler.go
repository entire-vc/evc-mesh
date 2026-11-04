package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// putRuntimeStateRequest is the body of PUT /tasks/:task_id/runtime-state.
// `kind: null` (or an absent kind alongside no other field) clears the state.
type putRuntimeStateRequest struct {
	Kind   *string    `json:"kind"`
	Since  *time.Time `json:"since"`
	Until  *time.Time `json:"until"`
	Ref    *string    `json:"ref"`
	Lane   *string    `json:"lane"`
	Reason *string    `json:"reason"`
	// ReasonCode is a code from the closed domain.RuntimeReasonCode list; the web
	// renders it in the viewer's language. ReasonParams fill its placeholders.
	ReasonCode   *string           `json:"reason_code"`
	ReasonParams map[string]string `json:"reason_params"`
	// Constraints is the operational-constraints list (additive v1). Absent keeps
	// the legacy behaviour; it never bypasses the malformed-write rule below.
	Constraints []domain.OperationalConstraint `json:"constraints"`
}

// PutRuntimeState handles PUT /tasks/:task_id/runtime-state.
//
// Who may write is decided by the route (workspace access + update_task), the same
// gate as human-gate: fleet service agents report on behalf of lanes, so tying it to
// assignee/checkout holder would lock out exactly the writer this endpoint is for.
// 422 on a bad value, naming the field.
func (h *TaskHandler) PutRuntimeState(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	var req putRuntimeStateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	// An omitted kind next to other fields is a malformed write, not a clear:
	// silently deleting the stored state would also drop what the caller sent.
	if req.Kind == nil && (req.Since != nil || req.Until != nil || req.Ref != nil || req.Lane != nil || req.Reason != nil || req.ReasonCode != nil || req.ReasonParams != nil || req.Constraints != nil) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"error":      "Unprocessable Entity",
			"error_code": apierror.CodeValidationFailed,
			"message":    "invalid runtime_state: kind is required unless the state is being cleared",
			"field":      "kind",
		})
	}

	var state *domain.RuntimeState
	if req.Kind != nil {
		state = &domain.RuntimeState{Kind: domain.RuntimeKind(*req.Kind), Until: req.Until, Ref: req.Ref, Lane: req.Lane, Reason: req.Reason, ReasonCode: req.ReasonCode, ReasonParams: req.ReasonParams, Constraints: req.Constraints}
		if req.Since != nil {
			state.Since = *req.Since
		}
	}
	return h.writeRuntimeState(c, taskID, state)
}

// DeleteRuntimeState handles DELETE /tasks/:task_id/runtime-state.
func (h *TaskHandler) DeleteRuntimeState(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskService)
	if err != nil {
		return handleError(c, err)
	}
	if _, err := h.taskService.SetRuntimeState(c.Request().Context(), taskID, nil); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *TaskHandler) writeRuntimeState(c echo.Context, taskID uuid.UUID, state *domain.RuntimeState) error {
	eff, err := h.taskService.SetRuntimeState(c.Request().Context(), taskID, state)
	if err != nil {
		var vErr *domain.RuntimeStateValidationError
		if errors.As(err, &vErr) {
			return c.JSON(http.StatusUnprocessableEntity, map[string]any{
				"error":      "Unprocessable Entity",
				"error_code": apierror.CodeValidationFailed,
				"message":    "invalid runtime_state: " + vErr.Field + " " + vErr.Message,
				"field":      vErr.Field,
			})
		}
		return handleError(c, err)
	}
	if eff == nil {
		return c.JSON(http.StatusOK, map[string]any{"runtime_state": nil})
	}
	h.attachDependencyConstraints(c, taskID, eff)
	return c.JSON(http.StatusOK, eff)
}

// blockingEdgeReader is the batch read the board uses; the Postgres repository
// has it, and a reader without it is asked one task at a time.
type blockingEdgeReader interface {
	ListBlockingByTasks(ctx context.Context, taskIDs []uuid.UUID) (map[uuid.UUID][]domain.TaskDependency, error)
}

// blockingEdges returns the blocks edges holding each task. ok=false means the
// edges could not be read: the caller then leaves the producer's own list as it
// is, never an invented empty one.
func (h *TaskHandler) blockingEdges(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]domain.TaskDependency, bool) {
	if h.depReader == nil || len(ids) == 0 {
		return nil, false
	}
	if b, ok := h.depReader.(blockingEdgeReader); ok {
		m, err := b.ListBlockingByTasks(ctx, ids)
		return m, err == nil
	}
	m := make(map[uuid.UUID][]domain.TaskDependency, len(ids))
	for _, id := range ids {
		edges, err := h.depReader.ListByTask(ctx, id)
		if err != nil {
			return nil, false
		}
		for _, e := range edges {
			if e.DependencyType == domain.DependencyTypeBlocks && !e.ConditionSatisfied {
				m[id] = append(m[id], e)
			}
		}
	}
	return m, true
}

// unionConstraints is the read-side union for one runtime state. The server adds
// the live Mesh blocks edges that still hold the card to whatever the producer
// wrote, without touching the stored value. A blocker the caller may not open
// stays in the list as an explicit constraint with no ref, so a 403 is never read
// as "no constraint". With no state at all, live blockers alone produce a derived
// waiting state, and so does a checkout-derived one; with none, nil stays nil.
func unionConstraints(c echo.Context, rs *domain.RuntimeState, edges []domain.TaskDependency) *domain.RuntimeState {
	if len(edges) == 0 {
		return rs
	}
	views := make([]domain.DependencyEdgeView, 0, len(edges))
	for _, e := range edges {
		views = append(views, domain.DependencyEdgeView{Edge: e, Visible: middleware.MayAccessTask(c, e.DependsOnTaskID)})
	}
	now := time.Now().UTC()
	derived := domain.DependencyConstraints(views, now)
	// A checkout-derived state was never written by a producer: it is a read-time
	// guess, so live blockers replace it with the derived waiting state.
	if rs == nil || rs.Source == domain.RuntimeStateSourceCheckout {
		if only := domain.DependencyOnlyRuntimeState(derived, views, now); only != nil {
			return only
		}
		return rs
	}
	rs.Constraints = domain.MergeConstraints(rs.Constraints, derived)
	return rs
}

// attachDependencyConstraints applies the union to the PUT response state.
func (h *TaskHandler) attachDependencyConstraints(c echo.Context, taskID uuid.UUID, rs *domain.RuntimeState) {
	if rs == nil {
		return
	}
	if m, ok := h.blockingEdges(c.Request().Context(), []uuid.UUID{taskID}); ok {
		unionConstraints(c, rs, m[taskID])
	}
}

// attachTaskConstraints applies the union to tasks read through detail or list
// endpoints, in one batch. Closed cards carry no active blockers.
func (h *TaskHandler) attachTaskConstraints(c echo.Context, tasks []*domain.Task) {
	if h.depReader == nil || len(tasks) == 0 {
		return
	}
	ctx := c.Request().Context()
	cats := map[uuid.UUID]domain.StatusCategory{}
	var ids []uuid.UUID
	var open []*domain.Task
	for _, t := range tasks {
		cat := t.StatusCategory
		if cat == "" {
			var seen bool
			if cat, seen = cats[t.StatusID]; !seen {
				if st, err := h.taskService.GetStatusByID(ctx, t.StatusID); err == nil && st != nil {
					cat = st.Category
				}
				cats[t.StatusID] = cat
			}
		}
		if cat == domain.StatusCategoryDone || cat == domain.StatusCategoryCancelled {
			// a closed card carries no active constraint, derived or stored; the
			// stored value itself stays untouched, only this response drops them
			if t.RuntimeState != nil && len(t.RuntimeState.Constraints) > 0 {
				rs := *t.RuntimeState
				rs.Constraints = nil
				t.RuntimeState = &rs
			}
			continue
		}
		ids = append(ids, t.ID)
		open = append(open, t)
	}
	m, ok := h.blockingEdges(ctx, ids)
	if !ok {
		return
	}
	for _, t := range open {
		t.RuntimeState = unionConstraints(c, t.RuntimeState, m[t.ID])
	}
}

// WithDependencyReader lets task reads union live Mesh dependencies into
// runtime_state.constraints.
func (h *TaskHandler) WithDependencyReader(r repository.TaskDependencyRepository) *TaskHandler {
	h.depReader = r
	return h
}
