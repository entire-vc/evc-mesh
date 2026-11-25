package handler

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// usageDefaultTZ is the zone of days and weeks when the viewer names none.
const usageDefaultTZ = "Europe/Moscow"

// GetUsage handles GET /workspaces/:ws_id/analytics/usage (format=csv for a file).
// Query params:
//   - metric   cost | tokens | turns | duration | cards_done (default tokens)
//   - tokens   total | in | out | cache_read | cache_write (metric=tokens)
//   - group_by model | provider | account | agent | project (required; workspace at /me)
//   - stack_by one more dimension, not group_by (optional)
//   - period   1d | 7d | 30d (default 7d), or from/to (RFC3339 or YYYY-MM-DD; a date `to` is inclusive)
//   - step     auto | hour | day | week (default auto)
//   - tz       IANA zone of the buckets (default Europe/Moscow)
//   - project_id[], agent_id[], model[], provider[], account[], billing_mode[] (repeatable; plain names accepted too)
//   - top      1..20 (default 8): groups past it are folded into "Other"
//
// The account cut (group_by, stack_by or account[]) is open only to owner/admin
// of a workspace with the runner integration: 403 otherwise.
func (h *AnalyticsHandler) GetUsage(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	viewer, ok := analyticsViewer(c)
	if !ok {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("user or agent context required"))
	}
	return h.getUsage(c, service.UsageQuery{WorkspaceID: wsID, Viewer: viewer})
}

// GetMyUsage handles the same contract over every workspace of a signed-in user.
// A trusted agent key means its operator; an OAuth connector has no such identity here.
func (h *AnalyticsHandler) GetMyUsage(c echo.Context) error {
	viewer, ok := analyticsViewer(c)
	if !ok {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("user context required"))
	}
	if _, connector := middleware.GetOAuthConnectorUserID(c); connector {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("user context required"))
	}
	return h.getUsage(c, service.UsageQuery{Viewer: viewer, UserScope: true})
}

func (h *AnalyticsHandler) getUsage(c echo.Context, scope service.UsageQuery) error {
	q, apiErr := parseUsageQuery(c.QueryParams(), time.Now())
	if apiErr != nil {
		return c.JSON(http.StatusBadRequest, apiErr)
	}
	q.WorkspaceID = scope.WorkspaceID
	q.UserScope = scope.UserScope
	q.Viewer = scope.Viewer

	format := c.QueryParam("format")
	if format != "" && format != "json" && format != "csv" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("unsupported format; use json or csv"))
	}

	res, err := h.analyticsService.GetUsage(c.Request().Context(), q)
	if err != nil {
		return handleError(c, err)
	}
	if format != "csv" {
		return c.JSON(http.StatusOK, res)
	}
	return writeUsageCSV(c, q, res)
}

