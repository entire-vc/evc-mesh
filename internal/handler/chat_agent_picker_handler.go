package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type ChatAgentPickerHandler struct {
	repo *postgres.ChatAgentPickerRepo
}

func NewChatAgentPickerHandler(repo *postgres.ChatAgentPickerRepo) *ChatAgentPickerHandler {
	return &ChatAgentPickerHandler{repo: repo}
}

// List is GET /projects/:proj_id/chat/agents: the project's agents that a
// workspace runner serves, with online/offline/busy presence.
func (h *ChatAgentPickerHandler) List(c echo.Context) error {
	project, err := uuid.Parse(c.Param("proj_id"))
	if err != nil || project == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid proj_id"))
	}
	items, err := h.repo.List(c.Request().Context(), userRuntimeActor(c), project)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items})
}
