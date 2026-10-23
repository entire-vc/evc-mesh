package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type ChatThreadDeleteHandler struct {
	repo *postgres.ChatThreadDeleteRepo
}

func NewChatThreadDeleteHandler(repo *postgres.ChatThreadDeleteRepo) *ChatThreadDeleteHandler {
	return &ChatThreadDeleteHandler{repo: repo}
}

// Delete is DELETE /projects/:proj_id/chat/threads/:thread_id. Soft delete now;
// another user's, a deleted and a missing thread all answer 404 thread_not_found.
func (h *ChatThreadDeleteHandler) Delete(c echo.Context) error {
	project, err := uuid.Parse(c.Param("proj_id"))
	if err != nil || project == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid proj_id"))
	}
	thread, err := uuid.Parse(c.Param("thread_id"))
	if err != nil || thread == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid thread_id"))
	}
	if err := h.repo.SoftDeleteThread(c.Request().Context(), userRuntimeActor(c), project, thread); err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
