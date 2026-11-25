package handler

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MemoryAnalyticsHandler serves GET /workspaces/:ws_id/analytics/memory, its
// drill-downs and the CSV export of the same selection.
type MemoryAnalyticsHandler struct {
	svc service.MemoryAnalyticsService
}

// NewMemoryAnalyticsHandler creates a MemoryAnalyticsHandler.
func NewMemoryAnalyticsHandler(svc service.MemoryAnalyticsService) *MemoryAnalyticsHandler {
	return &MemoryAnalyticsHandler{svc: svc}
}

// multiParam reads a repeatable query parameter: agent_id=a&agent_id=b,
// agent_id[]=a or agent_id=a,b.
func multiParam(c echo.Context, name string) []string {
	q := c.QueryParams()
	var out []string
	for _, k := range []string{name, name + "[]"} {
		for _, v := range q[k] {
			for _, part := range strings.Split(v, ",") {
				if part = strings.TrimSpace(part); part != "" {
					out = append(out, part)
				}
			}
		}
	}
	return out
}

var memoryCallerKinds = map[string]bool{"agent_tool": true, "runner": true, "script": true, "ui": true, "unknown": true}
var memorySources = map[string]bool{"memory": true, "project_knowledge": true, "project_update": true, "doc": true, "unknown": true}

// memoryFilter parses the selection shared by the widget, drill-downs and export.
// Period: days=1|7|30|90 (the last N UTC days, today included) or from/to.
func memoryFilter(c echo.Context) (service.MemoryAnalyticsFilter, *apierror.Error) {
	var f service.MemoryAnalyticsFilter
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return f, apierror.BadRequest("invalid workspace_id")
	}
	f.WorkspaceID = wsID
	now := time.Now().UTC()
	f.Now = now
	f.To = now
	today := now.Truncate(24 * time.Hour)
	f.From = today.AddDate(0, 0, -6)

	if d := c.QueryParam("days"); d != "" {
		switch d {
		case "1", "7", "30", "90":
			n, _ := strconv.Atoi(d)
			f.From = today.AddDate(0, 0, -(n - 1))
		default:
			return f, apierror.BadRequest("invalid days; use 1, 7, 30 or 90")
		}
	}
	if s := c.QueryParam("from"); s != "" {
		t, perr := parseAnalyticsDate(s)
		if perr != nil {
			return f, apierror.BadRequest("invalid 'from' date")
		}
		f.From = t
	}
	if s := c.QueryParam("to"); s != "" {
		t, perr := parseAnalyticsDate(s)
		if perr != nil {
			return f, apierror.BadRequest("invalid 'to' date")
		}
		f.To = t
	}
	// Validate the effective window that the service will query. Otherwise a
	// wholly future range passes here, then becomes inverted when To is clamped.
	if f.To.After(now) {
		f.To = now
	}
	if !f.From.Before(f.To) {
		return f, apierror.BadRequest("'from' must be before 'to'")
	}
	if f.To.Sub(f.From) > 366*24*time.Hour {
		return f, apierror.BadRequest("period longer than 366 days")
	}
	prevFrom, prevTo := c.QueryParam("prev_from"), c.QueryParam("prev_to")
	if prevFrom != "" || prevTo != "" {
		from, fromErr := parseAnalyticsDate(prevFrom)
		to, toErr := parseAnalyticsDate(prevTo)
		if fromErr != nil || toErr != nil || !from.Before(to) || to.After(now) || to.Sub(from) > 366*24*time.Hour {
			return f, apierror.BadRequest("invalid previous period; provide prev_from and prev_to in order, at most 366 days and not in the future")
		}
		f.PreviousFrom, f.PreviousTo = &from, &to
	}

	viewer, ok := analyticsViewer(c)
	if !ok {
		return f, apierror.Forbidden("user or agent context required")
	}
	f.Viewer = viewer

	if s := c.QueryParam("project_id"); s != "" {
		p, perr := uuid.Parse(s)
		if perr != nil {
			return f, apierror.BadRequest("invalid project_id")
		}
		f.ProjectID = &p
	}
	for _, s := range multiParam(c, "agent_id") {
		id, perr := uuid.Parse(s)
		if perr != nil {
			return f, apierror.BadRequest("invalid agent_id")
		}
		f.AgentIDs = append(f.AgentIDs, id)
	}
	for _, op := range multiParam(c, "op") {
		if !service.ValidateMemoryOp(op) {
			return f, apierror.BadRequest("invalid op " + strconv.Quote(op))
		}
		f.Ops = append(f.Ops, op)
	}
	for _, s := range multiParam(c, "source") {
		if !memorySources[s] {
			return f, apierror.BadRequest("invalid source; use memory, project_knowledge, project_update, doc or unknown")
		}
		f.Sources = append(f.Sources, s)
	}
	for _, s := range multiParam(c, "caller_kind") {
		if !memoryCallerKinds[s] {
			return f, apierror.BadRequest("invalid caller_kind; use agent_tool, runner, script, ui or unknown")
		}
		f.CallerKinds = append(f.CallerKinds, s)
	}
	f.Kinds = multiParam(c, "kind")
	for _, scope := range multiParam(c, "scope") {
		if scope != "agent" && scope != "project" && scope != "workspace" {
			return f, apierror.BadRequest("invalid scope; use agent, project or workspace")
		}
		f.Scopes = append(f.Scopes, scope)
	}
	if value := c.QueryParam("dead_days"); value != "" {
		if value != "30" && value != "90" {
			return f, apierror.BadRequest("invalid dead_days; use 30 or 90")
		}
		f.DeadDays, _ = strconv.Atoi(value)
	}
	f.GroupBy = c.QueryParam("group_by")
	f.GroupBy2 = c.QueryParam("group_by2")
	if f.GroupBy != "" && !service.ValidateMemoryGroupBy(f.GroupBy) ||
		f.GroupBy2 != "" && (!service.ValidateMemoryGroupBy(f.GroupBy2) || f.GroupBy == "" || f.GroupBy2 == f.GroupBy) {
		return f, apierror.BadRequest("invalid group_by; use agent, project, op, source, caller_kind, kind, scope or day")
	}
	if v := c.QueryParam("view"); v != "" && v != "timeseries" && v != "top" && v != "table" {
		return f, apierror.BadRequest("invalid view; use timeseries, top or table")
	}
	if s := c.QueryParam("top_n"); s != "" {
		n, perr := strconv.Atoi(s)
		if perr != nil || n < 1 || n > 200 {
			return f, apierror.BadRequest("invalid top_n; use 1..200")
		}
		f.TopN = n
	}
	return f, nil
}

