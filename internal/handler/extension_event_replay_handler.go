package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionEventReplayHandler lets an extension catch up on the events sent to
// its webhook subscriptions and have one sent again. Only an extension token
// reaches it, and only for its own connection.
type ExtensionEventReplayHandler struct {
	replayer service.ExtensionEventReplayer
}

// NewExtensionEventReplayHandler creates an ExtensionEventReplayHandler; a nil
// replayer answers 503.
func NewExtensionEventReplayHandler(replayer service.ExtensionEventReplayer) *ExtensionEventReplayHandler {
	return &ExtensionEventReplayHandler{replayer: replayer}
}

// List handles GET /ext/me/events?since=&limit=&undelivered=: the events of the
// connection after since (a next_cursor or an RFC 3339 time), oldest first.
func (h *ExtensionEventReplayHandler) List(c echo.Context) error {
	p := middleware.GetExtensionPrincipal(c)
	if p == nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("only an extension actor can call this endpoint"))
	}
	if h.replayer == nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("the extension event log is not available"))
	}
	after, err := service.ParseExtensionEventSince(c.QueryParam("since"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(err.Error()))
	}
	q := service.ExtensionEventsQuery{After: after}
	if v := c.QueryParam("limit"); v != "" {
		if q.Limit, err = strconv.Atoi(v); err != nil || q.Limit < 1 {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("limit must be a positive integer"))
		}
	}
	if v := c.QueryParam("undelivered"); v != "" {
		if q.Undelivered, err = strconv.ParseBool(v); err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("undelivered must be true or false"))
		}
	}
	page, err := h.replayer.ListExtensionEvents(c.Request().Context(), p, q)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

// Redeliver handles POST /ext/me/events/:delivery_id/redeliver: one more
// attempt, answered with its outcome. A receiver that did not answer 2xx is a
// 200 with success=false, not an error of this call.
func (h *ExtensionEventReplayHandler) Redeliver(c echo.Context) error {
	p := middleware.GetExtensionPrincipal(c)
	if p == nil {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("only an extension actor can call this endpoint"))
	}
	if h.replayer == nil {
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("the extension event log is not available"))
	}
	id, err := uuid.Parse(c.Param("delivery_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid delivery_id"))
	}
	res, err := h.replayer.RedeliverExtensionEvent(c.Request().Context(), p, id)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}
