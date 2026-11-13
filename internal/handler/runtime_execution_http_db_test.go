package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// execFixture is the runtime HTTP fixture plus an admitted binding and one
// Runner-enabled project the bound worker is a member of.
type execFixture struct {
	*runtimeHTTP
	project                       uuid.UUID
	owner, receiver, reporterHdrs map[string]string
	worker                        map[string]string
	base, execBase                string
}

func (f *runtimeHTTP) newProject(ws, agent uuid.UUID, runnerOn bool) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(), `INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'exec',$3)`, id, ws, "exec-"+id.String()[:8])
	require.NoError(f.t, err)
	_, err = f.db.ExecContext(context.Background(), `INSERT INTO project_members (id, project_id, agent_id, workspace_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), id, agent, ws)
	require.NoError(f.t, err)
	if runnerOn {
		_, err = f.db.ExecContext(context.Background(), `INSERT INTO project_integrations (id, project_id, type, extension_id, enabled, settings) VALUES ($1,$2,'agent-runner','agent-runner',true,'{}')`, uuid.New(), id)
		require.NoError(f.t, err)
	}
	return id
}

func newExecFixture(t *testing.T) *execFixture {
	t.Helper()
	f := &execFixture{runtimeHTTP: newRuntimeHTTP(t)}
	f.owner = map[string]string{"X-Test-User": f.ownerUser.String()}
	f.receiver = map[string]string{"X-Test-User": f.receiverUser.String()}
	f.reporterHdrs = map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.runtimeHTTP.owner.String()}
	f.worker = map[string]string{"X-Test-Agent": f.runtimeHTTP.worker.String(), "X-Test-Agent-Workspace": f.runtimeHTTP.receiver.String()}
	f.base = "/workspaces/" + f.runtimeHTTP.owner.String() + "/runtime"
	f.execBase = f.base + "/controllers/runner-a"
	code, _, body := f.do(http.MethodPut, f.base, `{"if_revision":0,"enabled":true,"config":`+string(f.catalog)+`}`, f.owner)
	require.Equal(t, http.StatusOK, code, body)
	binding := "/workspaces/" + f.runtimeHTTP.receiver.String() + "/runtime/bindings/worker-b"
	code, _, body = f.do(http.MethodPut, binding+"/admission?resource_owner_workspace_id="+f.runtimeHTTP.owner.String(), `{"if_revision":0,"enabled":true,"permitted_profiles":["preferred","reserve"]}`, f.receiver)
	require.Equal(t, http.StatusOK, code, body)
	f.project = f.newProject(f.runtimeHTTP.receiver, f.runtimeHTTP.worker, true)
	return f
}

func (f *execFixture) register(instance string) (status int, generation int64) {
	f.t.Helper()
	code, out, body := f.do(http.MethodPost, f.execBase+"/execution-instances", fmt.Sprintf(`{"schema_version":1,"instance_id":%q}`, instance), f.reporterHdrs)
	if code != http.StatusOK {
		return code, 0
	}
	g, _ := out["generation"].(float64)
	require.NotEmpty(f.t, body)
	return code, int64(g)
}

type execAttempt struct {
	id, turn, state, model string
	observed               time.Time
	project                uuid.UUID
}

func (f *execFixture) report(instance string, generation, sequence int64, reportedAt time.Time, attempts ...execAttempt) (status int, body string) {
	f.t.Helper()
	code, _, raw := f.do(http.MethodPost, f.execBase+"/execution-reports", f.reportBody(instance, generation, sequence, reportedAt, attempts...), f.reporterHdrs)
	return code, raw
}

func (f *execFixture) reportBody(instance string, generation, sequence int64, reportedAt time.Time, attempts ...execAttempt) string {
	items := ""
	for i, a := range attempts {
		if i > 0 {
			items += ","
		}
		model := "null"
		if a.model != "" {
			model = fmt.Sprintf("%q", a.model)
		}
		project := a.project
		if project == uuid.Nil {
			project = f.project
		}
		items += fmt.Sprintf(`{"workspace_id":%q,"project_id":%q,"agent_id":%q,"binding_ref":"worker-b","task_id":null,"controller_ref":"runner-a","instance_id":%q,"generation":%d,"attempt_id":%q,"turn_id":%q,"state":%q,
			"resolved_route":{"source":"runner_resolver","revision":1,"profile_ref":"preferred","harness":"prepared-harness","model":"model-a"},
			"observed":{"harness":"prepared-harness","model":%s,"effort":null,"source":"adapter_handshake","observed_at":%q}}`,
			f.runtimeHTTP.receiver, project, f.runtimeHTTP.worker, instance, generation, a.id, a.turn, a.state, model, a.observed.UTC().Format(time.RFC3339Nano))
	}
	return fmt.Sprintf(`{"schema_version":1,"instance_id":%q,"generation":%d,"sequence":%d,"reported_at":%q,"attempts":[%s]}`, instance, generation, sequence, reportedAt.UTC().Format(time.RFC3339Nano), items)
}

func (f *execFixture) read(query string, headers map[string]string) (status int, body map[string]any) {
	f.t.Helper()
	code, out, _ := f.do(http.MethodGet, "/workspaces/"+f.runtimeHTTP.receiver.String()+"/agent-executions"+query, "", headers)
	return code, out
}

func attemptsOf(t *testing.T, out map[string]any, i int) []map[string]any {
	t.Helper()
	items, _ := out["items"].([]any)
	require.Greater(t, len(items), i)
	raw, _ := items[i].(map[string]any)["attempts"].([]any)
	res := make([]map[string]any, 0, len(raw))
	for _, a := range raw {
		res = append(res, a.(map[string]any))
	}
	return res
}

func (f *execFixture) storedAttempts() int {
	var n int
	require.NoError(f.t, f.db.Get(&n, `SELECT count(*) FROM runtime_execution_attempts WHERE workspace_id=$1`, f.runtimeHTTP.receiver))
	return n
}

func TestExecutionReportHTTPRoundTrip(t *testing.T) {
	f := newExecFixture(t)
	code, gen := f.register("boot-1")
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 1, gen)
	code, again := f.register("boot-1")
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 1, again, "same instance_id is idempotent and keeps its generation")

	now := time.Now()
	code, raw := f.report("boot-1", 1, 1, now.Add(-2*time.Second), execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-5 * time.Second)})
	require.Equal(t, http.StatusOK, code, raw)

	code, out := f.read("", f.receiver)
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 1, out["schema_version"])
	require.Contains(t, out, "next_cursor")
	require.EqualValues(t, 120, out["max_age_seconds"])
	item := out["items"].([]any)[0].(map[string]any)
	require.Equal(t, true, item["managed"])
	require.Equal(t, true, item["enabled"])
	require.Equal(t, "current", item["freshness"])
	routes := item["configured"].(map[string]any)["routes"].([]any)
	require.Equal(t, "preferred", routes[0].(map[string]any)["profile_ref"])
	require.Equal(t, "reserve", routes[1].(map[string]any)["profile_ref"])
	require.Nil(t, routes[0].(map[string]any)["effort"])
	at := attemptsOf(t, out, 0)[0]
	require.Equal(t, "model-a", at["observed"].(map[string]any)["model"])
	require.Equal(t, "current", at["freshness"])
	require.Equal(t, "agent_profile", item["profile"].(map[string]any)["source"])
}

func TestExecutionReportHTTPReplayAndSequence(t *testing.T) {
	f := newExecFixture(t)
	_, gen := f.register("boot-1")
	now := time.Now()
	a := execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-5 * time.Second)}
	code, raw := f.report("boot-1", gen, 5, now.Add(-4*time.Second), a)
	require.Equal(t, http.StatusOK, code, raw)
	code, raw = f.report("boot-1", gen, 5, now.Add(-4*time.Second), a)
	require.Equal(t, http.StatusOK, code, "same instance+sequence+body is idempotent")
	require.Contains(t, raw, `"duplicate":true`)
	changed := a
	changed.model = "model-b"
	code, _ = f.report("boot-1", gen, 5, now.Add(-4*time.Second), changed)
	require.Equal(t, http.StatusConflict, code, "changed replay")
	code, _ = f.report("boot-1", gen, 4, now.Add(-3*time.Second), a)
	require.Equal(t, http.StatusConflict, code, "lower sequence")
	code, _ = f.report("boot-1", gen, 6, now.Add(time.Hour), a)
	require.Equal(t, http.StatusBadRequest, code, "future reported_at")
	future := a
	future.observed = now.Add(time.Hour)
	code, _ = f.report("boot-1", gen, 6, now.Add(-time.Second), future)
	require.Equal(t, http.StatusBadRequest, code, "future observed_at")
}

func TestExecutionReportHTTPRestartFencing(t *testing.T) {
	f := newExecFixture(t)
	_, g1 := f.register("boot-1")
	now := time.Now()
	code, raw := f.report("boot-1", g1, 1, now.Add(-3*time.Second), execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-4 * time.Second)})
	require.Equal(t, http.StatusOK, code, raw)

	_, g2 := f.register("boot-2")
	require.Equal(t, g1+1, g2)
	code, _ = f.report("boot-1", g1, 2, now.Add(-2*time.Second), execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-3 * time.Second)})
	require.Equal(t, http.StatusConflict, code, "an old instance is fenced after a newer boot")
	code, _ = f.report("boot-2", g2, 1, now.Add(-time.Second), execAttempt{id: "a2", turn: "t9", state: "running", observed: now.Add(-time.Second)})
	require.Equal(t, http.StatusOK, code)

	_, out := f.read("", f.receiver)
	byID := map[string]map[string]any{}
	for _, a := range attemptsOf(t, out, 0) {
		byID[a["attempt_id"].(string)] = a
	}
	require.Equal(t, "stale", byID["a1"]["freshness"], "old generation is not current")
	require.Equal(t, "unknown", byID["a2"]["freshness"], "a new boot does not inherit the old model")
	require.Nil(t, byID["a2"]["observed"].(map[string]any)["model"])
}

func TestExecutionReportHTTPConcurrentBootsGetDistinctGenerations(t *testing.T) {
	f := newExecFixture(t)
	var wg sync.WaitGroup
	gens := make([]int64, 8)
	for i := range gens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, gens[i] = f.register(fmt.Sprintf("boot-%d", i))
		}()
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, g := range gens {
		require.Greater(t, g, int64(0))
		require.False(t, seen[g], "generation %d issued twice", g)
		seen[g] = true
	}
}

func TestExecutionReportHTTPConcurrentAttemptsAndResumeReset(t *testing.T) {
	f := newExecFixture(t)
	_, gen := f.register("boot-1")
	now := time.Now()
	obs := now.Add(-10 * time.Second)
	code, raw := f.report("boot-1", gen, 1, now.Add(-9*time.Second),
		execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: obs},
		execAttempt{id: "a2", turn: "t1", state: "running", model: "model-b", observed: obs})
	require.Equal(t, http.StatusOK, code, raw)
	_, out := f.read("", f.receiver)
	require.Len(t, attemptsOf(t, out, 0), 2, "concurrent attempts are both listed")

	// Resume = new turn: the old observation cannot be carried over.
	code, _ = f.report("boot-1", gen, 2, now.Add(-8*time.Second), execAttempt{id: "a1", turn: "t2", state: "running", model: "model-a", observed: obs})
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = f.report("boot-1", gen, 3, now.Add(-7*time.Second), execAttempt{id: "a1", turn: "t2", state: "running", observed: now.Add(-7 * time.Second)})
	require.Equal(t, http.StatusOK, code)
	_, out = f.read("", f.receiver)
	for _, a := range attemptsOf(t, out, 0) {
		if a["attempt_id"] == "a1" {
			require.Equal(t, "t2", a["turn_id"])
			require.Nil(t, a["observed"].(map[string]any)["model"], "model resets on resume")
			require.Equal(t, "unknown", a["freshness"])
		}
	}

	code, _ = f.report("boot-1", gen, 4, now.Add(-6*time.Second), execAttempt{id: "a1", turn: "t2", state: "completed", observed: now.Add(-6 * time.Second)})
	require.Equal(t, http.StatusOK, code)
	code, _ = f.report("boot-1", gen, 5, now.Add(-5*time.Second), execAttempt{id: "a1", turn: "t2", state: "running", observed: now.Add(-5 * time.Second)})
	require.Equal(t, http.StatusConflict, code, "a finished attempt does not run again")
}

func TestExecutionReportHTTPRejectsForeignAndSecretPayloads(t *testing.T) {
	f := newExecFixture(t)
	_, gen := f.register("boot-1")
	now := time.Now()
	good := execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-3 * time.Second)}

	foreignWS, foreignUser := seedRuntimeWorkspace(t, f.db)
	_ = foreignUser
	foreignAgent, _ := seedRuntimeAgent(t, f.db, foreignWS)
	foreignProject := f.newProject(foreignWS, foreignAgent, true)
	for name, project := range map[string]uuid.UUID{"foreign workspace project": foreignProject} {
		bad := good
		bad.project = project
		code, raw := f.report("boot-1", gen, 1, now.Add(-2*time.Second), good, bad)
		require.Equal(t, http.StatusForbidden, code, name+": "+raw)
		require.Zero(t, f.storedAttempts(), "an atomic batch stores nothing when one row is foreign")
	}

	for name, extra := range map[string]string{
		"top-level env":      `"env":{"TOKEN":"x"},`,
		"credential in body": `"credential_ref":"x",`,
	} {
		body := fmt.Sprintf(`{%s"schema_version":1,"instance_id":"boot-1","generation":%d,"sequence":1,"reported_at":%q,"attempts":[]}`, extra, gen, now.Format(time.RFC3339Nano))
		code, _, _ := f.do(http.MethodPost, f.execBase+"/execution-reports", body, f.reporterHdrs)
		require.Equal(t, http.StatusBadRequest, code, name)
	}
	body := fmt.Sprintf(`{"schema_version":1,"instance_id":"boot-1","generation":%d,"sequence":1,"reported_at":%q,"attempts":[{"workspace_id":%q,"prompt":"secret"}]}`, gen, now.Format(time.RFC3339Nano), f.runtimeHTTP.receiver)
	code, _, _ := f.do(http.MethodPost, f.execBase+"/execution-reports", body, f.reporterHdrs)
	require.Equal(t, http.StatusBadRequest, code, "nested unknown field")
	require.Zero(t, f.storedAttempts())

	// Write authority is the exact reporter key, independent of read rights.
	code, _ = f.report("boot-1", gen, 1, now.Add(-time.Second), good)
	require.Equal(t, http.StatusOK, code)
	forbidden := []map[string]string{f.owner, f.receiver, f.worker}
	for _, h := range forbidden {
		code, _, _ = f.do(http.MethodPost, f.execBase+"/execution-instances", `{"schema_version":1,"instance_id":"x"}`, h)
		require.Equal(t, http.StatusForbidden, code)
		code, _, _ = f.do(http.MethodPost, f.execBase+"/execution-reports", f.reportBody("boot-1", gen, 9, now.Add(-time.Second), good), h)
		require.Equal(t, http.StatusForbidden, code)
	}

	// A revoked reporter grant can no longer write.
	_, err := f.db.ExecContext(context.Background(), `UPDATE agent_workspace_grants SET revoked_at=now() WHERE agent_id=$1 AND workspace_id=$2`, f.reporter, f.runtimeHTTP.owner)
	require.NoError(t, err)
	code, _ = f.report("boot-1", gen, 2, now, good)
	require.Equal(t, http.StatusForbidden, code)
}

func TestAgentExecutionReadScope(t *testing.T) {
	f := newExecFixture(t)
	_, gen := f.register("boot-1")
	now := time.Now()
	code, raw := f.report("boot-1", gen, 1, now.Add(-time.Second), execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-2 * time.Second)})
	require.Equal(t, http.StatusOK, code, raw)

	// The owner-workspace admin is not a member of the receiver workspace.
	code, _ = f.read("", f.owner)
	require.Equal(t, http.StatusForbidden, code)
	// Read rights do not carry write rights.
	code, _, _ = f.do(http.MethodPost, f.execBase+"/execution-reports", f.reportBody("boot-1", gen, 9, now, execAttempt{id: "a1", turn: "t1", state: "running", observed: now.Add(-time.Second)}), f.receiver)
	require.Equal(t, http.StatusForbidden, code)

	// A plain workspace member sees only the projects they belong to.
	reader, readerUser := seedRuntimeWorkspace(t, f.db)
	_ = reader
	_, err := f.db.ExecContext(context.Background(), `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'member')`, uuid.New(), f.runtimeHTTP.receiver, readerUser)
	require.NoError(t, err)
	hdr := map[string]string{"X-Test-User": readerUser.String()}
	code, out := f.read("", hdr)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, out["items"], "a member of no project reads nothing")
	_, err = f.db.ExecContext(context.Background(), `INSERT INTO project_members (id, project_id, user_id, workspace_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.project, readerUser, f.runtimeHTTP.receiver)
	require.NoError(t, err)
	code, out = f.read("", hdr)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, out["items"], 1)

	// The bound agent reads its own scope only; a foreign project filter is 404.
	code, out = f.read("", f.worker)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, out["items"], 1)
	code, _ = f.read("?project_id="+uuid.NewString(), f.receiver)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = f.read("?limit=0", f.receiver)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = f.read("?limit=201", f.receiver)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = f.read("?cursor=nope", f.receiver)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestAgentExecutionUnmanagedAndDisabled(t *testing.T) {
	f := newExecFixture(t)
	_, gen := f.register("boot-1")
	now := time.Now()
	code, raw := f.report("boot-1", gen, 1, now.Add(-time.Second), execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-2 * time.Second)})
	require.Equal(t, http.StatusOK, code, raw)

	// Runner off for the project: unmanaged, history only, profile with source.
	_, err := f.db.ExecContext(context.Background(), `UPDATE project_integrations SET enabled=false WHERE project_id=$1`, f.project)
	require.NoError(t, err)
	code, out := f.read("?project_id="+f.project.String(), f.receiver)
	require.Equal(t, http.StatusOK, code)
	item := out["items"].([]any)[0].(map[string]any)
	require.Equal(t, false, item["managed"])
	require.Equal(t, "integration_disabled", item["reason"], "binding exists, project integration off")
	require.Equal(t, "agent_profile", item["profile"].(map[string]any)["source"])
	require.Nil(t, item["configured"])
	require.Equal(t, "history", attemptsOf(t, out, 0)[0]["freshness"], "kept attempts are history, not running")
	// A live attempt can no longer be reported for an unmanaged project.
	code, _ = f.report("boot-1", gen, 2, now, execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-time.Second)})
	require.Equal(t, http.StatusForbidden, code)
	// ...but a finished one can still be closed.
	code, _ = f.report("boot-1", gen, 3, now, execAttempt{id: "a1", turn: "t1", state: "stopped", observed: now.Add(-time.Second)})
	require.Equal(t, http.StatusOK, code)

	// Runner on again, binding disabled at the catalog admission: disabled.
	_, err = f.db.ExecContext(context.Background(), `UPDATE project_integrations SET enabled=true WHERE project_id=$1`, f.project)
	require.NoError(t, err)
	_, err = f.db.ExecContext(context.Background(), `UPDATE runtime_binding_admissions SET enabled=false WHERE workspace_id=$1`, f.runtimeHTTP.receiver)
	require.NoError(t, err)
	_, out = f.read("?project_id="+f.project.String(), f.receiver)
	item = out["items"].([]any)[0].(map[string]any)
	require.Equal(t, true, item["managed"])
	require.Equal(t, false, item["enabled"])
	require.Equal(t, "disabled", item["reason"])
}