func memoryListParams(c echo.Context) (service.MemoryListParams, *apierror.Error) {
	var p service.MemoryListParams
	for name, dst := range map[string]*int{"page": &p.Page, "page_size": &p.PageSize} {
		if s := c.QueryParam(name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || (name == "page_size" && n > 100) {
				return p, apierror.BadRequest("invalid " + name)
			}
			*dst = n
		}
	}
	p.Sort = c.QueryParam("sort")
	p.Search = strings.TrimSpace(c.QueryParam("q"))
	return p, nil
}

// Get handles GET /workspaces/:ws_id/analytics/memory.
func (h *MemoryAnalyticsHandler) Get(c echo.Context) error {
	f, perr := memoryFilter(c)
	if perr != nil {
		return c.JSON(perr.Code, perr)
	}
	out, err := h.svc.Get(c.Request().Context(), f)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

type memoryListFunc[T any] func(context.Context, service.MemoryAnalyticsFilter, service.MemoryListParams) (*service.MemoryList[T], error)

func handleMemoryError(c echo.Context, err error) error {
	var timeout *service.MemoryDetailTimeoutError
	if errors.As(err, &timeout) {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{
			"code": http.StatusServiceUnavailable, "error_code": apierror.CodeServiceUnavailable,
			"message": "Memory detail query timed out", "reason": service.MemoryReasonDetailTimeout,
		})
	}
	return handleError(c, err)
}

