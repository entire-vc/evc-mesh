package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Use the migrated database, never a private imitation of the upstream schema.
// These contract tests must also run once U1/U3/U4 are stacked into this branch.
func TestMemoryAnalyticsContractEmptyJournal(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	result, err := NewMemoryAnalyticsService(db).Get(context.Background(), MemoryAnalyticsFilter{
		WorkspaceID: uuid.New(), From: utcDay(now).AddDate(0, 0, -6), To: now, Now: now,
		Viewer: AnalyticsViewer{AllProjects: true},
	})
	require.NoError(t, err)
	require.Nil(t, result.KPIs.UsefulRate)
	require.Equal(t, MemoryReasonNoJournalBefore, result.Reasons["useful_rate"])
	require.Nil(t, result.KPIs.Reads)
	require.Nil(t, result.Coverage.JournalSince)
	require.Empty(t, result.Coverage.CallerKind)
	for _, point := range result.Series {
		require.Nil(t, point.Reads)
		require.Equal(t, MemoryReasonNoJournalBefore, point.Reasons["reads"])
	}
}

func TestMemoryAnalyticsSeriesUnavailable(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, dim := range []string{"", "kind", "scope"} {
		t.Run(dim, func(t *testing.T) {
			filter := MemoryAnalyticsFilter{From: utcDay(now).AddDate(0, 0, -1), To: now, Now: now,
				Ops: []string{"recall"}, GroupBy: dim}
			// There is no journal and writes are excluded. No SQL should run to
			// manufacture measurements for either of those unavailable signals.
			svc := &memoryAnalyticsService{}
			points, err := svc.series(context.Background(), filter, memoryRecallOps, nil)
			require.NoError(t, err)
			require.Len(t, points, 2)
			for _, point := range points {
				require.Nil(t, point.Reads)
				require.Nil(t, point.Hits)
				require.Nil(t, point.Useful)
				require.Nil(t, point.ReadsByOp)
				require.Nil(t, point.ServedRowsBySource)
				require.Nil(t, point.UsefulRate)
				require.Nil(t, point.Writes)
				reason := MemoryReasonNoJournalBefore
				if dim != "" {
					reason = MemoryReasonNotApplicable
				}
				require.Equal(t, reason, point.Reasons["reads"])
				require.Equal(t, reason, point.Reasons["reads_by_op"])
				require.Equal(t, reason, point.Reasons["served_rows_by_source"])
				require.Equal(t, reason, point.Reasons["useful_rate"])
				require.Equal(t, MemoryReasonExcludedByFilter, point.Reasons["writes"])
				encoded, err := json.Marshal(point)
				require.NoError(t, err)
				require.Contains(t, string(encoded), `"reads":null`)
			}
		})
	}
}

func TestMemoryAnalyticsSeriesCallAndResultCounters(t *testing.T) {
	// One mixed-source recall call can serve many records. Its call counter
	// belongs to the operation stack, and never to either result-source bucket.
	point := MemorySeriesPoint{Reads: i64(0), Hits: i64(0), Useful: i64(0),
		ReadsByOp: map[string]int64{}, ServedRowsBySource: map[string]int64{}}
	for _, row := range []memorySeriesRow{
		{Op: "recall", Source: "*", Reads: 3, Hits: 2, Useful: 1},
		{Op: "recall", Source: "memory", ServedRows: 8},
		{Op: "recall", Source: "doc", ServedRows: 4},
		{Op: "get_memory", Source: "memory", Reads: 2, ServedRows: 2},
	} {
		point.addReadStats(row)
	}
	require.EqualValues(t, 5, *point.Reads)
	require.Equal(t, map[string]int64{"recall": 3, "get_memory": 2}, point.ReadsByOp)
	require.Equal(t, map[string]int64{"memory": 10, "doc": 4}, point.ServedRowsBySource)
	require.EqualValues(t, 2, *point.Hits)
	require.EqualValues(t, 1, *point.Useful)
	require.InDelta(t, 0.5, *ratio(*point.Useful, *point.Hits), 0.000001)
	encoded, err := json.Marshal(point)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "reads_by_source")
}

