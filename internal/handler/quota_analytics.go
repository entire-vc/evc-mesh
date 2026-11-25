package handler

import (
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	// quotaAtLimit is the used share from which an hour counts as "at the limit".
	// It must stay equal to THRESHOLD in web/src/core/agent-usage/quota-panel.tsx
	// (MR !2013): the panel's "At limit (≥ 90%)" column and hours_at_limit share
	// one limit threshold.
	quotaAtLimit = 0.90
	// quotaMaxRange is how far back the history reaches (its retention).
	quotaMaxRange = 400 * 24 * time.Hour
	// quotaMaxBuckets bounds the chart: an hourly step over a long range is refused.
	quotaMaxBuckets = 2000
	quotaDefaultTZ  = "Europe/Moscow"
)

// quotaWindowLabel is how a reason names a window.
var quotaWindowLabel = map[string]string{
	domain.QuotaWindow5h:     "5h",
	domain.QuotaWindowWeek:   "week",
	domain.QuotaWindowMonth:  "month",
	domain.QuotaWindowBudget: "budget",
}

type quotaQuery struct {
	Window   string    `json:"window"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Step     string    `json:"step"`
	TZ       string    `json:"tz"`
	Accounts []string  `json:"account"`
	loc      *time.Location
}

type quotaSeries struct {
	Key      string     `json:"key"`
	Label    string     `json:"label"`
	Provider string     `json:"provider"`
	Values   []*float64 `json:"values"`
}

type quotaOtherWindow struct {
	Window    string   `json:"window"`
	WindowRaw string   `json:"window_raw"`
	Now       *float64 `json:"now"`
	Status    string   `json:"status"`
}

type quotaReason struct {
	// Code is no_window (the account has no observation of the asked window) or
	// no_data (the newest observation carries no measurement).
	Code         string             `json:"code"`
	Text         string             `json:"text"`
	OtherWindows []quotaOtherWindow `json:"other_windows,omitempty"`
}

type quotaRow struct {
	Account      string       `json:"account"`
	Provider     string       `json:"provider"`
	WindowRaw    *string      `json:"window_raw"`
	Now          *float64     `json:"now"`
	ResetAt      *time.Time   `json:"reset_at"`
	Peak         *float64     `json:"peak"`
	HoursAtLimit *int         `json:"hours_at_limit"`
	Status       *string      `json:"status"`
	ObservedAt   *time.Time   `json:"observed_at"`
	Stale        bool         `json:"stale"`
	Reason       *quotaReason `json:"reason"`
	// OtherWindows lists every other window of the account, also on a row
	// that has the asked window.
	OtherWindows []quotaOtherWindow `json:"other_windows"`
}

type quotaAnalytics struct {
	Query        quotaQuery      `json:"query"`
	Buckets      []time.Time     `json:"buckets"`
	Series       []quotaSeries   `json:"series"`
	Rows         []quotaRow      `json:"rows"`
	Capabilities map[string]bool `json:"capabilities"`
}

// QuotaAnalytics handles GET /workspaces/:ws_id/analytics/quota (+ format=csv):
// the Runner's provider quota history by catalog account, for the workspace owner/admin
// (or an agent key with its admin operator's runtime_manager grant) only.
func (h *RuntimeHandler) QuotaAnalytics(c echo.Context) error {
	ws, err := runtimeWorkspace(c)
	if err != nil {
		return handleError(c, err)
	}
	allowed, err := h.repo.CanSeeQuota(c.Request().Context(), ws, runtimeActor(c))
	if err != nil {
		return handleError(c, err)
	}
	if !allowed {
		return c.JSON(http.StatusForbidden, struct {
			Code         int             `json:"code"`
			ErrorCode    string          `json:"error_code"`
			Message      string          `json:"message"`
			Capabilities map[string]bool `json:"capabilities"`
		}{http.StatusForbidden, apierror.CodeForbidden, "quota analytics require a workspace owner or admin", map[string]bool{"quota": false}})
	}
	now := time.Now()
	q, err := parseQuotaQuery(c, now)
	if err != nil {
		return handleError(c, err)
	}
	data, err := h.repo.QuotaAnalytics(c.Request().Context(), ws, runtimeActor(c), q.Window, q.From, q.To)
	if err != nil {
		return handleError(c, err)
	}
	out := buildQuotaAnalytics(q, data, now)
	if c.QueryParam("format") == "csv" {
		return writeQuotaCSV(c, out)
	}
	return c.JSON(http.StatusOK, out)
}

func quotaQueryList(c echo.Context, name string) []string {
	var out []string
	for _, key := range []string{name, name + "[]"} {
		for _, v := range c.QueryParams()[key] {
			for _, part := range strings.Split(v, ",") {
				if part = strings.TrimSpace(part); part != "" && !slices.Contains(out, part) {
					out = append(out, part)
				}
			}
		}
	}
	return out
}

func parseQuotaQuery(c echo.Context, now time.Time) (quotaQuery, error) {
	q := quotaQuery{Window: c.QueryParam("window"), Step: c.QueryParam("step"), TZ: c.QueryParam("tz"), Accounts: quotaQueryList(c, "account")}
	if q.Window == "" {
		q.Window = domain.QuotaWindowWeek
	}
	if !slices.Contains(domain.QuotaWindows, q.Window) {
		return q, apierror.BadRequest("window must be one of 5h, week, month, budget")
	}
	if q.TZ == "" {
		q.TZ = quotaDefaultTZ
	}
	if format := c.QueryParam("format"); format != "" && format != "json" && format != "csv" {
		return q, apierror.BadRequest("format must be json or csv")
	}
	loc, err := time.LoadLocation(q.TZ)
	if err != nil {
		return q, apierror.BadRequest("tz must be an IANA time zone")
	}
	q.loc = loc
	period, from, to := c.QueryParam("period"), c.QueryParam("from"), c.QueryParam("to")
	if period != "" && (from != "" || to != "") {
		return q, apierror.BadRequest("give period or from/to, not both")
	}
	q.To = now
	switch {
	case from != "":
		if q.From, err = parseQuotaDate(from, loc); err != nil {
			return q, apierror.BadRequest("from must be RFC3339 or YYYY-MM-DD")
		}
		if to != "" {
			if q.To, err = parseQuotaDate(to, loc); err != nil {
				return q, apierror.BadRequest("to must be RFC3339 or YYYY-MM-DD")
			}
		}
	case to != "":
		return q, apierror.BadRequest("to needs from")
	default:
		days := map[string]int{"": 7, "1d": 1, "7d": 7, "30d": 30}
		n, ok := days[period]
		if !ok {
			return q, apierror.BadRequest("period must be one of 1d, 7d, 30d")
		}
		q.From = now.Add(-time.Duration(n) * 24 * time.Hour)
	}
	if !q.From.Before(q.To) {
		return q, apierror.BadRequest("from must be before to")
	}
	if q.To.Sub(q.From) > quotaMaxRange {
		return q, apierror.BadRequest("the range is at most 400 days")
	}
	span := q.To.Sub(q.From)
	switch q.Step {
	case "", "auto":
		switch {
		case span <= 48*time.Hour:
			q.Step = "hour"
		case span <= 60*24*time.Hour:
			q.Step = "day"
		default:
			q.Step = "week"
		}
	case "hour", "day", "week":
	default:
		return q, apierror.BadRequest("step must be one of auto, hour, day, week")
	}
	if n := len(quotaBuckets(q)); n > quotaMaxBuckets {
		return q, apierror.BadRequest(fmt.Sprintf("too many buckets (%d): choose a larger step", n))
	}
	return q, nil
}

// Date-only boundaries are midnight in the requested zone.
func parseQuotaDate(raw string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", raw, loc)
}

// quotaBucketStart is the start of the step containing t in the query's zone;
// weeks start on Monday.
func quotaBucketStart(q quotaQuery, t time.Time) time.Time {
	t = t.In(q.loc)
	switch q.Step {
	case "hour":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, q.loc)
	case "day":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, q.loc)
	default:
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, q.loc)
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	}
}

func quotaNextBucket(q quotaQuery, t time.Time) time.Time {
	switch q.Step {
	case "hour":
		return t.Add(time.Hour)
	case "day":
		return t.AddDate(0, 0, 1)
	default:
		return t.AddDate(0, 0, 7)
	}
}

func quotaBuckets(q quotaQuery) []time.Time {
	out := []time.Time{}
	for b := quotaBucketStart(q, q.From); b.Before(q.To) && len(out) <= quotaMaxBuckets; b = quotaNextBucket(q, b) {
		out = append(out, b)
	}
	return out
}

func quotaPercent(v float64) string {
	return strconv.Itoa(int(math.Round(v*100))) + "%"
}

// quotaWindowOrder sorts other windows: the named windows first, then raw names.
var quotaWindowOrder = map[string]int{domain.QuotaWindow5h: 0, domain.QuotaWindowWeek: 1, domain.QuotaWindowMonth: 2, domain.QuotaWindowBudget: 3}

// quotaGroup is one row of the answer: a catalog account with its pools, or a
// pool no account lists, keyed by its own pool_ref.
type quotaGroup struct {
	key      string
	provider string
	pools    []string
}

// quotaGroups maps the pools onto catalog accounts. A pool shared by several
// accounts feeds each of them; a pool no account lists keeps its pool_ref.
func quotaGroups(data *postgres.QuotaAnalyticsData) []quotaGroup {
	index := map[string]int{}
	var groups []quotaGroup
	add := func(key, provider, pool string) {
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, quotaGroup{key: key, provider: provider})
		}
		if groups[i].provider == "" {
			groups[i].provider = provider
		}
		if pool != "" && !slices.Contains(groups[i].pools, pool) {
			groups[i].pools = append(groups[i].pools, pool)
		}
	}
	mapped := map[string]bool{}
	for _, a := range data.Accounts {
		add(a.Account, a.Provider, "")
		for _, pool := range a.Pools {
			add(a.Account, a.Provider, pool)
			mapped[pool] = true
		}
	}
	for _, p := range data.Pools {
		if !mapped[p.Pool] {
			add(p.Pool, p.Provider, p.Pool)
		}
	}
	for _, l := range data.Latest {
		if !mapped[l.Pool] {
			add(l.Pool, l.Provider, l.Pool)
		}
	}
	for _, h := range data.History {
		if !mapped[h.Pool] {
			add(h.Pool, "", h.Pool)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].key < groups[j].key })
	return groups
}

// buildQuotaAnalytics shapes the stored rows into the widget answer: one row
// and one line per account (no stacking, shares of different accounts do not
// add up), an account without the window or without data included with its
// reason. The account's current value is the newest observation of the asked
// window among its pools; its line and peak are the largest share of its
// pools in a bucket.
func buildQuotaAnalytics(q quotaQuery, data *postgres.QuotaAnalyticsData, now time.Time) quotaAnalytics {
	out := quotaAnalytics{Query: q, Buckets: quotaBuckets(q), Series: []quotaSeries{}, Rows: []quotaRow{}, Capabilities: map[string]bool{"quota": true}}
	if q.Accounts == nil {
		out.Query.Accounts = []string{}
	}
	latest := map[string][]postgres.QuotaLatest{}
	for _, l := range data.Latest {
		latest[l.Pool] = append(latest[l.Pool], l)
	}
	history := map[string][]postgres.QuotaHour{}
	for _, h := range data.History {
		history[h.Pool] = append(history[h.Pool], h)
	}

	for _, g := range quotaGroups(data) {
		if len(q.Accounts) > 0 && !slices.Contains(q.Accounts, g.key) {
			continue
		}
		row := quotaRow{Account: g.key, Provider: g.provider}
		var cur *postgres.QuotaLatest
		others := map[string]postgres.QuotaLatest{}
		for _, pool := range g.pools {
			for i, l := range latest[pool] {
				// 7d and 1w are one week.
				w := domain.NormalizeQuotaWindow(l.WindowRaw)
				if w != q.Window {
					key := w
					if w == domain.QuotaWindowOther {
						key = w + ":" + l.WindowRaw
					}
					if o, ok := others[key]; !ok || quotaNewer(l, o) {
						others[key] = l
					}
					continue
				}
				if cur == nil || quotaNewer(l, *cur) {
					cur = &latest[pool][i]
				}
			}
		}
		row.OtherWindows = quotaOtherWindows(others)
		if cur == nil {
			text := "no " + quotaWindowLabel[q.Window] + " window"
			var parts []string
			for _, o := range row.OtherWindows {
				if o.Now != nil {
					label := quotaWindowLabel[o.Window]
					if label == "" {
						label = o.WindowRaw
					}
					parts = append(parts, label+" — "+quotaPercent(*o.Now))
				}
			}
			if len(parts) > 0 {
				text += ": " + strings.Join(parts, ", ")
			}
			row.Reason = &quotaReason{Code: "no_window", Text: text, OtherWindows: row.OtherWindows}
		} else {
			raw, status, observed := cur.WindowRaw, cur.Status, cur.ObservedAt
			row.WindowRaw, row.Status, row.ObservedAt, row.ResetAt = &raw, &status, &observed, cur.ResetAt
			row.Stale = now.Sub(cur.ObservedAt) > time.Duration(cur.MaxAgeSeconds)*time.Second
			if domain.QuotaMeasured(cur.Status) && cur.UsedFraction != nil {
				row.Now = cur.UsedFraction
			} else {
				row.Reason = &quotaReason{Code: "no_data", Text: "source provided no data (" + cur.Status + ")", OtherWindows: row.OtherWindows}
			}
		}
		// The account's hour is the largest share of its pools in that hour.
		// Keyed by the instant, so one hour counts once however many pools have it.
		hours := map[int64]postgres.QuotaHour{}
		for _, pool := range g.pools {
			for _, h := range history[pool] {
				prev, seen := hours[h.Hour.Unix()]
				if !seen || (h.UsedMax != nil && (prev.UsedMax == nil || *h.UsedMax > *prev.UsedMax)) {
					hours[h.Hour.Unix()] = h
				}
			}
		}
		if len(hours) > 0 {
			values := make([]*float64, len(out.Buckets))
			measured, atLimit := false, 0
			for _, h := range hours {
				hour, used := h.Hour, h.UsedMax
				if used == nil {
					continue
				}
				measured = true
				if row.Peak == nil || *used > *row.Peak {
					v := *used
					row.Peak = &v
				}
				if *used >= quotaAtLimit {
					atLimit++
				}
				i := sort.Search(len(out.Buckets), func(i int) bool { return out.Buckets[i].After(hour) }) - 1
				if i >= 0 && (values[i] == nil || *used > *values[i]) {
					v := *used
					values[i] = &v
				}
			}
			if measured {
				row.HoursAtLimit = &atLimit
			}
			out.Series = append(out.Series, quotaSeries{Key: g.key, Label: g.key, Provider: g.provider, Values: values})
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// quotaNewer orders observations of one window: the newest wins, then the
// smaller controller and pool, so the choice never depends on read order.
func quotaNewer(a, b postgres.QuotaLatest) bool {
	if !a.ObservedAt.Equal(b.ObservedAt) {
		return a.ObservedAt.After(b.ObservedAt)
	}
	if a.ControllerRef != b.ControllerRef {
		return a.ControllerRef < b.ControllerRef
	}
	return a.Pool < b.Pool
}

// quotaOtherWindows lists every window of the account except the asked one,
// the newest observation of each, named windows first.
func quotaOtherWindows(others map[string]postgres.QuotaLatest) []quotaOtherWindow {
	out := []quotaOtherWindow{}
	for _, l := range others {
		o := quotaOtherWindow{Window: domain.NormalizeQuotaWindow(l.WindowRaw), WindowRaw: l.WindowRaw, Status: l.Status}
		if domain.QuotaMeasured(l.Status) {
			o.Now = l.UsedFraction
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, iok := quotaWindowOrder[out[i].Window]
		oj, jok := quotaWindowOrder[out[j].Window]
		if !iok {
			oi = len(quotaWindowOrder)
		}
		if !jok {
			oj = len(quotaWindowOrder)
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].WindowRaw < out[j].WindowRaw
	})
	return out
}

func quotaCSVFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func quotaCSVTime(v *time.Time) string {
	if v == nil {
		return ""
	}
	return v.UTC().Format(time.RFC3339)
}

func quotaCSVString(v *string) string {
	if v == nil {
		return ""
	}
	return csvText(*v)
}

// writeQuotaCSV writes the rows, a blank line and the series in long form
// (bucket, account, used_max). An empty cell is "no data", never 0.
func writeQuotaCSV(c echo.Context, out quotaAnalytics) error {
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="quota-`+out.Query.Window+`.csv"`)
	c.Response().WriteHeader(http.StatusOK)
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"account", "provider", "window_raw", "now", "reset_at", "peak", "hours_at_limit", "status", "observed_at", "stale", "reason"})
	for _, r := range out.Rows {
		hours := ""
		if r.HoursAtLimit != nil {
			hours = strconv.Itoa(*r.HoursAtLimit)
		}
		reason := ""
		if r.Reason != nil {
			reason = csvText(r.Reason.Text)
		}
		_ = w.Write([]string{csvText(r.Account), csvText(r.Provider), quotaCSVString(r.WindowRaw), quotaCSVFloat(r.Now), quotaCSVTime(r.ResetAt), quotaCSVFloat(r.Peak), hours, quotaCSVString(r.Status), quotaCSVTime(r.ObservedAt), strconv.FormatBool(r.Stale), reason})
	}
	_ = w.Write(nil)
	_ = w.Write([]string{"bucket", "account", "used_max"})
	for _, s := range out.Series {
		for i, b := range out.Buckets {
			_ = w.Write([]string{b.Format(time.RFC3339), csvText(s.Key), quotaCSVFloat(s.Values[i])})
		}
	}
	w.Flush()
	return w.Error()
}
