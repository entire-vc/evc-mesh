package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// BindingProposalStore is the persistence the proposal endpoints need.
type BindingProposalStore interface {
	Create(ctx context.Context, p *domain.StoredBindingProposal) error
	Get(ctx context.Context, id uuid.UUID) (*domain.StoredBindingProposal, error)
	// EdgeStateFingerprint digests the edges around taskID; read it before the
	// generation so a later change is detected by ApplyConfirm.
	EdgeStateFingerprint(ctx context.Context, taskID uuid.UUID) (string, error)
	// ApplyConfirm consumes the pending proposal and writes the edge condition
	// in one transaction. domain.ErrReplay / domain.ErrStaleGeneration mean
	// nothing changed; any error leaves the edge and the pending proposal intact.
	ApplyConfirm(ctx context.Context, proposalID, edgeID, taskID, by uuid.UUID, at time.Time, expectFingerprint string) (*domain.TaskDependency, error)
}

// WithConditionResolver wires the auto-transition hook run when a confirmed
// binding releases its blocker edge (same hook UpdateCondition uses).
func (h *DependencyHandler) WithConditionResolver(r service.DependencyResolver) *DependencyHandler {
	h.resolver = r
	return h
}

// WithBindingProposals wires the proposal store.
func (h *DependencyHandler) WithBindingProposals(s BindingProposalStore) *DependencyHandler {
	h.proposals = s
	return h
}

func actorID(c echo.Context) (uuid.UUID, bool) {
	if id, err := middleware.GetAgentID(c); err == nil {
		return id, true
	}
	if id, err := middleware.GetUserID(c); err == nil {
		return id, true
	}
	return uuid.Nil, false
}

type proposeBindingRequest struct {
	NewCondition   domain.DependencyCondition `json:"new_condition"`
	Sources        []string                   `json:"sources"`
	Scope          string                     `json:"scope"`
	BaseGeneration string                     `json:"base_generation"`
}

// ProposeBinding handles POST /tasks/:task_id/dependencies/:dep_id/binding-proposals.
// It only records the suggestion; it never changes the edge.
func (h *DependencyHandler) ProposeBinding(c echo.Context) error {
	taskID, err1 := uuid.Parse(c.Param("task_id"))
	depID, err2 := uuid.Parse(c.Param("dep_id"))
	if err1 != nil || err2 != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id or dep_id"))
	}
	actor, ok := actorID(c)
	if h.proposals == nil || !ok {
		return c.JSON(http.StatusServiceUnavailable, apierror.BadRequest("binding proposals unavailable"))
	}
	var req proposeBindingRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	edge, err := h.findEdge(c, taskID, depID)
	if err != nil {
		return handleError(c, err)
	}
	if edge == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Dependency"))
	}
	if edge.DependencyType != domain.DependencyTypeBlocks {
		return c.JSON(http.StatusUnprocessableEntity, apierror.BadRequest("only a blocks edge has a binding"))
	}
	old := edge.Condition
	if old == "" {
		old = domain.DependencyConditionTaskDone
	}
	p := &domain.StoredBindingProposal{
		BindingProposal: domain.BindingProposal{ID: uuid.NewString(), EdgeID: depID, Old: old, New: req.NewCondition,
			Sources: req.Sources, Scope: req.Scope, Generation: req.BaseGeneration},
		ProposedBy: actor,
	}
	if err := p.Validate(); err != nil {
		return c.JSON(http.StatusUnprocessableEntity, apierror.BadRequest(err.Error()))
	}
	if err := h.proposals.Create(c.Request().Context(), p); err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, p)
}

