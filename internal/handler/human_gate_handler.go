package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	// Predicate is the four-question check (task #5d3dc714) and is REQUIRED on this
	// route. The audit measured that 40-45% of asks to Pavel were decidable from a rule
	// already written down; making the caller state the four answers, with reasons, is
	// what turns "I felt unsure" into something a reviewer can check afterwards.
	Predicate *domain.GateArmPredicate `json:"predicate"`
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
		Predicate:          req.Predicate,
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
// Users may always clear. Agents may clear exactly ONE shape — a gate they themselves
// armed through the API, which carries no marker comment (task #f933dc05) — and are
// otherwise refused with gateClearRefusal, the message that names the exits actually
// open to them rather than a bare 403 they would read as "I'm not allowed" and escalate.
//
// Why that one shape is not a weakening of the wall. An agent that raises an ask with a
// "❓ Blocking @pavel" marker has ALWAYS been able to take it back down by withdrawing
// it; withdrawing your own ask is a sanctioned exit, not a bypass. An agent that raises
// the same ask through POST /human-gate could not, for one reason only: ownership was
// read out of the comment thread, where an API arm leaves nothing, while the author sat
// on the task row the whole time. This closes that asymmetry and nothing else. The three
// shapes that still require a human are unchanged and are what the wall was always for:
// a gate armed by a USER, a gate armed by raw PATCH/UI (authorless by construction), and
// ANY gate carrying a live marker — including one armed via the API and then re-asked
// with a marker, since a marker means there is now an ask in the thread whose own
// withdrawal path applies.
//
// The check reads ownership from commentService (ONE predicate, the same one
// GetHumanGateOwner reports and releaseHumanGateOnWithdrawal gates on) rather than
// comparing task.GateAuthor here — a second copy of "who owns this ask" is exactly the
// drift this area has been bitten by before, and drift here means the API tells an agent
// something the server itself would refuse.
//
// Fail-closed: no commentService wired, a failed lookup, or any ambiguity leaves the
// agent refused. "I could not check" must never read as "checked, go ahead".
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
	clearedByOwnAuthor := false
	if task.HumanGate && actorType != domain.ActorTypeUser {
		if !h.agentOwnsAPIArmedGate(c.Request().Context(), task, actorID, actorType) {
			return c.JSON(http.StatusForbidden,
				apierror.Forbidden(h.gateClearRefusal(c.Request().Context(), task, actorID)))
		}
		clearedByOwnAuthor = true
	}

	if clearErr := h.taskService.ClearHumanGate(c.Request().Context(), taskID); clearErr != nil {
		return handleError(c, clearErr)
	}

	if clearedByOwnAuthor {
		h.postOwnGateClearedComment(c.Request().Context(), taskID, actorID)
	}

	fresh, err := h.taskService.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}
	fresh.URL = computeTaskURL(c.Request(), fresh.ID)
	return c.JSON(http.StatusOK, fresh)
}

// agentOwnsAPIArmedGate reports whether actorID is the recorded author of an
// API-armed, marker-less gate on task — the one shape an agent may clear with its
// own key (task #f933dc05).
//
// Deliberately delegates the whole judgement to commentService.GetHumanGateOwner
// instead of reading task.GateAuthor directly: that method is where the marker scan
// and the gate_author fallback are reconciled, and re-deriving either here would put
// a second, independently-maintained answer to "who owns this ask" into the codebase.
//
// ClearPath is load-bearing in the condition, not decoration. Owning a gate whose
// ClearPath is withdraw_marker means the ask lives in a comment and comes down the way
// it went up; only clear_endpoint says this endpoint is the door.
func (h *TaskHandler) agentOwnsAPIArmedGate(ctx context.Context, task *domain.Task, actorID uuid.UUID, actorType domain.ActorType) bool {
	if actorType != domain.ActorTypeAgent || h.commentService == nil || actorID == uuid.Nil {
		return false
	}
	info, err := h.commentService.GetHumanGateOwner(ctx, task.ID)
	if err != nil || info == nil {
		log.Printf("[human-gate] WARNING: ownership lookup for task %s failed, refusing the clear: %v", task.ID, err)
		return false
	}
	return info.ClearableByOwner &&
		info.ClearPath == domain.HumanGateClearPathClearEndpoint &&
		info.OwnerAgentID != nil && *info.OwnerAgentID == actorID
}

// postOwnGateClearedComment records an agent clearing its own API-armed gate.
//
// A marker withdrawal leaves a comment saying so, because withdrawing IS a comment.
// A DELETE leaves nothing visible on the thread, and a release nobody can see is how
// "the gate came down and no one knows why" becomes a question asked weeks later with
// no answer in the record. Authored as ActorTypeSystem/uuid.Nil for the same reason
// the raw-arm marker is (see task_handler.go's Update): a system-authored comment is
// the one thing no external caller can forge through the public comment API.
//
// Best-effort: a failed comment must never unwind a release that already happened.
// The wording deliberately avoids both the "❓ Blocking @" marker and every negator
// substring, so this comment can neither arm a gate nor be mistaken for a withdrawal
// by the scans that read the thread.
func (h *TaskHandler) postOwnGateClearedComment(ctx context.Context, taskID, actorID uuid.UUID) {
	if h.commentService == nil {
		return
	}
	_ = h.commentService.Create(ctx, &domain.Comment{
		TaskID:     taskID,
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf("🔓 Auto: human_gate снят автором собственного API-арма (agent: %s). "+
			"Маркерного коммента у этого гейта не было — снятие через DELETE /human-gate, "+
			"не отзыв аска.", actorID),
		IsInternal: true,
	})
}
