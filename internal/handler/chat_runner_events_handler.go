package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatRunnerEventsHandler serves POST /runner/chat/turns/:turn_id/events.
type ChatRunnerEventsHandler struct {
	svc *service.ChatTurnEventsService
}

func NewChatRunnerEventsHandler(svc *service.ChatTurnEventsService) *ChatRunnerEventsHandler {
	return &ChatRunnerEventsHandler{svc: svc}
}

type chatEventsRequest struct {
	ClaimToken string                   `json:"claim_token"`
	Events     []postgres.ChatTurnEvent `json:"events"`
}

// Events applies one batch of runner events (protocol §6).
func (h *ChatRunnerEventsHandler) Events(c echo.Context) error {
	caller, err := authorizeChatRunner(c)
	if err != nil {
		return runnerError(c, err)
	}
	turn, err := uuid.Parse(c.Param("id"))
	if err != nil || turn == uuid.Nil {
		return handleError(c, errChatTurnNotFound())
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, postgres.ChatMaxBatchBytes+1))
	if err != nil {
		return handleError(c, apierror.BadRequest("cannot read request body"))
	}
	if len(raw) > postgres.ChatMaxBatchBytes {
		return handleError(c, apierror.ChatRunner(http.StatusRequestEntityTooLarge, "chat_batch_too_large",
			"the batch is larger than 256 KiB", map[string]any{"max_bytes": postgres.ChatMaxBatchBytes}))
	}
	var req chatEventsRequest
	if err = json.Unmarshal(raw, &req); err != nil {
		return handleError(c, apierror.BadRequest("invalid request body"))
	}
	if len(req.Events) == 0 {
		return handleError(c, apierror.BadRequest("events must not be empty"))
	}
	res, err := h.svc.ApplyEvents(c.Request().Context(), postgres.ChatEventsInput{
		TurnID: turn, RunnerID: caller.runnerID, ClaimToken: req.ClaimToken, Events: req.Events,
		ServesAgent: func(agent uuid.UUID) bool { return agent == caller.agentID },
	})
	var limited *service.ChatRateLimitedError
	if errors.As(err, &limited) {
		c.Response().Header().Set("Retry-After", strconv.Itoa(int(limited.RetryAfter.Seconds())))
		return c.JSON(http.StatusTooManyRequests, apierror.ChatRunner(http.StatusTooManyRequests, "chat_rate_limited",
			"too many event batches for this turn", nil))
	}
	if err != nil {
		return handleError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, res)
}

func errChatTurnNotFound() error {
	return apierror.ChatRunner(http.StatusNotFound, "chat_turn_not_found", "chat turn not found", nil)
}