func TestAgentExecutionPaginationKeepsEveryItem(t *testing.T) {
	f := newExecFixture(t)
	want := map[string]bool{f.project.String(): true}
	for i := 0; i < 2; i++ {
		want[f.newProject(f.runtimeHTTP.receiver, f.runtimeHTTP.worker, true).String()] = true
	}
	got := map[string]bool{}
	cursor := ""
	for page := 0; page < 5; page++ {
		q := "?limit=1"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		code, out := f.read(q, f.receiver)
		require.Equal(t, http.StatusOK, code)
		for _, it := range out["items"].([]any) {
			got[it.(map[string]any)["project_id"].(string)] = true
		}
		next, _ := out["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	require.Equal(t, want, got)
}

func TestExecutionTelemetryDoesNotTouchCatalogOrAdmission(t *testing.T) {
	f := newExecFixture(t)
	_, gen := f.register("boot-1")
	var before, after json.RawMessage
	require.NoError(t, f.db.Get(&before, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY binding_ref),'[]'::jsonb) FROM runtime_binding_admissions a WHERE workspace_id=$1`, f.runtimeHTTP.receiver))
	now := time.Now()
	code, raw := f.report("boot-1", gen, 1, now.Add(-time.Second), execAttempt{id: "a1", turn: "t1", state: "running", model: "model-a", observed: now.Add(-2 * time.Second)})
	require.Equal(t, http.StatusOK, code, raw)
	require.Contains(t, raw, `"launch_authorized":false`)
	require.NoError(t, f.db.Get(&after, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY binding_ref),'[]'::jsonb) FROM runtime_binding_admissions a WHERE workspace_id=$1`, f.runtimeHTTP.receiver))
	require.JSONEq(t, string(before), string(after))
}

