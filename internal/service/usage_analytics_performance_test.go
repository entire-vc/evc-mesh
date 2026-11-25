package service

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the complete read path over 90 days with 100 hourly account cuts
// (216,000 aggregate rows). The raw journal is deliberately unnecessary.
func TestUsagePerformance_HourlyOnly90Days(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	_, err := db.Exec(`DELETE FROM agent_usage_hourly WHERE workspace_id=$1`, f.ws)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_usage_hourly
		(workspace_id,hour,agent_id,project_id,model,provider,account_ref,billing_mode,
		turns,turns_tokens_known,turns_cache_known,tokens_in,tokens_out)
		SELECT $1,h,$2,$3,$4,'anthropic','account-' || a,'',1,1,1,1000,100
		FROM generate_series($5::timestamptz,$5::timestamptz + interval '90 days' - interval '1 hour',interval '1 hour') h
		CROSS JOIN generate_series(1,100) a`, f.ws, f.agent, f.shared, f.split, usageFrom)
	require.NoError(t, err)
	_, err = db.Exec(`ANALYZE agent_usage_hourly`)
	require.NoError(t, err)
	q := usageQ(f, admin(), UsageMetricCost, UsageDimModel)
	q.To = q.From.AddDate(0, 0, 90)
	query, args := usageCellsSQL(q)
	// EXPLAIN under the same local work_mem as selectUsageCells, so the plan
	// logged here is the plan the request runs.
	var planJSON []byte
	tx, err := db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec("SET LOCAL work_mem = '" + usageCellsWorkMem + "'")
	require.NoError(t, err)
	require.NoError(t, tx.Get(&planJSON, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...))
	require.NoError(t, tx.Rollback())
	var plan []struct {
		Plan          map[string]any `json:"Plan"`
		ExecutionTime float64        `json:"Execution Time"`
	}
	require.NoError(t, json.Unmarshal(planJSON, &plan))
	relations := map[string]bool{}
	var visit func(map[string]any)
	visit = func(node map[string]any) {
		if relation, ok := node["Relation Name"].(string); ok {
			relations[relation] = true
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				visit(child.(map[string]any))
			}
		}
	}
	require.Len(t, plan, 1)
	visit(plan[0].Plan)
	require.True(t, relations["agent_usage_hourly"], "positive control: hourly table must be read")
	require.False(t, relations["agent_usage_turns"])
	require.False(t, relations["agent_sessions"])
	t.Logf("EXPLAIN relations=%v execution=%.2fms", relations, plan[0].ExecutionTime)
	explain := time.Duration(plan[0].ExecutionTime * float64(time.Millisecond))
	// Hard ceiling on the server's own run of the plan: concurrent CI load
	// scales the client budget below, but the plan itself must stay fast.
	require.Less(t, explain, 1500*time.Millisecond, "hourly 90-day plan itself degraded")
	svc := &analyticsService{db: db}
	var elapsed []time.Duration
	for i := 0; i < 22; i++ {
		start := time.Now()
		res, err := svc.GetUsage(context.Background(), q)
		duration := time.Since(start)
		require.NoError(t, err)
		require.EqualValues(t, 216_000, res.Totals.Turns)
		require.InDelta(t, seriesSum(res), *res.Totals.Value, 1e-9)
		if i >= 2 {
			elapsed = append(elapsed, duration)
		}
	}
	sort.Slice(elapsed, func(i, j int) bool { return elapsed[i] < elapsed[j] })
	p95 := elapsed[18] // nearest-rank 95th percentile of 20 measured requests
	budget := usagePerfBudget(explain)
	t.Logf("90-day rows=216000 samples=20 p95=%s explain=%s budget=%s", p95, explain, budget)
	require.Less(t, p95, budget)
}

// usagePerfBudget bounds one measured p95 request time. The floor is the
// original absolute bar for a healthy database. The shared CI database runs
// the same plan anywhere from ~150ms to ~350ms depending on concurrent load —
// three red pipelines (!2019, !2030 x2) had the server's EXPLAIN ANALYZE
// alone above the fixed 300ms — so when the server itself measures slow, the
// budget scales with that measurement: no client-side time can beat the
// server's own execution. Plan shape is guarded by the relations assertions
// and the explain ceiling above, not by this budget.
func usagePerfBudget(explain time.Duration) time.Duration {
	const floor = 300 * time.Millisecond
	scaled := explain*3/2 + 50*time.Millisecond
	if scaled <= floor {
		return floor
	}
	return scaled
}

func TestUsagePerfBudget(t *testing.T) {
	for _, tc := range []struct {
		explain time.Duration
		want    time.Duration
	}{
		{0, 300 * time.Millisecond},                      // no server measurement: the floor
		{100 * time.Millisecond, 300 * time.Millisecond}, // healthy server: floor applies
		{166 * time.Millisecond, 300 * time.Millisecond}, // scaled just under the floor
		{332 * time.Millisecond, 548 * time.Millisecond}, // observed CI flake band
		{400 * time.Millisecond, 650 * time.Millisecond}, // loaded server: 1.5x + 50ms
	} {
		require.Equal(t, tc.want, usagePerfBudget(tc.explain), "explain=%s", tc.explain)
	}
}
