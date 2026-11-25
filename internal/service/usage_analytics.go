package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Agent usage analytics (GET /workspaces/:ws_id/analytics/usage): one metric over
// time, cut by one dimension and optionally stacked by a second one. Turn
// metrics read only the hourly aggregate agent_usage_hourly, never the turn
// journal or agent_sessions; cards_done reads tasks.
//
// Contract: a figure no turn reported is null, never 0. A turn without the
// figure counts in turns_unknown. The sum of all series equals totals.

// Usage metrics.
const (
	UsageMetricCost      = "cost"
	UsageMetricTokens    = "tokens"
	UsageMetricTurns     = "turns"
	UsageMetricDuration  = "duration"
	UsageMetricCardsDone = "cards_done"
)

// Token kinds of metric=tokens.
const (
	UsageTokensTotal      = "total"
	UsageTokensIn         = "in"
	UsageTokensOut        = "out"
	UsageTokensCacheRead  = "cache_read"
	UsageTokensCacheWrite = "cache_write"
)

// Usage dimensions (group_by / stack_by).
const (
	UsageDimModel     = "model"
	UsageDimProvider  = "provider"
	UsageDimAccount   = "account"
	UsageDimAgent     = "agent"
	UsageDimProject   = "project"
	UsageDimWorkspace = "workspace"
)

// Usage buckets.
const (
	UsageStepHour = "hour"
	UsageStepDay  = "day"
	UsageStepWeek = "week"
)

// UsageOtherKey is the key of the row that folds every group past top.
const UsageOtherKey = "__other__"

// usageMaxBuckets bounds the series length (an hourly step over months).
const usageMaxBuckets = 2000

var nilUUID = uuid.Nil.String()

// UsageQuery is one request of the usage analytics. From is inclusive, To
// exclusive; Step is resolved (hour, day or week); Loc is the viewer's zone.
type UsageQuery struct {
	WorkspaceID  uuid.UUID
	WorkspaceIDs []uuid.UUID
	UserScope    bool
	Viewer       AnalyticsViewer
	Metric       string
	Tokens       string
	GroupBy      string
	StackBy      string
	From, To     time.Time
	Step         string
	Loc          *time.Location
	ProjectIDs   []uuid.UUID
	AgentIDs     []uuid.UUID
	Models       []string
	Providers    []string
	Accounts     []string
	BillingModes []string
	Top          int
}

// UsageResult is the answer of §3.1 of the agent usage plan.
type UsageResult struct {
	Query        UsageQueryEcho    `json:"query"`
	Buckets      []string          `json:"buckets"`
	Series       []UsageSeries     `json:"series"`
	Rows         []UsageRow        `json:"rows"`
	Totals       UsageTotals       `json:"totals"`
	Pricing      *UsagePricing     `json:"pricing,omitempty"`
	Coverage     UsageCoverage     `json:"coverage"`
	Capabilities UsageCapabilities `json:"capabilities"`
}

// UsageQueryEcho repeats the resolved query.
type UsageQueryEcho struct {
	Metric  string  `json:"metric"`
	Tokens  *string `json:"tokens"`
	GroupBy string  `json:"group_by"`
	StackBy *string `json:"stack_by"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	Step    string  `json:"step"`
	TZ      string  `json:"tz"`
	Top     int     `json:"top"`
}

// UsageSeries is one line/stack of the chart: a value per bucket.
type UsageSeries struct {
	Key          string     `json:"key"`
	Label        string     `json:"label"`
	Values       []*float64 `json:"values"`
	TurnsUnknown []int64    `json:"turns_unknown"`
}

// UsageLink points a row at the entity it names.
type UsageLink struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// UsageRow is one group (and stack member) over the whole period.
type UsageRow struct {
	Key          string     `json:"key"`
	Label        string     `json:"label"`
	StackKey     *string    `json:"stack_key"`
	StackLabel   *string    `json:"stack_label,omitempty"`
	Value        *float64   `json:"value"`
	Share        *float64   `json:"share"`
	Turns        int64      `json:"turns"`
	TurnsUnknown int64      `json:"turns_unknown"`
	TokensIn     *int64     `json:"tokens_in"`
	TokensOut    *int64     `json:"tokens_out"`
	CacheRead    *int64     `json:"cache_read"`
	CacheWrite   *int64     `json:"cache_write"`
	Cost         *float64   `json:"cost,omitempty"`
	CostBasis    *string    `json:"cost_basis,omitempty"`
	DurationMs   *int64     `json:"duration_ms"`
	Link         *UsageLink `json:"link"`
}

// UsageTotals sums every row of the period.
type UsageTotals struct {
	Value        *float64 `json:"value"`
	Turns        int64    `json:"turns"`
	TurnsUnknown int64    `json:"turns_unknown"`
	TokensIn     *int64   `json:"tokens_in"`
	TokensOut    *int64   `json:"tokens_out"`
	CacheRead    *int64   `json:"cache_read"`
	CacheWrite   *int64   `json:"cache_write"`
	Cost         *float64 `json:"cost,omitempty"`
	CostBasis    *string  `json:"cost_basis,omitempty"`
	DurationMs   *int64   `json:"duration_ms"`
}

// UsagePrice is one model_prices row applied in the period.
type UsagePrice struct {
	Model               string   `json:"model"`
	Provider            string   `json:"provider"`
	ValidFrom           string   `json:"valid_from"`
	InputPerMTok        float64  `json:"input_per_mtok"`
	OutputPerMTok       float64  `json:"output_per_mtok"`
	CacheReadPerMTok    *float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok   *float64 `json:"cache_write_per_mtok"`
	CacheWrite1hPerMTok *float64 `json:"cache_write_1h_per_mtok"`
	Currency            string   `json:"currency"`
	SourceURL           string   `json:"source_url"`
}

// UsagePricing explains the "≈ $ by API price" figure.
type UsagePricing struct {
	Label         string       `json:"label"`
	Models        []UsagePrice `json:"models"`
	NoPriceModels []string     `json:"no_price_models"`
	BillingModes  []string     `json:"billing_modes"`
}

// UsageBucketCoverage is how many turns of a bucket carry the metric.
type UsageBucketCoverage struct {
	Turns      int64 `json:"turns"`
	TurnsKnown int64 `json:"turns_known"`
}

// UsageCoverage is the per-bucket share of turns that carry the metric.
type UsageCoverage struct {
	ByBucket              []UsageBucketCoverage `json:"by_bucket"`
	HistoryPrecisionUntil *string               `json:"history_precision_until"`
}

// UsageCapabilities tells the client which cuts this viewer may ask for.
type UsageCapabilities struct {
	GroupBy          []string `json:"group_by"`
	Cost             bool     `json:"cost"`
	Quota            bool     `json:"quota"`
	CardsDoneGroupBy []string `json:"cards_done_group_by"`
}

// CanSeeCost is the single server predicate behind every $ figure and the
// pricing block. v1: whoever may see analytics sees the cost too; this is the
// point to narrow it for partner workspaces.
func CanSeeCost(v AnalyticsViewer) bool { return true }

// usageRunnerPredicate uses the same canonical/legacy connection preference as RuntimeRepo.
func usageRunnerPredicate(wsCol string) string {
	return fmt.Sprintf(`COALESCE((SELECT is_active FROM integration_configs
		WHERE workspace_id = %s AND provider IN ('runner','agent_runtime')
		ORDER BY (provider='runner') DESC LIMIT 1), false)`, wsCol)
}

// usageRunnerEnabled uses the same canonical/legacy connection preference as RuntimeRepo.
func (s *analyticsService) usageRunnerEnabled(ctx context.Context, q UsageQuery) (bool, error) {
	var ok bool
	if q.UserScope {
		a := &sqlArgs{}
		access := usageUserAdminPredicate(a, q, "uw.id")
		err := s.db.GetContext(ctx, &ok, fmt.Sprintf(`SELECT EXISTS (
			SELECT 1 FROM workspaces uw WHERE %s AND %s)`, access, usageRunnerPredicate("uw.id")), a.vals...)
		return ok, err
	}
	err := s.db.GetContext(ctx, &ok, `SELECT `+usageRunnerPredicate("$1"), q.WorkspaceID)
	return ok, err
}

// ResolveUsageStep turns step=auto (or empty) into a concrete bucket: up to two
// days by hour, up to 60 days by day, longer by week.
func ResolveUsageStep(step string, from, to time.Time) string {
	if step != "" && step != "auto" {
		return step
	}
	span := to.Sub(from)
	switch {
	case span <= 48*time.Hour:
		return UsageStepHour
	case span <= 60*24*time.Hour:
		return UsageStepDay
	default:
		return UsageStepWeek
	}
}

func usageUserMemberPredicate(a *sqlArgs, q UsageQuery, wsCol string) string {
	actor := a.add(q.Viewer.ActorID)
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM workspaces uow WHERE uow.id = %s AND uow.deleted_at IS NULL
		AND (uow.owner_id = %s OR EXISTS (SELECT 1 FROM workspace_members uwm WHERE uwm.workspace_id = uow.id AND uwm.user_id = %s)))`, wsCol, actor, actor)
}