func TestMemoryAnalyticsContractRecordDimensions(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_revisions WHERE workspace_id=$1`, f.ws)
		_, _ = db.Exec(`DELETE FROM memories WHERE workspace_id=$1`, f.ws)
	})
	for _, record := range []struct {
		project uuid.UUID
		kind    string
		deleted bool
	}{
		{f.shared, "guide", false}, {f.hidden, "hidden-kind", false}, {f.shared, "", true},
	} {
		id := uuid.New()
		if !record.deleted {
			_, err := db.Exec(`INSERT INTO memories (id,workspace_id,project_id,agent_id,key,content,scope,tags,status)
				VALUES ($1,$2,$3,$4,$5,'not exported','project','{kind:current}', 'active')`,
				id, f.ws, record.project, f.agent, id.String())
			require.NoError(t, err)
		}
		tags := "{}"
		if record.kind != "" {
			tags = "{kind:" + record.kind + ",kind:ignored-second}"
		}
		_, err := db.Exec(`INSERT INTO memory_revisions
			(memory_id,workspace_id,version,content,tags,action,reason,actor_agent_id,created_at)
			VALUES ($1,$2,1,'not exported',$3::text[],'created','contract',$4,$5)`, id, f.ws, tags, f.agent, now.Add(-time.Second))
		require.NoError(t, err)
	}
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: utcDay(now), To: now, Now: now,
		Viewer: AnalyticsViewer{AllProjects: true}, GroupBy: "kind", GroupBy2: "scope"}
	svc := NewMemoryAnalyticsService(db)
	result, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 3, *result.KPIs.Writes)
	require.EqualValues(t, 3, *result.KPIs.WithReason)
	require.Nil(t, result.KPIs.Reads)
	require.Equal(t, MemoryReasonNotApplicable, result.Reasons["reads"])
	require.Len(t, result.Rows, 3)
	for _, row := range result.Rows {
		require.EqualValues(t, 1, *row.Writes)
		require.Nil(t, row.Reads)
		require.Equal(t, MemoryReasonNotApplicable, row.Reasons["reads"])
		if row.Key == nil {
			require.Equal(t, "нет данных", row.Label)
			require.Nil(t, row.Key2, "a deleted memory keeps its revision with unknown current scope")
		} else {
			require.NotEqual(t, "current", *row.Key, "kind comes from the revision")
			require.NotEqual(t, "ignored-second", *row.Key)
		}
	}
	require.Nil(t, result.Totals.Reads)
	require.EqualValues(t, 3, *result.Totals.Writes)
	require.Len(t, result.Series, 1)
	require.Nil(t, result.Series[0].Reads)
	require.EqualValues(t, 3, *result.Series[0].Writes)

	filter.Viewer = AnalyticsViewer{ActorID: f.sharedMember}
	member, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 1, *member.KPIs.Writes)
	require.Len(t, member.Rows, 1)
	require.Equal(t, "guide", *member.Rows[0].Key)
}

func TestMemoryAnalyticsContractPrivateQueries(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, f.ws) })
	for i, age := range []int{0, 40} {
		_, err := db.Exec(`INSERT INTO memory_read_events
			(id, ts, workspace_id, project_id, agent_id, caller_kind, route, user_agent, op,
			 query_hash, query_text, result_count, pinned_count, superseded_count, results, response_chars)
			VALUES ($1,$2,$3,$4,$5,'agent_tool','/recall','contract-test','recall',$6,$7,0,0,0,'[]',0)`,
			uuid.New(), now.AddDate(0, 0, -age).Add(-time.Second), f.ws, f.shared, f.agent,
			strings.Repeat(string(rune('a'+i)), 64), []string{"recent private query", "expired private query"}[i])
		require.NoError(t, err)
	}
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: now.AddDate(0, 0, -60), To: now, Now: now,
		Viewer: AnalyticsViewer{ActorID: f.sharedMember}}
	svc := NewMemoryAnalyticsService(db)
	member, err := svc.EmptyQueries(context.Background(), filter, MemoryListParams{})
	require.NoError(t, err)
	require.Len(t, member.Items, 2, "the member sees permitted project counts")
	for _, row := range member.Items {
		require.Nil(t, row.QueryText)
		require.True(t, row.Redacted)
	}
	searched, err := svc.EmptyQueries(context.Background(), filter, MemoryListParams{Search: "recent private"})
	require.NoError(t, err)
	require.Empty(t, searched.Items, "search must not become an oracle for private text")
	for _, viewer := range []AnalyticsViewer{{AllProjects: true}, {ActorID: f.agent, Agent: true}} {
		filter.Viewer = viewer
		allowed, err := svc.EmptyQueries(context.Background(), filter, MemoryListParams{})
		require.NoError(t, err)
		require.Len(t, allowed.Items, 2)
		for _, row := range allowed.Items {
			require.False(t, row.Redacted)
			if row.LastAt.Before(now.AddDate(0, 0, -30)) {
				require.Nil(t, row.QueryText, "expired text is suppressed even before the retention job runs")
				require.Equal(t, MemoryReasonTextExpired, row.Reason)
			} else {
				require.NotNil(t, row.QueryText)
				require.Equal(t, "recent private query", *row.QueryText)
			}
		}
		control, err := svc.EmptyQueries(context.Background(), filter, MemoryListParams{Search: "recent private"})
		require.NoError(t, err)
		require.Len(t, control.Items, 1, "positive control: authorized recent text is searchable")
		expired, err := svc.EmptyQueries(context.Background(), filter, MemoryListParams{Search: "expired private"})
		require.NoError(t, err)
		require.Empty(t, expired.Items)
	}
}

// A drill-down over the raw journal must select the calls the aggregates count:
// the journal's recall-final and recall_graph collapse into the analytics
// recall and recall_with_graph (the same mapping as U4's memory_stats_rollup),
// and a pinned row is not retrieval, so top-records never ranks it as served.
func TestMemoryAnalyticsContractDrilldownRawOps(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, f.ws)
		_, _ = db.Exec(`DELETE FROM memories WHERE workspace_id=$1`, f.ws)
	})
	ids := make([]uuid.UUID, 3)
	for i := range ids {
		ids[i] = uuid.New()
		_, err := db.Exec(`INSERT INTO memories
			(id,workspace_id,project_id,agent_id,key,content,scope,tags,status,content_simhash)
			VALUES ($1,$2,$3,$4,$5,'drilldown contract','project','{kind:guide}','active',$6)`,
			ids[i], f.ws, f.shared, f.agent, "drilldown-key-"+strconv.Itoa(i), 1000000+i)
		require.NoError(t, err)
	}
	for i, op := range []string{"recall-final", "recall_graph"} {
		_, err := db.Exec(`INSERT INTO memory_read_events
			(id,ts,workspace_id,project_id,agent_id,caller_kind,route,user_agent,op,result_count,pinned_count,superseded_count,results,response_chars)
			VALUES ($1,$2,$3,$4,$5,'agent_tool','/recall','contract',$6,1,0,0,$7,0)`,
			uuid.New(), now.Add(-time.Second), f.ws, f.shared, f.agent, op,
			`[{"id":"`+ids[i].String()+`","source":"memory","score":0.5}]`)
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO memory_read_events
		(id,ts,workspace_id,project_id,agent_id,caller_kind,route,user_agent,op,result_count,pinned_count,superseded_count,results,response_chars)
		VALUES ($1,$2,$3,$4,$5,'agent_tool','/recall','contract','recall',1,1,0,$6,0)`,
		uuid.New(), now.Add(-time.Second), f.ws, f.shared, f.agent,
		`[{"id":"`+ids[2].String()+`","source":"memory","score":2.0}]`)
	require.NoError(t, err)
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: now.AddDate(0, 0, -1), To: now, Now: now,
		Viewer: AnalyticsViewer{AllProjects: true}}
	svc := NewMemoryAnalyticsService(db)
	top, err := svc.TopRecords(context.Background(), filter, MemoryListParams{})
	require.NoError(t, err)
	require.Len(t, top.Items, 2, "recall-final and recall_graph events are selected by the analytics op names")
	require.EqualValues(t, 2, top.Total)
	for _, row := range top.Items {
		require.EqualValues(t, 1, row.Served)
		require.NotEqual(t, ids[2], row.MemoryID, "a pinned-only serve does not rank a record")
	}
	filter.Ops = []string{"recall_with_graph"}
	graphOnly, err := svc.TopRecords(context.Background(), filter, MemoryListParams{})
	require.NoError(t, err)
	require.Len(t, graphOnly.Items, 1)
	require.Equal(t, ids[1], graphOnly.Items[0].MemoryID)
}

