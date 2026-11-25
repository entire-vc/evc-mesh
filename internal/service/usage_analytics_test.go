package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// usageFixture: workspace with the runner integration, projects Shared (Q) and
// Hidden (P), a member of Shared, an agent in Shared, a second agent; model
// "u-split-<sfx>" priced 1/2/0.1/1.25/2 until 2026-10-04 and 2/4/0.2/2.5/4 from
// then; model "u-noprice-<sfx>" without a price. Period 2026-10-01..2026-10-08 UTC.
type usageFixture struct {
	ws, shared, hidden    uuid.UUID
	owner                 uuid.UUID
	member, agentA, agent uuid.UUID
	agentB                uuid.UUID
	split, noPrice        string
	priceV1, priceV2      uuid.UUID
}

var (
	usageFrom = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	usageTo   = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
)

type hourlyRow struct {
	hour                                                            time.Time
	agent, project                                                  uuid.UUID
	model, provider, account, billing                               string
	turns, tokensKnown, cacheKnown, durationKnown, costRepKnown     int
	tokensIn, tokensOut, cacheRead, cacheWrite, cacheWrite1h, durMs int64
	costReported                                                    float64
	nosplitIn, nosplitOut                                           int64
	nosplitCacheRead, nosplitCacheWrite, nosplitCacheWrite1h        int64
	nosplitCost                                                     float64
	nosplitCostKnown                                                int
}

