package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// HumanGateDecisionHandler exposes the third human_gate exit — "decision
// recorded" (task #c56339b1, docs/human-gate-decision-recorded.md) — over
// REST, so a gated task can be closed by an agent or the Telegram bridge
// without a trip through the UI (acceptance criterion 1).
type HumanGateDecisionHandler struct {
	commentService service.CommentService
	taskSvc        taskIDResolver
}

// NewHumanGateDecisionHandler creates a new HumanGateDecisionHandler.
func NewHumanGateDecisionHandler(cs service.CommentService, ts taskIDResolver) *HumanGateDecisionHandler {
	return &HumanGateDecisionHandler{commentService: cs, taskSvc: ts}
}

// recordDecisionRequest is the JSON body for POST /tasks/:task_id/human-gate-decisions.
type recordDecisionRequest struct {
	QuestionRef  *uuid.UUID `json:"question_ref"`
	CanonicalKey *string    `json:"canonical_key"`
	// DecidedBy is Pavel's user id. Required: this endpoint never infers WHO
	// decided from the authenticated caller, because the caller is very
	// often an agent recording on Pavel's behalf (provenance=bridged/attested).
	DecidedBy  uuid.UUID       `json:"decided_by"`
	Provenance string          `json:"provenance"`
	Channel    string          `json:"channel"`
	Quote      *string         `json:"quote"`
	ChannelRef json.RawMessage `json:"channel_ref"`
}

// revokeDecisionRequest is the JSON body for POST /human-gate-decisions/:decision_id/revoke.
type revokeDecisionRequest struct {
	Reason string `json:"reason"`
}

// Create handles POST /tasks/:task_id/human-gate-decisions — records a
// decision and, if the task's gate is currently live, releases it as a
// consequence (contract §3). The caller is recorded as RecordedBy UNLESS
// they are Pavel himself acting directly (provenance=direct), in which case
// RecordedBy stays nil per the domain model (contract §3: "recorded_by |
// null для direct").
func (h *HumanGateDecisionHandler) Create(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}

	var req recordDecisionRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.DecidedBy == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"decided_by": "decided_by is required",
		}))
	}

	input := domain.RecordHumanGateDecisionInput{
		TaskID:       taskID,
		QuestionRef:  req.QuestionRef,
		CanonicalKey: req.CanonicalKey,
		DecidedBy:    req.DecidedBy,
		Provenance:   domain.HumanGateProvenance(req.Provenance),
		Channel:      domain.HumanGateChannel(req.Channel),
		Quote:        req.Quote,
		ChannelRef:   req.ChannelRef,
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())
	// direct provenance is reserved for Pavel's own act inside Mesh; a
	// caller cannot self-declare it — it is only true when the authenticated
	// actor IS the user this decision is attributed to. Every other case
	// (an agent recording Pavel's answer from elsewhere) is at most attested
	// or bridged, and recorded_by names the recording agent (contract §2 P2:
	// direct is unforgeable BY CONSTRUCTION, not by convention).
	if input.Provenance == domain.HumanGateProvenanceDirect {
		if actorType != domain.ActorTypeUser || actorID != req.DecidedBy {
			return c.JSON(http.StatusForbidden, apierror.Forbidden(
				"provenance=direct requires the authenticated caller to BE decided_by — an agent recording on Pavel's behalf must use provenance=attested or bridged"))
		}
	} else if actorType == domain.ActorTypeAgent {
		input.RecordedBy = &actorID
	}

	decision, err := h.commentService.RecordHumanGateDecision(c.Request().Context(), input)
	if err != nil {
		return handleError(c, mapHumanGateDecisionError(err))
	}
	return c.JSON(http.StatusCreated, decision)
}

// Revoke handles POST /human-gate-decisions/:decision_id/revoke. Only a
// human user may call this — same guard as task_handler.go's PATCH
// {human_gate:false} 403, because a revocation re-freezes a task exactly
// the way that PATCH clears one, and agents must not be able to erase their
// own attested record on demand.
func (h *HumanGateDecisionHandler) Revoke(c echo.Context) error {
	decisionID, err := uuid.Parse(c.Param("decision_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid decision_id"))
	}

	var req revokeDecisionRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.Reason == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"reason": "reason is required",
		}))
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorType != domain.ActorTypeUser {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("revoking a human_gate decision requires user authentication"))
	}

	err = h.commentService.RevokeHumanGateDecision(c.Request().Context(), domain.RevokeHumanGateDecisionInput{
		DecisionID:    decisionID,
		RevokedBy:     actorID,
		RevokedByType: actorType,
		Reason:        req.Reason,
	})
	if err != nil {
		return handleError(c, mapHumanGateDecisionError(err))
	}
	return c.NoContent(http.StatusNoContent)
}

// List handles GET /tasks/:task_id/human-gate-decisions.
func (h *HumanGateDecisionHandler) List(c echo.Context) error {
	taskID, err := resolveTaskID(c.Request().Context(), c.Param("task_id"), h.taskSvc)
	if err != nil {
		return handleError(c, err)
	}
	list, err := h.commentService.ListHumanGateDecisions(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, mapHumanGateDecisionError(err))
	}
	return c.JSON(http.StatusOK, list)
}

// mapHumanGateDecisionError translates the service-layer sentinel errors
// into the right HTTP status via apierror, so handleError's generic 500
// fallback isn't what a caller sees for an ordinary "not found" or "already
// revoked".
func mapHumanGateDecisionError(err error) error {
	switch {
	case errors.Is(err, service.ErrHumanGateDecisionNotFound):
		return apierror.NotFound("HumanGateDecision")
	case errors.Is(err, service.ErrHumanGateDecisionAlreadyRevoked):
		return apierror.Conflict("this decision was already revoked")
	case errors.Is(err, service.ErrHumanGateDecisionCannotRevokeRevocation):
		return apierror.BadRequest("cannot revoke a revocation row")
	case errors.Is(err, service.ErrHumanGateDecisionRepoUnavailable):
		return apierror.ServiceUnavailable("human gate decisions are not configured on this deployment")
	default:
		return err
	}
}
