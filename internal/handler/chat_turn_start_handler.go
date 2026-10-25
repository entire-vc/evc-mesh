package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// chatEpochHeader carries the execution_epoch the runner got from its last
// start / lease response (protocol §5.4).
const chatEpochHeader = "X-Chat-Epoch"

// ChatTurnStore is the database half of start and session-token issue.
type ChatTurnStore interface {
	StartTurn(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID, turnID uuid.UUID,
		claimToken string, leaseSeconds int) (*postgres.ChatStarted, error)
	SessionContext(ctx context.Context, workspaceID uuid.UUID, agentIDs []uuid.UUID, runnerID, turnID uuid.UUID,
		claimToken string, epoch int64) (*postgres.ChatSessionContext, error)
	SessionGeneration(ctx context.Context, sessionID, tokenID uuid.UUID) (int, error)
}

// ChatTurnStartHandler serves POST /runner/chat/turns/:id/start (§5.1) and
// POST /runner/chat/turns/:id/session-token (§5.4).
type ChatTurnStartHandler struct {
	repo     ChatTurnStore
	sessions service.ChatSessionService
}

func NewChatTurnStartHandler(repo ChatTurnStore, sessions service.ChatSessionService) *ChatTurnStartHandler {
	return &ChatTurnStartHandler{repo: repo, sessions: sessions}
}

func chatTurnClaimToken(c echo.Context) (uuid.UUID, string, error) {
	turnID, perr := uuid.Parse(c.Param("id"))
	if perr != nil || turnID == uuid.Nil {
		return uuid.Nil, "", errChatTurnNotFound()
	}
	var req chatLeaseRequest
	if berr := c.Bind(&req); berr != nil || strings.TrimSpace(req.ClaimToken) == "" {
		return uuid.Nil, "", apierror.BadRequest("claim_token is required")
	}
	return turnID, req.ClaimToken, nil
}

// Start moves a claimed turn to streaming; the runner may touch the model only after the 200.
func (h *ChatTurnStartHandler) Start(c echo.Context) error {
	caller, err := authorizeChatRunner(c)
	if err != nil {
		return runnerError(c, err)
	}
	turnID, token, err := chatTurnClaimToken(c)
	if err != nil {
		return runnerError(c, err)
	}
	out, err := h.repo.StartTurn(c.Request().Context(), caller.workspaceID, []uuid.UUID{caller.agentID},
		caller.runnerID, turnID, token, postgres.ChatLeaseSeconds)
	if err != nil {
		return runnerError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusOK, map[string]any{
		"state": out.State, "lease_until": out.LeaseUntil, "cancel_requested": out.CancelRequested,
		"last_seq": out.LastSeq, "execution_epoch": out.ExecutionEpoch,
	})
}

// SessionToken issues (or renews) the cst_ bearer of the turn's chat session. The
// binding is read from the turn's own thread, never from the request.
func (h *ChatTurnStartHandler) SessionToken(c echo.Context) error {
	caller, err := authorizeChatRunner(c)
	if err != nil {
		return runnerError(c, err)
	}
	turnID, token, err := chatTurnClaimToken(c)
	if err != nil {
		return runnerError(c, err)
	}
	epoch, perr := strconv.ParseInt(strings.TrimSpace(c.Request().Header.Get(chatEpochHeader)), 10, 64)
	if perr != nil || epoch < 0 {
		return runnerError(c, apierror.BadRequest("X-Chat-Epoch must be the execution_epoch from the last start or lease response"))
	}
	sc, err := h.repo.SessionContext(c.Request().Context(), caller.workspaceID, []uuid.UUID{caller.agentID},
		caller.runnerID, turnID, token, epoch)
	if err != nil {
		return runnerError(c, err)
	}
	issued, err := h.sessions.Issue(c.Request().Context(), service.ChatSessionBinding{
		UserID: sc.UserID, AgentID: sc.AgentID, WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID,
		ChatThreadID: sc.ThreadID, ChatSessionID: sc.TurnID, RunnerID: caller.runnerID.String(), ExecutionEpoch: sc.ExecutionEpoch,
	})
	if err != nil {
		return runnerError(c, err)
	}
	generation, err := h.repo.SessionGeneration(c.Request().Context(), sc.TurnID, issued.TokenID)
	if err != nil {
		return runnerError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusOK, map[string]any{
		"token": issued.Token, "expires_at": issued.ExpiresAt.UTC(), "execution_epoch": sc.ExecutionEpoch,
		"generation": generation, "kind": "chat_session",
	})
}
