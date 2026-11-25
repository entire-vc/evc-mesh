package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func measuredQuotaObs(pool, window string, used float64, observed time.Time) string {
	return quotaObs(pool, window, "available", fmt.Sprintf(`,"used_fraction":%v`, used), observed)
}

func TestRuntimeHTTPQuotaAnalytics(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	base := "/workspaces/" + f.owner.String()
	report := base + "/runtime/controllers/runner-a/quota-observations"
	analytics := base + "/analytics/quota"

	code, _, body := f.do(http.MethodPut, base+"/runtime", `{"if_revision":0,"enabled":true,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)

	now := time.Now()
	hour := now.UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	hourUnder := hour.Add(time.Hour)
	for _, b := range []string{
		quotaBody(measuredQuotaObs("long-window", "7d", 0.5, hour.Add(5*time.Minute)), measuredQuotaObs("shared-window", "5h", 0.71, hour.Add(5*time.Minute))),
		quotaBody(measuredQuotaObs("long-window", "7d", 0.90, hour.Add(10*time.Minute))),
		quotaBody(measuredQuotaObs("long-window", "7d", 0.89, hourUnder.Add(5*time.Minute))),
		quotaBody(measuredQuotaObs("long-window", "1w", 0.4, now.Add(-time.Minute)), quotaObs("reserve-window", "7d", "unknown", "", now.Add(-time.Minute))),
	} {
		code, _, body = f.do(http.MethodPost, report, b, reporter)
		require.Equal(t, http.StatusOK, code, body)
	}

	t.Run("member who is not admin gets 403", func(t *testing.T) {
		member := uuid.New()
		suffix := member.String()[:8]
		_, err := f.db.ExecContext(context.Background(), `INSERT INTO users (id,email,password_hash,display_name,username,is_active) VALUES ($1,$2,'x','Quota member',$3,true)`, member, "qm-"+suffix+"@example.com", "qm-"+suffix)
		require.NoError(t, err)
		_, err = f.db.ExecContext(context.Background(), `INSERT INTO workspace_members (id,workspace_id,user_id,role) VALUES ($1,$2,$3,'member')`, uuid.New(), f.owner, member)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = f.db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, member) })
		for name, hdr := range map[string]map[string]string{"member": {"X-Test-User": member.String()}, "reporter key": reporter, "anonymous": nil} {
			code, out, body := f.do(http.MethodGet, analytics+"?window=week&period=1d", "", hdr)
			require.Equal(t, http.StatusForbidden, code, name+": "+body)
			require.Equal(t, map[string]any{"quota": false}, out["capabilities"], body)
			require.NotContains(t, out, "rows")
		}
		code, _, body := f.do(http.MethodGet, analytics+"?window=week&period=1d&format=csv", "", map[string]string{"X-Test-User": member.String()})
		require.Equal(t, http.StatusForbidden, code, "csv too: "+body)
	})

	t.Run("owner reads rows and series", func(t *testing.T) {
		code, out, body := f.do(http.MethodGet, analytics+"?window=week&period=1d&tz=UTC", "", owner)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, map[string]any{"quota": true}, out["capabilities"])
		q := out["query"].(map[string]any)
		require.Equal(t, "hour", q["step"], "auto step for one day is an hour")
		buckets := out["buckets"].([]any)
		require.GreaterOrEqual(t, len(buckets), 24)

		rows := map[string]map[string]any{}
		for _, r := range out["rows"].([]any) {
			row := r.(map[string]any)
			rows[row["account"].(string)] = row
		}
		require.Len(t, rows, 2, "one row per catalog account, not per pool: "+body)

		// preferred holds shared-window and long-window.
		long := rows["preferred"]
		require.Equal(t, 0.4, long["now"], "now is the newest observation of the week (1w)")
		require.Equal(t, "1w", long["window_raw"])
		require.Equal(t, "available", long["status"])
		require.Equal(t, 0.90, long["peak"])
		require.EqualValues(t, 1, long["hours_at_limit"], "the 0.90 hour counts, the 0.89 hour does not")
		require.Nil(t, long["reason"])
		other := long["other_windows"].([]any)
		require.Len(t, other, 1, body)
		require.Equal(t, "5h", other[0].(map[string]any)["window"], "the 5h window of shared-window belongs to the same account")
		require.Equal(t, 0.71, other[0].(map[string]any)["now"])

		reserve := rows["reserve"]
		require.Contains(t, reserve, "now")
		require.Nil(t, reserve["now"], "unknown is null, not 0")
		require.Equal(t, "unknown", reserve["status"])
		require.Equal(t, "no_data", reserve["reason"].(map[string]any)["code"])
		require.Nil(t, reserve["hours_at_limit"], "no number in the period is null, not 0")

		series := out["series"].([]any)
		keys := []string{}
		for _, s := range series {
			sm := s.(map[string]any)
			keys = append(keys, sm["key"].(string))
			require.Len(t, sm["values"].([]any), len(buckets))
		}
		require.Equal(t, []string{"preferred", "reserve"}, keys, "series are keyed by account")
		var peak any
		for i, b := range buckets {
			if b.(string) == hour.Format(time.RFC3339) {
				peak = series[0].(map[string]any)["values"].([]any)[i]
			}
		}
		require.Equal(t, 0.90, peak, "the hour's bucket carries used_max")
		for _, v := range series[1].(map[string]any)["values"].([]any) {
			require.Nil(t, v, "an hour of only unknown has no value")
		}
	})

	t.Run("account filter and other windows", func(t *testing.T) {
		code, out, body := f.do(http.MethodGet, analytics+"?window=5h&period=7d&account[]=preferred", "", owner)
		require.Equal(t, http.StatusOK, code, body)
		rows := out["rows"].([]any)
		require.Len(t, rows, 1)
		require.Equal(t, 0.71, rows[0].(map[string]any)["now"], "the account's 5h window comes from shared-window")
		code, out, body = f.do(http.MethodGet, analytics+"?window=5h&period=7d&account[]=shared-window", "", owner)
		require.Equal(t, http.StatusOK, code, body)
		require.Empty(t, out["rows"], "a pool_ref of an account is not a row key")
		require.Equal(t, "day", out["query"].(map[string]any)["step"])
	})

	t.Run("csv", func(t *testing.T) {
		code, _, body := f.do(http.MethodGet, analytics+"?window=week&period=1d&format=csv", "", owner)
		require.Equal(t, http.StatusOK, code, body)
		lines := strings.Split(body, "\n")
		require.Equal(t, "account,provider,window_raw,now,reset_at,peak,hours_at_limit,status,observed_at,stale,reason", lines[0])
		require.Contains(t, body, "\nreserve,provider-a,7d,,", "unknown now is an empty cell, not 0")
		require.Contains(t, body, "\nbucket,account,used_max\n")
		require.Contains(t, body, ",preferred,0.9\n")
		require.Contains(t, body, ",preferred,0.89\n", "the 0.89 hour is in the series but not in hours_at_limit")
	})

	t.Run("bad parameters", func(t *testing.T) {
		for _, qs := range []string{"format=xml", "window=day", "period=2d", "period=1d&from=2026-11-01", "from=2026-11-10&to=2026-11-01", "from=2025-01-01&to=2026-11-01", "step=minute", "tz=Mars/Base", "to=2026-11-01", "from=2026-01-01&step=hour"} {
			code, _, body := f.do(http.MethodGet, analytics+"?"+qs, "", owner)
			require.Equal(t, http.StatusBadRequest, code, qs+": "+body)
		}
	})
}

// AC1 and AC4 on a real database: the Runner's pool_refs carry a
// window suffix (acc/x/7d, acc/x/5h), the rows and series are keyed by the
// catalog account (acc/x), and exhausted with a share is at the limit.
func TestRuntimeHTTPQuotaAnalyticsByAccount(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	base := "/workspaces/" + f.owner.String()
	report := base + "/runtime/controllers/runner-a/quota-observations"
	analytics := base + "/analytics/quota"

	catalog, err := domain.ParseRuntimeCatalog(f.catalog)
	require.NoError(t, err)
	for _, pool := range []string{"acc/x/7d", "acc/x/5h", "acc/y/7d"} {
		catalog.Pools[pool] = domain.RuntimePool{Provider: "provider-a", ResourceRef: pool, Aliases: []string{}, MaxConcurrency: 1}
	}
	catalog.Accounts["acc/x"] = domain.RuntimeAccount{Provider: "provider-a", CredentialRef: "cred:acc-x", QuotaPoolsByMode: map[string][]string{"subscription": {"acc/x/7d", "acc/x/5h"}}}
	catalog.Accounts["acc/y"] = domain.RuntimeAccount{Provider: "provider-a", CredentialRef: "cred:acc-y", QuotaPoolsByMode: map[string][]string{"subscription": {"acc/y/7d"}}}
	// alias/z lists shared-window by its alias: it shares the pool with preferred.
	catalog.Accounts["alias/z"] = domain.RuntimeAccount{Provider: "provider-a", CredentialRef: "cred:alias-z", QuotaPoolsByMode: map[string][]string{"subscription": {"another-credential-view"}}}
	for _, acc := range []string{"acc/x", "acc/y", "alias/z"} {
		p := catalog.Profiles["preferred"]
		p.AccountRef = acc
		catalog.Profiles[strings.ReplaceAll(acc, "/", "-")] = p
	}
	config, err := json.Marshal(catalog)
	require.NoError(t, err)
	code, _, body := f.do(http.MethodPut, base+"/runtime", `{"if_revision":0,"enabled":true,"config":`+string(config)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)

	now := time.Now()
	hour := now.UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for _, b := range []string{
		quotaBody(measuredQuotaObs("acc/x/7d", "7d", 0.6, hour.Add(5*time.Minute)), measuredQuotaObs("acc/x/5h", "5h", 0.71, hour.Add(5*time.Minute))),
		quotaBody(measuredQuotaObs("acc/x/7d", "7d", 0.4, now.Add(-time.Minute))),
		quotaBody(quotaObs("acc/y/7d", "7d", "exhausted", `,"used_fraction":1`, hour.Add(10*time.Minute))),
		quotaBody(measuredQuotaObs("shared-window", "7d", 0.3, now.Add(-time.Minute))),
	} {
		code, _, body = f.do(http.MethodPost, report, b, reporter)
		require.Equal(t, http.StatusOK, code, body)
	}

	code, out, body := f.do(http.MethodGet, analytics+"?window=week&period=1d&tz=UTC", "", owner)
	require.Equal(t, http.StatusOK, code, body)
	rows := map[string]map[string]any{}
	for _, r := range out["rows"].([]any) {
		row := r.(map[string]any)
		acc := row["account"].(string)
		require.NotContains(t, rows, acc, "every account exactly once: "+body)
		require.NotRegexp(t, `/(7d|5h|month|unknown|spend)$`, acc, "no pool window suffix in an account")
		rows[acc] = row
	}
	require.Len(t, rows, 5, "preferred, reserve, acc/x, acc/y, alias/z: "+body)

	// AC1: acc/x/7d and acc/x/5h are one row acc/x with 5h among its other windows.
	x := rows["acc/x"]
	require.Equal(t, 0.4, x["now"], body)
	require.Equal(t, "7d", x["window_raw"])
	require.Equal(t, 0.6, x["peak"])
	windows := map[string]any{}
	for _, o := range x["other_windows"].([]any) {
		windows[o.(map[string]any)["window"].(string)] = o.(map[string]any)["now"]
	}
	require.Equal(t, map[string]any{"5h": 0.71}, windows)

	// AC4: exhausted with a share is at the limit, its hour is recorded.
	y := rows["acc/y"]
	require.Equal(t, 1.0, y["now"], "exhausted with used_fraction is at the limit, not no data: "+body)
	require.Equal(t, "exhausted", y["status"])
	require.Nil(t, y["reason"])
	require.Equal(t, 1.0, y["peak"], "runtime_quota_hourly keeps the share of exhausted")
	require.EqualValues(t, 1, y["hours_at_limit"])
	var usedMax *float64
	require.NoError(t, f.db.GetContext(context.Background(), &usedMax, `SELECT h.used_max FROM runtime_quota_hourly h JOIN integration_configs i ON i.id=h.integration_id WHERE i.workspace_id=$1 AND h.pool_ref='acc/y/7d'`, f.owner))
	require.NotNil(t, usedMax, "re-read after the save: hourly used_max is stored for exhausted")
	require.Equal(t, 1.0, *usedMax)

	// A pool listed by two accounts (here by its alias) feeds both rows.
	require.Equal(t, 0.3, rows["preferred"]["now"])
	require.Equal(t, 0.3, rows["alias/z"]["now"])

	series := map[string]bool{}
	for _, s := range out["series"].([]any) {
		sm := s.(map[string]any)
		require.Equal(t, sm["key"], sm["label"])
		series[sm["key"].(string)] = true
	}
	require.True(t, series["acc/x"], "the series key is the account: "+body)
	require.True(t, series["acc/y"])
	require.False(t, series["acc/x/7d"])

	code, _, body = f.do(http.MethodGet, analytics+"?window=week&period=1d&format=csv", "", owner)
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, "\nacc/x,provider-a,7d,0.4,", "csv account is the account_ref")
	require.NotContains(t, body, "acc/x/7d")
}