// ConfirmBinding handles POST /tasks/:task_id/dependencies/:dep_id/binding-proposals/:proposal_id/confirm.
// Only the dependent card's assignee (the owner) may confirm, never the proposer;
// a held human gate refuses; the proposal's base generation must equal the
// current one; it applies once. The edge changes through the regular
// UpdateCondition path without evidence, so it is never marked satisfied here.
func (h *DependencyHandler) ConfirmBinding(c echo.Context) error {
	taskID, err1 := uuid.Parse(c.Param("task_id"))
	depID, err2 := uuid.Parse(c.Param("dep_id"))
	propID, err3 := uuid.Parse(c.Param("proposal_id"))
	if err1 != nil || err2 != nil || err3 != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid id"))
	}
	actor, ok := actorID(c)
	if h.proposals == nil || h.taskService == nil || !ok {
		return c.JSON(http.StatusServiceUnavailable, apierror.BadRequest("binding proposals unavailable"))
	}
	ctx := c.Request().Context()
	sp, err := h.proposals.Get(ctx, propID)
	if err != nil {
		return handleError(c, err)
	}
	if sp == nil || sp.EdgeID != depID {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Binding proposal"))
	}
	task, err := h.taskService.GetByID(ctx, taskID)
	if err != nil {
		return handleError(c, err)
	}
	edge, err := h.findEdge(c, taskID, depID)
	if err != nil {
		return handleError(c, err)
	}
	if task == nil || edge == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Dependency"))
	}
	// Read the edge-state digest BEFORE the generation: anything that changes the
	// edges after this point makes ApplyConfirm refuse instead of overwrite.
	fingerprint, err := h.proposals.EdgeStateFingerprint(ctx, taskID)
	if err != nil {
		return handleError(c, err)
	}
	cur, err := h.projection(c, taskID)
	if err != nil {
		return handleError(c, err)
	}
	isOwner := task.AssigneeID != nil && *task.AssigneeID == actor
	// An edge whose condition moved since the proposal was made is stale even if
	// the generation happened to match.
	if edge.Condition != sp.Old && (edge.Condition != "" || sp.Old != domain.DependencyConditionTaskDone) {
		return c.JSON(http.StatusConflict, apierror.BadRequest(domain.ErrStaleGeneration.Error()))
	}
	if _, err = sp.Confirm(isOwner, actor == sp.ProposedBy, task.HumanGate, cur.Generation); err != nil {
		return c.JSON(confirmStatus(err), apierror.BadRequest(err.Error()))
	}
	// One transaction: re-check the edges, consume the proposal, write the edge.
	updated, err := h.proposals.ApplyConfirm(ctx, propID, depID, taskID, actor, time.Now().UTC(), fingerprint)
	if err != nil {
		if errors.Is(err, domain.ErrReplay) || errors.Is(err, domain.ErrStaleGeneration) ||
			errors.Is(err, domain.ErrNotOwner) || errors.Is(err, domain.ErrHeldGate) {
			return c.JSON(confirmStatus(err), apierror.BadRequest(err.Error()))
		}
		return handleError(c, err)
	}
	if h.resolver != nil && updated != nil && updated.ConditionSatisfied && !edge.ConditionSatisfied {
		if rerr := h.resolver.CheckDependencyResolution(ctx, updated.DependsOnTaskID); rerr != nil {
			log.Printf("[binding-proposal] WARNING: resolution check after confirm on %s failed: %v", updated.DependsOnTaskID, rerr)
		}
	}
	return c.JSON(http.StatusOK, updated)
}

func confirmStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrStaleGeneration), errors.Is(err, domain.ErrReplay):
		return http.StatusConflict
	case errors.Is(err, domain.ErrNotOwner), errors.Is(err, domain.ErrAutomatedActor), errors.Is(err, domain.ErrHeldGate):
		return http.StatusForbidden
	}
	return http.StatusUnprocessableEntity
}

// findEdge returns the edge depID of taskID, or nil.
func (h *DependencyHandler) findEdge(c echo.Context, taskID, depID uuid.UUID) (*domain.TaskDependency, error) {
	deps, err := h.depService.ListByTask(c.Request().Context(), taskID)
	if err != nil {
		return nil, err
	}
	for i := range deps {
		if deps[i].ID == depID {
			return &deps[i], nil
		}
	}
	return nil, nil
}
