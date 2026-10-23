package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatSessionHandler serves the runner-only control routes for chat-session
// tokens: issue, renew, revoke. They sit outside the DualAuth group and are gated
// by the chat controller credential (middleware.ChatControllerAuth).
type ChatSessionHandler struct {
	svc service.ChatSessionService
}

// NewChatSessionHandler creates a ChatSessionHandler.
func NewChatSessionHandler(svc service.ChatSessionService) *ChatSessionHandler {
	return &ChatSessionHandler{svc: svc}
}

type chatSessionIssueRequest struct {
	UserID         uuid.UUID `json:"user_id"`
	AgentID        uuid.UUID `json:"agent_id"`
	WorkspaceID    uuid.UUID `json:"workspace_id"`
	ProjectID      uuid.UUID `json:"project_id"`
	ChatThreadID   uuid.UUID `json:"chat_thread_id"`
	ChatSessionID  uuid.UUID `json:"chat_session_id"`
	RunnerID       string    `json:"runner_id"`
	ExecutionEpoch int64     `json:"execution_epoch"`
}

type chatSessionRenewRequest struct {
	RunnerID       string `json:"runner_id"`
	ExecutionEpoch int64  `json:"execution_epoch"`
}

type chatSessionTokenResponse struct {
	Token          string    `json:"token"`
	TokenID        uuid.UUID `json:"token_id"`
	ExpiresAt      time.Time `json:"expires_at"`
	ChatSessionID  uuid.UUID `json:"chat_session_id"`
	ExecutionEpoch int64     `json:"execution_epoch"`
}

func chatSessionResponse(out *service.ChatSessionIssued) chatSessionTokenResponse {
	return chatSessionTokenResponse{
		Token: out.Token, TokenID: out.TokenID, ExpiresAt: out.ExpiresAt.UTC(),
		ChatSessionID: out.Binding.ChatSessionID, ExecutionEpoch: out.Binding.ExecutionEpoch,
	}
}

// noStore keeps the bearer out of any intermediary cache.
func noStore(c echo.Context) { c.Response().Header().Set("Cache-Control", "no-store") }

// Issue mints the session's first token (or the first of a newer execution epoch).
func (h *ChatSessionHandler) Issue(c echo.Context) error {
	var req chatSessionIssueRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	out, err := h.svc.Issue(c.Request().Context(), service.ChatSessionBinding{
		UserID: req.UserID, AgentID: req.AgentID, WorkspaceID: req.WorkspaceID, ProjectID: req.ProjectID,
		ChatThreadID: req.ChatThreadID, ChatSessionID: req.ChatSessionID,
		RunnerID: strings.TrimSpace(req.RunnerID), ExecutionEpoch: req.ExecutionEpoch,
	})
	if err != nil {
		return handleError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusCreated, chatSessionResponse(out))
}

// Renew replaces the session's token under the same binding.
func (h *ChatSessionHandler) Renew(c echo.Context) error {
	sessionID, err := uuid.Parse(c.Param("session_id"))
	if err != nil || sessionID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid session_id"))
	}
	var req chatSessionRenewRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	out, err := h.svc.Renew(c.Request().Context(), sessionID, strings.TrimSpace(req.RunnerID), req.ExecutionEpoch)
	if err != nil {
		return handleError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusOK, chatSessionResponse(out))
}

// Revoke is Stop / chat deletion: all of the session's tokens die immediately.
func (h *ChatSessionHandler) Revoke(c echo.Context) error {
	sessionID, err := uuid.Parse(c.Param("session_id"))
	if err != nil || sessionID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid session_id"))
	}
	n, err := h.svc.RevokeSession(c.Request().Context(), sessionID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"revoked": n})
}
