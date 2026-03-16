package handler

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ActivityHandler handles HTTP requests for activity log.
type ActivityHandler struct {
	activityService service.ActivityLogService
}

// NewActivityHandler creates a new ActivityHandler with the given service.
func NewActivityHandler(as service.ActivityLogService) *ActivityHandler {
	return &ActivityHandler{activityService: as}
}

// listActivityQuery represents query parameters for listing activity.
type listActivityQuery struct {
	EntityType string `query:"entity_type"`
	EntityID   string `query:"entity_id"`
	ActorID    string `query:"actor_id"`
	ActorType  string `query:"actor_type"`
	Action     string `query:"action"`
}

// ListByWorkspace handles GET /workspaces/:ws_id/activity
func (h *ActivityHandler) ListByWorkspace(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var q listActivityQuery
	err = c.Bind(&q)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	var pg pagination.Params
	err = c.Bind(&pg)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	filter := repository.ActivityLogFilter{}

	if q.EntityType != "" {
		filter.EntityType = &q.EntityType
	}
	if q.EntityID != "" {
		var entityID uuid.UUID
		entityID, err = uuid.Parse(q.EntityID)
		if err == nil {
			filter.EntityID = &entityID
		}
	}
	if q.ActorID != "" {
		var actorID uuid.UUID
		actorID, err = uuid.Parse(q.ActorID)
		if err == nil {
			filter.ActorID = &actorID
		}
	}
	if q.ActorType != "" {
		at := domain.ActorType(q.ActorType)
		filter.ActorType = &at
	}
	if q.Action != "" {
		filter.Action = &q.Action
	}

	page, err := h.activityService.List(c.Request().Context(), wsID, filter, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// ListByTask handles GET /tasks/:task_id/activity
func (h *ActivityHandler) ListByTask(c echo.Context) error {
	taskIDStr := c.Param("task_id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var pg pagination.Params
	err = c.Bind(&pg)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid pagination parameters"))
	}
	pg.Normalize()

	page, err := h.activityService.ListByTask(c.Request().Context(), taskID, pg)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusOK, page)
}

// exportActivityQuery represents query parameters for the export endpoint.
type exportActivityQuery struct {
	Format     string `query:"format"`
	From       string `query:"from"`
	To         string `query:"to"`
	EntityType string `query:"entity_type"`
	Action     string `query:"action"`
	Limit      string `query:"limit"`
}

const (
	exportDefaultLimit = 1000
	exportMaxLimit     = 10000
)

// Export handles GET /workspaces/:ws_id/activity/export
// Returns activity log data as CSV or JSON depending on the `format` query parameter
// (or Accept header). Defaults to JSON.
func (h *ActivityHandler) Export(c echo.Context) error {
	wsIDStr := c.Param("ws_id")
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}

	var q exportActivityQuery
	err = c.Bind(&q)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid query parameters"))
	}

	// Determine format: query param takes priority, then Accept header.
	format := q.Format
	if format == "" {
		accept := c.Request().Header.Get("Accept")
		if accept == "text/csv" {
			format = "csv"
		} else {
			format = "json"
		}
	}
	if format != "csv" && format != "json" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("format must be 'csv' or 'json'"))
	}

	// Parse limit.
	limit := exportDefaultLimit
	if q.Limit != "" {
		var parsed int
		parsed, err = strconv.Atoi(q.Limit)
		if err != nil || parsed <= 0 {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("limit must be a positive integer"))
		}
		if parsed > exportMaxLimit {
			parsed = exportMaxLimit
		}
		limit = parsed
	}

	// Build filter.
	filter := repository.ActivityLogFilter{}
	if q.EntityType != "" {
		filter.EntityType = &q.EntityType
	}
	if q.Action != "" {
		filter.Action = &q.Action
	}
	if q.From != "" {
		var t time.Time
		t, err = time.Parse(time.RFC3339, q.From)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("from must be an ISO 8601 datetime (e.g. 2024-01-01T00:00:00Z)"))
		}
		filter.From = &t
	}
	if q.To != "" {
		var t time.Time
		t, err = time.Parse(time.RFC3339, q.To)
		if err != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("to must be an ISO 8601 datetime (e.g. 2024-12-31T23:59:59Z)"))
		}
		filter.To = &t
	}

	entries, err := h.activityService.Export(c.Request().Context(), wsID, filter, limit)
	if err != nil {
		return handleError(c, err)
	}

	dateTag := time.Now().UTC().Format("2006-01-02")

	switch format {
	case "csv":
		return h.exportCSV(c, entries, dateTag)
	default:
		return h.exportJSON(c, entries, dateTag)
	}
}

// exportCSV writes activity log entries as a CSV attachment to the response.
func (h *ActivityHandler) exportCSV(c echo.Context, entries []domain.ActivityLog, dateTag string) error {
	filename := fmt.Sprintf("audit-log-%s.csv", dateTag)
	c.Response().Header().Set("Content-Type", "text/csv")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Response().WriteHeader(http.StatusOK)

	w := csv.NewWriter(c.Response())

	// Header row.
	if err := w.Write([]string{
		"id", "entity_type", "entity_id", "action",
		"actor_id", "actor_type", "changes", "created_at",
	}); err != nil {
		return err
	}

	// Data rows — stream directly, no buffering.
	for _, e := range entries {
		changes := string(e.Changes)
		if changes == "" {
			changes = "{}"
		}
		record := []string{
			e.ID.String(),
			e.EntityType,
			e.EntityID.String(),
			e.Action,
			e.ActorID.String(),
			string(e.ActorType),
			changes,
			e.CreatedAt.UTC().Format(time.RFC3339),
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

// exportJSON writes activity log entries as a JSON attachment to the response.
func (h *ActivityHandler) exportJSON(c echo.Context, entries []domain.ActivityLog, dateTag string) error {
	filename := fmt.Sprintf("audit-log-%s.json", dateTag)
	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Response().WriteHeader(http.StatusOK)

	enc := json.NewEncoder(c.Response())
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}