// Uses PostgreSQL's real timeout and a successful query on a fresh transaction;
// no SQL mock can prove SET LOCAL isolation or that the canceled page is lost.
func TestMemoryAnalyticsDetailTimeout(t *testing.T) {
	db := testDB(t)
	svc := &memoryAnalyticsService{db: db, pool: db}
	ctx := context.Background()
	result, err := memoryDetail(ctx, svc, func(s *memoryAnalyticsService) (*MemoryList[int], error) {
		var value interface{}
		err := s.db.GetContext(ctx, &value, `SELECT pg_sleep(6)`)
		return &MemoryList[int]{Items: []int{123}}, err
	})
	require.Nil(t, result, "no partial page after timeout")
	var timeout *MemoryDetailTimeoutError
	require.ErrorAs(t, err, &timeout)
	result, err = memoryDetail(ctx, svc, func(s *memoryAnalyticsService) (*MemoryList[int], error) {
		var value int
		if getErr := s.db.GetContext(ctx, &value, `SELECT 42`); getErr != nil {
			return nil, getErr
		}
		return &MemoryList[int]{Items: []int{value}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, []int{42}, result.Items)
}

func TestMemoryAnalyticsDetailRecordPrivacy(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memories WHERE workspace_id=$1`, f.ws) })
	for _, scope := range []string{"agent", "project"} {
		_, err := db.Exec(`INSERT INTO memories
			(id,workspace_id,project_id,agent_id,key,content,scope,tags,status,content_simhash)
			VALUES ($1,$2,$3,$4,$5,'very secret content',$6,'{kind:guide}','active',123456789)`,
			uuid.New(), f.ws, f.shared, f.agent, scope+"-private-test-key", scope)
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: now.AddDate(0, 0, -7), To: now, Now: now,
		Viewer: AnalyticsViewer{ActorID: f.sharedMember}}
	svc := NewMemoryAnalyticsService(db)
	member, err := svc.Duplicates(context.Background(), filter, MemoryListParams{})
	require.NoError(t, err)
	require.Len(t, member.Items, 2)
	for _, row := range member.Items {
		require.NotNil(t, row.Kind)
		require.Equal(t, "guide", *row.Kind)
		if row.Scope == "agent" {
			require.Nil(t, row.Key)
			require.True(t, row.Redacted)
		} else {
			require.NotNil(t, row.Key)
			require.False(t, row.Redacted)
		}
	}
	encoded, err := json.Marshal(member)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "very secret content")
	require.NotContains(t, string(encoded), "agent-private-test-key")
	search, err := svc.Duplicates(context.Background(), filter, MemoryListParams{Search: "agent-private-test-key"})
	require.NoError(t, err)
	require.Empty(t, search.Items)
	for _, viewer := range []AnalyticsViewer{{AllProjects: true}, {Agent: true, ActorID: f.agent}} {
		filter.Viewer = viewer
		allowed, err := svc.Duplicates(context.Background(), filter, MemoryListParams{Search: "agent-private-test-key"})
		require.NoError(t, err)
		require.Len(t, allowed.Items, 1)
		require.NotNil(t, allowed.Items[0].Key)
		require.Equal(t, "agent-private-test-key", *allowed.Items[0].Key)
		require.False(t, allowed.Items[0].Redacted)
	}
}

func TestMemoryAnalyticsContractProjectAccess(t *testing.T) {
	db := testDB(t)
	fixture := seedAccessFixture(t, db)
	now := time.Now().UTC()
	filter := MemoryAnalyticsFilter{WorkspaceID: fixture.ws, ProjectID: &fixture.hidden,
		From: utcDay(now).AddDate(0, 0, -6), To: now, Now: now,
		Viewer: AnalyticsViewer{ActorID: fixture.sharedMember}}
	svc := NewMemoryAnalyticsService(db)
	result, err := svc.Get(context.Background(), filter)
	require.Nil(t, result, "a hidden project's counts must not be returned")
	var forbidden *apierror.Error
	require.True(t, errors.As(err, &forbidden))
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	// Positive control: the same viewer can query their own project.
	filter.ProjectID = &fixture.shared
	result, err = svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestMemoryAnalyticsContractVisibleCounts(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	yesterday := utcDay(now).AddDate(0, 0, -1)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, f.ws)
		_, _ = db.Exec(`DELETE FROM memory_stats_daily WHERE workspace_id=$1`, f.ws)
	})
	for i, project := range []uuid.UUID{f.shared, f.hidden} {
		_, err := db.Exec(`INSERT INTO memory_stats_daily
			(day,workspace_id,project_id,agent_id,caller_kind,op,source,reads,hits,useful,misses,served_rows,served_superseded,response_chars)
			VALUES ($1,$2,$3,$4,'agent_tool','recall','*',$5,0,0,0,0,0,0)`, yesterday, f.ws, project, f.agent, (i+1)*10)
		require.NoError(t, err)
		for j := 0; j <= i; j++ {
			_, err := db.Exec(`INSERT INTO memory_read_events
				(id,ts,workspace_id,project_id,agent_id,caller_kind,route,user_agent,op,result_count,pinned_count,superseded_count,results,response_chars)
				VALUES ($1,$2,$3,$4,$5,'agent_tool','/recall','contract','recall',0,0,0,'[]',0)`,
				uuid.New(), now.Add(-time.Second), f.ws, project, f.agent)
			require.NoError(t, err)
		}
	}
	// A closed day's raw event remains retained for drill-downs. The summary
	// must use the daily number instead of counting this event a second time.
	_, err := db.Exec(`INSERT INTO memory_read_events
		(id,ts,workspace_id,project_id,agent_id,caller_kind,route,user_agent,op,result_count,pinned_count,superseded_count,results,response_chars)
		VALUES ($1,$2,$3,$4,$5,'agent_tool','/recall','contract','recall',0,0,0,'[]',0)`,
		uuid.New(), yesterday.Add(time.Hour), f.ws, f.shared, f.agent)
	require.NoError(t, err)
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: yesterday, To: now, Now: now,
		GroupBy: "project", Viewer: AnalyticsViewer{ActorID: f.sharedMember}}
	svc := NewMemoryAnalyticsService(db)
	member, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 11, *member.KPIs.Reads)
	require.EqualValues(t, 11, *member.Totals.Reads)
	require.Len(t, member.Rows, 1)
	require.Equal(t, f.shared.String(), *member.Rows[0].Key)
	require.EqualValues(t, 10, *member.Series[0].Reads)
	require.EqualValues(t, 1, *member.Series[1].Reads)
	require.NotEmpty(t, member.Coverage.CallerKind)
	require.EqualValues(t, 11, member.Coverage.CallerKind[0].Reads)
	filter.Viewer = AnalyticsViewer{AllProjects: true}
	admin, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 33, *admin.KPIs.Reads, "positive control: hidden rows are present and visible to an admin")
	require.Len(t, admin.Rows, 2)
}

func TestMemoryAnalyticsContractRecallRates(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	day := utcDay(now).AddDate(0, 0, -1)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_stats_daily WHERE workspace_id=$1`, f.ws) })
	// U4 stores call counters on '*' and served-row counters on each result
	// source. Errors are volume, but do not belong to the recall denominator.
	for _, row := range []struct {
		caller, op, source                              string
		reads, errors, hits, useful, served, superseded int
		chars                                           int
	}{
		{"agent_tool", "recall", "*", 4, 2, 1, 1, 0, 0, 80},
		{"agent_tool", "recall", "memory", 0, 0, 0, 0, 10, 2, 0},
		{"agent_tool", "recall", "doc", 0, 0, 0, 0, 5, 1, 0},
		{"unknown", "recall", "*", 10, 0, 10, 10, 0, 0, 400},
		{"agent_tool", "get_memory", "memory", 3, 0, 0, 0, 0, 0, 120},
	} {
		_, err := db.Exec(`INSERT INTO memory_stats_daily
			(day,workspace_id,project_id,agent_id,caller_kind,op,source,reads,errors,hits,useful,misses,served_rows,served_superseded,response_chars)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,0,$12,$13,$14)`,
			day, f.ws, f.shared, f.agent, row.caller, row.op, row.source, row.reads, row.errors, row.hits, row.useful, row.served, row.superseded, row.chars)
		require.NoError(t, err)
	}
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: day, To: utcDay(now), Now: now,
		Ops: []string{"read"}, GroupBy: "agent", Viewer: AnalyticsViewer{ActorID: f.sharedMember}}
	svc := NewMemoryAnalyticsService(db)
	result, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 17, *result.KPIs.Reads)
	require.Nil(t, result.Previous.Reads)
	require.Equal(t, MemoryReasonNoJournalBefore, result.Reasons["previous_kpis.reads"])
	require.InDelta(t, 0.5, *result.KPIs.HitRate, 0.000001)
	require.InDelta(t, 1, *result.KPIs.UsefulRate, 0.000001)
	require.InDelta(t, 0.2, *result.KPIs.SupersededServedRate, 0.000001)
	require.EqualValues(t, 150, *result.KPIs.TokensEst)
	require.EqualValues(t, 1, *result.Series[0].Hits)
	require.EqualValues(t, 17, *result.Series[0].Reads)
	require.EqualValues(t, 14, result.Series[0].ReadsByOp["recall"])
	require.EqualValues(t, 3, result.Series[0].ReadsByOp["get_memory"])
	require.EqualValues(t, 10, result.Series[0].ServedRowsBySource["memory"])
	require.EqualValues(t, 5, result.Series[0].ServedRowsBySource["doc"])
	require.NotContains(t, result.Series[0].ServedRowsBySource, "*")
	require.InDelta(t, 1, *result.Series[0].UsefulRate, 0.000001)
	require.Len(t, result.Rows, 1)
	require.EqualValues(t, 17, *result.Rows[0].Reads)
	require.InDelta(t, 0.5, *result.Rows[0].HitRate, 0.000001)
	require.EqualValues(t, 1, *result.Totals.Hits)
	filter.GroupBy = "source"
	bySource, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	var reads int64
	for _, row := range bySource.Rows {
		reads += *row.Reads
		require.NotNil(t, row.Key)
		if *row.Key == "*" {
			require.Equal(t, "поиск, несколько источников", row.Label)
			require.InDelta(t, 0.5, *row.HitRate, 0.000001)
			require.Nil(t, row.ServedRows)
			require.Equal(t, MemoryReasonNotApplicable, row.Reasons["served_rows"])
		} else {
			require.Nil(t, row.Hits)
			require.Nil(t, row.UsefulRate)
			require.Equal(t, MemoryReasonNotApplicable, row.Reasons["useful_rate"])
			require.NotNil(t, row.ServedRows)
			require.InDelta(t, 0.2, *row.SupersededServedRate, 0.000001)
		}
	}
	require.EqualValues(t, 17, reads, "source rows never double-count calls")
	filter.CallerKinds = []string{"unknown"}
	explicit, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 10, *explicit.KPIs.Reads)
	require.InDelta(t, 1, *explicit.KPIs.HitRate, 0.000001, "an explicit caller overrides the agent_tool default")
	filter.CallerKinds = nil
	filter.Sources = []string{"memory"}
	filtered, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 3, *filtered.KPIs.Reads, "source filter retains direct calls, never attributes mixed recall calls")
	for _, name := range []string{"hit_rate", "useful_rate", "miss_rate"} {
		require.Equal(t, MemoryReasonNotApplicable, filtered.Reasons[name])
	}
	require.Nil(t, filtered.KPIs.HitRate)
	require.Nil(t, filtered.KPIs.UsefulRate)
	require.Nil(t, filtered.KPIs.MissRate)
	require.InDelta(t, 0.2, *filtered.KPIs.SupersededServedRate, 0.000001)
	require.EqualValues(t, 3, *filtered.Series[0].Reads)
	require.Nil(t, filtered.Series[0].UsefulRate)
	require.Nil(t, filtered.Totals.Hits)
	require.Equal(t, MemoryReasonNotApplicable, filtered.Series[0].Reasons["useful_rate"])
	filter.Sources = nil
	previousTo := utcDay(now)
	filter.PreviousFrom, filter.PreviousTo = &day, &previousTo
	comparison, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.Equal(t, day, comparison.PreviousPeriod.From)
	require.Equal(t, previousTo, comparison.PreviousPeriod.To)
	require.EqualValues(t, 17, *comparison.Previous.Reads, "comparison uses the explicit window, with the same visibility and rate rules")
	require.InDelta(t, 0.5, *comparison.Previous.HitRate, 0.000001)
}

