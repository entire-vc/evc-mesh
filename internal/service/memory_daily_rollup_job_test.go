package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeRollupStore struct {
	since       *time.Time
	written     map[time.Time]bool
	pending     map[time.Time]int64
	statsWrites []time.Time
	baseWrites  []time.Time
	baseSince   []*time.Time

	sinceErr, writtenErr, pendingErr, statsErr, baseErr error
	sinceCalls                                          int
	onSince                                             func(calls int)
}

func (s *fakeRollupStore) JournalSince(context.Context) (*time.Time, error) {
	s.sinceCalls++
	if s.onSince != nil {
		s.onSince(s.sinceCalls)
	}
	return s.since, s.sinceErr
}

func (s *fakeRollupStore) StatsWrittenDays(_ context.Context, from, to time.Time) (map[time.Time]bool, error) {
	if s.writtenErr != nil {
		return nil, s.writtenErr
	}
	out := map[time.Time]bool{}
	for d := range s.written {
		if !d.Before(from) && d.Before(to) {
			out[d] = true
		}
	}
	return out, nil
}

func (s *fakeRollupStore) PendingUsageReads(_ context.Context, day time.Time) (int64, error) {
	return s.pending[day], s.pendingErr
}

func (s *fakeRollupStore) WriteStatsDay(_ context.Context, day time.Time) (int64, error) {
	if s.statsErr != nil {
		return 0, s.statsErr
	}
	s.statsWrites = append(s.statsWrites, day)
	if s.written == nil {
		s.written = map[time.Time]bool{}
	}
	s.written[day] = true
	return 1, nil
}

func (s *fakeRollupStore) WriteBaseDay(_ context.Context, day time.Time, since *time.Time) (n int64, wrote bool, err error) {
	s.baseWrites = append(s.baseWrites, day)
	s.baseSince = append(s.baseSince, since)
	if s.baseErr != nil {
		return 0, false, s.baseErr
	}
	return 1, true, nil
}

func utcDate(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func runRollupAt(store *fakeRollupStore, now time.Time) {
	newMemoryDailyRollupJob(store, time.Minute, func() time.Time { return now }).RunOnce(context.Background())
}

func TestMemoryDailyRollupJob_CatchesUpClosedDaysOnce(t *testing.T) {
	since := time.Date(2026, 11, 17, 7, 15, 0, 0, time.UTC)
	store := &fakeRollupStore{since: &since}
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

	runRollupAt(store, now)
	require.Equal(t, []time.Time{utcDate(2026, 11, 17), utcDate(2026, 11, 18), utcDate(2026, 11, 19)}, store.statsWrites,
		"every closed day since the journal starts; never today")
	require.Equal(t, []time.Time{utcDate(2026, 11, 19)}, store.baseWrites, "base snapshot of yesterday only")
	require.Equal(t, &since, store.baseSince[0])

	runRollupAt(store, now.Add(10*time.Minute))
	require.Len(t, store.statsWrites, 3, "a written day is not written again")
}

func TestMemoryDailyRollupJob_WaitsForFinalUsage(t *testing.T) {
	since := utcDate(2026, 11, 19)
	yesterday := utcDate(2026, 11, 19)
	store := &fakeRollupStore{since: &since, pending: map[time.Time]int64{yesterday: 2}}

	runRollupAt(store, time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC))
	require.Empty(t, store.statsWrites, "recall usage of the day is not final yet")

	store.pending[yesterday] = 0
	runRollupAt(store, time.Date(2026, 11, 20, 2, 30, 0, 0, time.UTC))
	require.Equal(t, []time.Time{yesterday}, store.statsWrites)
}

func TestMemoryDailyRollupJob_WaitsForTheMissWindow(t *testing.T) {
	since := utcDate(2026, 11, 19)
	yesterday := utcDate(2026, 11, 19)
	store := &fakeRollupStore{since: &since}

	runRollupAt(store, time.Date(2026, 11, 20, 2, 4, 0, 0, time.UTC))
	require.Empty(t, store.statsWrites, "reads of the miss window after the day may still be on their way")
	runRollupAt(store, time.Date(2026, 11, 20, 2, 5, 0, 0, time.UTC))
	require.Equal(t, []time.Time{yesterday}, store.statsWrites, "written MemoryRollupMissSettle after the day end")
}

func TestMemoryDailyRollupJob_ForcesAStuckDay(t *testing.T) {
	since := utcDate(2026, 11, 19)
	yesterday := utcDate(2026, 11, 19)
	store := &fakeRollupStore{since: &since, pending: map[time.Time]int64{yesterday: 1}}

	runRollupAt(store, time.Date(2026, 11, 20, 11, 59, 0, 0, time.UTC))
	require.Empty(t, store.statsWrites)
	runRollupAt(store, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	require.Equal(t, []time.Time{yesterday}, store.statsWrites, "written MemoryRollupForceAfter after the day end")
}

func TestMemoryDailyRollupJob_LookbackAndNoJournal(t *testing.T) {
	store := &fakeRollupStore{}
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	runRollupAt(store, now)
	require.Empty(t, store.statsWrites, "no journal: no stats days")
	require.Equal(t, []time.Time{utcDate(2026, 11, 19)}, store.baseWrites, "the base snapshot does not need the journal")
	require.Nil(t, store.baseSince[0])

	since := utcDate(2026, 1, 1)
	store = &fakeRollupStore{since: &since}
	runRollupAt(store, now)
	require.Equal(t, utcDate(2026, 8, 23), store.statsWrites[0], "older days have lost raw rows to the 90-day purge")
	require.Equal(t, utcDate(2026, 11, 19), store.statsWrites[len(store.statsWrites)-1])
}

func TestMemoryDailyRollupJob_StoreErrorsStopThePass(t *testing.T) {
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	boom := errors.New("boom")
	since := utcDate(2026, 11, 17)

	// No journal start: nothing is written, not even the base snapshot.
	store := &fakeRollupStore{since: &since, sinceErr: boom}
	runRollupAt(store, now)
	require.Empty(t, store.baseWrites)
	require.Empty(t, store.statsWrites)

	// A failed base snapshot does not stop the stats days.
	store = &fakeRollupStore{since: &since, baseErr: boom}
	runRollupAt(store, now)
	require.Len(t, store.statsWrites, 3)

	// Unknown written days: no stats write, the next pass retries.
	store = &fakeRollupStore{since: &since, writtenErr: boom}
	runRollupAt(store, now)
	require.Empty(t, store.statsWrites)

	// A day whose pending usage cannot be read stops the pass at that day.
	store = &fakeRollupStore{since: &since, pendingErr: boom}
	runRollupAt(store, now)
	require.Empty(t, store.statsWrites)

	// A failed stats write stops the pass: later days are not written past it.
	store = &fakeRollupStore{since: &since, statsErr: boom}
	runRollupAt(store, now)
	require.Empty(t, store.written)
}

func TestMemoryDailyRollupJob_DefaultInterval(t *testing.T) {
	require.Equal(t, MemoryRollupInterval, NewMemoryDailyRollupJob(nil, 0).interval)
	require.Equal(t, time.Second, NewMemoryDailyRollupJob(nil, time.Second).interval)
}

func TestMemoryDailyRollupJob_RunPassesOnTicksUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeRollupStore{onSince: func(calls int) {
		if calls == 3 {
			cancel()
		}
	}}
	done := make(chan struct{})
	go func() {
		newMemoryDailyRollupJob(store, time.Millisecond, time.Now).Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	require.GreaterOrEqual(t, store.sinceCalls, 3, "one pass at start, then one per tick until the cancel")
}
