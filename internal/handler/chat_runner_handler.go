package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatRunnerHandler serves the runner's orders and session-recovery history
// (runner protocol §8, §9). The caller is the agent key of the runner; a user or
// anything else gets 403 chat_runner_forbidden.
type ChatRunnerHandler struct {
	repo *postgres.ChatRunnerRepo
}

func NewChatRunnerHandler(repo *postgres.ChatRunnerRepo) *ChatRunnerHandler {
	return &ChatRunnerHandler{repo: repo}
}

func chatRunnerCaller(c echo.Context) (uuid.UUID, error) {
	id, err := middleware.GetAgentID(c)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, apierror.ForbiddenWithDetails("chat runner calls need an agent key", "chat_runner_forbidden")
	}
	return id, nil
}

// ChatRunnerTokenHeader carries the runner's own bearer next to the agent key.
const ChatRunnerTokenHeader = "X-Runner-Token"

// chatRunnerIdentity is the verified runner of the call: the id behind the
// runner bearer, or nil when the caller sent none (it then acts as the agent as
// a whole). A runner_id the caller names itself is only a claim: it must equal
// the verified one, and without a bearer it is refused.
func (h *ChatRunnerHandler) chatRunnerIdentity(c echo.Context, caller uuid.UUID) (*uuid.UUID, error) {
	var named *uuid.UUID
	if raw := c.QueryParam("runner_id"); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			return nil, apierror.BadRequest("invalid runner_id")
		}
		named = &id
	}
	token := strings.TrimSpace(c.Request().Header.Get(ChatRunnerTokenHeader))
	if token == "" {
		if named != nil {
			return nil, apierror.ForbiddenWithDetails("runner_id needs the runner credential", "chat_runner_forbidden")
		}
		return nil, nil
	}
	id, err := h.repo.AuthenticateRunner(c.Request().Context(), caller, token)
	if err != nil {
		return nil, err
	}
	if named != nil && *named != id {
		return nil, apierror.ForbiddenWithDetails("runner_id does not match the runner credential", "chat_runner_forbidden")
	}
	return &id, nil
}

// RegisterRunner is POST /runner/chat/runners {name}: the bearer is in this answer only.
func (h *ChatRunnerHandler) RegisterRunner(c echo.Context) error {
	caller, err := chatRunnerCaller(c)
	if err != nil {
		return handleError(c, err)
	}
	var req struct {
		Name string `json:"name"`
	}
	if bindErr := c.Bind(&req); bindErr != nil {
		return handleError(c, apierror.BadRequest("invalid request body"))
	}
	runner, token, err := h.repo.RegisterRunner(c.Request().Context(), caller, req.Name)
	if err != nil {
		return handleError(c, err)
	}
	noStore(c)
	return c.JSON(http.StatusCreated, map[string]any{
		"runner_id": runner.ID, "agent_id": runner.AgentID, "name": runner.Name,
		"created_at": runner.CreatedAt.UTC(), "token": token,
	})
}

// RevokeRunner is DELETE /runner/chat/runners/:runner_id.
func (h *ChatRunnerHandler) RevokeRunner(c echo.Context) error {
	caller, err := chatRunnerCaller(c)
	if err != nil {
		return handleError(c, err)
	}
	id, perr := uuid.Parse(c.Param("runner_id"))
	if perr != nil || id == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid runner_id"))
	}
	if err := h.repo.RevokeRunner(c.Request().Context(), caller, id); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Commands is GET /runner/chat/commands?agents=<uuid>[,<uuid>…]&runner_id=<uuid> with the X-Runner-Token bearer.
func (h *ChatRunnerHandler) Commands(c echo.Context) error {
	caller, err := chatRunnerCaller(c)
	if err != nil {
		return handleError(c, err)
	}
	var agents []uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("agents")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			id, perr := uuid.Parse(strings.TrimSpace(p))
			if perr != nil {
				return handleError(c, apierror.BadRequest("invalid agents"))
			}
			agents = append(agents, id)
		}
	}
	runner, err := h.chatRunnerIdentity(c, caller)
	if err != nil {
		return handleError(c, err)
	}
	cmds, err := h.repo.PendingCommands(c.Request().Context(), caller, agents, runner, 0)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"commands": cmds})
}

type chatCommandAckRequest struct {
	Result string `json:"result"`
	Detail string `json:"detail"`
}

// AckCommand is POST /runner/chat/commands/:command_id/ack.
func (h *ChatRunnerHandler) AckCommand(c echo.Context) error {
	caller, err := chatRunnerCaller(c)
	if err != nil {
		return handleError(c, err)
	}
	id, perr := uuid.Parse(c.Param("command_id"))
	if perr != nil || id == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid command_id"))
	}
	runner, err := h.chatRunnerIdentity(c, caller)
	if err != nil {
		return handleError(c, err)
	}
	var req chatCommandAckRequest
	if bindErr := c.Bind(&req); bindErr != nil {
		return handleError(c, apierror.BadRequest("invalid request body"))
	}
	state, err := h.repo.AckCommand(c.Request().Context(), caller, id, runner, req.Result, req.Detail)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"command_id": id, "state": state})
}

// History is GET /runner/chat/threads/:thread_id/history?limit=<N>&before=<message_id>.
func (h *ChatRunnerHandler) History(c echo.Context) error {
	caller, err := chatRunnerCaller(c)
	if err != nil {
		return handleError(c, err)
	}
	thread, perr := uuid.Parse(c.Param("thread_id"))
	if perr != nil || thread == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid thread_id"))
	}
	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n < 1 {
			return handleError(c, apierror.BadRequest("invalid limit"))
		}
		limit = n
	}
	var before *uuid.UUID
	if raw := c.QueryParam("before"); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			return handleError(c, apierror.BadRequest("invalid before"))
		}
		before = &id
	}
	// The holder of the turn is the runner instance in X-Chat-Runner-Id (the same
	// identity claim and lease are fenced on). Missing or malformed → the same 403
	// as a foreign thread, before anything is read.
	var runner *uuid.UUID
	if id, perr := uuid.Parse(strings.TrimSpace(c.Request().Header.Get(chatRunnerIDHeader))); perr == nil && id != uuid.Nil {
		runner = &id
	}
	out, err := h.repo.History(c.Request().Context(), caller, runner, thread, limit, before)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, out)
}
