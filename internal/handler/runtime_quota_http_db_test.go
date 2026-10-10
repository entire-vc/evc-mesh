package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func quotaObs(pool, window, status, extra string, observed time.Time) string {
	return fmt.Sprintf(`{"pool":%q,"provider":"provider-a","window":%q,"unit":"fraction","observed_at":%q,"source":"provider_headers","confidence":"high","max_age_seconds":1800,"status":%q%s}`,
		pool, window, observed.UTC().Format(time.RFC3339Nano), status, extra)
}

func quotaBody(obs ...string) string {
	body := `{"schema_version":1,"observations":[`
	for i, o := range obs {
		if i > 0 {
			body += ","
		}
		body += o
	}
	return body + `]}`
}

func TestRuntimeHTTPQuotaObservations(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	worker := map[string]string{"X-Test-Agent": f.worker.String(), "X-Test-Agent-Workspace": f.receiver.String()}
	base := "/workspaces/" + f.owner.String() + "/runtime"
	path := base + "/controllers/runner-a/quota-observations"

	code, _, body := f.do(http.MethodPut, base, `{"if_revision":0,"enabled":true,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)

	now := time.Now()
	t.Run("rejections store nothing", func(t *testing.T) {
		good := quotaObs("shared-window", "5h", "unknown", "", now.Add(-time.Minute))
		bad := map[string]string{
			"future observed_at":    quotaObs("shared-window", "7d", "unknown", "", now.Add(time.Hour)),
			"unknown field":         quotaObs("shared-window", "7d", "unknown", `,"token":"x"`, now.Add(-time.Minute)),
			"out of range fraction": quotaObs("shared-window", "7d", "available", "", now.Add(-time.Minute))[:0] + fmt.Sprintf(`{"pool":"shared-window","provider":"provider-a","window":"7d","unit":"fraction","used_fraction":1.5,"observed_at":%q,"source":"s","confidence":"high","max_age_seconds":60,"status":"available"}`, now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)),
			"provider mismatch":     fmt.Sprintf(`{"pool":"shared-window","provider":"provider-z","window":"7d","unit":"fraction","observed_at":%q,"source":"s","confidence":"high","max_age_seconds":60,"status":"unknown"}`, now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)),
			"duplicate window":      quotaObs("another-credential-view", "5h", "unknown", "", now.Add(-time.Minute)),
		}
		for name, o := range bad {
			code, _, body := f.do(http.MethodPost, path, quotaBody(good, o), reporter)
			require.Equal(t, http.StatusBadRequest, code, name+": "+body)
		}
		code, _, body := f.do(http.MethodPost, path, quotaBody(good, quotaObs("not-a-pool", "5h", "unknown", "", now.Add(-time.Minute))), reporter)
		require.Equal(t, http.StatusForbidden, code, "unrelated or unknown pool: "+body)
		code, list, body := f.do(http.MethodGet, path, "", reporter)
		require.Equal(t, http.StatusOK, code, body)
		require.Empty(t, list["observations"], "a rejected batch must leave no partial rows")
	})

	t.Run("identity", func(t *testing.T) {
		b := quotaBody(quotaObs("shared-window", "5h", "unknown", "", now.Add(-time.Minute)))
		for name, hdr := range map[string]map[string]string{"human admin": owner, "other workspace worker": worker, "anonymous": nil} {
			code, _, body := f.do(http.MethodPost, path, b, hdr)
			require.Equal(t, http.StatusForbidden, code, name+": "+body)
		}
		code, _, body := f.do(http.MethodPost, base+"/controllers/nope/quota-observations", b, reporter)
		require.Equal(t, http.StatusNotFound, code, body)
		code, _, _ = f.do(http.MethodGet, path, "", worker)
		require.Equal(t, http.StatusForbidden, code, "worker key cannot read")
		code, _, _ = f.do(http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusForbidden, code, "anonymous cannot read")
	})

	t.Run("unknown is null not zero, canonical pool, stale flag, older never overwrites newer", func(t *testing.T) {
		fresh := quotaObs("another-credential-view", "5h", "unknown", `,"error_kind":"stale"`, now.Add(-time.Minute))
		measured := fmt.Sprintf(`{"pool":"long-window","provider":"provider-a","window":"7d","unit":"fraction","used_fraction":0.42,"remaining":58,"reset_at":%q,"observed_at":%q,"source":"provider_headers","confidence":"medium","max_age_seconds":60,"status":"available"}`,
			now.Add(24*time.Hour).UTC().Format(time.RFC3339), now.Add(-2*time.Hour).UTC().Format(time.RFC3339Nano))
		code, rec, body := f.do(http.MethodPost, path, quotaBody(fresh, measured), reporter)
		require.Equal(t, http.StatusOK, code, body)
		require.EqualValues(t, 2, rec["recorded"])
		require.Equal(t, false, rec["launch_authorized"])

		for name, hdr := range map[string]map[string]string{"reporter": reporter, "human admin": owner} {
			getCode, list, getBody := f.do(http.MethodGet, path, "", hdr)
			require.Equal(t, http.StatusOK, getCode, name+": "+getBody)
			rows := list["observations"].([]any)
			require.Len(t, rows, 2, getBody)
			long, shared := rows[0].(map[string]any), rows[1].(map[string]any)
			require.Equal(t, "long-window", long["pool"])
			require.Equal(t, 0.42, long["used_fraction"])
			require.Equal(t, 58.0, long["remaining"])
			require.Equal(t, true, long["stale"], "observed 2h ago with max_age 60s")
			require.Equal(t, "shared-window", shared["pool"], "alias resolved to the canonical pool")
			require.Contains(t, shared, "used_fraction")
			require.Nil(t, shared["used_fraction"], "unknown stays null")
			require.Nil(t, shared["remaining"])
			require.Equal(t, false, shared["stale"])
			require.Equal(t, "stale", shared["error_kind"])
		}

		older := quotaObs("shared-window", "5h", "exhausted", "", now.Add(-time.Hour))
		code, _, body = f.do(http.MethodPost, path, quotaBody(older), reporter)
		require.Equal(t, http.StatusOK, code, body)
		_, list, _ := f.do(http.MethodGet, path, "", reporter)
		shared := list["observations"].([]any)[1].(map[string]any)
		require.Equal(t, "unknown", shared["status"], "an older observation must not replace a newer one")

		newer := quotaObs("shared-window", "5h", "exhausted", "", now.Add(-time.Second))
		code, _, body = f.do(http.MethodPost, path, quotaBody(newer), reporter)
		require.Equal(t, http.StatusOK, code, body)
		_, list, _ = f.do(http.MethodGet, path, "", reporter)
		require.Equal(t, "exhausted", list["observations"].([]any)[1].(map[string]any)["status"])
	})

	t.Run("revoked reporter grant", func(t *testing.T) {
		_, err := f.db.ExecContext(context.Background(), `UPDATE agent_workspace_grants SET revoked_at=now() WHERE agent_id=$1`, f.reporter)
		require.NoError(t, err)
		code, _, _ := f.do(http.MethodGet, path, "", reporter)
		require.Equal(t, http.StatusForbidden, code, "revoked key cannot read")
		code, list, body := f.do(http.MethodGet, path, "", owner)
		require.Equal(t, http.StatusOK, code, body)
		require.Empty(t, list["observations"], "observations of a revoked reporter are not served")
	})
}
