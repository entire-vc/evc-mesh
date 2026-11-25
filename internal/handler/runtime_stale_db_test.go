package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// staleRouteHTTP saves a catalog with route-a -> route-b -> route-c quota edges
// (each on its own account and pool) plus a second controller runner-b that
// also observes b-window, admits the three routes and returns the callers.
type staleRoute struct {
	*runtimeHTTP
	base, binding, ownerQuery, digest string
	reporterA, reporterB, worker      map[string]string
}

func staleRouteHTTP(t *testing.T) staleRoute {
	t.Helper()
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	receiver := map[string]string{"X-Test-User": f.receiverUser.String()}
	reporterA := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	agentB, grantB := seedRuntimeAgent(t, f.db, f.owner)
	reporterB := map[string]string{"X-Test-Agent": agentB.String(), "X-Test-Agent-Workspace": f.owner.String()}
	worker := map[string]string{"X-Test-Agent": f.worker.String(), "X-Test-Agent-Workspace": f.receiver.String()}

	catalog, err := domain.ParseRuntimeCatalog(f.catalog)
	require.NoError(t, err)
	second := catalog.Controllers["runner-a"]
	second.ReporterGrantID, second.Host = grantB, "other-host"
	catalog.Controllers["runner-b"] = second
	for _, ref := range []string{"a", "b", "c"} {
		window := ref + "-window"
		catalog.Pools[window] = domain.RuntimePool{Provider: "provider-a", ResourceRef: window, Aliases: []string{}, MaxConcurrency: 1}
		catalog.Accounts["acct-"+ref] = domain.RuntimeAccount{Provider: "provider-a", CredentialRef: "cred:route-" + ref, QuotaPoolsByMode: map[string][]string{"subscription": {window}}}
		p := catalog.Profiles["preferred"]
		p.AccountRef = "acct-" + ref
		catalog.Profiles["route-"+ref] = p
	}
	// runner-b runs a profile on account b, so it may report b-window too.
	onB := catalog.Profiles["route-b"]
	onB.ControllerRef = "runner-b"
	catalog.Profiles["route-b-on-b"] = onB
	b := catalog.Bindings["worker-b"]
	b.PermittedProfiles = []string{"route-a", "route-b", "route-c"}
	b.Policy.PrimaryProfiles = []string{"route-a"}
	b.Policy.QuotaEdges = map[string][]string{"route-a": {"route-b"}, "route-b": {"route-c"}}
	b.Policy.PreferredAccounts = map[string]string{}
	b.Policy.MaxAttempts = 5
	catalog.Bindings["worker-b"] = b
	raw, err := json.Marshal(catalog)
	require.NoError(t, err)

	base := "/workspaces/" + f.owner.String() + "/runtime"
	ownerQuery := "?resource_owner_workspace_id=" + f.owner.String()
	binding := "/workspaces/" + f.receiver.String() + "/runtime/bindings/worker-b"
	code, _, body := f.do(http.MethodPut, base, `{"if_revision":0,"enabled":true,"config":`+string(raw)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	code, snapshot, body := f.do(http.MethodGet, base, "", owner)
	require.Equal(t, http.StatusOK, code, body)
	digest, _ := snapshot["digest"].(string)
	require.NotEmpty(t, digest)
	code, _, body = f.do(http.MethodPut, binding+"/admission"+ownerQuery, `{"if_revision":0,"enabled":true,"permitted_profiles":["route-a","route-b","route-c"]}`, receiver)
	require.Equal(t, http.StatusOK, code, body)
	return staleRoute{runtimeHTTP: f, base: base, binding: binding, ownerQuery: ownerQuery, digest: digest, reporterA: reporterA, reporterB: reporterB, worker: worker}
}

func staleReport(t *testing.T, f *runtimeHTTP, digest string, pools map[string]map[string]any) string {
	t.Helper()
	capabilities, err := json.Marshal(f.capabilities)
	require.NoError(t, err)
	observed := time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339Nano)
	for _, pool := range pools {
		pool["observed_at"] = observed
	}
	data, err := json.Marshal(pools)
	require.NoError(t, err)
	return `{"schema_version":2,"revision":1,"digest":"` + digest + `","status":"applied","capabilities":` + string(capabilities) + `,"emergency_paused":false,"pools":` + string(data) + `}`
}

func available() map[string]any { return map[string]any{"state": "available", "verified": true} }
func exhausted() map[string]any { return map[string]any{"state": "exhausted", "verified": true} }
func stale() map[string]any {
	return map[string]any{"state": "unknown", "verified": false, "error_kind": "credential_stale"}
}
func authError() map[string]any { return map[string]any{"state": "auth_error", "verified": false} }

func previewTrace(t *testing.T, result map[string]any) []string {
	t.Helper()
	steps, _ := result["trace"].([]any)
	out := []string{}
	for _, step := range steps {
		s := step.(map[string]any)
		out = append(out, s["profile_id"].(string)+":"+s["reason"].(string))
	}
	return out
}

