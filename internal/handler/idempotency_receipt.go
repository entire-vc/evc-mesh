package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// A keyed write records a receipt next to the row it wrote, in the same
// transaction (docs/idempotency.md). The helpers below let a
// write route answer a repeat from that receipt: whatever happened to the
// middleware's stored answer (a failed store, a 5xx after the commit, the
// 24 hours past), a repeat whose write exists gets that write back and writes
// nothing.

// idemReceipt is the receipt for this request's write on route and task, or
// nil when the request carries no Idempotency-Key.
func idemReceipt(c echo.Context, route string, taskID uuid.UUID) *domain.IdempotencyReceipt {
	return mw.IdempotencyRequestFrom(c.Request().Context()).Receipt(route, taskID)
}

// replayWritten answers a keyed repeat whose write already exists. A write
// route calls it before any check that could refuse the repeat (the checkout
// fence, a gate, the task's current state): the write happened, so the
// repeat reconciles to it. done is false when there is nothing to replay.
func replayWritten(c echo.Context, route string, taskID uuid.UUID, render func(json.RawMessage) error) (done bool, err error) {
	ctx := c.Request().Context()
	w, err := mw.IdempotencyRequestFrom(ctx).Written(ctx, route, taskID)
	if err != nil {
		// Not fatal: the write's own transaction still refuses a second row
		// for this key and answers from the receipt then.
		log.Printf("[idempotency] receipt lookup %s %s: %v", route, taskID, err)
		return false, nil
	}
	if w == nil {
		return false, nil
	}
	return true, answerWritten(c, w, render)
}

// replayAfterRefusal answers a keyed write that failed with the write its key
// produced meanwhile: one its own transaction found recorded (a concurrent
// repeat committed first), or one a concurrent repeat recorded while this
// request was refused by what that write changed (the task's version, its
// lock). done is false when the key wrote nothing: answer the error.
func replayAfterRefusal(c echo.Context, err error, route string, taskID uuid.UUID, render func(json.RawMessage) error) (done bool, answer error) {
	var w *domain.IdempotentWrite
	if errors.As(err, &w) {
		return true, answerWritten(c, w, render)
	}
	return replayWritten(c, route, taskID, render)
}

// answerWritten answers with the recorded write, or 409
// idempotency_key_reused when the key was used with another body.
func answerWritten(c echo.Context, w *domain.IdempotentWrite, render func(json.RawMessage) error) error {
	if !w.SameRequest {
		return c.JSON(http.StatusConflict, &apierror.Error{
			Code: http.StatusConflict, ErrorCode: mw.CodeIdempotencyKeyReused,
			Message: "this Idempotency-Key was already used with a different request body",
		})
	}
	c.Response().Header().Set(mw.HeaderIdempotentReplayed, "true")
	return render(w.Response)
}

// renderWritten answers status with the snapshot decoded into a fresh T,
// after fix (may be nil) has filled in per-response fields.
func renderWritten[T any](c echo.Context, status int, fix func(*T)) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		v := new(T)
		if err := json.Unmarshal(raw, v); err != nil {
			return handleError(c, err)
		}
		if fix != nil {
			fix(v)
		}
		return c.JSON(status, v)
	}
}

// IdempotencyReceiptFinder reads receipts; *postgres.IdempotencyRepo.
type IdempotencyReceiptFinder interface {
	FindReceipt(ctx context.Context, s domain.IdempotencyReceiptScope, hash string) (*domain.IdempotentWrite, error)
}

// IdempotencyReceiptHandler serves GET /me/idempotency-receipts: a caller that
// lost the answer to a keyed write reconciles by reading (docs/idempotency.md),
// without sending the write again.
type IdempotencyReceiptHandler struct {
	receipts IdempotencyReceiptFinder
}

// NewIdempotencyReceiptHandler creates the handler.
func NewIdempotencyReceiptHandler(r IdempotencyReceiptFinder) *IdempotencyReceiptHandler {
	return &IdempotencyReceiptHandler{receipts: r}
}

var idemReceiptRoutes = map[string]bool{
	domain.IdemRouteCommentCreate:  true,
	domain.IdemRouteArtifactCreate: true,
	domain.IdemRouteTaskFinish:     true,
	domain.IdemRouteSessionReport:  true,
}

// Get handles GET /me/idempotency-receipts?route=&key=&task_id=: the caller's
// own write recorded under that key, 404 idempotency_receipt_not_found when
// the key wrote nothing (then the write did not happen and may be sent again
// with the same key).
func (h *IdempotencyReceiptHandler) Get(c echo.Context) error {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if actorID == uuid.Nil {
		return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}
	route, key := c.QueryParam("route"), c.QueryParam("key")
	if !idemReceiptRoutes[route] {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
			"route": "one of comment.create, artifact.create, task.finish, session.report"}))
	}
	if key == "" {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"key": "the Idempotency-Key of the write"}))
	}
	taskID := uuid.Nil
	if v := c.QueryParam("task_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{"task_id": "a full task UUID"}))
		}
		taskID = id
	}
	w, err := h.receipts.FindReceipt(c.Request().Context(), domain.IdempotencyReceiptScope{
		ActorType: actorType, ActorID: actorID, Route: route, TaskID: taskID, Key: key}, "")
	if err != nil {
		return handleError(c, err)
	}
	if w == nil {
		return c.JSON(http.StatusNotFound, &apierror.Error{
			Code: http.StatusNotFound, ErrorCode: "idempotency_receipt_not_found",
			Message: "this key recorded no write on this route and task",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"route": route, "task_id": taskID, "key": key,
		"resource_id": w.ResourceID, "created_at": w.CreatedAt, "response": w.Response,
	})
}