// U7 fills misses and writes misses_reason NULL; a day written before U7 keeps
// misses_reason = 'not_collected' (the column default) until it is rewritten.
func TestMemoryAnalyticsContractMissRate(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	collected := utcDay(now).AddDate(0, 0, -2)
	legacy := utcDay(now).AddDate(0, 0, -1)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_stats_daily WHERE workspace_id=$1`, f.ws) })
	insert := func(day time.Time, caller, op, source string, reads, errors, misses int, reason *string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO memory_stats_daily
			(day,workspace_id,project_id,agent_id,caller_kind,op,source,reads,errors,hits,useful,misses,misses_reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,0,$10,$11)`,
			day, f.ws, f.shared, f.agent, caller, op, source, reads, errors, misses, reason)
		require.NoError(t, err)
	}
	notCollected := MemoryReasonNotCollected
	// Collected day: 3 misses over 10-2 successful agent recalls. The unknown
	// caller is outside the default rate filter and must not enter the ratio.
	insert(collected, "agent_tool", "recall", "*", 10, 2, 3, nil)
	insert(collected, "agent_tool", "recall", "memory", 0, 0, 0, nil)
	insert(collected, "unknown", "recall", "*", 50, 0, 40, &notCollected)
	insert(collected, "agent_tool", "get_memory", "memory", 5, 0, 0, &notCollected)
	// Pre-U7 day: the rollup stored misses_reason = 'not_collected'.
	insert(legacy, "agent_tool", "recall", "*", 6, 0, 0, &notCollected)

	svc := NewMemoryAnalyticsService(db)
	viewer := AnalyticsViewer{ActorID: f.sharedMember}
	got, err := svc.Get(context.Background(), MemoryAnalyticsFilter{WorkspaceID: f.ws,
		From: collected, To: legacy, Now: now, Viewer: viewer})
	require.NoError(t, err)
	require.NotNil(t, got.KPIs.MissRate, "reason=%q", got.Reasons["miss_rate"])
	require.InDelta(t, 3.0/8.0, *got.KPIs.MissRate, 0.000001)
	require.NotContains(t, got.Reasons, "miss_rate")

	explicit, err := svc.Get(context.Background(), MemoryAnalyticsFilter{WorkspaceID: f.ws,
		From: collected, To: legacy, Now: now, Viewer: viewer, CallerKinds: []string{"unknown"}})
	require.NoError(t, err)
	require.Nil(t, explicit.KPIs.MissRate, "a caller whose rows predate U7 has no miss rate")
	require.Equal(t, MemoryReasonNotCollected, explicit.Reasons["miss_rate"])

	// A window that includes a pre-U7 day cannot produce a partial ratio.
	mixed, err := svc.Get(context.Background(), MemoryAnalyticsFilter{WorkspaceID: f.ws,
		From: collected, To: utcDay(now), Now: now, Viewer: viewer})
	require.NoError(t, err)
	require.Nil(t, mixed.KPIs.MissRate)
	require.Equal(t, MemoryReasonNotCollected, mixed.Reasons["miss_rate"])
	require.NotNil(t, mixed.KPIs.HitRate, "only miss_rate depends on misses_reason")

	// Collected rows with no successful recall: no data, not "not collected".
	empty := utcDay(now).AddDate(0, 0, -4)
	insert(empty, "agent_tool", "recall", "*", 2, 2, 0, nil)
	none, err := svc.Get(context.Background(), MemoryAnalyticsFilter{WorkspaceID: f.ws,
		From: empty, To: empty.AddDate(0, 0, 1), Now: now, Viewer: viewer})
	require.NoError(t, err)
	require.Nil(t, none.KPIs.MissRate)
	require.Equal(t, MemoryReasonNoData, none.Reasons["miss_rate"])
}