func TestAgentExecutionHTTPRejectsMalformedRequests(t *testing.T) {
	f := newExecFixture(t)
	for _, q := range []string{"?project_id=nope", "?agent_id=nope", "?project_id=" + uuid.Nil.String(), "?limit=0", "?limit=abc", "?limit=500"} {
		code, _ := f.read(q, f.receiver)
		require.Equal(t, http.StatusBadRequest, code, q)
	}
	code, _, _ := f.do(http.MethodPost, f.execBase+"/execution-instances", `{"schema_version":1}`, f.reporterHdrs)
	require.Equal(t, http.StatusBadRequest, code, "instance_id is required")
	code, _, _ = f.do(http.MethodPost, f.execBase+"/execution-reports", `{"schema_version":1,"instance_id":"x"}`, f.reporterHdrs)
	require.Equal(t, http.StatusBadRequest, code, "an incomplete report is refused")
}

// policy.profile_models saved through the policy route comes back on the
// binding and becomes the route's model in the execution view; a profile
// without an override keeps its catalog model.
func TestExecutionViewTakesTheBindingProfileModel(t *testing.T) {
	f := newExecFixture(t)
	routesOf := func() []map[string]any {
		code, out := f.read("", f.receiver)
		require.Equal(t, http.StatusOK, code)
		items, _ := out["items"].([]any)
		require.NotEmpty(t, items)
		configured, _ := items[0].(map[string]any)["configured"].(map[string]any)
		require.NotNil(t, configured, "managed and admitted: configured is present")
		raw, _ := configured["routes"].([]any)
		routes := make([]map[string]any, 0, len(raw))
		for _, r := range raw {
			routes = append(routes, r.(map[string]any))
		}
		return routes
	}

	// Before: no override anywhere, every route says where its model came from.
	before := routesOf()
	require.Len(t, before, 2)
	for _, r := range before {
		require.Equal(t, "profile", r["model_source"], r)
		require.Equal(t, "model-a", r["model"], r)
	}

	policyURL := "/workspaces/" + f.runtimeHTTP.receiver.String() + "/runtime/bindings/worker-b/policy?resource_owner_workspace_id=" + f.runtimeHTTP.owner.String()
	policy := func(models string) string {
		return `{"if_revision":1,"policy":{"primary_profiles":["preferred"],"quota_edges":{"preferred":["reserve"]},"preferred_accounts":{},"api_reserve_providers":[],"max_attempts":2,"evidence_max_age_seconds":60,"qa":{"mode":"strict","quota_fallback":false},"profile_models":` + models + `}}`
	}
	// Red controls: a profile outside permitted_profiles, a model that is not one token.
	code, _, body := f.do(http.MethodPut, policyURL, policy(`{"no-such-profile":"claude-opus-5-5"}`), f.owner)
	require.Equal(t, http.StatusBadRequest, code, body)
	code, _, body = f.do(http.MethodPut, policyURL, policy(`{"preferred":"two words"}`), f.owner)
	require.Equal(t, http.StatusBadRequest, code, body)
	// A full catalog save names the offending field.
	var doc map[string]any
	require.NoError(t, json.Unmarshal(f.catalog, &doc))
	doc["bindings"].(map[string]any)["worker-b"].(map[string]any)["policy"].(map[string]any)["profile_models"] = map[string]any{"no-such-profile": "claude-opus-5-5"}
	bad, err := json.Marshal(doc)
	require.NoError(t, err)
	code, _, body = f.do(http.MethodPut, f.base, `{"if_revision":1,"enabled":true,"config":`+string(bad)+`}`, f.owner)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, `"config.bindings.worker-b.policy.profile_models":"not_permitted"`)

	code, view, body := f.do(http.MethodPut, policyURL, policy(`{"preferred":"claude-opus-5-5"}`), f.owner)
	require.Equal(t, http.StatusOK, code, body)
	saved := view["binding"].(map[string]any)["policy"].(map[string]any)
	require.Equal(t, map[string]any{"preferred": "claude-opus-5-5"}, saved["profile_models"])

	after := routesOf()
	require.Len(t, after, 2)
	byProfile := map[string]map[string]any{}
	for _, r := range after {
		byProfile[r["profile_ref"].(string)] = r
	}
	require.Equal(t, "claude-opus-5-5", byProfile["preferred"]["model"])
	require.Equal(t, "binding", byProfile["preferred"]["model_source"])
	require.Equal(t, "model-a", byProfile["reserve"]["model"])
	require.Equal(t, "profile", byProfile["reserve"]["model_source"])
}
