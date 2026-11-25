package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestQuotaBuckets(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)
	// Wednesday 18 Nov 2026 10:30 MSK.
	from := time.Date(2026, 11, 18, 10, 30, 0, 0, msk)

	q := quotaQuery{Step: "week", loc: msk, From: from, To: from.Add(15 * 24 * time.Hour)}
	b := quotaBuckets(q)
	require.Equal(t, time.Date(2026, 11, 16, 0, 0, 0, 0, msk), b[0], "weeks start on Monday in the asked zone")
	require.Len(t, b, 3)

	q = quotaQuery{Step: "day", loc: msk, From: from, To: from.Add(48 * time.Hour)}
	b = quotaBuckets(q)
	require.Equal(t, time.Date(2026, 11, 18, 0, 0, 0, 0, msk), b[0])
	require.Len(t, b, 3)

	q = quotaQuery{Step: "hour", loc: msk, From: from, To: from.Add(3 * time.Hour)}
	require.Equal(t, time.Date(2026, 11, 18, 10, 0, 0, 0, msk), quotaBuckets(q)[0])
}

func TestBuildQuotaAnalyticsNullIsNotZero(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	q := quotaQuery{Window: "week", Step: "hour", loc: time.UTC, From: now.Add(-3 * time.Hour), To: now}
	half := 0.5
	data := &postgres.QuotaAnalyticsData{
		Pools: []postgres.QuotaPoolRef{{Pool: "a", Provider: "p"}, {Pool: "b", Provider: "p"}},
		Latest: []postgres.QuotaLatest{
			{Pool: "a", Provider: "p", WindowRaw: "7d", Status: "unknown", ObservedAt: now.Add(-time.Minute), MaxAgeSeconds: 600, ControllerRef: "new"},
			{Pool: "a", Provider: "p", WindowRaw: "1w", UsedFraction: &half, Status: "available", ObservedAt: now.Add(-48 * time.Hour), MaxAgeSeconds: 600, ControllerRef: "old"},
		},
		History: []postgres.QuotaHour{{Pool: "a", Hour: now.Add(-2 * time.Hour)}, {Pool: "a", Hour: now.Add(-time.Hour)}},
	}
	out := buildQuotaAnalytics(q, data, now)
	require.Len(t, out.Rows, 2)
	a := out.Rows[0]
	require.Equal(t, "unknown", *a.Status, "the newest observation across raw names and controllers wins")
	require.Nil(t, a.Now)
	require.Equal(t, "no_data", a.Reason.Code)
	require.Nil(t, a.Peak)
	require.Nil(t, a.HoursAtLimit, "unmeasured hours do not invent limit usage")
	require.Equal(t, []*float64{nil, nil, nil}, out.Series[0].Values)
	b := out.Rows[1]
	require.Equal(t, "no_window", b.Reason.Code)
	require.Equal(t, "no week window", b.Reason.Text)
	require.Nil(t, b.HoursAtLimit)
}

func TestQuotaQueryAndAggregation(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?window=week&from=2026-11-17&to=2026-11-18&tz=Europe/Moscow&account[]=a&account[]=b&account=a", http.NoBody)
	q, err := parseQuotaQuery(e.NewContext(req, httptest.NewRecorder()), now)
	require.NoError(t, err)
	require.Equal(t, "2026-11-17T00:00:00+03:00", q.From.Format(time.RFC3339))
	require.Equal(t, []string{"a", "b"}, q.Accounts)
	require.Equal(t, "hour", q.Step)

	q = quotaQuery{Window: "week", Step: "day", loc: time.UTC, From: now.Add(-48 * time.Hour), To: now}
	low, under, limit, high := 0.2, 0.89, 0.90, 0.99
	data := &postgres.QuotaAnalyticsData{
		Pools:   []postgres.QuotaPoolRef{{Pool: "a", Provider: "p"}},
		Latest:  []postgres.QuotaLatest{{Pool: "a", WindowRaw: "7d", Status: "exhausted", UsedFraction: &high, ObservedAt: now, MaxAgeSeconds: 600}},
		History: []postgres.QuotaHour{{Pool: "a", Hour: now.Add(-47 * time.Hour), UsedMax: &low}, {Pool: "a", Hour: now.Add(-46 * time.Hour), UsedMax: &limit}, {Pool: "a", Hour: now.Add(-45 * time.Hour), UsedMax: &under}, {Pool: "a", Hour: now.Add(-24 * time.Hour), UsedMax: &high}},
	}
	out := buildQuotaAnalytics(q, data, now)
	require.Equal(t, high, *out.Rows[0].Now, "exhausted with a share is at the limit, not no data")
	require.Nil(t, out.Rows[0].Reason)
	require.Equal(t, high, *out.Rows[0].Peak)
	require.Equal(t, 2, *out.Rows[0].HoursAtLimit, "0.90 and 0.99 count, 0.89 does not: the threshold is inclusive and counts hours, not days")
	require.NotNil(t, out.Series[0].Values[0])
	require.NotNil(t, out.Series[0].Values[1])
	require.Equal(t, limit, *out.Series[0].Values[0], "day contains the max, not a sum")
	require.Equal(t, high, *out.Series[0].Values[1])
	require.Nil(t, out.Series[0].Values[2], "a gap is not zero")
	q.Accounts = []string{"missing"}
	out = buildQuotaAnalytics(q, data, now)
	require.Empty(t, out.Rows)
	require.Empty(t, out.Series)
}

