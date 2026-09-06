package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ArmHumanGateRequest is the body of POST /tasks/:task_id/human-gate — the explicit
// set_human_gate entry point (task #4545660b).
//
// gate_author is deliberately NOT a request field. It is taken from the caller's
// authenticated identity, the same way comment_handler.go derives author_type, so the
// answer to "who is waiting on Pavel here" cannot be forged by a caller filling in
// somebody else's id. That is the whole difference between this and the 21 text-grepping
// implementations it replaces: those read a claim, this records an identity.
type ArmHumanGateRequest struct {
	Reason             string     `json:"reason"`
	RecommendedDefault string     `json:"recommended_default"`
	Deadline           *time.Time `json:"deadline"`
	// Class is "hard" (default, never timed out) or "soft". Omitted means hard —
	// fail-closed, matching the column default: a gate is never softened by omission.
	Class string `json:"class"`
}

// ArmHumanGate handles POST /tasks/:task_id/human-gate.
//
// 422 (not 400) on a validation miss, matching the rest of task_handler.go's
// business-rule refusals: the request is well-formed JSON, it is the ASK that is
// incomplete. The response names the offending field, because "Validation failed" with
// no field is the message that made agents silently lose their first remember() write
// (CLAUDE-memory.md §9.2) — a refusal that does not say what to fix gets retried
// verbatim or read as "I'm not allowed".
func (h *TaskHandler) ArmHumanGate(c echo.Context) error {
	taskID, parseErr := uuid.Parse(c.Param("task_id"))
	if parseErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task id"))
	}

	var req ArmHumanGateRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())

	class := domain.HumanGateClassHard
	if req.Class == string(domain.HumanGateClassSoft) {
		class = domain.HumanGateClassSoft
	}

	in := domain.ArmHumanGateInput{
		TaskID:             taskID,
		Author:             actorID,
		AuthorType:         actorType,
		Reason:             req.Reason,
		RecommendedDefault: req.RecommendedDefault,
		Deadline:           req.Deadline,
		Class:              class,
		Source:             domain.ArmHumanGateSourceAPI,
	}

	if armErr := h.taskService.ArmHumanGate(c.Request().Context(), in); armErr != nil {
		var vErr *domain.ArmHumanGateValidationError
		if errors.As(armErr, &vErr) {
			return c.JSON(http.StatusUnprocessableEntity, map[string]any{
				"error": "Unprocessable Entity",
				// The field name goes in the MESSAGE too, not only in the separate
				// "field" key. A caller that logs or surfaces just the message —
				// which is what most of them do — would otherwise read "required —
				// a gate with no stated default cannot time out" and still not know
				// which field to add.
				"message": "cannot arm human_gate: " + vErr.Field + " " + vErr.Message,
				"field":   vErr.Field,
			})
		}
		return handleError(c, armErr)
	}

	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}
	h.attachHumanGateInfo(c.Request().Context(), task)
	task.URL = computeTaskURL(c.Request(), task.ID)
	return c.JSON(http.StatusOK, task)
}

// ClearHumanGate handles DELETE /tasks/:task_id/human-gate.
//
// Enforces the SAME user-only rule as PATCH {human_gate:false} — including reusing
// gateClearRefusal so an agent that lands here gets the message that names its actual
// options (withdraw your own marker, or record a decision) rather than a bare 403 it
// would read as "I'm not allowed" and escalate. Adding a second clearing endpoint with a
// weaker check would have re-opened the hole this card is closing, one level down.
func (h *TaskHandler) ClearHumanGate(c echo.Context) error {
	taskID, parseErr := uuid.Parse(c.Param("task_id"))
	if parseErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task id"))
	}

	task, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if task.HumanGate && actorType != domain.ActorTypeUser {
		return c.JSON(http.StatusForbidden,
			apierror.Forbidden(h.gateClearRefusal(c.Request().Context(), task, actorID)))
	}

	if clearErr := h.taskService.ClearHumanGate(c.Request().Context(), taskID); clearErr != nil {
		return handleError(c, clearErr)
	}

	fresh, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}
	fresh.URL = computeTaskURL(c.Request(), fresh.ID)
	return c.JSON(http.StatusOK, fresh)
}