func insertHourly(t *testing.T, db *sqlx.DB, ws uuid.UUID, r hourlyRow) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO agent_usage_hourly (workspace_id, hour, agent_id, project_id, model, provider, account_ref, billing_mode,
			turns, turns_tokens_known, turns_cache_known, turns_duration_known, turns_cost_reported_known,
			tokens_in, tokens_out, cache_read, cache_write, cache_write_1h, cost_reported, duration_ms,
			nosplit_tokens_in, nosplit_tokens_out, nosplit_cost_reported, nosplit_turns_cost_known, nosplit_cache_read, nosplit_cache_write, nosplit_cache_write_1h)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`,
		ws, r.hour, r.agent, r.project, r.model, r.provider, r.account, r.billing,
		r.turns, r.tokensKnown, r.cacheKnown, r.durationKnown, r.costRepKnown,
		r.tokensIn, r.tokensOut, r.cacheRead, r.cacheWrite, r.cacheWrite1h, r.costReported, r.durMs,
		r.nosplitIn, r.nosplitOut, r.nosplitCost, r.nosplitCostKnown, r.nosplitCacheRead, r.nosplitCacheWrite, r.nosplitCacheWrite1h)
	require.NoError(t, err)
}

func seedUsageFixture(t *testing.T, db *sqlx.DB, withRunner bool) usageFixture {
	t.Helper()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		_, err := db.Exec(q, args...)
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	f := usageFixture{ws: uuid.New(), shared: uuid.New(), hidden: uuid.New(), member: uuid.New(), agentA: uuid.New(), agentB: uuid.New(),
		priceV1: uuid.New(), priceV2: uuid.New()}
	f.agent = f.agentA
	sfx := f.ws.String()[:8]
	f.split, f.noPrice = "u-split-"+sfx, "u-noprice-"+sfx
	owner := uuid.New()
	f.owner = owner
	for _, u := range []uuid.UUID{owner, f.member} {
		exec(`INSERT INTO users (id, email, password_hash, display_name, username, is_active) VALUES ($1,$2,'x',$3,$4,true)`,
			u, u.String()+"@usage.test", "U "+u.String()[:6], "us-"+u.String()[:30])
	}
	exec(`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at) VALUES ($1,'Usage WS',$2,$3,'{}',$4,$4)`, f.ws, "usage-ws-"+sfx, owner, now)
	exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'member')`, uuid.New(), f.ws, f.member)
	for name, p := range map[string]uuid.UUID{"Shared": f.shared, "Hidden": f.hidden} {
		exec(`INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,'none','{}',$5,$5)`,
			p, f.ws, name+" usage", "usage-"+strings.ToLower(name)+"-"+sfx, now)
	}
	for i, a := range []uuid.UUID{f.agentA, f.agentB} {
		exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,$3,$4,'hash','agk_x')`,
			a, f.ws, fmt.Sprintf("Usage Agent %d", i), fmt.Sprintf("usage-agent-%d-%s", i, sfx))
	}
	exec(`INSERT INTO project_members (id, project_id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.shared, f.ws, f.member)
	exec(`INSERT INTO project_members (id, project_id, workspace_id, agent_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.shared, f.ws, f.agentA)
	if withRunner {
		exec(`INSERT INTO integration_configs (workspace_id, provider, is_active) VALUES ($1,'runner',true)`, f.ws)
	}
	exec(`INSERT INTO model_prices (id, model, provider, valid_from, input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok, cache_write_1h_per_mtok, source_url)
		VALUES ($1,$2,'anthropic','2026-09-01T00:00:00Z',1,2,0.1,1.25,2,'https://example.test/v1'),
		       ($3,$2,'anthropic','2026-10-04T00:00:00Z',2,4,0.2,2.5,4,'https://example.test/v2')`, f.priceV1, f.split, f.priceV2)

	nilP := uuid.Nil
	// r1: 2 split turns in Shared, price v1: 0.5·1 + 0.4·0.1 + 0.05·1.25 + 0.05·2 + 0.1·2 = 0.9025
	insertHourly(t, db, f.ws, hourlyRow{hour: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), agent: f.agentA, project: f.shared,
		model: f.split, provider: "anthropic", account: "claude/bob-team", billing: "subscription",
		turns: 2, tokensKnown: 2, cacheKnown: 2, durationKnown: 2, costRepKnown: 2,
		tokensIn: 1_000_000, tokensOut: 100_000, cacheRead: 400_000, cacheWrite: 100_000, cacheWrite1h: 50_000, durMs: 60_000, costReported: 9.99})
	// r2: the same after the price change, price v2: 1.805
	insertHourly(t, db, f.ws, hourlyRow{hour: time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC), agent: f.agentA, project: f.shared,
		model: f.split, provider: "anthropic", account: "claude/bob-team", billing: "subscription",
		turns: 2, tokensKnown: 2, cacheKnown: 2, durationKnown: 2, costRepKnown: 2,
		tokensIn: 1_000_000, tokensOut: 100_000, cacheRead: 400_000, cacheWrite: 100_000, cacheWrite1h: 50_000, durMs: 60_000, costReported: 9.99})
	// r3: 1 split turn in Hidden: 2.0
	insertHourly(t, db, f.ws, hourlyRow{hour: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), agent: f.agentA, project: f.hidden,
		model: f.split, provider: "anthropic", turns: 1, tokensKnown: 1, cacheKnown: 1, tokensIn: 2_000_000})
	// r4: 1 split turn without a card: 1.0
	insertHourly(t, db, f.ws, hourlyRow{hour: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), agent: f.agentB, project: nilP,
		model: f.split, provider: "anthropic", turns: 1, tokensKnown: 1, cacheKnown: 1, tokensIn: 1_000_000})
	// r5: 3 split turns of a model without a price: no sum
	insertHourly(t, db, f.ws, hourlyRow{hour: time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC), agent: f.agentA, project: f.shared,
		model: f.noPrice, provider: "openai", account: "codex/alex", billing: "subscription",
		turns: 3, tokensKnown: 3, cacheKnown: 3, tokensIn: 300, tokensOut: 30})
	// r6: 3 turns without a cache split, one reported $0.5, two with nothing: 0.5 and 2 unknown
	insertHourly(t, db, f.ws, hourlyRow{hour: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC), agent: f.agentA, project: f.shared,
		model: f.split, provider: "anthropic", turns: 3, tokensKnown: 3, costRepKnown: 1,
		tokensIn: 3000, tokensOut: 300, costReported: 0.5, nosplitIn: 3000, nosplitOut: 300, nosplitCost: 0.5, nosplitCostKnown: 1})
	// r7: outside the period.
	insertHourly(t, db, f.ws, hourlyRow{hour: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC), agent: f.agentA, project: f.shared,
		model: f.split, provider: "anthropic", turns: 5, tokensKnown: 5, cacheKnown: 5, tokensIn: 5_000_000})
	return f
}

func usageQ(f usageFixture, v AnalyticsViewer, metric, groupBy string) UsageQuery {
	return UsageQuery{WorkspaceID: f.ws, Viewer: v, Metric: metric, GroupBy: groupBy, From: usageFrom, To: usageTo,
		Step: UsageStepDay, Loc: time.UTC, Top: 8}
}

func admin() AnalyticsViewer { return AnalyticsViewer{ActorID: uuid.New(), AllProjects: true} }

func seriesSum(res *UsageResult) float64 {
	var s float64
	for _, sr := range res.Series {
		for _, v := range sr.Values {
			if v != nil {
				s += *v
			}
		}
	}
	return s
}

func rowByKey(res *UsageResult, key string) *UsageRow {
	for i := range res.Rows {
		if res.Rows[i].Key == key {
			return &res.Rows[i]
		}
	}
	return nil
}

func rowKeys(res *UsageResult) []string {
	var out []string
	for _, r := range res.Rows {
		out = append(out, r.Key)
	}
	sort.Strings(out)
	return out
}

func requireUsageStatus(t *testing.T, err error, status int) {
	t.Helper()
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "want apierror, got %v", err)
	require.Equal(t, status, ae.Code)
}

func TestUsage_CostPricingAndCoverage(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	ctx := context.Background()

	res, err := svc.GetUsage(ctx, usageQ(f, admin(), UsageMetricCost, UsageDimModel))
	require.NoError(t, err)

	// 0.9025 + 1.805 + 2.0 + 1.0 + 0.5; r7 is outside the period.
	require.NotNil(t, res.Totals.Value)
	assert.InDelta(t, 6.2075, *res.Totals.Value, 1e-9)
	assert.Equal(t, int64(12), res.Totals.Turns)
	// 3 turns of the unpriced model + 2 no-split turns without a reported cost.
	assert.Equal(t, int64(5), res.Totals.TurnsUnknown)
	assert.InDelta(t, *res.Totals.Value, seriesSum(res), 1e-9, "sum of series = totals")
	require.Len(t, res.Buckets, 7)

	split := rowByKey(res, f.split)
	require.NotNil(t, split)
	assert.InDelta(t, 6.2075, *split.Value, 1e-9)
	assert.Equal(t, "mixed", *split.CostBasis)
	assert.Equal(t, int64(2), split.TurnsUnknown)

	// A model without a price: in no_price_models and in coverage, never 0.
	np := rowByKey(res, f.noPrice)
	require.NotNil(t, np)
	assert.Nil(t, np.Value, "unpriced model has no sum, not 0")
	assert.Equal(t, int64(3), np.Turns)
	assert.Equal(t, int64(3), np.TurnsUnknown)
	require.NotNil(t, res.Pricing)
	assert.Equal(t, []string{f.noPrice}, res.Pricing.NoPriceModels)

	// The price changed mid-period: both rows are listed.
	require.Len(t, res.Pricing.Models, 2)
	assert.Equal(t, "2026-09-01T00:00:00Z", res.Pricing.Models[0].ValidFrom)
	assert.Equal(t, "2026-10-04T00:00:00Z", res.Pricing.Models[1].ValidFrom)
	assert.Equal(t, 1.0, res.Pricing.Models[0].InputPerMTok)
	assert.Equal(t, 2.0, res.Pricing.Models[1].InputPerMTok)

	// Day 2026-10-06: the unpriced turns and the no-split turns, only $0.5 known.
	day6 := 5
	assert.Equal(t, UsageBucketCoverage{Turns: 6, TurnsKnown: 1}, res.Coverage.ByBucket[day6])
	var npSeries *UsageSeries
	for i := range res.Series {
		if res.Series[i].Key == f.noPrice {
			npSeries = &res.Series[i]
		}
	}
	require.NotNil(t, npSeries)
	assert.Nil(t, npSeries.Values[day6])
	assert.Equal(t, int64(3), npSeries.TurnsUnknown[day6])
}

func TestUsage_NoSplitWithoutReportedCostHasNoSum(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	q := usageQ(f, admin(), UsageMetricCost, UsageDimModel)
	q.From = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	q.To = q.From.Add(time.Hour)
	q.Step = UsageStepHour
	q.Models = []string{f.split}
	res, err := svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, *res.Totals.Value, 1e-9, "only the reported cost, the input is not priced at the input rate")
	assert.Equal(t, int64(3), res.Totals.Turns)
	assert.Equal(t, int64(2), res.Totals.TurnsUnknown)
	assert.Equal(t, "reported", *res.Totals.CostBasis)

	// Without the one reported cost the hour has no sum at all: null, not 0.
	_, err = db.Exec(`UPDATE agent_usage_hourly SET nosplit_turns_cost_known = 0, nosplit_cost_reported = 0, turns_cost_reported_known = 0, cost_reported = 0
		WHERE workspace_id = $1 AND hour = $2`, f.ws, q.From)
	require.NoError(t, err)
	res, err = svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	assert.Nil(t, res.Totals.Value)
	assert.Nil(t, res.Series[0].Values[0])
	assert.Equal(t, int64(3), res.Totals.TurnsUnknown)
}

func TestUsage_TokensSeriesEqualTotals(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	for _, g := range []string{UsageDimModel, UsageDimProvider, UsageDimAgent, UsageDimProject, UsageDimAccount} {
		for _, stack := range []string{"", UsageDimAgent} {
			if stack == g {
				continue
			}
			q := usageQ(f, admin(), UsageMetricTokens, g)
			q.Tokens = UsageTokensTotal
			q.StackBy = stack
			res, err := svc.GetUsage(context.Background(), q)
			require.NoError(t, err, g)
			// r1+r2: 2·1.1M, r3 2M, r4 1M, r5 330, r6 3300.
			want := 2*1_100_000.0 + 2_000_000 + 1_000_000 + 330 + 3300
			require.NotNil(t, res.Totals.Value)
			assert.Equal(t, want, *res.Totals.Value, g)
			assert.Equal(t, want, seriesSum(res), "series = totals for %s/%s", g, stack)
			var rows float64
			for _, r := range res.Rows {
				rows += *r.Value
			}
			assert.Equal(t, want, rows, "rows = totals for %s/%s", g, stack)
		}
	}

	// Cache read counts only split turns as known.
	q := usageQ(f, admin(), UsageMetricTokens, UsageDimModel)
	q.Tokens = UsageTokensCacheRead
	res, err := svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, 800_000.0, *res.Totals.Value)
	assert.Equal(t, int64(3), res.Totals.TurnsUnknown, "the three no-split turns")

	// An "account not given" row stays its own row.
	q = usageQ(f, admin(), UsageMetricTurns, UsageDimAccount)
	res, err = svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	none := rowByKey(res, "")
	require.NotNil(t, none)
	assert.Equal(t, "Unspecified", none.Label)
	assert.Equal(t, int64(5), none.Turns)
}

func TestUsage_ProjectFilterAndVisibility(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	ctx := context.Background()

	t.Run("project filter keeps only that project", func(t *testing.T) {
		q := usageQ(f, admin(), UsageMetricCost, UsageDimProject)
		q.ProjectIDs = []uuid.UUID{f.shared}
		res, err := svc.GetUsage(ctx, q)
		require.NoError(t, err)
		assert.Equal(t, []string{f.shared.String()}, rowKeys(res))
		assert.InDelta(t, 0.9025+1.805+0.5, *res.Totals.Value, 1e-9)
		assert.Equal(t, int64(10), res.Totals.Turns)
		assert.Equal(t, "Shared usage", res.Rows[0].Label)
		assert.Equal(t, &UsageLink{Kind: "project", ID: f.shared.String()}, res.Rows[0].Link)
	})

	t.Run("admin sees every project and the turns without a card", func(t *testing.T) {
		res, err := svc.GetUsage(ctx, usageQ(f, admin(), UsageMetricTurns, UsageDimProject))
		require.NoError(t, err)
		keys := []string{f.shared.String(), f.hidden.String(), uuid.Nil.String()}
		sort.Strings(keys)
		assert.Equal(t, keys, rowKeys(res))
		assert.Equal(t, "No card", rowByKey(res, uuid.Nil.String()).Label)
	})

	for name, v := range map[string]AnalyticsViewer{
		"member":       {ActorID: f.member},
		"member agent": {ActorID: f.agentA, Agent: true},
	} {
		t.Run(name+" sees only the projects it may open", func(t *testing.T) {
			for _, g := range []string{UsageDimProject, UsageDimModel, UsageDimAgent} {
				res, err := svc.GetUsage(ctx, usageQ(f, v, UsageMetricTurns, g))
				require.NoError(t, err)
				assert.Equal(t, 10.0, *res.Totals.Value, "only Shared: neither Hidden nor the turn without a card (%s)", g)
				assert.Equal(t, 10.0, seriesSum(res))
			}
			res, err := svc.GetUsage(ctx, usageQ(f, v, UsageMetricTurns, UsageDimProject))
			require.NoError(t, err)
			assert.Equal(t, []string{f.shared.String()}, rowKeys(res))

			q := usageQ(f, v, UsageMetricTurns, UsageDimProject)
			q.ProjectIDs = []uuid.UUID{f.hidden}
			_, err = svc.GetUsage(ctx, q)
			requireUsageStatus(t, err, http.StatusForbidden)
		})
	}
}

func TestUsage_AccountCutOnlyForAdminsWithRunner(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	ctx := context.Background()

	res, err := svc.GetUsage(ctx, usageQ(f, AnalyticsViewer{ActorID: f.owner, AllProjects: true}, UsageMetricTurns, UsageDimModel))
	require.NoError(t, err)
	assert.Contains(t, res.Capabilities.GroupBy, UsageDimAccount)
	assert.True(t, res.Capabilities.Cost)
	assert.True(t, res.Capabilities.Quota)
	res, err = svc.GetUsage(ctx, usageQ(f, admin(), UsageMetricTurns, UsageDimAccount))
	require.NoError(t, err)
	assert.Equal(t, &UsageLink{Kind: "account", ID: "claude/bob-team"}, rowByKey(res, "claude/bob-team").Link)

	for name, v := range map[string]AnalyticsViewer{
		"member": {ActorID: f.member},
		"agent":  {ActorID: f.agentA, Agent: true},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := svc.GetUsage(ctx, usageQ(f, v, UsageMetricTurns, UsageDimModel))
			require.NoError(t, err)
			assert.NotContains(t, res.Capabilities.GroupBy, UsageDimAccount)
			assert.False(t, res.Capabilities.Quota)

			_, err = svc.GetUsage(ctx, usageQ(f, v, UsageMetricTurns, UsageDimAccount))
			requireUsageStatus(t, err, http.StatusForbidden)
			q := usageQ(f, v, UsageMetricTurns, UsageDimModel)
			q.StackBy = UsageDimAccount
			_, err = svc.GetUsage(ctx, q)
			requireUsageStatus(t, err, http.StatusForbidden)
			q = usageQ(f, v, UsageMetricTurns, UsageDimModel)
			q.Accounts = []string{"claude/bob-team"}
			_, err = svc.GetUsage(ctx, q)
			requireUsageStatus(t, err, http.StatusForbidden)
		})
	}

	t.Run("admin of a workspace without the runner", func(t *testing.T) {
		g := seedUsageFixture(t, db, false)
		res, err := svc.GetUsage(ctx, usageQ(g, admin(), UsageMetricTurns, UsageDimModel))
		require.NoError(t, err)
		assert.NotContains(t, res.Capabilities.GroupBy, UsageDimAccount)
		_, err = svc.GetUsage(ctx, usageQ(g, admin(), UsageMetricTurns, UsageDimAccount))
		requireUsageStatus(t, err, http.StatusForbidden)
	})
}

func TestUsage_TopFoldsTheRestIntoOther(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	q := usageQ(f, admin(), UsageMetricTurns, UsageDimProject)
	q.Top = 1
	res, err := svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{UsageOtherKey, f.shared.String()}, rowKeys(res))
	assert.Equal(t, "Other", rowByKey(res, UsageOtherKey).Label)
	assert.Equal(t, int64(2), rowByKey(res, UsageOtherKey).Turns)
	assert.Equal(t, 12.0, seriesSum(res))
	q.Top = 2 // exactly top+1 groups still needs an explicit "Other" group
	res, err = svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	require.NotNil(t, rowByKey(res, UsageOtherKey))
	assert.Equal(t, int64(1), rowByKey(res, UsageOtherKey).Turns)
}

func TestUsage_InactiveRunnerDoesNotGrantAccounts(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	_, err := db.Exec(`UPDATE integration_configs SET is_active=false WHERE workspace_id=$1`, f.ws)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO integration_configs(workspace_id,provider,is_active) VALUES ($1,'agent_runtime',true)`, f.ws)
	require.NoError(t, err) // canonical disabled connection wins over legacy
	svc := &analyticsService{db: db}
	q := usageQ(f, admin(), UsageMetricTurns, UsageDimModel)
	res, err := svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	require.NotContains(t, res.Capabilities.GroupBy, UsageDimAccount)
	q.GroupBy = UsageDimAccount
	_, err = svc.GetUsage(context.Background(), q)
	requireUsageStatus(t, err, http.StatusForbidden)
}