func TestQuotaHoursAtLimitThreshold(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	q := quotaQuery{Window: "week", Step: "hour", loc: time.UTC, From: now.Add(-3 * time.Hour), To: now}
	under, at := 0.89, 0.90
	data := &postgres.QuotaAnalyticsData{
		Pools: []postgres.QuotaPoolRef{{Pool: "under", Provider: "p"}, {Pool: "at", Provider: "p"}},
		History: []postgres.QuotaHour{
			{Pool: "under", Hour: now.Add(-2 * time.Hour), UsedMax: &under},
			{Pool: "at", Hour: now.Add(-2 * time.Hour), UsedMax: &at},
		},
	}
	out := buildQuotaAnalytics(q, data, now)
	byPool := map[string]int{}
	for _, r := range out.Rows {
		byPool[r.Account] = *r.HoursAtLimit
	}
	require.Equal(t, 0, byPool["under"], "an hour at 0.89 is not at the limit")
	require.Equal(t, 1, byPool["at"], "an hour at 0.90 is at the limit")
}

// One row per catalog account, not per pool_ref: the pool names
// carry a window suffix and need not resemble the account at all.
func TestBuildQuotaAnalyticsByAccount(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	q := quotaQuery{Window: "week", Step: "hour", loc: time.UTC, From: now.Add(-3 * time.Hour), To: now}
	f := func(v float64) *float64 { return &v }
	h1, h2 := now.Add(-2*time.Hour), now.Add(-time.Hour)
	data := &postgres.QuotaAnalyticsData{
		Accounts: []postgres.QuotaAccountRef{
			{Account: "acc/x", Provider: "claude", Pools: []string{"acc/x/5h", "acc/x/7d", "zz-unrelated"}},
			{Account: "glm/alex", Provider: "glm", Pools: []string{"glm/alex/unknown"}},
			{Account: "glm-zcode/alex", Provider: "glm-zcode", Pools: []string{"glm/alex/unknown"}},
			{Account: "kimi/alex", Provider: "kimi", Pools: []string{"kimi/alex/month"}},
			{Account: "empty", Provider: "none"},
		},
		Pools: []postgres.QuotaPoolRef{{Pool: "acc/x/5h"}, {Pool: "acc/x/7d"}, {Pool: "zz-unrelated"}, {Pool: "glm/alex/unknown"}, {Pool: "kimi/alex/month"}, {Pool: "orphan/7d", Provider: "lost"}},
		Latest: []postgres.QuotaLatest{
			{Pool: "acc/x/7d", WindowRaw: "7d", Status: "available", UsedFraction: f(0.5), ObservedAt: now.Add(-2 * time.Minute), MaxAgeSeconds: 600},
			{Pool: "zz-unrelated", WindowRaw: "1w", Status: "available", UsedFraction: f(0.3), ObservedAt: now.Add(-time.Minute), MaxAgeSeconds: 600},
			{Pool: "acc/x/5h", WindowRaw: "5h", Status: "available", UsedFraction: f(0.71), ObservedAt: now.Add(-time.Minute), MaxAgeSeconds: 600},
			{Pool: "zz-unrelated", WindowRaw: "primary", Status: "unknown", ObservedAt: now.Add(-time.Minute), MaxAgeSeconds: 600},
			{Pool: "glm/alex/unknown", WindowRaw: "7d", Status: "exhausted", UsedFraction: f(1), ObservedAt: now.Add(-time.Minute), MaxAgeSeconds: 600},
			{Pool: "kimi/alex/month", WindowRaw: "month", Status: "available", UsedFraction: f(0.33), ObservedAt: now.Add(-time.Minute), MaxAgeSeconds: 600},
			{Pool: "orphan/7d", Provider: "lost", WindowRaw: "7d", Status: "available", UsedFraction: f(0.2), ObservedAt: now.Add(-time.Minute), MaxAgeSeconds: 600},
		},
		History: []postgres.QuotaHour{
			{Pool: "acc/x/7d", Hour: h1, UsedMax: f(0.95)},
			{Pool: "zz-unrelated", Hour: h1, UsedMax: f(0.4)},
			{Pool: "acc/x/7d", Hour: h2, UsedMax: f(0.2)},
			{Pool: "zz-unrelated", Hour: h2, UsedMax: f(0.92)},
			{Pool: "glm/alex/unknown", Hour: h2, UsedMax: f(1)},
		},
	}
	out := buildQuotaAnalytics(q, data, now)
	rows := map[string]quotaRow{}
	var keys []string
	for _, r := range out.Rows {
		keys = append(keys, r.Account)
		rows[r.Account] = r
	}
	require.Equal(t, []string{"acc/x", "empty", "glm-zcode/alex", "glm/alex", "kimi/alex", "orphan/7d"}, keys, "one row per account; an unmapped pool keeps its pool_ref")

	x := rows["acc/x"]
	require.Equal(t, "claude", x.Provider, "provider comes from the account")
	require.Equal(t, 0.3, *x.Now, "the newest week observation among the account's pools, whatever the pool's name")
	require.Equal(t, "1w", *x.WindowRaw)
	require.Nil(t, x.Reason)
	require.Equal(t, []quotaOtherWindow{{Window: "5h", WindowRaw: "5h", Now: f(0.71), Status: "available"}, {Window: "other", WindowRaw: "primary", Status: "unknown"}}, x.OtherWindows, "all other windows of all its pools")
	require.Equal(t, 0.95, *x.Peak, "peak is the max across pools")
	require.Equal(t, 2, *x.HoursAtLimit, "h1 (0.95) and h2 (0.92) once each, though two pools report each hour")

	for _, acc := range []string{"glm/alex", "glm-zcode/alex"} {
		r := rows[acc]
		require.Equal(t, 1.0, *r.Now, acc+": a shared pool feeds every account that lists it; exhausted with a share is at the limit")
		require.Equal(t, "exhausted", *r.Status)
		require.Nil(t, r.Reason)
	}
	require.Equal(t, "glm-zcode", rows["glm-zcode/alex"].Provider)

	kimi := rows["kimi/alex"]
	require.Equal(t, "no_window", kimi.Reason.Code)
	require.Equal(t, "no week window: month — 33%", kimi.Reason.Text)
	require.Len(t, kimi.Reason.OtherWindows, 1)
	require.Equal(t, "no_window", rows["empty"].Reason.Code, "a catalog account without pools is listed with its reason")
	require.Equal(t, "lost", rows["orphan/7d"].Provider)
	require.Equal(t, 0.2, *rows["orphan/7d"].Now)

	series := map[string]quotaSeries{}
	for _, s := range out.Series {
		require.Equal(t, s.Key, s.Label)
		series[s.Key] = s
	}
	require.Len(t, series, 3, "acc/x and both glm accounts have history")
	require.Equal(t, 0.95, *series["acc/x"].Values[1], "a bucket carries the max across the account's pools")
	require.Equal(t, 0.92, *series["acc/x"].Values[2])

	q.Accounts = []string{"acc/x"}
	out = buildQuotaAnalytics(q, data, now)
	require.Len(t, out.Rows, 1, "the account filter matches account_ref")
	q.Accounts = []string{"acc/x/7d"}
	require.Empty(t, buildQuotaAnalytics(q, data, now).Rows, "a mapped pool_ref is not a row key")
}

func TestBuildQuotaAnalyticsExhaustedWithoutShare(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	q := quotaQuery{Window: "week", Step: "hour", loc: time.UTC, From: now.Add(-3 * time.Hour), To: now}
	data := &postgres.QuotaAnalyticsData{
		Accounts: []postgres.QuotaAccountRef{{Account: "acc/x", Pools: []string{"acc/x/7d"}}},
		Latest:   []postgres.QuotaLatest{{Pool: "acc/x/7d", WindowRaw: "7d", Status: "exhausted", ObservedAt: now, MaxAgeSeconds: 600}},
	}
	r := buildQuotaAnalytics(q, data, now).Rows[0]
	require.Nil(t, r.Now, "exhausted without used_fraction has no share, never 0")
	require.Equal(t, "no_data", r.Reason.Code)
}