// parseUsageQuery reads the query string into a service.UsageQuery (without
// workspace and viewer).
func parseUsageQuery(v url.Values, now time.Time) (service.UsageQuery, *apierror.Error) {
	q := service.UsageQuery{
		Metric:  v.Get("metric"),
		Tokens:  v.Get("tokens"),
		GroupBy: v.Get("group_by"),
		StackBy: v.Get("stack_by"),
	}
	if q.Metric == "" {
		q.Metric = service.UsageMetricTokens
	}
	if q.GroupBy == "" {
		return q, apierror.BadRequest("group_by is required: model, provider, account, agent, project or workspace")
	}

	tzName := v.Get("tz")
	if tzName == "" {
		tzName = usageDefaultTZ
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil || tzName == "Local" {
		return q, apierror.BadRequest("invalid tz; use an IANA zone like Europe/Moscow")
	}
	q.Loc = loc

	fromStr, toStr := v.Get("from"), v.Get("to")
	if fromStr != "" || toStr != "" {
		if fromStr == "" || toStr == "" {
			return q, apierror.BadRequest("from and to go together")
		}
		if q.From, err = parseUsageTime(fromStr, loc, false); err != nil {
			return q, apierror.BadRequest("invalid 'from' date")
		}
		if q.To, err = parseUsageTime(toStr, loc, true); err != nil {
			return q, apierror.BadRequest("invalid 'to' date")
		}
	} else {
		period := v.Get("period")
		if period == "" {
			period = "7d"
		}
		local := now.In(loc)
		today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		switch period {
		case "1d":
			hour := local.Add(-time.Duration(local.Minute())*time.Minute - time.Duration(local.Second())*time.Second - time.Duration(local.Nanosecond()))
			q.From = hour.Add(-23 * time.Hour)
		case "7d":
			q.From = today.AddDate(0, 0, -6)
		case "30d":
			q.From = today.AddDate(0, 0, -29)
		default:
			return q, apierror.BadRequest("invalid period; use 1d, 7d or 30d, or from/to")
		}
		q.To = now
	}
	if !q.To.After(q.From) {
		return q, apierror.BadRequest("'to' must be after 'from'")
	}

	step := v.Get("step")
	switch step {
	case "", "auto", service.UsageStepHour, service.UsageStepDay, service.UsageStepWeek:
	default:
		return q, apierror.BadRequest("invalid step; use auto, hour, day or week")
	}
	q.Step = service.ResolveUsageStep(step, q.From, q.To)

	q.Top = 8
	if t := v.Get("top"); t != "" {
		n, parseErr := strconv.Atoi(t)
		if parseErr != nil || n < 1 || n > 20 {
			return q, apierror.BadRequest("invalid top; use 1..20")
		}
		q.Top = n
	}

	if q.WorkspaceIDs, err = usageUUIDs(v, "workspace_id"); err != nil {
		return q, apierror.BadRequest("invalid workspace_id")
	}
	if q.ProjectIDs, err = usageUUIDs(v, "project_id"); err != nil {
		return q, apierror.BadRequest("invalid project_id")
	}
	if q.AgentIDs, err = usageUUIDs(v, "agent_id"); err != nil {
		return q, apierror.BadRequest("invalid agent_id")
	}
	q.Models = usageStrings(v, "model")
	q.Providers = usageStrings(v, "provider")
	q.Accounts = usageStrings(v, "account")
	q.BillingModes = usageStrings(v, "billing_mode")
	return q, nil
}

// parseUsageTime reads RFC3339 or a date in loc; a date as `to` means the end
// of that day.
func parseUsageTime(s string, loc *time.Location, end bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	d, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, err
	}
	if end {
		d = d.AddDate(0, 0, 1)
	}
	return d, nil
}

// usageStrings collects name[] and name, each value possibly comma-separated.
func usageStrings(v url.Values, name string) []string {
	var out []string
	for _, raw := range append(append([]string{}, v[name+"[]"]...), v[name]...) {
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func usageUUIDs(v url.Values, name string) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, s := range usageStrings(v, name) {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// writeUsageCSV writes the rows and the total of the same query as a file. Cost
// columns appear only when the answer carries them (canSeeCost).
func writeUsageCSV(c echo.Context, q service.UsageQuery, res *service.UsageResult) error {
	filename := fmt.Sprintf("agent-usage-%s-%s-%s.csv", q.Metric, q.GroupBy, time.Now().In(q.Loc).Format("2006-01-02"))
	c.Response().Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Response().WriteHeader(http.StatusOK)

	withCost := res.Pricing != nil
	header := []string{"group", "key", "stack", "stack_key", "value", "share", "turns", "turns_unknown",
		"tokens_in", "tokens_out", "cache_read", "cache_write", "duration_ms"}
	if withCost {
		header = append(header, "cost_usd_api_price", "cost_basis")
	}
	w := csv.NewWriter(c.Response())
	_ = w.Write(header)
	for _, r := range res.Rows {
		stack, stackKey := "", ""
		if r.StackKey != nil {
			stackKey = *r.StackKey
		}
		if r.StackLabel != nil {
			stack = *r.StackLabel
		}
		rec := []string{csvText(r.Label), csvText(r.Key), csvText(stack), csvText(stackKey), fmtFloat(r.Value), fmtFloat(r.Share),
			strconv.FormatInt(r.Turns, 10), strconv.FormatInt(r.TurnsUnknown, 10),
			fmtInt(r.TokensIn), fmtInt(r.TokensOut), fmtInt(r.CacheRead), fmtInt(r.CacheWrite), fmtInt(r.DurationMs)}
		if withCost {
			rec = append(rec, fmtFloat(r.Cost), fmtStr(r.CostBasis))
		}
		_ = w.Write(rec)
	}
	t := res.Totals
	rec := []string{"Total", "__total__", "", "", fmtFloat(t.Value), "", strconv.FormatInt(t.Turns, 10), strconv.FormatInt(t.TurnsUnknown, 10),
		fmtInt(t.TokensIn), fmtInt(t.TokensOut), fmtInt(t.CacheRead), fmtInt(t.CacheWrite), fmtInt(t.DurationMs)}
	if withCost {
		rec = append(rec, fmtFloat(t.Cost), fmtStr(t.CostBasis))
	}
	_ = w.Write(rec)
	w.Flush()
	return w.Error()
}

// An unknown figure is an empty cell, never 0.
func fmtFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func fmtInt(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

func fmtStr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