func TestUsage_LegacyRunnerConnectionGrantsAccounts(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	_, err := db.Exec(`UPDATE integration_configs SET provider='agent_runtime' WHERE workspace_id=$1`, f.ws)
	require.NoError(t, err)
	res, err := (&analyticsService{db: db}).GetUsage(context.Background(), usageQ(f, admin(), UsageMetricTurns, UsageDimAccount))
	require.NoError(t, err)
	require.Contains(t, res.Capabilities.GroupBy, UsageDimAccount)
	require.EqualValues(t, 12, res.Totals.Turns)
}

func TestUsage_MixedSplitAndReportedInOneHour(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	hour := usageFrom.Add(2 * time.Hour)
	insertHourly(t, db, f.ws, hourlyRow{hour: hour, agent: f.agent, project: f.shared,
		model: f.split, provider: "anthropic", turns: 3, tokensKnown: 3, cacheKnown: 1,
		tokensIn: 1_003_000, tokensOut: 100_300, cacheRead: 400_000, cacheWrite: 100_000, cacheWrite1h: 50_000,
		costRepKnown: 1, costReported: 0.5, nosplitIn: 3000, nosplitOut: 300, nosplitCost: 0.5, nosplitCostKnown: 1})
	q := usageQ(f, admin(), UsageMetricCost, UsageDimModel)
	q.From, q.To, q.Step = hour, hour.Add(time.Hour), UsageStepHour
	res, err := (&analyticsService{db: db}).GetUsage(context.Background(), q)
	require.NoError(t, err)
	require.InDelta(t, 1.4025, *res.Totals.Value, 1e-9)
	require.EqualValues(t, 1, res.Totals.TurnsUnknown)
	require.Equal(t, "mixed", *res.Totals.CostBasis)
	// A reported cost does not require a known model, but missing tariffs still
	// appear in pricing metadata and no-split input must never be repriced.
	q.Models = []string{f.noPrice}
	insertHourly(t, db, f.ws, hourlyRow{hour: hour, agent: f.agent, project: f.shared,
		model: f.noPrice, provider: "unknown", turns: 1, tokensKnown: 1, tokensIn: 1000,
		costRepKnown: 1, costReported: 0.25, nosplitIn: 1000, nosplitCost: 0.25, nosplitCostKnown: 1})
	res, err = (&analyticsService{db: db}).GetUsage(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, 0.25, *res.Totals.Value)
	require.EqualValues(t, 0, res.Totals.TurnsUnknown)
	require.Equal(t, []string{f.noPrice}, res.Pricing.NoPriceModels)
}