// report -> persistence -> preview over the real handler and DB: error_kind is
// decoded, stored and turns B into a passable credential_stale node.
func TestRuntimeHTTPCredentialStaleReportReachesPreview(t *testing.T) {
	r := staleRouteHTTP(t)
	f, base, binding, ownerQuery, digest, reporterA, worker := r.runtimeHTTP, r.base, r.binding, r.ownerQuery, r.digest, r.reporterA, r.worker
	report := staleReport(t, f, digest, map[string]map[string]any{"a-window": exhausted(), "b-window": stale(), "c-window": available()})
	code, _, body := f.do(http.MethodPost, base+"/controllers/runner-a/report", report, reporterA)
	require.Equal(t, http.StatusOK, code, body)

	var stored []byte
	require.NoError(t, f.db.GetContext(context.Background(), &stored, `SELECT r.report FROM runtime_controller_reports r JOIN integration_configs c ON c.id=r.integration_id WHERE c.workspace_id=$1 AND r.controller_ref='runner-a'`, f.owner))
	var saved domain.RuntimeReport
	require.NoError(t, json.Unmarshal(stored, &saved))
	require.Equal(t, domain.RuntimePoolErrorCredentialStale, saved.Pools["b-window"].ErrorKind, "error_kind is persisted")
	require.Empty(t, saved.Pools["a-window"].ErrorKind)

	code, result, body := f.do(http.MethodPost, binding+"/preview"+ownerQuery, `{"purpose":"new_launch","required_capabilities":[]}`, worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "route-c", result["selected_profile_id"], body)
	require.Equal(t, []string{"route-a:quota_blocked", "route-b:credential_stale", "route-c:available"}, previewTrace(t, result))
	require.Equal(t, false, result["launch_authorized"])

	// Control: B auth_error over the same path stops before C.
	report = staleReport(t, f, digest, map[string]map[string]any{"a-window": exhausted(), "b-window": authError(), "c-window": available()})
	code, _, body = f.do(http.MethodPost, base+"/controllers/runner-a/report", report, reporterA)
	require.Equal(t, http.StatusOK, code, body)
	code, result, body = f.do(http.MethodPost, binding+"/preview"+ownerQuery, `{"purpose":"new_launch","required_capabilities":[]}`, worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Nil(t, result["selected_profile_id"], body)
	require.Equal(t, "sibling_evidence_unknown", result["reason"])
}

// Only absent, empty or credential_stale is accepted; anything else rejects the
// whole report and leaves the stored one untouched.
func TestRuntimeHTTPReportRejectsUnknownErrorKind(t *testing.T) {
	r := staleRouteHTTP(t)
	f, base, digest, reporterA := r.runtimeHTTP, r.base, r.digest, r.reporterA
	for _, kind := range []string{"auth_rejected", "stale", "network", "CREDENTIAL_STALE"} {
		bad := stale()
		bad["error_kind"] = kind
		report := staleReport(t, f, digest, map[string]map[string]any{"b-window": bad})
		code, _, body := f.do(http.MethodPost, base+"/controllers/runner-a/report", report, reporterA)
		require.Equal(t, http.StatusBadRequest, code, kind+": "+body)
		require.Contains(t, body, "invalid pool observation error_kind")
	}
	empty := stale()
	empty["error_kind"] = ""
	code, _, body := f.do(http.MethodPost, base+"/controllers/runner-a/report", staleReport(t, f, digest, map[string]map[string]any{"b-window": empty}), reporterA)
	require.Equal(t, http.StatusOK, code, body)
	code, _, body = f.do(http.MethodPost, base+"/controllers/runner-a/report", staleReport(t, f, digest, map[string]map[string]any{"b-window": stale()}), reporterA)
	require.Equal(t, http.StatusOK, code, body)
}

// Two hosts that disagree on B's cause merge to plain unknown in either order:
// credential_stale from one and auth_error from the other never pass.
func TestRuntimeHTTPConflictingHostsDropCredentialStale(t *testing.T) {
	for _, staleOnA := range []bool{true, false} {
		r := staleRouteHTTP(t)
		f, base, binding, ownerQuery, digest := r.runtimeHTTP, r.base, r.binding, r.ownerQuery, r.digest
		reporterA, reporterB, worker := r.reporterA, r.reporterB, r.worker
		fromA, fromB := stale(), authError()
		if !staleOnA {
			fromA, fromB = authError(), stale()
		}
		code, _, body := f.do(http.MethodPost, base+"/controllers/runner-a/report", staleReport(t, f, digest, map[string]map[string]any{"a-window": exhausted(), "b-window": fromA, "c-window": available()}), reporterA)
		require.Equal(t, http.StatusOK, code, body)
		code, _, body = f.do(http.MethodPost, base+"/controllers/runner-b/report", staleReport(t, f, digest, map[string]map[string]any{"b-window": fromB}), reporterB)
		require.Equal(t, http.StatusOK, code, body)
		code, result, body := f.do(http.MethodPost, binding+"/preview"+ownerQuery, `{"purpose":"new_launch","required_capabilities":[]}`, worker)
		require.Equal(t, http.StatusOK, code, body)
		require.Nil(t, result["selected_profile_id"], "staleOnA=%v %s", staleOnA, body)
		require.Equal(t, "sibling_evidence_unknown", result["reason"], body)

		// Positive control: both hosts agree on credential_stale -> C.
		code, _, body = f.do(http.MethodPost, base+"/controllers/runner-a/report", staleReport(t, f, digest, map[string]map[string]any{"a-window": exhausted(), "b-window": stale(), "c-window": available()}), reporterA)
		require.Equal(t, http.StatusOK, code, body)
		code, _, body = f.do(http.MethodPost, base+"/controllers/runner-b/report", staleReport(t, f, digest, map[string]map[string]any{"b-window": stale()}), reporterB)
		require.Equal(t, http.StatusOK, code, body)
		code, result, body = f.do(http.MethodPost, binding+"/preview"+ownerQuery, `{"purpose":"new_launch","required_capabilities":[]}`, worker)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, "route-c", result["selected_profile_id"], body)
	}
}
