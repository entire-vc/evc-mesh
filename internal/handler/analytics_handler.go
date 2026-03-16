package handler

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
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

	var parsed time.Time
	if fromStr := c.QueryParam("from"); fromStr != "" {
		parsed, err = parseAnalyticsDate(fromStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid 'from' date"))
		}
		from = parsed
	}
	if toStr := c.QueryParam("to"); toStr != "" {
		parsed, err = parseAnalyticsDate(toStr)
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
		var projID uuid.UUID
		projID, err = uuid.Parse(projIDStr)
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

// ExportMetrics handles GET /workspaces/:ws_id/analytics/export
// Query params:
//   - format  (csv — required)
//   - from    (RFC3339 or YYYY-MM-DD, default: 30 days ago)
//   - to      (RFC3339 or YYYY-MM-DD, default: now)
//   - project_id (optional UUID)
func (h *AnalyticsHandler) ExportMetrics(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	format := c.QueryParam("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("unsupported format; use format=csv"))
	}

	now := time.Now()
	from := now.AddDate(0, 0, -30)
	to := now

	var parsed time.Time
	if fromStr := c.QueryParam("from"); fromStr != "" {
		parsed, err = parseAnalyticsDate(fromStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid 'from' date"))
		}
		from = parsed
	}
	if toStr := c.QueryParam("to"); toStr != "" {
		parsed, err = parseAnalyticsDate(toStr)
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
		var projID uuid.UUID
		projID, err = uuid.Parse(projIDStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		filter.ProjectID = &projID
	}

	metrics, err := h.analyticsService.GetMetrics(c.Request().Context(), filter)
	if err != nil {
		return handleError(c, err)
	}

	filename := fmt.Sprintf("analytics-%s.csv", now.Format("2006-01-02"))
	c.Response().Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Response().WriteHeader(http.StatusOK)

	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"Metric", "Category", "Value"})

	// Tasks by status category.
	categories := []string{"backlog", "todo", "in_progress", "review", "done", "cancelled"}
	for _, cat := range categories {
		v := metrics.TaskMetrics.ByStatusCategory[cat]
		_ = w.Write([]string{"Tasks by Status", cat, fmt.Sprintf("%d", v)})
	}

	// Tasks by priority.
	priorities := []string{"urgent", "high", "medium", "low", "none"}
	for _, pri := range priorities {
		v := metrics.TaskMetrics.ByPriority[pri]
		_ = w.Write([]string{"Tasks by Priority", pri, fmt.Sprintf("%d", v)})
	}

	// Summary task metrics.
	_ = w.Write([]string{"Task Summary", "total", fmt.Sprintf("%d", metrics.TaskMetrics.Total)})
	_ = w.Write([]string{"Task Summary", "created_this_period", fmt.Sprintf("%d", metrics.TaskMetrics.CreatedThisPeriod)})
	_ = w.Write([]string{"Task Summary", "completed_this_period", fmt.Sprintf("%d", metrics.TaskMetrics.CompletedThisPeriod)})

	// Agent activity.
	for _, row := range metrics.AgentMetrics.TasksByAgent {
		_ = w.Write([]string{"Agent Activity", row.AgentName, fmt.Sprintf("%d", row.Completed)})
	}

	// Events by type.
	eventTypes := make([]string, 0, len(metrics.EventMetrics.ByType))
	for et := range metrics.EventMetrics.ByType {
		eventTypes = append(eventTypes, et)
	}
	sort.Strings(eventTypes)
	for _, et := range eventTypes {
		_ = w.Write([]string{"Events by Type", et, fmt.Sprintf("%d", metrics.EventMetrics.ByType[et])})
	}

	// Daily timeline.
	for _, day := range metrics.Timeline {
		_ = w.Write([]string{"Daily Timeline (Created)", day.Date, fmt.Sprintf("%d", day.Created)})
		_ = w.Write([]string{"Daily Timeline (Completed)", day.Date, fmt.Sprintf("%d", day.Completed)})
	}

	w.Flush()
	return nil
}

// parseAnalyticsDate parses either RFC3339 or YYYY-MM-DD date strings.
func parseAnalyticsDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}