func serveMemoryList[T any](c echo.Context, fn memoryListFunc[T]) error {
	f, perr := memoryFilter(c)
	if perr != nil {
		return c.JSON(perr.Code, perr)
	}
	p, perr := memoryListParams(c)
	if perr != nil {
		return c.JSON(perr.Code, perr)
	}
	out, err := fn(c.Request().Context(), f, p)
	if err != nil {
		return handleMemoryError(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// EmptyQueries handles GET …/analytics/memory/empty-queries.
func (h *MemoryAnalyticsHandler) EmptyQueries(c echo.Context) error {
	return serveMemoryList(c, h.svc.EmptyQueries)
}

// TopRecords handles GET …/analytics/memory/top-records.
func (h *MemoryAnalyticsHandler) TopRecords(c echo.Context) error {
	return serveMemoryList(c, h.svc.TopRecords)
}

// DeadRecords handles GET …/analytics/memory/dead-records.
func (h *MemoryAnalyticsHandler) DeadRecords(c echo.Context) error {
	return serveMemoryList(c, h.svc.DeadRecords)
}

// Duplicates handles GET …/analytics/memory/duplicates.
func (h *MemoryAnalyticsHandler) Duplicates(c echo.Context) error {
	return serveMemoryList(c, h.svc.Duplicates)
}

// UnknownSources handles GET …/analytics/memory/unknown-sources.
func (h *MemoryAnalyticsHandler) UnknownSources(c echo.Context) error {
	return serveMemoryList(c, h.svc.UnknownSources)
}

// collectAll pages through a drill-down; reason is the list's no-data reason.
func collectAll[T any](ctx context.Context, fn memoryListFunc[T], f service.MemoryAnalyticsFilter, p service.MemoryListParams) (all []T, reason string, coverage *service.MemoryDetailCoverage, err error) {
	p.PageSize = 100
	for p.Page = 1; ; p.Page++ {
		l, listErr := fn(ctx, f, p)
		if listErr != nil {
			return nil, "", nil, listErr
		}
		all = append(all, l.Items...)
		if l.Reason != "" || len(l.Items) < p.PageSize || int64(len(all)) >= l.Total {
			return all, l.Reason, l.Coverage, nil
		}
	}
}

// fmtInt, fmtFloat and fmtStr of the CSV cells live in analytics_usage_handler.go.

func fmtUUID(v *uuid.UUID) string {
	if v == nil {
		return ""
	}
	return v.String()
}

// Export handles GET …/analytics/memory/export?format=csv[&list=<drill-down>]:
// the same selection as the JSON answer. Without list it is the summary (kpis,
// coverage, series, rows) as section,key,label,key2,label2,metric,value,reason;
// with list it is that drill-down, all pages. A null is an empty value with its
// reason; query text and keys follow the same redaction as the JSON.
func (h *MemoryAnalyticsHandler) Export(c echo.Context) error {
	if format := c.QueryParam("format"); format != "" && format != "csv" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("unsupported format; use format=csv"))
	}
	f, perr := memoryFilter(c)
	if perr != nil {
		return c.JSON(perr.Code, perr)
	}
	p, perr := memoryListParams(c)
	if perr != nil {
		return c.JSON(perr.Code, perr)
	}
	ctx := c.Request().Context()
	list := c.QueryParam("list")

	var records [][]string
	var err error
	switch list {
	case "", "summary":
		list = "summary"
		records, err = h.summaryCSV(ctx, f)
	case "empty-queries":
		var items []service.MemoryEmptyQuery
		var reason string
		var coverage *service.MemoryDetailCoverage
		items, reason, coverage, err = collectAll(ctx, h.svc.EmptyQueries, f, p)
		records = [][]string{{"query_hash", "query_text", "redacted", "agent_id", "agent_name", "count", "last_at", "reason"}}
		for _, it := range items {
			records = append(records, []string{it.QueryHash, csvText(fmtStr(it.QueryText)), strconv.FormatBool(it.Redacted),
				fmtUUID(it.AgentID), csvText(fmtStr(it.AgentName)), strconv.FormatInt(it.Count, 10), it.LastAt.UTC().Format(time.RFC3339), it.Reason})
		}
		records = withDetailCoverage(withReason(records, reason), coverage)
	case "top-records", "dead-records", "duplicates":
		fn := map[string]memoryListFunc[service.MemoryRecordRow]{
			"top-records": h.svc.TopRecords, "dead-records": h.svc.DeadRecords, "duplicates": h.svc.Duplicates}[list]
		var items []service.MemoryRecordRow
		var reason string
		var coverage *service.MemoryDetailCoverage
		items, reason, coverage, err = collectAll(ctx, fn, f, p)
		records = [][]string{{"memory_id", "key", "redacted", "scope", "kind", "project_id", "agent_id", "created_at", "served", "useful", "last_served_at", "group_id", "group_size", "reason"}}
		for _, it := range items {
			last := ""
			if it.LastServed != nil {
				last = it.LastServed.UTC().Format(time.RFC3339)
			}
			records = append(records, []string{it.MemoryID.String(), csvText(fmtStr(it.Key)), strconv.FormatBool(it.Redacted), it.Scope, csvText(fmtStr(it.Kind)),
				fmtUUID(it.ProjectID), fmtUUID(it.AgentID), it.CreatedAt.UTC().Format(time.RFC3339), strconv.FormatInt(it.Served, 10),
				strconv.FormatInt(it.Useful, 10), last, fmtStr(it.GroupID), strconv.FormatInt(it.GroupSize, 10), ""})
		}
		records = withDetailCoverage(withReason(records, reason), coverage)
	case "unknown-sources":
		var items []service.MemoryUnknownSource
		var reason string
		var coverage *service.MemoryDetailCoverage
		items, reason, coverage, err = collectAll(ctx, h.svc.UnknownSources, f, p)
		records = [][]string{{"route", "user_agent", "reads", "sample_keys", "reason"}}
		for _, it := range items {
			keys := make([]string, len(it.SampleKeys))
			copy(keys, it.SampleKeys)
			records = append(records, []string{csvText(it.Route), csvText(it.UserAgent), strconv.FormatInt(it.Reads, 10), csvText(strings.Join(keys, " ")), ""})
		}
		records = withDetailCoverage(withReason(records, reason), coverage)
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid list; use summary, empty-queries, top-records, dead-records, duplicates or unknown-sources"))
	}
	if err != nil {
		return handleMemoryError(c, err)
	}

	filename := fmt.Sprintf("memory-%s-%s.csv", list, f.Now.Format("2006-01-02"))
	c.Response().Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Response().WriteHeader(http.StatusOK)
	w := csv.NewWriter(c.Response())
	return w.WriteAll(records)
}

// withReason adds one row naming why an empty list has no data.
func withReason(records [][]string, reason string) [][]string {
	if reason == "" {
		return records
	}
	row := make([]string, len(records[0]))
	row[len(row)-1] = reason
	return append(records, row)
}

func (h *MemoryAnalyticsHandler) summaryCSV(ctx context.Context, f service.MemoryAnalyticsFilter) ([][]string, error) {
	m, err := h.svc.Get(ctx, f)
	if err != nil {
		return nil, err
	}
	rec := [][]string{{"section", "key", "label", "key2", "label2", "metric", "value", "reason"}}
	add := func(section, key, label, key2, label2, metric, value, reasonKey string) {
		reason := ""
		if value == "" {
			reason = m.Reasons[reasonKey]
		}
		rec = append(rec, []string{section, csvText(key), csvText(label), csvText(key2), csvText(label2), metric, value, reason})
	}
	add("period", "", "", "", "", "from", m.Period.From.UTC().Format(time.RFC3339), "")
	add("period", "", "", "", "", "to", m.Period.To.UTC().Format(time.RFC3339), "")
	add("previous_period", "", "", "", "", "from", m.PreviousPeriod.From.UTC().Format(time.RFC3339), "")
	add("previous_period", "", "", "", "", "to", m.PreviousPeriod.To.UTC().Format(time.RFC3339), "")
	for _, k := range []struct {
		section, prefix string
		v               service.MemoryKPIs
	}{{"kpis", "", m.KPIs}, {"previous_kpis", "previous_kpis.", m.Previous}} {
		add(k.section, "", "", "", "", "reads", fmtInt(k.v.Reads), k.prefix+"reads")
		add(k.section, "", "", "", "", "writes", fmtInt(k.v.Writes), k.prefix+"writes")
		add(k.section, "", "", "", "", "with_reason", fmtInt(k.v.WithReason), k.prefix+"with_reason")
		add(k.section, "", "", "", "", "hit_rate", fmtFloat(k.v.HitRate), k.prefix+"hit_rate")
		add(k.section, "", "", "", "", "useful_rate", fmtFloat(k.v.UsefulRate), k.prefix+"useful_rate")
		add(k.section, "", "", "", "", "miss_rate", fmtFloat(k.v.MissRate), k.prefix+"miss_rate")
		add(k.section, "", "", "", "", "dead30", fmtFloat(k.v.Dead30), k.prefix+"dead30")
		add(k.section, "", "", "", "", "tokens_est", fmtInt(k.v.TokensEst), k.prefix+"tokens_est")
		add(k.section, "", "", "", "", "superseded_served_rate", fmtFloat(k.v.SupersededServedRate), k.prefix+"superseded_served_rate")
	}
	journal := ""
	if m.Coverage.JournalSince != nil {
		journal = m.Coverage.JournalSince.UTC().Format(time.RFC3339)
	}
	add("coverage", "", "", "", "", "journal_since", journal, "coverage.journal_since")
	add("coverage", "", "", "", "", "unknown_share", fmtFloat(m.Coverage.UnknownShare), "coverage.unknown_share")
	for _, ck := range m.Coverage.CallerKind {
		add("caller_kind", ck.CallerKind, ck.CallerKind, "", "", "reads", strconv.FormatInt(ck.Reads, 10), "")
	}
	for _, p := range m.Series {
		for _, v := range []struct {
			metric string
			n      *int64
		}{{"reads", p.Reads}, {"writes", p.Writes}, {"with_reason", p.WithReason}, {"hits", p.Hits}, {"useful", p.Useful}} {
			add("series", p.Day, p.Day, "", "", v.metric, fmtInt(v.n), "")
			if v.n == nil {
				rec[len(rec)-1][7] = p.Reasons[v.metric]
			}
		}
		add("series", p.Day, p.Day, "", "", "useful_rate", fmtFloat(p.UsefulRate), "")
		if p.UsefulRate == nil {
			rec[len(rec)-1][7] = p.Reasons["useful_rate"]
		}
		for _, group := range []struct {
			name   string
			counts map[string]int64
		}{{"reads_by_op", p.ReadsByOp}, {"served_rows_by_source", p.ServedRowsBySource}} {
			if group.counts == nil {
				add("series", p.Day, p.Day, "", "", group.name, "", "")
				rec[len(rec)-1][7] = p.Reasons[group.name]
				continue
			}
			keys := make([]string, 0, len(group.counts))
			for key := range group.counts {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				add("series", p.Day, p.Day, "", "", group.name+"."+key, strconv.FormatInt(group.counts[key], 10), "")
			}
		}
	}
	addRow := func(section string, r service.MemoryBreakdownRow) {
		for _, v := range []struct{ metric, value string }{
			{"reads", fmtInt(r.Reads)}, {"hits", fmtInt(r.Hits)}, {"useful", fmtInt(r.Useful)},
			{"writes", fmtInt(r.Writes)}, {"with_reason", fmtInt(r.WithReason)},
			{"hit_rate", fmtFloat(r.HitRate)}, {"useful_rate", fmtFloat(r.UsefulRate)}, {"tokens_est", fmtInt(r.TokensEst)},
			{"served_rows", fmtInt(r.ServedRows)}, {"superseded_served_rate", fmtFloat(r.SupersededServedRate)},
		} {
			add(section, fmtStr(r.Key), r.Label, fmtStr(r.Key2), r.Label2, v.metric, v.value, "")
			if v.value == "" {
				rec[len(rec)-1][7] = r.Reasons[v.metric]
			}
		}
	}
	for _, r := range m.Rows {
		addRow("rows", r)
	}
	addRow("totals", m.Totals)
	add("quality", "", "", "", "", "day", fmtStr(m.Quality.Day), "quality")
	for _, v := range []struct {
		metric string
		n      *int64
	}{
		{"total", m.Quality.Total}, {"active", m.Quality.Active}, {"archived", m.Quality.Archived},
		{"superseded_active", m.Quality.SupersededActive}, {"conflicted", m.Quality.Conflicted},
		{"no_kind", m.Quality.NoKind}, {"statuslog_like", m.Quality.StatuslogLike},
		{"dup_groups", m.Quality.DupGroups}, {"dup_rows", m.Quality.DupRows},
		{"dead30", m.Quality.Dead30}, {"dead90", m.Quality.Dead90},
	} {
		add("quality", "", "", "", "", v.metric, fmtInt(v.n), "quality."+v.metric)
		if v.n == nil && rec[len(rec)-1][7] == "" {
			rec[len(rec)-1][7] = m.Reasons["quality"]
		}
	}
	return rec, nil
}

// Repeat coverage on each CSV row so a downloaded selection retains its bounds.
func withDetailCoverage(records [][]string, coverage *service.MemoryDetailCoverage) [][]string {
	if coverage == nil {
		return records
	}
	since := ""
	if coverage.DetailSince != nil {
		since = coverage.DetailSince.UTC().Format(time.RFC3339)
	}
	for i := range records {
		if i == 0 {
			records[i] = append(records[i], "detail_since", "coverage_reason")
		} else {
			records[i] = append(records[i], since, coverage.Reason)
		}
	}
	if len(records) == 1 && coverage.Reason != "" {
		row := make([]string, len(records[0]))
		row[len(row)-2], row[len(row)-1] = since, coverage.Reason
		records = append(records, row)
	}
	return records
}