func usageUserAdminPredicate(a *sqlArgs, q UsageQuery, wsCol string) string {
	actor := a.add(q.Viewer.ActorID)
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM workspaces uow WHERE uow.id = %s AND uow.deleted_at IS NULL
		AND (uow.owner_id = %s OR EXISTS (SELECT 1 FROM workspace_members uwm WHERE uwm.workspace_id = uow.id AND uwm.user_id = %s AND uwm.role IN ('owner','admin'))))`, wsCol, actor, actor)
}

// usageUserAccess applies workspace membership and that workspace's own role;
// the account cut additionally requires an active runner and owner/admin role
// in that same workspace. A workspace filter is checked separately and applied.
func usageUserAccess(a *sqlArgs, q UsageQuery, wsCol, projectCol string, accountOnly bool) string {
	member := usageUserMemberPredicate(a, q, wsCol)
	admin := usageUserAdminPredicate(a, q, wsCol)
	visible := fmt.Sprintf(`(%s AND %s)`, admin, usageRunnerPredicate(wsCol))
	if !accountOnly {
		visible = fmt.Sprintf(`(%s OR (%s <> '%s'::uuid AND %s))`, admin, projectCol, nilUUID, visibleProject(a, q.Viewer, projectCol))
	}
	w := []string{member, visible}
	if len(q.WorkspaceIDs) > 0 {
		w = append(w, wsCol+" = ANY("+a.add(pq.Array(uuidStrings(q.WorkspaceIDs)))+"::uuid[])")
	}
	return strings.Join(w, " AND ")
}

func validUsageDim(d string) bool {
	switch d {
	case UsageDimModel, UsageDimProvider, UsageDimAccount, UsageDimAgent, UsageDimProject, UsageDimWorkspace:
		return true
	}
	return false
}

// GetUsage answers one usage analytics query for q.Viewer.
func (s *analyticsService) GetUsage(ctx context.Context, q UsageQuery) (*UsageResult, error) {
	if q.UserScope && q.Viewer.Agent {
		var operator *uuid.UUID
		if err := s.db.GetContext(ctx, &operator, `SELECT operator_user_id FROM agents WHERE id = $1 AND deleted_at IS NULL`, q.Viewer.ActorID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, apierror.Forbidden("agent operator required")
			}
			return nil, fmt.Errorf("usage agent operator: %w", err)
		}
		if operator == nil {
			return nil, apierror.Forbidden("agent operator required")
		}
		q.Viewer = AnalyticsViewer{ActorID: *operator}
	}
	if q.UserScope {
		q.Viewer.AllProjects = false
	}
	if q.Loc == nil {
		q.Loc = time.UTC
	}
	if q.Top <= 0 {
		q.Top = 8
	}
	switch q.Metric {
	case UsageMetricCost, UsageMetricTurns, UsageMetricDuration, UsageMetricCardsDone:
	case UsageMetricTokens:
		if q.Tokens == "" {
			q.Tokens = UsageTokensTotal
		}
		switch q.Tokens {
		case UsageTokensTotal, UsageTokensIn, UsageTokensOut, UsageTokensCacheRead, UsageTokensCacheWrite:
		default:
			return nil, apierror.BadRequest("invalid tokens; use total, in, out, cache_read or cache_write")
		}
	default:
		return nil, apierror.BadRequest("invalid metric; use cost, tokens, turns, duration or cards_done")
	}
	if !validUsageDim(q.GroupBy) {
		return nil, apierror.BadRequest("group_by is required: model, provider, account, agent, project or workspace")
	}
	if q.StackBy != "" && (!validUsageDim(q.StackBy) || q.StackBy == q.GroupBy) {
		return nil, apierror.BadRequest("invalid stack_by; use another dimension than group_by")
	}
	if !q.UserScope && (q.GroupBy == UsageDimWorkspace || q.StackBy == UsageDimWorkspace || len(q.WorkspaceIDs) > 0) {
		return nil, apierror.BadRequest("the workspace cut is available only at /me/analytics/usage")
	}
	if q.Metric == UsageMetricCardsDone {
		for _, d := range []string{q.GroupBy, q.StackBy} {
			if d != "" && d != UsageDimAgent && d != UsageDimProject && d != UsageDimWorkspace {
				return nil, apierror.BadRequest("cards_done is cut only by agent, project or workspace")
			}
		}
	}
	if !q.To.After(q.From) {
		return nil, apierror.BadRequest("'to' must be after 'from'")
	}
	switch q.Step {
	case UsageStepHour, UsageStepDay, UsageStepWeek:
	default:
		return nil, apierror.BadRequest("invalid step; use auto, hour, day or week")
	}

	seeCost := CanSeeCost(q.Viewer)
	if q.Metric == UsageMetricCost && !seeCost {
		return nil, apierror.Forbidden("cost is not available to this viewer")
	}
	runnerEnabled, err := s.usageRunnerEnabled(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("usage account access: %w", err)
	}
	seeAccounts := runnerEnabled && !q.Viewer.Agent && (q.UserScope || q.Viewer.AllProjects)
	var seeQuota bool
	if runnerEnabled && !q.UserScope {
		actor := postgres.RuntimeActor{UserID: q.Viewer.ActorID}
		if q.Viewer.Agent {
			actor.UserID, actor.AgentID = uuid.Nil, q.Viewer.ActorID
		}
		seeQuota, err = postgres.NewRuntimeRepo(s.db).CanSeeQuota(ctx, q.WorkspaceID, actor)
		if err != nil {
			return nil, fmt.Errorf("usage quota access: %w", err)
		}
	}
	if !seeAccounts && (q.GroupBy == UsageDimAccount || q.StackBy == UsageDimAccount || len(q.Accounts) > 0) {
		return nil, apierror.Forbidden("the account cut is available to workspace owners and admins of a workspace with the runner integration")
	}
	if q.UserScope && len(q.WorkspaceIDs) > 0 {
		if workspaceErr := s.checkUsageWorkspaces(ctx, q); workspaceErr != nil {
			return nil, workspaceErr
		}
	}
	if len(q.ProjectIDs) > 0 {
		if projectErr := s.checkUsageProjects(ctx, q); projectErr != nil {
			return nil, projectErr
		}
	}

	buckets, starts := usageBuckets(q.From, q.To, q.Step, q.Loc)
	if len(buckets) > usageMaxBuckets {
		return nil, apierror.BadRequest("too many buckets; use a larger step or a shorter period")
	}

	var cells []usageCell
	var prices []usagePriceRow
	var noPrice, billingModes []string
	if q.Metric == UsageMetricCardsDone {
		cells, err = s.cardsDoneCells(ctx, q)
	} else {
		cells, prices, noPrice, billingModes, err = s.turnCells(ctx, q)
	}
	if err != nil {
		return nil, fmt.Errorf("usage aggregate: %w", err)
	}

	res := buildUsageResult(q, buckets, starts, cells, seeCost)
	if err := s.labelUsage(ctx, q, res); err != nil {
		return nil, fmt.Errorf("usage labels: %w", err)
	}

	caps := UsageCapabilities{
		GroupBy:          []string{UsageDimModel, UsageDimProvider, UsageDimAgent, UsageDimProject},
		Cost:             seeCost,
		Quota:            seeQuota,
		CardsDoneGroupBy: []string{UsageDimAgent, UsageDimProject},
	}
	if q.UserScope {
		caps.GroupBy = append(caps.GroupBy, UsageDimWorkspace)
		caps.CardsDoneGroupBy = append(caps.CardsDoneGroupBy, UsageDimWorkspace)
	}
	if seeAccounts {
		var hasAccounts bool
		if q.UserScope {
			a := &sqlArgs{}
			access := usageUserAccess(a, q, "h.workspace_id", "h.project_id", true)
			if err := s.db.GetContext(ctx, &hasAccounts, fmt.Sprintf(`SELECT EXISTS (
				SELECT 1 FROM agent_usage_hourly h WHERE %s AND h.hour >= %s AND h.hour < %s AND h.account_ref <> '')`,
				access, a.add(q.From), a.add(q.To)), a.vals...); err != nil {
				return nil, fmt.Errorf("usage account rows: %w", err)
			}
		} else if err := s.db.GetContext(ctx, &hasAccounts,
			`SELECT EXISTS (SELECT 1 FROM agent_usage_hourly WHERE workspace_id = $1 AND hour >= $2 AND hour < $3 AND account_ref <> '')`,
			q.WorkspaceID, q.From, q.To); err != nil {
			return nil, fmt.Errorf("usage account rows: %w", err)
		}
		if hasAccounts {
			caps.GroupBy = append(caps.GroupBy, UsageDimAccount)
		}
	}
	res.Capabilities = caps

	if seeCost && q.Metric != UsageMetricCardsDone {
		res.Pricing = &UsagePricing{Label: "≈ $ at API price", Models: []UsagePrice{}, NoPriceModels: noPrice, BillingModes: billingModes}
		for _, p := range prices {
			res.Pricing.Models = append(res.Pricing.Models, p.public())
		}
	}
	return res, nil
}

// checkUsageProjects refuses a project filter naming a project that is not in
// the workspace or not visible to the viewer, as GetMetrics does.
func (s *analyticsService) checkUsageProjects(ctx context.Context, q UsageQuery) error {
	a := &sqlArgs{}
	ids := make([]string, len(q.ProjectIDs))
	for i, id := range q.ProjectIDs {
		ids[i] = id.String()
	}
	var n int
	var access, workspace string
	if q.UserScope {
		access = usageUserAccess(a, q, "p.workspace_id", "p.id", false)
		workspace = "TRUE"
	} else {
		access = visibleProject(a, q.Viewer, "p.id")
		workspace = "p.workspace_id = " + a.add(q.WorkspaceID)
	}
	qs := fmt.Sprintf(`SELECT COUNT(*) FROM projects p WHERE p.id = ANY(%s::uuid[]) AND %s AND %s`,
		a.add(pq.Array(ids)), workspace, access)
	if err := s.db.GetContext(ctx, &n, qs, a.vals...); err != nil {
		return fmt.Errorf("usage project access: %w", err)
	}
	if n != len(uniqueUUIDs(q.ProjectIDs)) {
		return apierror.Forbidden("not a member of this project")
	}
	return nil
}

func (s *analyticsService) checkUsageWorkspaces(ctx context.Context, q UsageQuery) error {
	a := &sqlArgs{}
	ids := uuidStrings(q.WorkspaceIDs)
	access := usageUserMemberPredicate(a, q, "w.id")
	var n int
	if err := s.db.GetContext(ctx, &n, fmt.Sprintf(`SELECT COUNT(*) FROM workspaces w WHERE w.id = ANY(%s::uuid[]) AND %s`, a.add(pq.Array(ids)), access), a.vals...); err != nil {
		return fmt.Errorf("usage workspace access: %w", err)
	}
	if n != len(uniqueUUIDs(q.WorkspaceIDs)) {
		return apierror.Forbidden("not a member of this workspace")
	}
	return nil
}

func uniqueUUIDs(ids []uuid.UUID) map[uuid.UUID]struct{} {
	m := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// usageBuckets lists the bucket starts covering [from, to) in loc.
func usageBuckets(from, to time.Time, step string, loc *time.Location) ([]string, []time.Time) {
	start := truncateLocal(from.In(loc), step)
	var labels []string
	var starts []time.Time
	for t := start; t.Before(to) && len(starts) <= usageMaxBuckets; t = nextBucket(t, step) {
		labels = append(labels, t.Format(time.RFC3339))
		starts = append(starts, t)
	}
	return labels, starts
}

func truncateLocal(t time.Time, step string) time.Time {
	y, m, d := t.Date()
	switch step {
	case UsageStepHour:
		// Preserve which occurrence of a repeated local hour this instant is in.
		return t.Add(-time.Duration(t.Minute())*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond()))
	case UsageStepWeek:
		day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
		offset := (int(day.Weekday()) + 6) % 7 // Monday = 0, as Postgres date_trunc('week')
		return day.AddDate(0, 0, -offset)
	default:
		return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	}
}

func nextBucket(t time.Time, step string) time.Time {
	switch step {
	case UsageStepHour:
		return t.Add(time.Hour)
	case UsageStepWeek:
		return t.AddDate(0, 0, 7)
	default:
		return t.AddDate(0, 0, 1)
	}
}

// usageCell is one (bucket, group, stack) aggregate. Each sum counts only
// known values; the *Known counters say how many turns carried them.
type usageCell struct {
	Bucket            time.Time      `db:"bucket"`
	GKey              string         `db:"gkey"`
	SKey              string         `db:"skey"`
	Turns             int64          `db:"turns"`
	TokensKnown       int64          `db:"tokens_known"`
	CacheKnown        int64          `db:"cache_known"`
	DurationKnown     int64          `db:"duration_known"`
	CostKnown         int64          `db:"cost_known"`
	CostPriceTurns    int64          `db:"cost_price_turns"`
	CostReportedTurns int64          `db:"cost_reported_turns"`
	TokensIn          int64          `db:"tokens_in"`
	TokensOut         int64          `db:"tokens_out"`
	CacheRead         int64          `db:"cache_read"`
	CacheWrite        int64          `db:"cache_write"`
	DurationMs        int64          `db:"duration_ms"`
	Cost              float64        `db:"cost"`
	PriceIDs          pq.StringArray `db:"price_ids"`
	NoPriceModels     pq.StringArray `db:"no_price_models"`
	BillingModes      pq.StringArray `db:"billing_modes"`
}

// usageDimExpr is the SQL key of a dimension over agent_usage_hourly h.
func usageDimExpr(d string) string {
	switch d {
	case UsageDimModel:
		return "h.model"
	case UsageDimProvider:
		return "h.provider"
	case UsageDimAccount:
		return "h.account_ref"
	case UsageDimAgent:
		return "h.agent_id::text"
	case UsageDimProject:
		return "h.project_id::text"
	case UsageDimWorkspace:
		return "h.workspace_id::text"
	}
	return "''"
}

// usageTurnFilter is the WHERE of agent_usage_hourly h: workspace, period,
// filters and the viewer's projects. A row without a card (nil project) is
// visible only to owner and admin.
func usageTurnFilter(a *sqlArgs, q UsageQuery) string {
	w := []string{
		"h.hour >= " + a.add(q.From),
		"h.hour < " + a.add(q.To),
	}
	if q.UserScope {
		accountOnly := q.GroupBy == UsageDimAccount || q.StackBy == UsageDimAccount || len(q.Accounts) > 0
		w = append(w, usageUserAccess(a, q, "h.workspace_id", "h.project_id", accountOnly))
	} else {
		w = append(w, "h.workspace_id = "+a.add(q.WorkspaceID))
		if !q.Viewer.AllProjects {
			w = append(w, "h.project_id <> '"+nilUUID+"'::uuid AND "+visibleProject(a, q.Viewer, "h.project_id"))
		}
	}
	if len(q.ProjectIDs) > 0 {
		w = append(w, "h.project_id = ANY("+a.add(pq.Array(uuidStrings(q.ProjectIDs)))+"::uuid[])")
	}
	if len(q.AgentIDs) > 0 {
		w = append(w, "h.agent_id = ANY("+a.add(pq.Array(uuidStrings(q.AgentIDs)))+"::uuid[])")
	}
	for col, vals := range map[string][]string{"h.model": q.Models, "h.provider": q.Providers, "h.account_ref": q.Accounts, "h.billing_mode": q.BillingModes} {
		if len(vals) > 0 {
			w = append(w, col+" = ANY("+a.add(pq.Array(vals))+"::text[])")
		}
	}
	return strings.Join(w, " AND ")
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// usagePriceRow is one model_prices row with the end of its validity.
type usagePriceRow struct {
	ID                  uuid.UUID `db:"id"`
	Model               string    `db:"model"`
	Provider            string    `db:"provider"`
	ValidFrom           time.Time `db:"valid_from"`
	InputPerMTok        float64   `db:"input_per_mtok"`
	OutputPerMTok       float64   `db:"output_per_mtok"`
	CacheReadPerMTok    *float64  `db:"cache_read_per_mtok"`
	CacheWritePerMTok   *float64  `db:"cache_write_per_mtok"`
	CacheWrite1hPerMTok *float64  `db:"cache_write_1h_per_mtok"`
	Currency            string    `db:"currency"`
	SourceURL           string    `db:"source_url"`
}

func (p usagePriceRow) public() UsagePrice {
	return UsagePrice{
		Model: p.Model, Provider: p.Provider, ValidFrom: p.ValidFrom.UTC().Format(time.RFC3339),
		InputPerMTok: p.InputPerMTok, OutputPerMTok: p.OutputPerMTok,
		CacheReadPerMTok: p.CacheReadPerMTok, CacheWritePerMTok: p.CacheWritePerMTok, CacheWrite1hPerMTok: p.CacheWrite1hPerMTok,
		Currency: p.Currency, SourceURL: p.SourceURL,
	}
}

// usageHourlySQL prices every hourly row of the filter (the price of its model
// with the greatest valid_from <= hour) and exposes, per row, the turns whose
// cost is known and that cost. Cost rules (§2.5 of the plan):
//   - turns with a cache split: (in − cache_read − cache_write)·p_in +
//     cache_read·p_cr + (cache_write − cache_write_1h)·p_cw + cache_write_1h·p_cw1h
//   - out·p_out, only when the model has a price and every cache rate the
//     hour needs; otherwise those turns have no sum;
//   - turns without a split: the harness-reported cost when it came, else no sum.
func usageHourlySQL(a *sqlArgs, q UsageQuery) string {
	// Collapse unused dimensions before the numeric price calculation. Cache
	// presence flags keep rows requiring an unavailable tariff separate from
	// rows whose zero cache usage can still be priced. Project visibility and
	// every request filter apply before this aggregation.
	sk := "''::text"
	if q.StackBy != "" {
		sk = usageDimExpr(q.StackBy)
	}
	return fmt.Sprintf(`
		WITH mp AS (
			SELECT m.*, LEAD(m.valid_from) OVER (PARTITION BY m.model ORDER BY m.valid_from) AS valid_to
			FROM model_prices m
		), h AS (
			SELECT h.hour, h.model, h.provider, h.billing_mode, %[1]s AS gkey, %[2]s AS skey,
			       SUM(h.turns) AS turns, SUM(h.turns_tokens_known) AS turns_tokens_known,
			       SUM(h.turns_cache_known) AS turns_cache_known, SUM(h.turns_duration_known) AS turns_duration_known,
			       SUM(h.tokens_in) AS tokens_in, SUM(h.tokens_out) AS tokens_out,
			       SUM(h.cache_read) AS cache_read, SUM(h.cache_write) AS cache_write, SUM(h.cache_write_1h) AS cache_write_1h,
			       SUM(h.duration_ms) AS duration_ms, SUM(h.nosplit_tokens_in) AS nosplit_tokens_in,
			       SUM(h.nosplit_tokens_out) AS nosplit_tokens_out, SUM(h.nosplit_cost_reported) AS nosplit_cost_reported,
			       SUM(h.nosplit_turns_cost_known) AS nosplit_turns_cost_known,
			       SUM(h.nosplit_cache_read) AS nosplit_cache_read, SUM(h.nosplit_cache_write) AS nosplit_cache_write,
			       SUM(h.nosplit_cache_write_1h) AS nosplit_cache_write_1h
			FROM agent_usage_hourly h WHERE %[3]s
			GROUP BY h.hour, h.model, h.provider, h.billing_mode, %[1]s, %[2]s,
			         (h.turns_cache_known > 0), (h.cache_read - h.nosplit_cache_read > 0),
			         (h.cache_write - h.nosplit_cache_write - h.cache_write_1h + h.nosplit_cache_write_1h > 0), (h.cache_write_1h - h.nosplit_cache_write_1h > 0)
		), r AS (
			SELECT h.*, mp.id AS price_id,
			       (h.turns_cache_known > 0 AND mp.id IS NOT NULL
			        AND (h.cache_read - h.nosplit_cache_read = 0 OR mp.cache_read_per_mtok IS NOT NULL)
			        AND (h.cache_write - h.nosplit_cache_write - h.cache_write_1h + h.nosplit_cache_write_1h = 0 OR mp.cache_write_per_mtok IS NOT NULL)
			        AND (h.cache_write_1h - h.nosplit_cache_write_1h = 0 OR mp.cache_write_1h_per_mtok IS NOT NULL)) AS priced,
			       ((h.tokens_in - h.nosplit_tokens_in - h.cache_read + h.nosplit_cache_read - h.cache_write + h.nosplit_cache_write)::numeric * mp.input_per_mtok
			        + (h.cache_read - h.nosplit_cache_read)::numeric * COALESCE(mp.cache_read_per_mtok, 0)
			        + (h.cache_write - h.nosplit_cache_write - h.cache_write_1h + h.nosplit_cache_write_1h)::numeric * COALESCE(mp.cache_write_per_mtok, 0)
			        + (h.cache_write_1h - h.nosplit_cache_write_1h)::numeric * COALESCE(mp.cache_write_1h_per_mtok, 0)
			        + (h.tokens_out - h.nosplit_tokens_out)::numeric * mp.output_per_mtok) / 1000000 AS price_cost
			FROM h
			LEFT JOIN mp ON mp.model = h.model AND mp.valid_from <= h.hour AND (mp.valid_to IS NULL OR h.hour < mp.valid_to)
		)`, usageDimExpr(q.GroupBy), sk, usageTurnFilter(a, q))
}

// usageBucketSQL follows the same bucket boundaries as usageBuckets.
func usageBucketSQL(a *sqlArgs, q UsageQuery, column string) string {
	if q.Step == UsageStepHour {
		// date_trunc in a zone merges the two occurrences at a DST fold.
		// Use the same elapsed-hour sequence as usageBuckets instead.
		origin := truncateLocal(q.From.In(q.Loc), UsageStepHour)
		return "date_bin('1 hour', " + column + ", " + a.add(origin) + "::timestamptz)"
	}
	return "date_trunc(" + a.add(q.Step) + ", " + column + ", " + a.add(q.Loc.String()) + ")"
}

// usageCellsSQL is the (bucket, group, stack) aggregate of the turn metrics.
func usageCellsSQL(q UsageQuery) (query string, args []interface{}) {
	a := &sqlArgs{}
	with := usageHourlySQL(a, q)
	bucket := usageBucketSQL(a, q, "r.hour")
	return with + fmt.Sprintf(`
		SELECT %s AS bucket,
		       r.gkey, r.skey,
		       SUM(r.turns) AS turns,
		       SUM(r.turns_tokens_known) AS tokens_known,
		       SUM(r.turns_cache_known) AS cache_known,
		       SUM(r.turns_duration_known) AS duration_known,
		       SUM(CASE WHEN r.priced THEN r.turns_cache_known ELSE 0 END + r.nosplit_turns_cost_known) AS cost_known,
		       SUM(CASE WHEN r.priced THEN r.turns_cache_known ELSE 0 END) AS cost_price_turns,
		       SUM(r.nosplit_turns_cost_known) AS cost_reported_turns,
		       SUM(r.tokens_in) AS tokens_in,
		       SUM(r.tokens_out) AS tokens_out,
		       SUM(r.cache_read) AS cache_read,
		       SUM(r.cache_write) AS cache_write,
		       SUM(r.duration_ms) AS duration_ms,
		       COALESCE(SUM(CASE WHEN r.priced THEN r.price_cost ELSE 0 END + r.nosplit_cost_reported), 0)::float8 AS cost,
		       COALESCE(array_agg(DISTINCT r.price_id::text) FILTER (WHERE r.priced), '{}') AS price_ids,
		       COALESCE(array_agg(DISTINCT r.model) FILTER (WHERE r.price_id IS NULL), '{}') AS no_price_models,
		       array_agg(DISTINCT r.billing_mode) AS billing_modes
		FROM r
		GROUP BY 1, 2, 3`, bucket), a.vals
}

// usageCellsWorkMem is the per-node memory the cells query may use. The
// hourly aggregate groups by computed expressions, so the planner
// overestimates the group count and, at the default work_mem, chooses a full
// incremental sort of every hourly row over a hash aggregate (90-day seed:
// ~240 ms vs ~100 ms, 9.7 MB actually used). A hash aggregate that outgrows
// it spills to disk, so the setting caps memory rather than failing.
const usageCellsWorkMem = "64MB"

// selectUsageCells runs the cells query in a read-only transaction with a
// local work_mem, so the setting never leaks to the pooled connection.
func (s *analyticsService) selectUsageCells(ctx context.Context, dest *[]usageCell, query string, args []interface{}) error {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET LOCAL work_mem = '"+usageCellsWorkMem+"'"); err != nil {
		return err
	}
	if err := tx.SelectContext(ctx, dest, query, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// turnCells aggregates agent_usage_hourly into (bucket, group, stack) cells and
// returns the prices applied, the models that had no price and the
// billing modes present.
func (s *analyticsService) turnCells(ctx context.Context, q UsageQuery) (cells []usageCell, prices []usagePriceRow, noPriceModels, billingModes []string, err error) {
	cellsQ, args := usageCellsSQL(q)
	if cellsErr := s.selectUsageCells(ctx, &cells, cellsQ, args); cellsErr != nil {
		return nil, nil, nil, nil, cellsErr
	}

	ids, missing, modes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range cells {
		for _, id := range c.PriceIDs {
			ids[id] = true
		}
		for _, model := range c.NoPriceModels {
			missing[model] = true
		}
		for _, mode := range c.BillingModes {
			modes[mode] = true
		}
	}
	keys := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for value := range set {
			out = append(out, value)
		}
		sort.Strings(out)
		return out
	}
	pricesQ := `
		SELECT m.id, m.model, m.provider, m.valid_from, m.input_per_mtok::float8 AS input_per_mtok, m.output_per_mtok::float8 AS output_per_mtok,
		       m.cache_read_per_mtok::float8 AS cache_read_per_mtok, m.cache_write_per_mtok::float8 AS cache_write_per_mtok,
		       m.cache_write_1h_per_mtok::float8 AS cache_write_1h_per_mtok, m.currency, m.source_url
		FROM model_prices m
		WHERE m.id = ANY($1::uuid[])
		ORDER BY m.model, m.valid_from`
	if pricesErr := s.db.SelectContext(ctx, &prices, pricesQ, pq.Array(keys(ids))); pricesErr != nil {
		return nil, nil, nil, nil, pricesErr
	}

	return cells, prices, keys(missing), keys(modes), nil
}

// cardsDoneCells counts cards closed as done in the period, by the agent that
// was their assignee or by project, within the viewer's projects.
func (s *analyticsService) cardsDoneCells(ctx context.Context, q UsageQuery) ([]usageCell, error) {
	a := &sqlArgs{}
	dim := func(d string) string {
		switch d {
		case UsageDimAgent:
			return "CASE WHEN t.assignee_type = 'agent' AND t.assignee_id IS NOT NULL THEN t.assignee_id::text ELSE '' END"
		case UsageDimProject:
			return "t.project_id::text"
		case UsageDimWorkspace:
			return "p.workspace_id::text"
		}
		return "''"
	}
	w := []string{
		"t.completed_at >= " + a.add(q.From),
		"t.completed_at < " + a.add(q.To),
		"ts.category = 'done'",
	}
	if q.UserScope {
		w = append(w, usageUserAccess(a, q, "p.workspace_id", "t.project_id", false))
	} else {
		w = append(w, "p.workspace_id = "+a.add(q.WorkspaceID), visibleProject(a, q.Viewer, "t.project_id"))
	}
	if len(q.ProjectIDs) > 0 {
		w = append(w, "t.project_id = ANY("+a.add(pq.Array(uuidStrings(q.ProjectIDs)))+"::uuid[])")
	}
	if len(q.AgentIDs) > 0 {
		w = append(w, "t.assignee_type = 'agent' AND t.assignee_id = ANY("+a.add(pq.Array(uuidStrings(q.AgentIDs)))+"::uuid[])")
	}
	cq := fmt.Sprintf(`
		SELECT %s AS bucket, %s AS gkey, %s AS skey,
		       COUNT(*) AS turns
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		JOIN task_statuses ts ON ts.id = t.status_id
		WHERE %s
		GROUP BY 1, 2, 3`, usageBucketSQL(a, q, "t.completed_at"), dim(q.GroupBy), dim(q.StackBy), strings.Join(w, " AND "))
	var cells []usageCell
	if err := s.db.SelectContext(ctx, &cells, cq, a.vals...); err != nil {
		return nil, err
	}
	return cells, nil
}

// usageAgg accumulates cells with the known counters kept.
type usageAgg struct {
	turns, tokensKnown, cacheKnown, durationKnown, costKnown int64
	costPriceTurns, costReportedTurns                        int64
	tokensIn, tokensOut, cacheRead, cacheWrite, durationMs   int64
	cost                                                     float64
}

func (g *usageAgg) add(c usageCell) {
	g.turns += c.Turns
	g.tokensKnown += c.TokensKnown
	g.cacheKnown += c.CacheKnown
	g.durationKnown += c.DurationKnown
	g.costKnown += c.CostKnown
	g.costPriceTurns += c.CostPriceTurns
	g.costReportedTurns += c.CostReportedTurns
	g.tokensIn += c.TokensIn
	g.tokensOut += c.TokensOut
	g.cacheRead += c.CacheRead
	g.cacheWrite += c.CacheWrite
	g.durationMs += c.DurationMs
	g.cost += c.Cost
}

func (g *usageAgg) addAgg(o *usageAgg) {
	g.add(usageCell{Turns: o.turns, TokensKnown: o.tokensKnown, CacheKnown: o.cacheKnown, DurationKnown: o.durationKnown,
		CostKnown: o.costKnown, CostPriceTurns: o.costPriceTurns, CostReportedTurns: o.costReportedTurns,
		TokensIn: o.tokensIn, TokensOut: o.tokensOut, CacheRead: o.cacheRead, CacheWrite: o.cacheWrite,
		DurationMs: o.durationMs, Cost: o.cost})
}

// metric is the value of the query metric and the number of turns that carry it.
func (g *usageAgg) metric(q UsageQuery) (value float64, known int64) {
	switch q.Metric {
	case UsageMetricCost:
		return g.cost, g.costKnown
	case UsageMetricTurns, UsageMetricCardsDone:
		return float64(g.turns), g.turns
	case UsageMetricDuration:
		return float64(g.durationMs), g.durationKnown
	}
	switch q.Tokens {
	case UsageTokensIn:
		return float64(g.tokensIn), g.tokensKnown
	case UsageTokensOut:
		return float64(g.tokensOut), g.tokensKnown
	case UsageTokensCacheRead:
		return float64(g.cacheRead), g.cacheKnown
	case UsageTokensCacheWrite:
		return float64(g.cacheWrite), g.cacheKnown
	}
	return float64(g.tokensIn + g.tokensOut), g.tokensKnown
}

// value is the metric, or nil when no turn carries it. Turn and card counts
// are never unknown: an empty bucket is 0.
func (g *usageAgg) value(q UsageQuery) *float64 {
	v, known := g.metric(q)
	if known == 0 && q.Metric != UsageMetricTurns && q.Metric != UsageMetricCardsDone {
		return nil
	}
	v = roundUsage(v)
	return &v
}

func (g *usageAgg) unknown(q UsageQuery) int64 {
	_, known := g.metric(q)
	return g.turns - known
}

// roundUsage keeps a dollar figure to 1e-6 so JSON stays stable.
func roundUsage(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func knownInt(v, known int64) *int64 {
	if known == 0 {
		return nil
	}
	return &v
}

func (g *usageAgg) costBasis() *string {
	var b string
	switch {
	case g.costPriceTurns > 0 && g.costReportedTurns > 0:
		b = "mixed"
	case g.costPriceTurns > 0:
		b = "price"
	case g.costReportedTurns > 0:
		b = "reported"
	default:
		return nil
	}
	return &b
}

func (g *usageAgg) row(q UsageQuery, seeCost bool) UsageRow {
	r := UsageRow{
		Value: g.value(q), Turns: g.turns, TurnsUnknown: g.unknown(q),
		TokensIn: knownInt(g.tokensIn, g.tokensKnown), TokensOut: knownInt(g.tokensOut, g.tokensKnown),
		CacheRead: knownInt(g.cacheRead, g.cacheKnown), CacheWrite: knownInt(g.cacheWrite, g.cacheKnown),
		DurationMs: knownInt(g.durationMs, g.durationKnown),
	}
	if q.Metric == UsageMetricCardsDone {
		r.TurnsUnknown = 0
	}
	if seeCost && q.Metric != UsageMetricCardsDone {
		if g.costKnown > 0 {
			c := roundUsage(g.cost)
			r.Cost = &c
		}
		r.CostBasis = g.costBasis()
	}
	return r
}

// topKeys orders the keys by the metric (then turns, then key) and folds all
// past top into UsageOtherKey.
func topKeys(q UsageQuery, by map[string]*usageAgg, top int) map[string]string {
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		vi, _ := by[keys[i]].metric(q)
		vj, _ := by[keys[j]].metric(q)
		if vi != vj {
			return vi > vj
		}
		if by[keys[i]].turns != by[keys[j]].turns {
			return by[keys[i]].turns > by[keys[j]].turns
		}
		return keys[i] < keys[j]
	})
	out := make(map[string]string, len(keys))
	for i, k := range keys {
		if i < top {
			out[k] = k
		} else {
			out[k] = UsageOtherKey
		}
	}
	return out
}

// buildUsageResult folds the cells into buckets, series, rows and totals.
// Series run along the stack dimension when there is one, else along the
// group; rows are per group (and stack member). Totals are the sum of the
// series in their order, so the client's sum of series equals totals.
func buildUsageResult(q UsageQuery, buckets []string, starts []time.Time, cells []usageCell, seeCost bool) *UsageResult {
	idx := make(map[time.Time]int, len(starts))
	for i, t := range starts {
		idx[t.UTC()] = i
	}
	bucketOf := func(c usageCell) int {
		// The SQL bucket is timestamptz; retain its offset through DST folds.
		if i, ok := idx[c.Bucket.UTC()]; ok {
			return i
		}
		return -1
	}

	byGroup := map[string]*usageAgg{}
	byStack := map[string]*usageAgg{}
	for _, c := range cells {
		if byGroup[c.GKey] == nil {
			byGroup[c.GKey] = &usageAgg{}
		}
		byGroup[c.GKey].add(c)
		if byStack[c.SKey] == nil {
			byStack[c.SKey] = &usageAgg{}
		}
		byStack[c.SKey].add(c)
	}
	gMap := topKeys(q, byGroup, q.Top)
	sMap := map[string]string{"": ""}
	if q.StackBy != "" {
		sMap = topKeys(q, byStack, q.Top)
	}

	seriesKey := func(c usageCell) string {
		if q.StackBy != "" {
			return sMap[c.SKey]
		}
		return gMap[c.GKey]
	}

	type rowKey struct{ g, s string }
	rows := map[rowKey]*usageAgg{}
	series := map[string][]usageAgg{}
	coverage := make([]usageAgg, len(starts))
	for _, c := range cells {
		bi := bucketOf(c)
		if bi < 0 {
			continue
		}
		rk := rowKey{gMap[c.GKey], sMap[c.SKey]}
		if rows[rk] == nil {
			rows[rk] = &usageAgg{}
		}
		rows[rk].add(c)
		sk := seriesKey(c)
		if series[sk] == nil {
			series[sk] = make([]usageAgg, len(starts))
		}
		series[sk][bi].add(c)
		coverage[bi].add(c)
	}

	res := &UsageResult{
		Query: UsageQueryEcho{
			Metric: q.Metric, GroupBy: q.GroupBy,
			From: q.From.In(q.Loc).Format(time.RFC3339), To: q.To.In(q.Loc).Format(time.RFC3339),
			Step: q.Step, TZ: q.Loc.String(), Top: q.Top,
		},
		Buckets: buckets,
		Series:  []UsageSeries{},
		Rows:    []UsageRow{},
	}
	if res.Buckets == nil {
		res.Buckets = []string{}
	}
	if q.Metric == UsageMetricTokens {
		t := q.Tokens
		res.Query.Tokens = &t
	}
	if q.StackBy != "" {
		s := q.StackBy
		res.Query.StackBy = &s
	}

	// Series in descending order of their total, "other" last.
	sKeys := make([]string, 0, len(series))
	sTotals := map[string]*usageAgg{}
	for k, vals := range series {
		sKeys = append(sKeys, k)
		t := &usageAgg{}
		for i := range vals {
			t.addAgg(&vals[i])
		}
		sTotals[k] = t
	}
	sortUsageKeys(q, sKeys, sTotals)

	var total usageAgg
	var totalValue float64
	var totalKnown int64
	for _, k := range sKeys {
		s := UsageSeries{Key: k, Values: make([]*float64, len(starts)), TurnsUnknown: make([]int64, len(starts))}
		for i := range series[k] {
			cell := &series[k][i]
			s.Values[i] = cell.value(q)
			if q.Metric != UsageMetricCardsDone {
				s.TurnsUnknown[i] = cell.unknown(q)
			}
			if s.Values[i] != nil {
				totalValue += *s.Values[i]
			}
			_, kn := cell.metric(q)
			totalKnown += kn
			total.addAgg(cell)
		}
		res.Series = append(res.Series, s)
	}

	// Rows sorted by value, "other" last.
	rKeys := make([]rowKey, 0, len(rows))
	for k := range rows {
		rKeys = append(rKeys, k)
	}
	sort.Slice(rKeys, func(i, j int) bool {
		a, b := rKeys[i], rKeys[j]
		if (a.g == UsageOtherKey) != (b.g == UsageOtherKey) {
			return b.g == UsageOtherKey
		}
		va, _ := rows[a].metric(q)
		vb, _ := rows[b].metric(q)
		if va != vb {
			return va > vb
		}
		if a.g != b.g {
			return a.g < b.g
		}
		return a.s < b.s
	})
	for _, k := range rKeys {
		r := rows[k].row(q, seeCost)
		r.Key = k.g
		if q.StackBy != "" {
			s := k.s
			r.StackKey = &s
		}
		if r.Value != nil && totalValue != 0 {
			sh := math.Round(*r.Value/totalValue*1000) / 1000
			r.Share = &sh
		}
		res.Rows = append(res.Rows, r)
	}

	tr := total.row(q, seeCost)
	res.Totals = UsageTotals{
		Turns: tr.Turns, TurnsUnknown: tr.TurnsUnknown, TokensIn: tr.TokensIn, TokensOut: tr.TokensOut,
		CacheRead: tr.CacheRead, CacheWrite: tr.CacheWrite, Cost: tr.Cost, CostBasis: tr.CostBasis, DurationMs: tr.DurationMs,
	}
	if totalKnown > 0 || q.Metric == UsageMetricTurns || q.Metric == UsageMetricCardsDone {
		v := totalValue
		res.Totals.Value = &v
	}

	res.Coverage.ByBucket = make([]UsageBucketCoverage, len(starts))
	for i := range coverage {
		_, kn := coverage[i].metric(q)
		res.Coverage.ByBucket[i] = UsageBucketCoverage{Turns: coverage[i].turns, TurnsKnown: kn}
	}
	return res
}

func sortUsageKeys(q UsageQuery, keys []string, by map[string]*usageAgg) {
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == UsageOtherKey) != (keys[j] == UsageOtherKey) {
			return keys[j] == UsageOtherKey
		}
		vi, _ := by[keys[i]].metric(q)
		vj, _ := by[keys[j]].metric(q)
		if vi != vj {
			return vi > vj
		}
		return keys[i] < keys[j]
	})
}

// labelUsage names series and rows: agents and projects by name, an empty
// account as "Unspecified", a turn without a card as "No card".
func (s *analyticsService) labelUsage(ctx context.Context, q UsageQuery, res *UsageResult) error {
	names := map[string]map[string]string{UsageDimAgent: {}, UsageDimProject: {}, UsageDimWorkspace: {}}
	want := map[string][]string{}
	collect := func(dim, key string) {
		if (dim == UsageDimAgent || dim == UsageDimProject || dim == UsageDimWorkspace) && key != "" && key != UsageOtherKey && key != nilUUID {
			want[dim] = append(want[dim], key)
		}
	}
	seriesDim := q.GroupBy
	if q.StackBy != "" {
		seriesDim = q.StackBy
	}
	for _, sr := range res.Series {
		collect(seriesDim, sr.Key)
	}
	for _, r := range res.Rows {
		collect(q.GroupBy, r.Key)
		if r.StackKey != nil {
			collect(q.StackBy, *r.StackKey)
		}
	}
	if ids := want[UsageDimAgent]; len(ids) > 0 {
		var rows []struct {
			ID   string `db:"id"`
			Name string `db:"name"`
		}
		if err := s.db.SelectContext(ctx, &rows, `SELECT id::text AS id, name FROM agents WHERE id = ANY($1::uuid[])`, pq.Array(ids)); err != nil {
			return err
		}
		for _, r := range rows {
			names[UsageDimAgent][r.ID] = r.Name
		}
	}
	if ids := want[UsageDimProject]; len(ids) > 0 {
		var rows []struct {
			ID   string `db:"id"`
			Name string `db:"name"`
		}
		query := `SELECT id::text AS id, name FROM projects WHERE id = ANY($1::uuid[]) AND workspace_id = $2`
		args := []interface{}{pq.Array(ids), q.WorkspaceID}
		if q.UserScope {
			query = `SELECT id::text AS id, name FROM projects WHERE id = ANY($1::uuid[])`
			args = args[:1]
		}
		if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
			return err
		}
		for _, r := range rows {
			names[UsageDimProject][r.ID] = r.Name
		}
	}
	if ids := want[UsageDimWorkspace]; len(ids) > 0 {
		var rows []struct {
			ID   string `db:"id"`
			Name string `db:"name"`
		}
		if err := s.db.SelectContext(ctx, &rows, `SELECT id::text AS id, name FROM workspaces WHERE id = ANY($1::uuid[])`, pq.Array(ids)); err != nil {
			return err
		}
		for _, r := range rows {
			names[UsageDimWorkspace][r.ID] = r.Name
		}
	}
	label := func(dim, key string) string {
		switch {
		case key == UsageOtherKey:
			return "Other"
		case dim == UsageDimProject && (key == nilUUID || key == ""):
			return "No card"
		case dim == UsageDimAgent && key == "":
			return "Not an agent"
		case key == "":
			return "Unspecified"
		}
		if n, ok := names[dim][key]; ok && n != "" {
			return n
		}
		return key
	}
	link := func(dim, key string) *UsageLink {
		if key == "" || key == UsageOtherKey || key == nilUUID {
			return nil
		}
		switch dim {
		case UsageDimAgent, UsageDimProject, UsageDimAccount, UsageDimWorkspace:
			return &UsageLink{Kind: dim, ID: key}
		}
		return nil
	}
	for i := range res.Series {
		res.Series[i].Label = label(seriesDim, res.Series[i].Key)
	}
	for i := range res.Rows {
		r := &res.Rows[i]
		r.Label = label(q.GroupBy, r.Key)
		r.Link = link(q.GroupBy, r.Key)
		if r.StackKey != nil {
			l := label(q.StackBy, *r.StackKey)
			r.StackLabel = &l
		}
	}
	return nil
}
