package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
)

// DocumentWatchHandler serves /documents/:doc_id/watch — subscribing to a
// document's changes.
//
// The caller is never named in the URL or the body. Who is subscribing is read
// from the authenticated actor, so there is no shape of request that subscribes
// somebody else to anything, and no need for a rule saying you may not.
type DocumentWatchHandler struct {
	watchService service.DocumentWatchService
}

// NewDocumentWatchHandler returns a new DocumentWatchHandler.
func NewDocumentWatchHandler(ws service.DocumentWatchService) *DocumentWatchHandler {
	return &DocumentWatchHandler{watchService: ws}
}

// Get handles GET /documents/:doc_id/watch
func (h *DocumentWatchHandler) Get(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	state, err := h.watchService.State(c.Request().Context(), docID, wsID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, state)
}

// Watch handles PUT /documents/:doc_id/watch
//
// Idempotent: subscribing twice is the same as subscribing once, so a client
// that cannot tell whether its first call landed can simply repeat it.
func (h *DocumentWatchHandler) Watch(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	state, err := h.watchService.Watch(c.Request().Context(), docID, wsID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, state)
}

// Unwatch handles DELETE /documents/:doc_id/watch
//
// Returns the resulting state rather than 204: the answer a caller needs is
// "am I still going to be told about this", and after an unwatch that is a
// muted row rather than an absent one — a distinction 204 cannot carry.
func (h *DocumentWatchHandler) Unwatch(c echo.Context) error {
	docID, wsID, apiErr := documentScope(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	state, err := h.watchService.Unwatch(c.Request().Context(), docID, wsID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, state)
}
