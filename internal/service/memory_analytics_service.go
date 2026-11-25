package service

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MemoryReasonNoData: the journal covers the window but holds no event this
// metric is a share of (e.g. no recall at all, so no hit rate).
const MemoryReasonNoData = "no_data"

// memoryWriteOps are memory_revisions actions plus project updates.
var memoryWriteOps = []string{"created", "updated", "forgotten", "archived", "unarchived", "project_update"}

// memoryAnalyticsService answers /analytics/memory from the daily aggregates
// (memory_stats_daily, memory_base_daily) for closed days and from the raw
// read journal (memory_read_events, memory_read_usage) for the current day.
type memoryAnalyticsService struct {
	db   memoryAnalyticsReader
	pool *sqlx.DB
}

// Both the pool and a read-only transaction support the same SQL reads. A
// drill-down uses the transaction so its timeout cannot leak to another request.
type memoryAnalyticsReader interface {
	GetContext(context.Context, interface{}, string, ...interface{}) error
	SelectContext(context.Context, interface{}, string, ...interface{}) error
}

// NewMemoryAnalyticsService creates the memory analytics service.
func NewMemoryAnalyticsService(db *sqlx.DB) MemoryAnalyticsService {
	return &memoryAnalyticsService{db: db, pool: db}
}

func (f MemoryAnalyticsFilter) now() time.Time {
	if f.Now.IsZero() {
		return time.Now().UTC()
	}
	return f.Now.UTC()
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// normalize aligns the window to whole UTC days, the grain of the aggregates:
// from goes back to its day start, a past to goes forward to its day end, and to
// never passes now.
func (f MemoryAnalyticsFilter) normalize() MemoryAnalyticsFilter {
	now := f.now()
	f.Now = now
	f.From = utcDay(f.From)
	if f.To.IsZero() || f.To.After(now) {
		f.To = now
	}
	if f.To.Before(utcDay(now)) && !f.To.Equal(utcDay(f.To)) {
		f.To = utcDay(f.To).AddDate(0, 0, 1)
	}
	return f
}

// readOpsSelected returns the read ops the filter keeps; ok=false when the op
// filter names only writes.
func (f MemoryAnalyticsFilter) readOpsSelected() ([]string, bool) {
	if len(f.Ops) == 0 || slices.Contains(f.Ops, "read") {
		return memoryReadOps, true
	}
	var ops []string
	for _, op := range f.Ops {
		if slices.Contains(memoryReadOps, op) {
			ops = append(ops, op)
		}
	}
	return ops, len(ops) > 0
}

func (f MemoryAnalyticsFilter) writeOpsSelected() ([]string, bool) {
	if len(f.Ops) == 0 || slices.Contains(f.Ops, "write") {
		return memoryWriteOps, true
	}
	var ops []string
	for _, op := range f.Ops {
		if slices.Contains(memoryWriteOps, op) {
			ops = append(ops, op)
		}
	}
	return ops, len(ops) > 0
}

func (f MemoryAnalyticsFilter) recordDimensions() bool {
	return f.GroupBy == "kind" || f.GroupBy == "scope" || f.GroupBy2 == "kind" || f.GroupBy2 == "scope"
}

// readReason is shared by the headline, daily and grouped metrics. It keeps an
// unavailable reading distinct from a measured zero.
func (f MemoryAnalyticsFilter) readReason(since *time.Time) string {
	if f.recordDimensions() {
		return MemoryReasonNotApplicable
	}
	if _, on := f.readOpsSelected(); !on {
		return MemoryReasonExcludedByFilter
	}
	if since == nil || !since.Before(f.To) {
		return MemoryReasonNoJournalBefore
	}
	return ""
}

// ValidateMemoryOp reports whether op is a known op filter value.
func ValidateMemoryOp(op string) bool {
	return op == "read" || op == "write" || slices.Contains(memoryReadOps, op) || slices.Contains(memoryWriteOps, op)
}

// ValidateMemoryGroupBy reports whether dim is a supported group_by value.
func ValidateMemoryGroupBy(dim string) bool { return slices.Contains(memoryGroupDims, dim) }

// visibleRowProject: a row with a project counts when the viewer sees the
// project; a row with no project only for owner/admin (same rule as /analytics).
func visibleRowProject(a *sqlArgs, v AnalyticsViewer, projCol string) string {
	if v.AllProjects {
		return "TRUE"
	}
	return fmt.Sprintf("(%s IS NOT NULL AND %s)", projCol, visibleProject(a, v, projCol))
}

// mayReadPrivate is the SQL predicate "the viewer may read the query text or
// the agent-scope key owned by the agent in agentCol": workspace owner/admin or
// that agent itself.
func mayReadPrivate(a *sqlArgs, v AnalyticsViewer, agentCol string) string {
	if v.AllProjects {
		return "TRUE"
	}
	if v.Agent {
		return fmt.Sprintf("(%s IS NOT DISTINCT FROM %s)", agentCol, a.add(v.ActorID))
	}
	return "FALSE"
}

// checkProject refuses a project of another workspace or one the viewer cannot see.
func (s *memoryAnalyticsService) checkProject(ctx context.Context, f MemoryAnalyticsFilter) error {
	if f.ProjectID == nil {
		return nil
	}
	var ok bool
	a := &sqlArgs{}
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM projects p WHERE p.id = %s AND p.workspace_id = %s AND %s)`,
		a.add(*f.ProjectID), a.add(f.WorkspaceID), visibleProject(a, f.Viewer, "p.id"))
	if err := s.db.GetContext(ctx, &ok, q, a.vals...); err != nil {
		return fmt.Errorf("memory analytics project access: %w", err)
	}
	if !ok {
		return apierror.Forbidden("not a member of this project")
	}
	return nil
}

// eventScope narrows read journal rows (alias e) to the workspace, filters and viewer.
func memoryEventScope(a *sqlArgs, f MemoryAnalyticsFilter, ops []string) string {
	w := []string{"e.workspace_id = " + a.add(f.WorkspaceID), visibleRowProject(a, f.Viewer, "e.project_id")}
	if f.ProjectID != nil {
		w = append(w, "e.project_id = "+a.add(*f.ProjectID))
	}
	if len(f.AgentIDs) > 0 {
		w = append(w, "e.agent_id = ANY("+a.add(pq.Array(f.AgentIDs))+"::uuid[])")
	}
	if len(f.CallerKinds) > 0 {
		w = append(w, "e.caller_kind = ANY("+a.add(pq.Array(f.CallerKinds))+"::text[])")
	}
	if len(f.Sources) > 0 {
		w = append(w, "EXISTS (SELECT 1 FROM jsonb_array_elements(e.results) r WHERE r->>'source' = ANY("+a.add(pq.Array(f.Sources))+"::text[]))")
	}
	if ops != nil {
		w = append(w, "e.op = ANY("+a.add(pq.Array(memoryRawOps(ops)))+"::text[])")
	}
	return strings.Join(w, " AND ")
}

// memoryStatsSource is the one place the read numbers come from: written closed
// days from memory_stats_daily; the open day and every closed day the nightly
// rollup has not written yet (no memory_daily_rollups.stats_written_at and no
// memory_stats_daily row of the workspace; MemoryDailyRollupJob writes a day
// not before MemoryRollupMissSettle after its end) from U4's shared rollup
// function over the raw journal, one day per call. Each day therefore comes
// from exactly one source, the same classification serves the nightly writer
// and this reader, and a day's reads stay the same once it is written (useful
// and misses may still settle until its recall usage is final).
func memoryStatsSource(a *sqlArgs, f MemoryAnalyticsFilter, ops []string) string {
	today := utcDay(f.Now)
	aggTo := f.To
	if aggTo.After(today) {
		aggTo = today
	}
	columns := `day, workspace_id, project_id, agent_id, caller_kind, op, source,
		reads, errors, hits, useful, misses, misses_reason, served_rows, served_superseded, response_chars`
	q := `SELECT ` + columns + ` FROM memory_stats_daily WHERE day >= ` + a.add(f.From) + `::date AND day < ` + a.add(aggTo) + `::date`
	if aggTo.After(f.From) {
		// Days start at the workspace's first journal row: earlier days have no
		// raw rows, and a workspace without a journal yields no days at all.
		ws := a.add(f.WorkspaceID)
		q += ` UNION ALL SELECT r.day, r.workspace_id, r.project_id, r.agent_id, r.caller_kind, r.op, r.source,
		r.reads, r.errors, r.hits, r.useful, r.misses, r.misses_reason, r.served_rows, r.served_superseded, r.response_chars
		FROM generate_series(
			(SELECT GREATEST(` + a.add(utcDay(f.From).Format(time.DateOnly)) + `::timestamp, date_trunc('day', e.ts AT TIME ZONE 'UTC'))
				FROM memory_read_events e WHERE e.workspace_id = ` + ws + ` ORDER BY e.ts LIMIT 1),
			` + a.add(aggTo.UTC().Format(time.DateOnly)) + `::timestamp - interval '1 day', interval '1 day') g(d)
		CROSS JOIN LATERAL memory_stats_rollup(g.d AT TIME ZONE 'UTC', (g.d + interval '1 day') AT TIME ZONE 'UTC') r
		WHERE NOT EXISTS (SELECT 1 FROM memory_daily_rollups m WHERE m.day = g.d::date AND m.stats_written_at IS NOT NULL)
		  AND NOT EXISTS (SELECT 1 FROM memory_stats_daily sd WHERE sd.workspace_id = ` + ws + ` AND sd.day = g.d::date)`
	}
	if f.To.After(today) {
		rawFrom := f.From
		if rawFrom.Before(today) {
			rawFrom = today
		}
		q += ` UNION ALL SELECT ` + columns + ` FROM memory_stats_rollup(` + a.add(rawFrom) + `::timestamptz, ` + a.add(f.To) + `::timestamptz)`
	}
	agg := []string{"s.workspace_id = " + a.add(f.WorkspaceID), visibleRowProject(a, f.Viewer, "s.project_id"),
		"s.op = ANY(" + a.add(pq.Array(ops)) + "::text[])"}
	if f.ProjectID != nil {
		agg = append(agg, "s.project_id = "+a.add(*f.ProjectID))
	}
	if len(f.AgentIDs) > 0 {
		agg = append(agg, "s.agent_id = ANY("+a.add(pq.Array(f.AgentIDs))+"::uuid[])")
	}
	if len(f.CallerKinds) > 0 {
		agg = append(agg, "s.caller_kind = ANY("+a.add(pq.Array(f.CallerKinds))+"::text[])")
	}
	if len(f.Sources) > 0 {
		agg = append(agg, "s.source = ANY("+a.add(pq.Array(f.Sources))+"::text[])")
	}
	return `(SELECT s.day::date AS day, s.project_id, s.agent_id, s.caller_kind, s.op, s.source,
		s.reads, s.errors, s.hits, s.useful, s.misses, s.misses_reason, s.served_rows, s.served_superseded, s.response_chars
		FROM (` + q + `) s WHERE ` + strings.Join(agg, " AND ") + `)`
}

// journalSince is when the read journal of the workspace starts: the earliest
// aggregated day or raw row. Nil when nothing was ever journaled.
func (s *memoryAnalyticsService) journalSince(ctx context.Context, ws uuid.UUID) (*time.Time, error) {
	var since sql.NullTime
	err := s.db.GetContext(ctx, &since, `SELECT LEAST(
		(SELECT day::timestamptz FROM memory_stats_daily WHERE workspace_id = $1 ORDER BY day LIMIT 1),
		(SELECT ts FROM memory_read_events WHERE workspace_id = $1 ORDER BY ts LIMIT 1))`, ws)
	if err != nil {
		return nil, fmt.Errorf("memory journal start: %w", err)
	}
	if !since.Valid {
		return nil, nil
	}
	t := since.Time.UTC()
	return &t, nil
}

type memoryTotalsRow struct {
	Reads       int64 `db:"reads"`
	RecallReads int64 `db:"recall_reads"`
	Hits        int64 `db:"hits"`
	Useful      int64 `db:"useful"`
	Misses      int64 `db:"misses"`
	// MissesReason is set when a counted recall row has no misses (a day the
	// rollup wrote before U7 stores 'not_collected'); NULL once all are counted.
	MissesReason     sql.NullString `db:"misses_reason"`
	ServedRows       int64          `db:"served_rows"`
	ServedSuperseded int64          `db:"served_superseded"`
	ResponseChars    int64          `db:"response_chars"`
	Unknown          int64          `db:"unknown_reads"`
}

// Recall rates default to agent tools, while volume and coverage retain all
// callers. An explicit caller filter overrides that rate default.
func memoryRecallPredicate(f MemoryAnalyticsFilter) string {
	p := `x.op IN ('recall','recall_with_graph')`
	if len(f.CallerKinds) == 0 {
		p += ` AND x.caller_kind = 'agent_tool'`
	}
	return p
}

// Selecting result sources removes the '*' call counters. Those rows cannot
// tell how many mixed-source recalls happened, so their recall rates are N/A.
func (f MemoryAnalyticsFilter) recallApplicable() bool {
	ops, on := f.readOpsSelected()
	return on && len(f.Sources) == 0 && (slices.Contains(ops, "recall") || slices.Contains(ops, "recall_with_graph"))
}

func memoryTotalsSelect(f MemoryAnalyticsFilter) string {
	p := memoryRecallPredicate(f)
	return `COALESCE(SUM(reads),0) AS reads,
	COALESCE(SUM(reads-errors) FILTER (WHERE ` + p + `),0) AS recall_reads,
	COALESCE(SUM(hits) FILTER (WHERE ` + p + `),0) AS hits,
	COALESCE(SUM(useful) FILTER (WHERE ` + p + `),0) AS useful,
	COALESCE(SUM(misses) FILTER (WHERE ` + p + `),0) AS misses,
	MIN(misses_reason) FILTER (WHERE ` + p + `) AS misses_reason,
	COALESCE(SUM(served_rows) FILTER (WHERE ` + p + `),0) AS served_rows,
	COALESCE(SUM(served_superseded) FILTER (WHERE ` + p + `),0) AS served_superseded,
	COALESCE(SUM(response_chars),0) AS response_chars,
	COALESCE(SUM(reads) FILTER (WHERE caller_kind = 'unknown'),0) AS unknown_reads`
}

func ratio(num, den int64) *float64 {
	if den == 0 {
		return nil
	}
	v := float64(num) / float64(den)
	return &v
}

func i64(v int64) *int64 { return &v }

// kpis fills the read and write KPIs of f into k; reasons get prefix+name keys.
func (s *memoryAnalyticsService) kpis(ctx context.Context, f MemoryAnalyticsFilter, since *time.Time, prefix string, reasons map[string]string) (MemoryKPIs, memoryTotalsRow, error) {
	var k MemoryKPIs
	var t memoryTotalsRow
	reason := func(name, r string) { reasons[prefix+name] = r }
	// Only the selected period's base snapshot is populated by quality below.
	reason("dead30", MemoryReasonNotCollected)

	readNames := []string{"reads", "hit_rate", "useful_rate", "miss_rate", "tokens_est", "superseded_served_rate"}
	ops, _ := f.readOpsSelected()
	switch r := f.readReason(since); r {
	case MemoryReasonNotApplicable, MemoryReasonExcludedByFilter, MemoryReasonNoJournalBefore:
		for _, n := range readNames {
			reason(n, r)
		}
	default:
		a := &sqlArgs{}
		q := `SELECT ` + memoryTotalsSelect(f) + ` FROM ` + memoryStatsSource(a, f, ops) + ` x`
		if err := s.db.GetContext(ctx, &t, q, a.vals...); err != nil {
			return k, t, fmt.Errorf("memory kpis: %w", err)
		}
		k.Reads = i64(t.Reads)
		k.TokensEst = i64(t.ResponseChars / 4)
		set := func(name string, dst **float64, v *float64) {
			if v == nil {
				reason(name, MemoryReasonNoData)
				return
			}
			*dst = v
		}
		set("superseded_served_rate", &k.SupersededServedRate, ratio(t.ServedSuperseded, t.ServedRows))
		if !f.recallApplicable() {
			for _, name := range []string{"hit_rate", "useful_rate", "miss_rate"} {
				reason(name, MemoryReasonNotApplicable)
			}
		} else {
			set("hit_rate", &k.HitRate, ratio(t.Hits, t.RecallReads))
			set("useful_rate", &k.UsefulRate, ratio(t.Useful, t.Hits))
			// One uncounted day makes the window's ratio partial: report its reason.
			if t.MissesReason.Valid {
				reason("miss_rate", t.MissesReason.String)
			} else {
				set("miss_rate", &k.MissRate, ratio(t.Misses, t.RecallReads))
			}
		}
	}

	wops, writesOn := f.writeOpsSelected()
	if !writesOn {
		reason("writes", MemoryReasonExcludedByFilter)
		reason("with_reason", MemoryReasonExcludedByFilter)
	} else {
		a := &sqlArgs{}
		q := `SELECT COUNT(*) AS writes, COUNT(*) FILTER (WHERE with_reason) AS with_reason FROM ` + memoryWritesSource(a, f, wops) + ` w`
		var n struct {
			Writes     int64 `db:"writes"`
			WithReason int64 `db:"with_reason"`
		}
		if err := s.db.GetContext(ctx, &n, q, a.vals...); err != nil {
			return k, t, fmt.Errorf("memory writes: %w", err)
		}
		k.Writes, k.WithReason = i64(n.Writes), i64(n.WithReason)
	}
	return k, t, nil
}

// memoryWritesSource reads the U4 view, including revisions whose memory was
// deleted. Kind belongs to the revision; scope is the current record scope.
func memoryWritesSource(a *sqlArgs, f MemoryAnalyticsFilter, ops []string) string {
	end := utcDay(f.To)
	if f.To.After(end) {
		end = end.AddDate(0, 0, 1)
	}
	w := []string{"w.workspace_id = " + a.add(f.WorkspaceID),
		"w.day >= " + a.add(f.From.UTC().Format("2006-01-02")) + "::date", "w.day < " + a.add(end.Format("2006-01-02")) + "::date",
		visibleRowProject(a, f.Viewer, "w.project_id"),
		"w.action = ANY(" + a.add(pq.Array(ops)) + "::text[])"}
	if f.ProjectID != nil {
		w = append(w, "w.project_id = "+a.add(*f.ProjectID))
	}
	if len(f.AgentIDs) > 0 {
		w = append(w, "w.agent_id = ANY("+a.add(pq.Array(f.AgentIDs))+"::uuid[])")
	}
	if len(f.Sources) > 0 {
		w = append(w, "CASE WHEN w.action = 'project_update' THEN 'project_update' ELSE 'memory' END = ANY("+a.add(pq.Array(f.Sources))+"::text[])")
	}
	return `(SELECT w.day, w.project_id, w.agent_id, w.action AS op, w.with_reason, w.kind, w.scope,
		CASE WHEN w.action = 'project_update' THEN 'project_update' ELSE 'memory' END AS source
		FROM memory_writes_daily w WHERE ` + strings.Join(w, " AND ") + ")"
}

// Get answers GET /analytics/memory.
func (s *memoryAnalyticsService) Get(ctx context.Context, f MemoryAnalyticsFilter) (*MemoryAnalytics, error) {
	f = f.normalize()
	if err := s.checkProject(ctx, f); err != nil {
		return nil, err
	}
	since, err := s.journalSince(ctx, f.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := &MemoryAnalytics{
		Period:  MemoryPeriod{From: f.From, To: f.To},
		Reasons: map[string]string{},
		Series:  []MemorySeriesPoint{},
		Rows:    []MemoryBreakdownRow{},
	}
	out.Coverage.JournalSince = since
	out.Coverage.CallerKind = []MemoryCallerKindRow{}

	var totals memoryTotalsRow
	if out.KPIs, totals, err = s.kpis(ctx, f, since, "", out.Reasons); err != nil {
		return nil, err
	}

	prev := f
	length := f.To.Sub(f.From)
	prev.To = f.From
	prev.From = utcDay(f.From.Add(-length))
	if f.PreviousFrom != nil && f.PreviousTo != nil {
		prev.From, prev.To = *f.PreviousFrom, *f.PreviousTo
		prev = prev.normalize()
	}
	out.PreviousPeriod = MemoryPeriod{From: prev.From, To: prev.To}
	if out.Previous, _, err = s.kpis(ctx, prev, since, "previous_kpis.", out.Reasons); err != nil {
		return nil, err
	}

	ops, _ := f.readOpsSelected()
	readReason := f.readReason(since)
	journalCovers := readReason == ""
	if journalCovers {
		if totals.Reads > 0 {
			out.Coverage.UnknownShare = ratio(totals.Unknown, totals.Reads)
		} else {
			out.Reasons["coverage.unknown_share"] = MemoryReasonNoData
		}
		a := &sqlArgs{}
		q := `SELECT caller_kind, SUM(reads)::bigint AS reads FROM ` + memoryStatsSource(a, f, ops) +
			` x GROUP BY caller_kind HAVING SUM(reads) > 0 ORDER BY reads DESC, caller_kind`
		if listErr := s.db.SelectContext(ctx, &out.Coverage.CallerKind, q, a.vals...); listErr != nil {
			return nil, fmt.Errorf("memory caller kinds: %w", listErr)
		}
	} else {
		out.Reasons["coverage.unknown_share"] = readReason
	}

	if out.Series, err = s.series(ctx, f, ops, since); err != nil {
		return nil, err
	}
	if out.Rows, err = s.rows(ctx, f, ops, readReason); err != nil {
		return nil, err
	}
	totalKey := "total"
	out.Totals = MemoryBreakdownRow{Key: &totalKey, Label: "total", Reads: out.KPIs.Reads,
		Writes: out.KPIs.Writes, WithReason: out.KPIs.WithReason,
		HitRate: out.KPIs.HitRate, UsefulRate: out.KPIs.UsefulRate, TokensEst: out.KPIs.TokensEst,
		Reasons: map[string]string{}}
	if journalCovers {
		out.Totals.ServedRows = i64(totals.ServedRows)
		out.Totals.SupersededServedRate = out.KPIs.SupersededServedRate
		if f.recallApplicable() {
			out.Totals.Hits, out.Totals.Useful = i64(totals.Hits), i64(totals.Useful)
		} else {
			out.Totals.Reasons["hits"], out.Totals.Reasons["useful"] = MemoryReasonNotApplicable, MemoryReasonNotApplicable
		}
	} else {
		for _, name := range []string{"reads", "hits", "useful", "hit_rate", "useful_rate", "tokens_est", "served_rows", "superseded_served_rate"} {
			out.Totals.Reasons[name] = readReason
		}
	}
	for _, name := range []string{"writes", "with_reason", "hit_rate", "useful_rate", "superseded_served_rate"} {
		if reason := out.Reasons[name]; reason != "" {
			out.Totals.Reasons[name] = reason
		}
	}

	if err := s.quality(ctx, f, since, out); err != nil {
		return nil, err
	}
	// Compare the historical dead-weight snapshot too, using its own eligible
	// denominator; current records cannot reconstruct a prior period's KPI.
	previous := &MemoryAnalytics{KPIs: out.Previous, Reasons: map[string]string{}}
	if err := s.quality(ctx, prev, since, previous); err != nil {
		return nil, err
	}
	out.Previous = previous.KPIs
	delete(out.Reasons, "previous_kpis.dead30")
	if reason := previous.Reasons["dead30"]; reason != "" {
		out.Reasons["previous_kpis.dead30"] = reason
	}
	return out, nil
}

type memorySeriesRow struct {
	Day        time.Time `db:"day"`
	Op         string    `db:"op"`
	Source     string    `db:"source"`
	Reads      int64     `db:"reads"`
	ServedRows int64     `db:"served_rows"`
	Hits       int64     `db:"hits"`
	Useful     int64     `db:"useful"`
	Writes     int64     `db:"writes"`
	WithReason int64     `db:"with_reason"`
}

func (p *MemorySeriesPoint) addReadStats(r memorySeriesRow) {
	*p.Reads += r.Reads
	*p.Hits += r.Hits
	*p.Useful += r.Useful
	p.ReadsByOp[r.Op] += r.Reads
	if r.Source != "*" && r.ServedRows > 0 {
		p.ServedRowsBySource[r.Source] += r.ServedRows
	}
}

// series zero-fills measured days only. Days before the journal carry nulls.
func (s *memoryAnalyticsService) series(ctx context.Context, f MemoryAnalyticsFilter, ops []string, since *time.Time) ([]MemorySeriesPoint, error) {
	byDay := map[string]*MemorySeriesPoint{}
	var days []string
	readReason := f.readReason(since)
	wops, writesOn := f.writeOpsSelected()
	for d := f.From; d.Before(f.To); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		days = append(days, k)
		p := &MemorySeriesPoint{Day: k, Reasons: map[string]string{}}
		r := readReason
		if r == "" && !d.AddDate(0, 0, 1).After(*since) {
			r = MemoryReasonNoJournalBefore
		}
		if r == "" {
			p.Reads, p.Hits, p.Useful = i64(0), i64(0), i64(0)
			p.ReadsByOp = map[string]int64{}
			for _, op := range ops {
				p.ReadsByOp[op] = 0
			}
			p.ServedRowsBySource = map[string]int64{"memory": 0, "project_knowledge": 0, "doc": 0, "unknown": 0}
		} else {
			for _, name := range []string{"reads", "hits", "useful", "reads_by_op", "served_rows_by_source", "useful_rate"} {
				p.Reasons[name] = r
			}
		}
		if writesOn {
			p.Writes, p.WithReason = i64(0), i64(0)
		} else {
			p.Reasons["writes"], p.Reasons["with_reason"] = MemoryReasonExcludedByFilter, MemoryReasonExcludedByFilter
		}
		byDay[k] = p
	}
	if readReason == "" {
		a := &sqlArgs{}
		q := `SELECT day, op, source, SUM(reads)::bigint AS reads, SUM(served_rows)::bigint AS served_rows,
			COALESCE(SUM(hits) FILTER (WHERE ` + memoryRecallPredicate(f) + `),0)::bigint AS hits,
			COALESCE(SUM(useful) FILTER (WHERE ` + memoryRecallPredicate(f) + `),0)::bigint AS useful, 0::bigint AS writes
			FROM ` + memoryStatsSource(a, f, ops) + ` x GROUP BY day, op, source`
		var rows []memorySeriesRow
		if err := s.db.SelectContext(ctx, &rows, q, a.vals...); err != nil {
			return nil, fmt.Errorf("memory series: %w", err)
		}
		for _, r := range rows {
			if p := byDay[r.Day.Format("2006-01-02")]; p != nil && p.Reads != nil {
				p.addReadStats(r)
			}
		}
	}
	if writesOn {
		a := &sqlArgs{}
		q := `SELECT day, COUNT(*)::bigint AS writes, COUNT(*) FILTER (WHERE with_reason)::bigint AS with_reason
			FROM ` + memoryWritesSource(a, f, wops) + ` w GROUP BY day`
		var rows []memorySeriesRow
		if err := s.db.SelectContext(ctx, &rows, q, a.vals...); err != nil {
			return nil, fmt.Errorf("memory write series: %w", err)
		}
		for _, r := range rows {
			if p := byDay[r.Day.Format("2006-01-02")]; p != nil {
				p.Writes, p.WithReason = i64(r.Writes), i64(r.WithReason)
			}
		}
	}
	out := make([]MemorySeriesPoint, 0, len(days))
	for _, d := range days {
		p := byDay[d]
		if p.Reads != nil {
			if !f.recallApplicable() {
				p.Hits, p.Useful = nil, nil
				for _, name := range []string{"hits", "useful", "useful_rate"} {
					p.Reasons[name] = MemoryReasonNotApplicable
				}
			} else {
				p.UsefulRate = ratio(*p.Useful, *p.Hits)
				if p.UsefulRate == nil {
					p.Reasons["useful_rate"] = MemoryReasonNoData
				}
			}
		}
		out = append(out, *p)
	}
	return out, nil
}

// memoryDimSQL maps a group_by dimension to its key and label expressions over
// memoryDimLabelsRU is the display dictionary of the breakdown labels the
// widget renders as served; U6's memory-widget.test.tsx pins these strings.
var memoryDimLabelsRU = struct{ none, manySources string }{
	none:        "нет данных",
	manySources: "поиск, несколько источников",
}

// the stats rows x (agents ag and projects pr are joined by the caller).
func memoryDimSQL(dim string) (key, label string) {
	switch dim {
	case "agent":
		return "COALESCE(x.agent_id::text, '')", "COALESCE(ag.name, '')"
	case "project":
		return "COALESCE(x.project_id::text, '')", "COALESCE(pr.name, '')"
	case "day":
		return "to_char(x.day, 'YYYY-MM-DD')", "to_char(x.day, 'YYYY-MM-DD')"
	case "source":
		return "COALESCE(x.source, '')", "CASE WHEN x.source = '*' THEN '" + memoryDimLabelsRU.manySources + "' ELSE COALESCE(x.source, '') END"
	case "op", "caller_kind":
		return "COALESCE(x." + dim + ", '')", "COALESCE(x." + dim + ", '')"
	case "kind", "scope":
		return "x." + dim, "COALESCE(x." + dim + ", '" + memoryDimLabelsRU.none + "')"
	}
	return "''", "''"
}

type memoryBreakdownQueryRow struct {
	Key              *string `db:"k"`
	Label            string  `db:"l"`
	Key2             *string `db:"k2"`
	Label2           string  `db:"l2"`
	Reads            int64   `db:"reads"`
	RecallReads      int64   `db:"recall_reads"`
	Hits             int64   `db:"hits"`
	Useful           int64   `db:"useful"`
	ResponseChars    int64   `db:"response_chars"`
	ServedRows       int64   `db:"served_rows"`
	ServedSuperseded int64   `db:"served_superseded"`
	Writes           int64   `db:"writes"`
	WithReason       int64   `db:"with_reason"`
}

// Both readers and writers participate in the table. Record dimensions use
// writes alone because a recall may serve several kinds and scopes.
func memoryBreakdownSource(a *sqlArgs, f MemoryAnalyticsFilter, ops []string, readReason string) string {
	var parts []string
	if readReason == "" {
		parts = append(parts, `SELECT day, project_id, agent_id, caller_kind, op, source,
			NULL::text AS kind, NULL::text AS scope, reads, errors, hits, useful, response_chars, served_rows, served_superseded,
			0::bigint AS writes, 0::bigint AS with_reason FROM `+memoryStatsSource(a, f, ops)+` r`)
	}
	if wops, on := f.writeOpsSelected(); on {
		parts = append(parts, `SELECT day, project_id, agent_id, NULL::text AS caller_kind, op, source, kind, scope,
			0::bigint AS reads, 0::bigint AS errors, 0::bigint AS hits, 0::bigint AS useful, 0::bigint AS response_chars,
			0::bigint AS served_rows, 0::bigint AS served_superseded,
			1::bigint AS writes, CASE WHEN with_reason THEN 1 ELSE 0 END::bigint AS with_reason
			FROM `+memoryWritesSource(a, f, wops)+` w`)
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " UNION ALL ") + ")"
}

func (s *memoryAnalyticsService) rows(ctx context.Context, f MemoryAnalyticsFilter, ops []string, readReason string) ([]MemoryBreakdownRow, error) {
	if f.GroupBy == "" {
		return []MemoryBreakdownRow{}, nil
	}
	k1, l1 := memoryDimSQL(f.GroupBy)
	k2, l2 := "NULL::text", "''"
	if f.GroupBy2 != "" {
		k2, l2 = memoryDimSQL(f.GroupBy2)
	}
	limit := f.TopN
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	a := &sqlArgs{}
	source := memoryBreakdownSource(a, f, ops, readReason)
	if source == "" {
		return []MemoryBreakdownRow{}, nil
	}
	order := "reads DESC, writes DESC, k NULLS LAST, k2 NULLS LAST"
	if f.recordDimensions() {
		order = "writes DESC, k NULLS LAST, k2 NULLS LAST"
	}
	q := fmt.Sprintf(`SELECT %s AS k, %s AS l, %s AS k2, %s AS l2,
		COALESCE(SUM(x.reads),0)::bigint AS reads,
		COALESCE(SUM(x.reads-x.errors) FILTER (WHERE %s),0)::bigint AS recall_reads,
		COALESCE(SUM(x.hits) FILTER (WHERE %s),0)::bigint AS hits,
		COALESCE(SUM(x.useful) FILTER (WHERE %s),0)::bigint AS useful,
		COALESCE(SUM(x.served_rows) FILTER (WHERE %s),0)::bigint AS served_rows,
		COALESCE(SUM(x.served_superseded) FILTER (WHERE %s),0)::bigint AS served_superseded,
		COALESCE(SUM(x.response_chars),0)::bigint AS response_chars,
		COALESCE(SUM(x.writes),0)::bigint AS writes,
		COALESCE(SUM(x.with_reason),0)::bigint AS with_reason
		FROM %s x
		LEFT JOIN agents ag ON ag.id = x.agent_id
		LEFT JOIN projects pr ON pr.id = x.project_id
		GROUP BY 1, 2, 3, 4
		ORDER BY %s
		LIMIT %d`, k1, l1, k2, l2, memoryRecallPredicate(f), memoryRecallPredicate(f), memoryRecallPredicate(f), memoryRecallPredicate(f), memoryRecallPredicate(f), source, order, limit)
	var qr []memoryBreakdownQueryRow
	if err := s.db.SelectContext(ctx, &qr, q, a.vals...); err != nil {
		return nil, fmt.Errorf("memory breakdown: %w", err)
	}
	out := make([]MemoryBreakdownRow, len(qr))
	for i, r := range qr {
		row := MemoryBreakdownRow{Key: r.Key, Label: r.Label, Key2: r.Key2, Label2: r.Label2,
			Reasons: map[string]string{}}
		if readReason == "" {
			row.Reads, row.Hits, row.Useful = i64(r.Reads), i64(r.Hits), i64(r.Useful)
			row.TokensEst = i64(r.ResponseChars / 4)
			row.HitRate, row.UsefulRate = ratio(r.Hits, r.RecallReads), ratio(r.Useful, r.Hits)
			row.ServedRows = i64(r.ServedRows)
			row.SupersededServedRate = ratio(r.ServedSuperseded, r.ServedRows)
			if row.HitRate == nil {
				row.Reasons["hit_rate"] = MemoryReasonNoData
			}
			if row.UsefulRate == nil {
				row.Reasons["useful_rate"] = MemoryReasonNoData
			}
			if row.SupersededServedRate == nil {
				row.Reasons["superseded_served_rate"] = MemoryReasonNoData
			}
			source := ""
			if f.GroupBy == "source" && row.Key != nil {
				source = *row.Key
			} else if f.GroupBy2 == "source" && row.Key2 != nil {
				source = *row.Key2
			}
			if !f.recallApplicable() || (source != "" && source != "*") {
				row.Hits, row.Useful, row.HitRate, row.UsefulRate = nil, nil, nil, nil
				for _, name := range []string{"hits", "useful", "hit_rate", "useful_rate"} {
					row.Reasons[name] = MemoryReasonNotApplicable
				}
			}
			if source == "*" {
				row.ServedRows, row.SupersededServedRate = nil, nil
				row.Reasons["served_rows"], row.Reasons["superseded_served_rate"] = MemoryReasonNotApplicable, MemoryReasonNotApplicable
			}
		} else {
			for _, name := range []string{"reads", "hits", "useful", "hit_rate", "useful_rate", "tokens_est", "served_rows", "superseded_served_rate"} {
				row.Reasons[name] = readReason
			}
		}
		if _, on := f.writeOpsSelected(); on {
			row.Writes, row.WithReason = i64(r.Writes), i64(r.WithReason)
		} else {
			row.Reasons["writes"], row.Reasons["with_reason"] = MemoryReasonExcludedByFilter, MemoryReasonExcludedByFilter
		}
		out[i] = row
	}
	return out, nil
}

type memoryQualityRow struct {
	Day              time.Time     `db:"day"`
	Total            int64         `db:"total"`
	Active           int64         `db:"active"`
	Archived         int64         `db:"archived"`
	SupersededActive int64         `db:"superseded_active"`
	Conflicted       int64         `db:"conflicted"`
	NoKind           int64         `db:"no_kind"`
	StatuslogLike    int64         `db:"statuslog_like"`
	DupGroups        int64         `db:"dup_groups"`
	DupRows          int64         `db:"dup_rows"`
	Dead30           sql.NullInt64 `db:"dead30"`
	Dead90           sql.NullInt64 `db:"dead90"`
	Eligible30       sql.NullInt64 `db:"eligible30"`
	Eligible90       sql.NullInt64 `db:"eligible90"`
}

// quality reads the base snapshot of the last aggregated day of the window and
// the dead30 KPI: dead records among active records older than 30 days.
func (s *memoryAnalyticsService) quality(ctx context.Context, f MemoryAnalyticsFilter, since *time.Time, out *MemoryAnalytics) error {
	a := &sqlArgs{}
	end := utcDay(f.To)
	if f.To.After(end) {
		end = end.AddDate(0, 0, 1)
	}
	w := []string{"b.workspace_id = " + a.add(f.WorkspaceID), visibleRowProject(a, f.Viewer, "b.project_id"),
		"b.day < " + a.add(end) + "::date"}
	if f.ProjectID != nil {
		w = append(w, "b.project_id = "+a.add(*f.ProjectID))
	}
	q := `WITH last AS (SELECT MAX(b.day) AS day FROM memory_base_daily b WHERE ` + strings.Join(w, " AND ") + `)
		SELECT last.day::timestamptz AS day,
			COALESCE(SUM(b.total),0)::bigint AS total, COALESCE(SUM(b.active),0)::bigint AS active,
			COALESCE(SUM(b.archived),0)::bigint AS archived, COALESCE(SUM(b.superseded_active),0)::bigint AS superseded_active,
			COALESCE(SUM(b.conflicted),0)::bigint AS conflicted, COALESCE(SUM(b.no_kind),0)::bigint AS no_kind,
			COALESCE(SUM(b.statuslog_like),0)::bigint AS statuslog_like, COALESCE(SUM(b.dup_groups),0)::bigint AS dup_groups,
			COALESCE(SUM(b.dup_rows),0)::bigint AS dup_rows,
			CASE WHEN COUNT(b.dead30) = COUNT(*) THEN SUM(b.dead30)::bigint END AS dead30,
			CASE WHEN COUNT(b.dead90) = COUNT(*) THEN SUM(b.dead90)::bigint END AS dead90,
			CASE WHEN COUNT(b.eligible30) = COUNT(*) THEN SUM(b.eligible30)::bigint END AS eligible30,
			CASE WHEN COUNT(b.eligible90) = COUNT(*) THEN SUM(b.eligible90)::bigint END AS eligible90
		FROM last JOIN memory_base_daily b ON b.day = last.day AND ` + strings.Join(w, " AND ") + `
		GROUP BY last.day`
	var r memoryQualityRow
	err := s.db.GetContext(ctx, &r, q, a.vals...)
	if err == sql.ErrNoRows {
		// No base snapshot yet: every quality number and dead30 are null.
		out.Reasons["quality"] = MemoryReasonNotCollected
		if since == nil {
			out.Reasons["dead30"] = MemoryReasonNoJournalBefore
		} else {
			out.Reasons["dead30"] = MemoryReasonNotCollected
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory quality: %w", err)
	}
	day := r.Day.Format("2006-01-02")
	out.Quality = MemoryQuality{Day: &day, Total: i64(r.Total), Active: i64(r.Active), Archived: i64(r.Archived),
		SupersededActive: i64(r.SupersededActive), Conflicted: i64(r.Conflicted), NoKind: i64(r.NoKind),
		StatuslogLike: i64(r.StatuslogLike), DupGroups: i64(r.DupGroups), DupRows: i64(r.DupRows)}
	if r.Dead30.Valid {
		out.Quality.Dead30 = i64(r.Dead30.Int64)
	} else {
		out.Reasons["quality.dead30"] = MemoryReasonNoJournalBefore
	}
	if r.Dead90.Valid {
		out.Quality.Dead90 = i64(r.Dead90.Int64)
	} else {
		out.Reasons["quality.dead90"] = MemoryReasonNoJournalBefore
	}

	// Numerator and denominator must belong to the same historical snapshot.
	if !r.Dead30.Valid || !r.Eligible30.Valid {
		out.Reasons["dead30"] = MemoryReasonNoJournalBefore
		return nil
	}
	if v := ratio(r.Dead30.Int64, r.Eligible30.Int64); v != nil {
		out.KPIs.Dead30 = v
		delete(out.Reasons, "dead30")
	} else {
		out.Reasons["dead30"] = MemoryReasonNoData
	}
	return nil
}

func (p MemoryListParams) norm() MemoryListParams {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 || p.PageSize > 100 {
		p.PageSize = 50
	}
	return p
}

func (p MemoryListParams) limitOffset(a *sqlArgs) string {
	return " LIMIT " + a.add(p.PageSize) + " OFFSET " + a.add((p.Page-1)*p.PageSize)
}

func searchLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// journalList prepares a drill-down that reads the raw journal: nil filter when
// the journal does not reach into the window (the list then carries the reason).
func (s *memoryAnalyticsService) journalList(ctx context.Context, f *MemoryAnalyticsFilter) (*MemoryDetailCoverage, string, error) {
	*f = f.normalize()
	if err := s.checkProject(ctx, *f); err != nil {
		return nil, "", err
	}
	since, err := s.journalSince(ctx, f.WorkspaceID)
	if err != nil {
		return nil, "", err
	}
	coverage := &MemoryDetailCoverage{}
	if since == nil || !since.Before(f.To) {
		coverage.Reason = MemoryReasonNoJournalBefore
		return coverage, MemoryReasonNoJournalBefore, nil
	}
	start := f.now().AddDate(0, 0, -90)
	reason := MemoryReasonRetention
	if since.After(start) {
		start, reason = *since, MemoryReasonNoJournalBefore
	}
	coverage.DetailSince = &start
	if f.From.Before(start) {
		f.From = start
		coverage.Reason = reason
	}
	if !f.From.Before(f.To) {
		return coverage, reason, nil
	}
	return coverage, "", nil
}

// EmptyQueries lists recall queries that came back with no unpinned row, grouped
// by normalised query. The text is shown to owner/admin and to the agent who asked.
func (s *memoryAnalyticsService) emptyQueries(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryEmptyQuery], error) {
	p = p.norm()
	out := &MemoryList[MemoryEmptyQuery]{Items: []MemoryEmptyQuery{}, Page: p.Page, PageSize: p.PageSize}
	coverage, reason, err := s.journalList(ctx, &f)
	out.Coverage = coverage
	if err != nil || reason != "" {
		out.Reason = reason
		return out, err
	}
	a := &sqlArgs{}
	allowed := mayReadPrivate(a, f.Viewer, "e.agent_id")
	textSince := a.add(f.now().AddDate(0, 0, -30))
	ops, _ := f.readOpsSelected()
	recallOps := []string{}
	for _, op := range ops {
		if slices.Contains(memoryRecallOps, op) {
			recallOps = append(recallOps, op)
		}
	}
	w := memoryEventScope(a, f, recallOps) + " AND e.ts >= " + a.add(f.From) + " AND e.ts < " + a.add(f.To) +
		" AND e.error IS NULL AND e.result_count - e.pinned_count <= 0"
	if p.Search != "" {
		w += " AND " + allowed + " AND e.ts >= " + textSince + " AND e.query_text ILIKE " + a.add(searchLike(p.Search))
	}
	order := "cnt DESC, last_at DESC"
	if p.Sort == "last_at" {
		order = "last_at DESC, cnt DESC"
	}
	q := `SELECT g.query_hash, CASE WHEN g.allowed THEN g.query_text END AS query_text, NOT g.allowed AS redacted,
			CASE WHEN g.allowed AND g.last_at < ` + textSince + ` THEN 'text_expired' ELSE '' END AS reason,
			g.agent_id, ag.name AS agent_name, g.cnt, g.last_at, COUNT(*) OVER() AS total
		FROM (SELECT e.query_hash, e.agent_id, ` + allowed + ` AS allowed,
				MAX(e.query_text) FILTER (WHERE e.ts >= ` + textSince + `) AS query_text,
				COUNT(*) AS cnt, MAX(e.ts) AS last_at
			FROM memory_read_events e WHERE ` + w + ` GROUP BY e.query_hash, e.agent_id) g
		LEFT JOIN agents ag ON ag.id = g.agent_id
		ORDER BY ` + order + `, g.query_hash` + p.limitOffset(a)
	var rows []struct {
		MemoryEmptyQuery
		Total int64 `db:"total"`
	}
	if err := s.db.SelectContext(ctx, &rows, q, a.vals...); err != nil {
		return nil, fmt.Errorf("memory empty queries: %w", err)
	}
	for _, r := range rows {
		out.Items = append(out.Items, r.MemoryEmptyQuery)
		out.Total = r.Total
	}
	return out, nil
}

// memoryRecordSelect is the safe projection of a memory row: no content, and the
// key of an agent-scope record only for owner/admin and its agent.
func memoryRecordSelect(a *sqlArgs, v AnalyticsViewer) string {
	allowed := "(m.scope <> 'agent' OR " + mayReadPrivate(a, v, "m.agent_id") + ")"
	return `m.id AS memory_id, CASE WHEN ` + allowed + ` THEN m.key END AS key, NOT ` + allowed + ` AS redacted,
		m.scope, (SELECT substr(t,6) FROM unnest(m.tags) WITH ORDINALITY AS tags(t,n)
			WHERE t LIKE 'kind:%' ORDER BY n LIMIT 1) AS kind, m.project_id, m.agent_id, m.created_at`
}

// memoryRecordScope: records of the workspace the viewer may see in analytics.
func memoryRecordScope(a *sqlArgs, f MemoryAnalyticsFilter) string {
	w := []string{"m.workspace_id = " + a.add(f.WorkspaceID), visibleRowProject(a, f.Viewer, "m.project_id")}
	if f.ProjectID != nil {
		w = append(w, "m.project_id = "+a.add(*f.ProjectID))
	}
	if len(f.Kinds) > 0 {
		w = append(w, "(SELECT substr(t,6) FROM unnest(m.tags) WITH ORDINALITY AS tags(t,n) WHERE t LIKE 'kind:%' ORDER BY n LIMIT 1) = ANY("+a.add(pq.Array(f.Kinds))+"::text[])")
	}
	if len(f.Scopes) > 0 {
		w = append(w, "m.scope = ANY("+a.add(pq.Array(f.Scopes))+"::text[])")
	}
	return strings.Join(w, " AND ")
}

func memoryKeySearch(a *sqlArgs, v AnalyticsViewer, s string) string {
	if s == "" {
		return ""
	}
	return " AND (m.scope <> 'agent' OR " + mayReadPrivate(a, v, "m.agent_id") + ") AND m.key ILIKE " + a.add(searchLike(s))
}

type memoryRecordQueryRow struct {
	MemoryRecordRow
	Total int64 `db:"total"`
}

func (s *memoryAnalyticsService) recordList(ctx context.Context, q string, a *sqlArgs, out *MemoryList[MemoryRecordRow]) error {
	var rows []memoryRecordQueryRow
	if err := s.db.SelectContext(ctx, &rows, q, a.vals...); err != nil {
		return err
	}
	for _, r := range rows {
		out.Items = append(out.Items, r.MemoryRecordRow)
		out.Total = r.Total
	}
	return nil
}

// TopRecords ranks records by how often reads served them (sort=served, the
// default) or by useful uses (sort=useful).
func (s *memoryAnalyticsService) topRecords(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error) {
	p = p.norm()
	out := &MemoryList[MemoryRecordRow]{Items: []MemoryRecordRow{}, Page: p.Page, PageSize: p.PageSize}
	coverage, reason, err := s.journalList(ctx, &f)
	out.Coverage = coverage
	if err != nil || reason != "" {
		out.Reason = reason
		return out, err
	}
	ops, _ := f.readOpsSelected()
	if ops == nil {
		ops = []string{}
	}
	a := &sqlArgs{}
	ev := memoryEventScope(a, f, ops) + " AND e.ts >= " + a.add(f.From) + " AND e.ts < " + a.add(f.To)
	src := ""
	if len(f.Sources) > 0 {
		src = " AND r->>'source' = ANY(" + a.add(pq.Array(f.Sources)) + "::text[])"
	}
	order := "served DESC, useful DESC"
	if p.Sort == "useful" {
		order = "useful DESC, served DESC"
	}
	q := `WITH served AS (
			SELECT (r->>'id')::uuid AS memory_id, e.id AS event_id, e.ts
			FROM memory_read_events e, jsonb_array_elements(e.results) r
			WHERE ` + ev + src + ` AND r ? 'id' AND NOT memory_read_row_pinned(r)),
		agg AS (SELECT s.memory_id, COUNT(*) AS served, MAX(s.ts) AS last_served_at,
				COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM memory_read_usage u
					WHERE u.read_event_id = s.event_id AND u.memory_id = s.memory_id)) AS useful
			FROM served s GROUP BY s.memory_id)
		SELECT ` + memoryRecordSelect(a, f.Viewer) + `, agg.served, agg.useful, agg.last_served_at, COUNT(*) OVER() AS total
		FROM agg JOIN memories m ON m.id = agg.memory_id
		WHERE ` + memoryRecordScope(a, f) + memoryKeySearch(a, f.Viewer, p.Search) + `
		ORDER BY ` + order + `, m.id` + p.limitOffset(a)
	if err := s.recordList(ctx, q, a, out); err != nil {
		return nil, fmt.Errorf("memory top records: %w", err)
	}
	return out, nil
}

// memoryLivePredicate is the current-record set every read path serves
// (status <> 'superseded', not archived) and U4's live set for dead30/dead90:
// review_needed, stale and conflicted records are current, an expired record
// (status 'archived') is not.
const memoryLivePredicate = `m.status NOT IN ('superseded', 'archived') AND NOT m.archived`

// DeadRecords lists live records older than N days that no agent read served
// in the last N days (§3), anchored at now. An incomplete journal makes the
// metric unavailable instead of labeling unobserved records as dead.
func (s *memoryAnalyticsService) deadRecords(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error) {
	p = p.norm()
	out := &MemoryList[MemoryRecordRow]{Items: []MemoryRecordRow{}, Page: p.Page, PageSize: p.PageSize}
	f.Now = f.now()
	if f.DeadDays != 90 {
		f.DeadDays = 30
	}
	f.From, f.To = f.Now.AddDate(0, 0, -f.DeadDays), f.Now
	cutoff := f.From
	coverage, reason, err := s.journalList(ctx, &f)
	out.Coverage = coverage
	if err != nil {
		return nil, err
	}
	if reason != "" || coverage.DetailSince == nil || coverage.DetailSince.After(cutoff) {
		out.Reason = MemoryReasonNoJournalBefore
		return out, nil
	}
	a := &sqlArgs{}
	ws, cut, to := a.add(f.WorkspaceID), a.add(cutoff), a.add(f.To)
	order := "m.created_at ASC"
	if p.Sort == "newest" {
		order = "m.created_at DESC"
	}
	q := `SELECT ` + memoryRecordSelect(a, f.Viewer) + `, 0::bigint AS served, 0::bigint AS useful,
			NULL::timestamptz AS last_served_at, COUNT(*) OVER() AS total
		FROM memories m
		WHERE ` + memoryRecordScope(a, f) + ` AND ` + memoryLivePredicate + ` AND m.created_at < ` + cut + `
		  AND NOT EXISTS (SELECT 1 FROM memory_read_events e, jsonb_array_elements(e.results) r
			WHERE e.workspace_id = ` + ws + ` AND e.caller_kind = 'agent_tool' AND e.ts >= ` + cut + ` AND e.ts < ` + to + `
			  AND r->>'id' = m.id::text)` + memoryKeySearch(a, f.Viewer, p.Search) + `
		ORDER BY ` + order + `, m.id` + p.limitOffset(a)
	if err := s.recordList(ctx, q, a, out); err != nil {
		return nil, fmt.Errorf("memory dead records: %w", err)
	}
	return out, nil
}

// Duplicates lists live records sharing a content simhash with another live
// record of the workspace (Stage 1: exact equality), largest groups first.
func (s *memoryAnalyticsService) duplicates(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryRecordRow], error) {
	p = p.norm()
	out := &MemoryList[MemoryRecordRow]{Items: []MemoryRecordRow{}, Page: p.Page, PageSize: p.PageSize}
	f = f.normalize()
	if err := s.checkProject(ctx, f); err != nil {
		return nil, err
	}
	a := &sqlArgs{}
	q := `WITH vis AS (SELECT m.* FROM memories m WHERE ` + memoryRecordScope(a, f) + `
			AND ` + memoryLivePredicate + ` AND m.content_simhash IS NOT NULL),
		grp AS (SELECT content_simhash, COUNT(*) AS n FROM vis GROUP BY content_simhash HAVING COUNT(*) > 1)
		SELECT ` + memoryRecordSelect(a, f.Viewer) + `, 0::bigint AS served, 0::bigint AS useful,
			NULL::timestamptz AS last_served_at, to_hex(m.content_simhash) AS group_id, grp.n AS group_size,
			COUNT(*) OVER() AS total
		FROM vis m JOIN grp ON grp.content_simhash = m.content_simhash
		WHERE TRUE` + memoryKeySearch(a, f.Viewer, p.Search) + `
		ORDER BY grp.n DESC, m.content_simhash, m.created_at, m.id` + p.limitOffset(a)
	if err := s.recordList(ctx, q, a, out); err != nil {
		return nil, fmt.Errorf("memory duplicates: %w", err)
	}
	return out, nil
}

// UnknownSources names who reads memory without a caller header: route, user
// agent, number of reads and up to three keys they were served (keys under the
// same rule as every record list).
func (s *memoryAnalyticsService) unknownSources(ctx context.Context, f MemoryAnalyticsFilter, p MemoryListParams) (*MemoryList[MemoryUnknownSource], error) {
	p = p.norm()
	out := &MemoryList[MemoryUnknownSource]{Items: []MemoryUnknownSource{}, Page: p.Page, PageSize: p.PageSize}
	coverage, reason, err := s.journalList(ctx, &f)
	out.Coverage = coverage
	if err != nil || reason != "" {
		out.Reason = reason
		return out, err
	}
	if len(f.CallerKinds) > 0 && !slices.Contains(f.CallerKinds, "unknown") {
		return out, nil
	}
	f.CallerKinds = []string{"unknown"}
	ops, _ := f.readOpsSelected()
	if ops == nil {
		ops = []string{}
	}
	a := &sqlArgs{}
	ev := memoryEventScope(a, f, ops) + " AND e.ts >= " + a.add(f.From) + " AND e.ts < " + a.add(f.To)
	if p.Search != "" {
		like := a.add(searchLike(p.Search))
		ev += " AND (e.route ILIKE " + like + " OR e.user_agent ILIKE " + like + ")"
	}
	keyOK := "(m.scope <> 'agent' OR " + mayReadPrivate(a, f.Viewer, "m.agent_id") + ")"
	q := `WITH ev AS (SELECT e.* FROM memory_read_events e WHERE ` + ev + `),
		src AS (SELECT COALESCE(route,'') AS route, COALESCE(user_agent,'') AS user_agent, COUNT(*) AS reads
			FROM ev GROUP BY 1, 2)
		SELECT src.route, src.user_agent, src.reads,
			COALESCE((SELECT array_agg(k) FROM (
				SELECT DISTINCT m.key AS k FROM ev, jsonb_array_elements(ev.results) r
				JOIN memories m ON m.id::text = r->>'id'
				WHERE COALESCE(ev.route,'') = src.route AND COALESCE(ev.user_agent,'') = src.user_agent
				  AND ` + memoryRecordScope(a, f) + ` AND ` + keyOK + `
				ORDER BY 1 LIMIT 3) ks), '{}') AS sample_keys,
			COUNT(*) OVER() AS total
		FROM src ORDER BY src.reads DESC, src.route, src.user_agent` + p.limitOffset(a)
	var rows []struct {
		Route      string         `db:"route"`
		UserAgent  string         `db:"user_agent"`
		Reads      int64          `db:"reads"`
		SampleKeys pq.StringArray `db:"sample_keys"`
		Total      int64          `db:"total"`
	}
	if err := s.db.SelectContext(ctx, &rows, q, a.vals...); err != nil {
		return nil, fmt.Errorf("memory unknown sources: %w", err)
	}
	for _, r := range rows {
		out.Items = append(out.Items, MemoryUnknownSource{Route: r.Route, UserAgent: r.UserAgent, Reads: r.Reads, SampleKeys: []string(r.SampleKeys)})
		out.Total = r.Total
	}
	return out, nil
}
