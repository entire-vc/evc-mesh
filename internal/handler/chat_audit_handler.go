package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatAuditHandler serves the read-only QA audit of Chat with Agent. Access is
// decided in the repository (human owner/admin of the workspace in the path).
type ChatAuditHandler struct {
	repo *postgres.ChatAuditRepo
}

func NewChatAuditHandler(repo *postgres.ChatAuditRepo) *ChatAuditHandler {
	return &ChatAuditHandler{repo: repo}
}

func auditUUIDParam(c echo.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, apierror.BadRequest("invalid " + name)
	}
	return id, nil
}

// Thread is GET /workspaces/:ws_id/chat/audit/threads/:thread_id.
func (h *ChatAuditHandler) Thread(c echo.Context) error {
	ws, err := auditUUIDParam(c, "ws_id")
	if err != nil {
		return handleError(c, err)
	}
	thread, err := auditUUIDParam(c, "thread_id")
	if err != nil {
		return handleError(c, err)
	}
	out, err := h.repo.ThreadAudit(c.Request().Context(), userRuntimeActor(c), ws, thread)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// ThreadEvents is GET /workspaces/:ws_id/chat/audit/threads/:thread_id/events.
func (h *ChatAuditHandler) ThreadEvents(c echo.Context) error {
	ws, err := auditUUIDParam(c, "ws_id")
	if err != nil {
		return handleError(c, err)
	}
	thread, err := auditUUIDParam(c, "thread_id")
	if err != nil {
		return handleError(c, err)
	}
	out, err := h.repo.ThreadEvents(c.Request().Context(), userRuntimeActor(c), ws, thread)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// AgentSessions is GET /workspaces/:ws_id/chat/audit/agents/:agent_id/sessions.
func (h *ChatAuditHandler) AgentSessions(c echo.Context) error {
	ws, err := auditUUIDParam(c, "ws_id")
	if err != nil {
		return handleError(c, err)
	}
	agent, err := auditUUIDParam(c, "agent_id")
	if err != nil {
		return handleError(c, err)
	}
	out, err := h.repo.AgentSessions(c.Request().Context(), userRuntimeActor(c), ws, agent)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// MemoryWrites is GET /workspaces/:ws_id/chat/audit/memory-writes?thread_id=&limit=.
func (h *ChatAuditHandler) MemoryWrites(c echo.Context) error {
	ws, err := auditUUIDParam(c, "ws_id")
	if err != nil {
		return handleError(c, err)
	}
	var thread *uuid.UUID
	if raw := c.QueryParam("thread_id"); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil || id == uuid.Nil {
			return handleError(c, apierror.BadRequest("invalid thread_id"))
		}
		thread = &id
	}
	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n < 1 {
			return handleError(c, apierror.BadRequest("invalid limit"))
		}
		limit = n
	}
	items, err := h.repo.MemoryWrites(c.Request().Context(), userRuntimeActor(c), ws, thread, limit)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items})
}