func TestMemoryAnalyticsContractDeadSnapshot(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	day := utcDay(now).AddDate(0, 0, -1)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_base_daily WHERE workspace_id=$1`, f.ws) })
	for i, project := range []uuid.UUID{f.shared, f.hidden} {
		_, err := db.Exec(`INSERT INTO memory_base_daily
			(day,workspace_id,project_id,total,active,archived,superseded_active,conflicted,no_kind,statuslog_like,dup_groups,dup_rows,dead30,dead90,eligible30,eligible90)
			VALUES ($1,$2,$3,100,100,0,0,0,0,0,0,0,$4,NULL,$5,NULL)`,
			day, f.ws, project, 2+i*4, 8+i*4)
		require.NoError(t, err)
	}
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: day, To: utcDay(now), Now: now,
		Viewer: AnalyticsViewer{ActorID: f.sharedMember}}
	svc := NewMemoryAnalyticsService(db)
	result, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.NotNil(t, result.KPIs.Dead30)
	require.InDelta(t, 0.25, *result.KPIs.Dead30, 0.000001, "denominator is the historical eligible30, even with no current memories")
	require.EqualValues(t, 2, *result.Quality.Dead30)
	require.Nil(t, result.Quality.Dead90)
	require.Equal(t, MemoryReasonNoJournalBefore, result.Reasons["quality.dead90"])
	require.NotContains(t, result.Reasons, "dead30")
	filter.Viewer = AnalyticsViewer{AllProjects: true}
	admin, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.InDelta(t, 0.4, *admin.KPIs.Dead30, 0.000001, "hidden project snapshots are available only to admin")
	_, err = db.Exec(`UPDATE memory_base_daily SET dead30=NULL,eligible30=NULL WHERE workspace_id=$1 AND project_id=$2`, f.ws, f.shared)
	require.NoError(t, err)
	missing, err := svc.Get(context.Background(), filter)
	require.NoError(t, err)
	require.Nil(t, missing.KPIs.Dead30, "a partly uncovered snapshot cannot produce a partial ratio")
	require.Equal(t, MemoryReasonNoJournalBefore, missing.Reasons["dead30"])
}

// Dead-records and duplicates list the current set every read path serves:
// review_needed, stale and conflicted records are live; superseded, archived
// and expired (status 'archived') records are not.
func TestMemoryAnalyticsContractLiveRecordSet(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -40)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, f.ws)
		_, _ = db.Exec(`DELETE FROM memories WHERE workspace_id=$1`, f.ws)
	})
	_, err := db.Exec(`INSERT INTO memory_read_events
		(id,ts,workspace_id,project_id,agent_id,caller_kind,route,user_agent,op,result_count,pinned_count,superseded_count,results,response_chars)
		VALUES ($1,$2,$3,$4,$5,'agent_tool','/recall','contract','recall',0,0,0,'[]',0)`,
		uuid.New(), old, f.ws, f.shared, f.agent)
	require.NoError(t, err, "journal starts before the 30-day window")
	live := map[uuid.UUID]string{}
	for i, rec := range []struct {
		status   string
		archived bool
	}{
		{"active", false}, {"review_needed", false}, {"review_needed", false}, {"stale", false}, {"conflicted", false},
		{"superseded", false}, {"archived", false}, {"active", true},
	} {
		id := uuid.New()
		_, err = db.Exec(`INSERT INTO memories
			(id,workspace_id,project_id,agent_id,key,content,scope,tags,status,archived,content_simhash,created_at)
			VALUES ($1,$2,$3,$4,$5,'live set contract','project','{kind:guide}',$6,$7,424242,$8)`,
			id, f.ws, f.shared, f.agent, "live-set-key-"+strconv.Itoa(i), rec.status, rec.archived, old)
		require.NoError(t, err)
		if rec.status != "superseded" && rec.status != "archived" && !rec.archived {
			live[id] = rec.status
		}
	}
	filter := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: now.AddDate(0, 0, -7), To: now, Now: now,
		Viewer: AnalyticsViewer{AllProjects: true}}
	svc := NewMemoryAnalyticsService(db)
	ids := func(rows []MemoryRecordRow) map[uuid.UUID]string {
		got := map[uuid.UUID]string{}
		for _, row := range rows {
			got[row.MemoryID] = live[row.MemoryID]
		}
		return got
	}
	dead, err := svc.DeadRecords(context.Background(), filter, MemoryListParams{})
	require.NoError(t, err)
	require.Empty(t, dead.Reason)
	require.EqualValues(t, len(live), dead.Total)
	require.Equal(t, live, ids(dead.Items), "dead-records = live, old, unserved records incl. review_needed")
	dups, err := svc.Duplicates(context.Background(), filter, MemoryListParams{})
	require.NoError(t, err)
	require.EqualValues(t, len(live), dups.Total)
	require.Equal(t, live, ids(dups.Items), "duplicates group the live set incl. review_needed")
	for _, row := range dups.Items {
		require.EqualValues(t, len(live), row.GroupSize)
	}
}
