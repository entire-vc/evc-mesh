package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AnalyticsHandler handles HTTP requests for analytics data.
type AnalyticsHandler struct {
	analyticsService service.AnalyticsService
}

// NewAnalyticsHandler creates a new AnalyticsHandler.
func NewAnalyticsHandler(svc service.AnalyticsService) *AnalyticsHandler {
	return &AnalyticsHandler{analyticsService: svc}
}

// GetMetrics handles GET /workspaces/:ws_id/analytics
// Query params:
//   - from   (RFC3339 or YYYY-MM-DD, default: 30 days ago)
//   - to     (RFC3339 or YYYY-MM-DD, default: now)
//   - project_id (optional UUID)
func (h *AnalyticsHandler) GetMetrics(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	now := time.Now()
	from := now.AddDate(0, 0, -30)
	to := now

	if fromStr := c.QueryParam("from"); fromStr != "" {
		parsed, err := parseAnalyticsDate(fromStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid 'from' date"))
		}
		from = parsed
	}
	if toStr := c.QueryParam("to"); toStr != "" {
		parsed, err := parseAnalyticsDate(toStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid 'to' date"))
		}
		to = parsed
	}

	filter := service.AnalyticsFilter{
		WorkspaceID: wsID,
		From:        from,
		To:          to,
	}

	if projIDStr := c.QueryParam("project_id"); projIDStr != "" {
		projID, err := uuid.Parse(projIDStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		filter.ProjectID = &projID
	}

	metrics, err := h.analyticsService.GetMetrics(c.Request().Context(), filter)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, metrics)
}

// parseAnalyticsDate parses either RFC3339 or YYYY-MM-DD date strings.
func parseAnalyticsDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}
