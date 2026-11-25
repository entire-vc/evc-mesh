package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// A closed day whose memory_stats_daily rows are not written yet (the nightly
// rollup writes it at the earliest MemoryRollupMissSettle after its end) is
// read from the raw journal, like today: from 00:00Z until the write the
// widget used to show zero reads for the whole of yesterday.
func TestMemoryAnalyticsUnwrittenClosedDayReadsRawJournal(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	ctx := context.Background()
	// A private "now" in 2000-2010: the marker table memory_daily_rollups is
	// global per day, so a day no other test writes keeps this test isolated.
	id := uuid.New()
	today := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, (int(id[0])<<8|int(id[1]))%4000)
	now := today.Add(80 * time.Minute) // 01:20Z, before the 02:05Z rollup
	yesterday := today.AddDate(0, 0, -1)
	before := today.AddDate(0, 0, -2)
	clean := func() {
		_, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, f.ws)
		for _, d := range []time.Time{before, yesterday} {
			_, _ = db.Exec(`DELETE FROM memory_stats_daily WHERE day = $1::date`, d.Format(time.DateOnly))
			_, _ = db.Exec(`DELETE FROM memory_daily_rollups WHERE day = $1::date`, d.Format(time.DateOnly))
		}
	}
	clean()
	t.Cleanup(clean)

	ev := func(ts time.Time) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO memory_read_events
			(id, ts, workspace_id, project_id, agent_id, caller_kind, route, user_agent, op,
			 query_hash, query_text, result_count, pinned_count, superseded_count, results, response_chars)
			VALUES ($1,$2,$3,$4,$5,'agent_tool','/memory','unwritten-day-test','get_memory',NULL,NULL,1,0,0,'[]',10)`,
			uuid.New(), ts, f.ws, f.shared, f.agent)
		require.NoError(t, err)
	}
	for i := 0; i < 3; i++ {
		ev(before.Add(time.Duration(i+1) * time.Hour))
	}
	for i := 0; i < 5; i++ {
		ev(yesterday.Add(time.Duration(4*i+1) * time.Hour))
	}
	ev(today.Add(10 * time.Minute))
	ev(today.Add(20 * time.Minute))

	repo := postgres.NewMemoryDailyRollupRepo(db)
	_, err := repo.WriteStatsDay(ctx, before) // the day before yesterday is aggregated
	require.NoError(t, err)
	written, err := repo.StatsWrittenDays(ctx, before, today)
	require.NoError(t, err)
	require.True(t, written[before])
	require.False(t, written[yesterday], "yesterday must still be unwritten")

	svc := NewMemoryAnalyticsService(db)
	viewer := AnalyticsViewer{AllProjects: true}
	yesterdayOnly := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: yesterday, To: today, Now: now, GroupBy: "agent", Viewer: viewer}
	week := MemoryAnalyticsFilter{WorkspaceID: f.ws, From: today.AddDate(0, 0, -6), To: now, Now: now, Viewer: viewer}

	check := func(stage string) {
		t.Helper()
		got, gerr := svc.Get(ctx, yesterdayOnly)
		require.NoError(t, gerr)
		require.NotNil(t, got.KPIs.Reads, stage)
		require.EqualValues(t, 5, *got.KPIs.Reads, "%s: ?from=yesterday&to=today reads = yesterday's journal rows", stage)
		require.NotEmpty(t, got.Coverage.CallerKind, stage)
		require.NotEmpty(t, got.Rows, stage)
		var rowReads int64
		for _, row := range got.Rows {
			if row.Reads != nil {
				rowReads += *row.Reads
			}
		}
		require.EqualValues(t, 5, rowReads, "%s: breakdown rows", stage)

		got, gerr = svc.Get(ctx, week)
		require.NoError(t, gerr)
		require.NotNil(t, got.KPIs.Reads, stage)
		require.EqualValues(t, 3+5+2, *got.KPIs.Reads, "%s: 7-day window = aggregate + unwritten day + today", stage)
		perDay := map[string]int64{}
		for _, p := range got.Series {
			if p.Reads != nil {
				perDay[p.Day] = *p.Reads
			}
		}
		require.EqualValues(t, 3, perDay[before.Format(time.DateOnly)], "%s: series, day before yesterday", stage)
		require.EqualValues(t, 5, perDay[yesterday.Format(time.DateOnly)], "%s: series, yesterday", stage)
		require.EqualValues(t, 2, perDay[today.Format(time.DateOnly)], "%s: series, today", stage)
	}
	check("before the nightly write")

	_, err = repo.WriteStatsDay(ctx, yesterday)
	require.NoError(t, err)
	written, err = repo.StatsWrittenDays(ctx, yesterday, today)
	require.NoError(t, err)
	require.True(t, written[yesterday])
	check("after the nightly write")
}