func TestUsage_MissingCacheRateKeepsPriceableTurns(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	_, err := db.Exec(`UPDATE model_prices SET cache_write_per_mtok=NULL WHERE id=$1`, f.priceV1)
	require.NoError(t, err)
	hour := usageFrom.Add(3 * time.Hour)
	for _, account := range []string{"no-write", "with-write"} {
		r := hourlyRow{hour: hour, agent: f.agent, project: f.shared, model: f.split, provider: "anthropic",
			account: account, turns: 1, tokensKnown: 1, cacheKnown: 1, tokensIn: 1000, tokensOut: 100}
		if account == "with-write" {
			r.cacheWrite = 100
		}
		insertHourly(t, db, f.ws, r)
	}
	q := usageQ(f, admin(), UsageMetricCost, UsageDimModel)
	q.From, q.To, q.Step = hour, hour.Add(time.Hour), UsageStepHour
	res, err := (&analyticsService{db: db}).GetUsage(context.Background(), q)
	require.NoError(t, err)
	require.InDelta(t, 0.0012, *res.Totals.Value, 1e-9)
	require.EqualValues(t, 1, res.Totals.TurnsUnknown)
	require.Len(t, res.Pricing.Models, 1)
}

func TestUsage_CardsDone(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	for p, n := range map[uuid.UUID]int{f.shared: 2, f.hidden: 1} {
		open, done := uuid.New(), uuid.New()
		_, err := db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$3,'Open','open','#00ff00',0,'todo',true,'{}'),($2,$3,'Done','done','#0000ff',1,'done',false,'{}')`, open, done, p)
		require.NoError(t, err)
		for i := 0; i < n+1; i++ {
			status, completed := done, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			if i == n { // one open card, not counted
				status = open
			}
			_, err = db.Exec(`INSERT INTO tasks (id, project_id, status_id, title, description, assignee_type, assignee_id, priority, position, custom_fields, labels, task_number, created_by, created_by_type, created_at, updated_at, completed_at)
				VALUES ($1,$2,$3,'c','', 'agent',$4,'medium',0,'{}','{}',$5,$6,'user',$7,$7,$8)`,
				uuid.New(), p, status, f.agentA, i+1, uuid.New(), completed, completed)
			require.NoError(t, err)
		}
	}
	q := usageQ(f, admin(), UsageMetricCardsDone, UsageDimProject)
	res, err := svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, 3.0, *res.Totals.Value)
	assert.Nil(t, res.Pricing)

	q = usageQ(f, AnalyticsViewer{ActorID: f.member}, UsageMetricCardsDone, UsageDimAgent)
	res, err = svc.GetUsage(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, 2.0, *res.Totals.Value, "member sees only Shared cards")
	assert.Equal(t, "Usage Agent 0", res.Rows[0].Label)

	_, err = svc.GetUsage(context.Background(), usageQ(f, admin(), UsageMetricCardsDone, UsageDimModel))
	requireUsageStatus(t, err, http.StatusBadRequest)
}

func TestUsage_Validation(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	for name, mut := range map[string]func(*UsageQuery){
		"metric":         func(q *UsageQuery) { q.Metric = "money" },
		"group_by":       func(q *UsageQuery) { q.GroupBy = "" },
		"stack=group":    func(q *UsageQuery) { q.StackBy = q.GroupBy },
		"tokens":         func(q *UsageQuery) { q.Metric, q.Tokens = UsageMetricTokens, "cache" },
		"to before from": func(q *UsageQuery) { q.To = q.From },
	} {
		q := usageQ(f, admin(), UsageMetricTurns, UsageDimModel)
		mut(&q)
		_, err := svc.GetUsage(context.Background(), q)
		requireUsageStatus(t, err, http.StatusBadRequest)
		_ = name
	}
}

func TestResolveUsageStep(t *testing.T) {
	from := usageFrom
	assert.Equal(t, UsageStepHour, ResolveUsageStep("auto", from, from.Add(48*time.Hour)))
	assert.Equal(t, UsageStepDay, ResolveUsageStep("", from, from.Add(49*time.Hour)))
	assert.Equal(t, UsageStepDay, ResolveUsageStep("auto", from, from.AddDate(0, 0, 60)))
	assert.Equal(t, UsageStepWeek, ResolveUsageStep("auto", from, from.AddDate(0, 0, 61)))
	assert.Equal(t, UsageStepHour, ResolveUsageStep(UsageStepHour, from, from.AddDate(0, 0, 61)))
}

func TestUsageBuckets_ViewerZone(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)
	// 2026-10-01 00:00 UTC is 03:00 MSK: the first day bucket starts at MSK midnight.
	labels, _ := usageBuckets(usageFrom, usageFrom.Add(48*time.Hour), UsageStepDay, msk)
	assert.Equal(t, []string{"2026-10-01T00:00:00+03:00", "2026-10-02T00:00:00+03:00", "2026-10-03T00:00:00+03:00"}, labels)
	labels, _ = usageBuckets(usageFrom, usageFrom.AddDate(0, 0, 14), UsageStepWeek, time.UTC)
	assert.Equal(t, "2026-09-28T00:00:00Z", labels[0], "weeks start on Monday")
}

func TestUsage_HourBucketsAtDSTFold(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	from := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		insertHourly(t, db, f.ws, hourlyRow{hour: from.Add(time.Duration(i) * time.Hour), agent: f.agent,
			project: f.shared, model: f.split, provider: "anthropic", turns: 1})
	}
	q := usageQ(f, admin(), UsageMetricTurns, UsageDimModel)
	q.From, q.To, q.Step, q.Loc = from, from.Add(2*time.Hour), UsageStepHour, loc
	res, err := (&analyticsService{db: db}).GetUsage(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, []string{"2026-11-01T01:00:00-04:00", "2026-11-01T01:00:00-05:00"}, res.Buckets)
	require.Len(t, res.Series, 1)
	require.Equal(t, 1.0, *res.Series[0].Values[0])
	require.Equal(t, 1.0, *res.Series[0].Values[1])
	// Starting inside the second repeated hour must not add the first one.
	_, starts := usageBuckets(from.Add(90*time.Minute), from.Add(2*time.Hour), UsageStepHour, loc)
	require.Len(t, starts, 1)
	require.True(t, starts[0].Equal(from.Add(time.Hour)))
}

func TestUsage_PartialCacheFromJournalIsNotRepriced(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	hour := usageFrom.Add(4 * time.Hour)
	ids := []string{uuid.NewString(), uuid.NewString()}
	_, err := db.Exec(`INSERT INTO agent_usage_turns
		(id,workspace_id,agent_id,project_id,source,occurred_at,model,provider,tokens_in,tokens_out,cache_read,cache_write,cache_write_1h)
		VALUES ($1,$3,$4,$5,'turn',$6,$7,'anthropic',1000000,100000,400000,100000,50000),
		       ($2,$3,$4,$5,'turn',$6,$7,'anthropic',1000,NULL,300,200,100)`,
		ids[0], ids[1], f.ws, f.agent, f.shared, hour, f.split)
	require.NoError(t, err)
	_, err = db.Exec(`SELECT agent_usage_hourly_add($1::uuid[])`, pq.Array(ids))
	require.NoError(t, err)
	var partialRead int64
	require.NoError(t, db.Get(&partialRead, `SELECT nosplit_cache_read FROM agent_usage_hourly WHERE workspace_id=$1 AND hour=$2`, f.ws, hour))
	require.EqualValues(t, 300, partialRead)
	q := usageQ(f, admin(), UsageMetricCost, UsageDimModel)
	q.From, q.To, q.Step = hour, hour.Add(time.Hour), UsageStepHour
	res, err := (&analyticsService{db: db}).GetUsage(context.Background(), q)
	require.NoError(t, err)
	require.InDelta(t, 0.9025, *res.Totals.Value, 1e-9)
	require.EqualValues(t, 1, res.Totals.TurnsUnknown)
}

func TestUsage_QuotaCapabilityUsesRuntimeManagerRights(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)
	svc := &analyticsService{db: db}
	q := usageQ(f, AnalyticsViewer{ActorID: f.agent, Agent: true}, UsageMetricTurns, UsageDimModel)
	check := func(allowed bool) {
		t.Helper()
		res, err := svc.GetUsage(context.Background(), q)
		require.NoError(t, err)
		require.Equal(t, allowed, res.Capabilities.Quota)
		require.NotContains(t, res.Capabilities.GroupBy, UsageDimAccount)
	}
	check(false)
	_, err := db.Exec(`UPDATE agents SET operator_user_id=$1 WHERE id=$2`, f.owner, f.agent)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_runtime_managers(operator_user_id,agent_id) VALUES($1,$2)`, f.owner, f.agent)
	require.NoError(t, err)
	check(true)
	_, err = db.Exec(`UPDATE user_runtime_managers SET revoked_at=now() WHERE agent_id=$1`, f.agent)
	require.NoError(t, err)
	check(false)
}
