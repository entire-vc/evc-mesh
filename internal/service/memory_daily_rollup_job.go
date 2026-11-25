package service

import (
	"context"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Timing of MemoryDailyRollupJob.
const (
	// MemoryRollupInterval is how often the job looks for closed days to write.
	MemoryRollupInterval = 10 * time.Minute
	// MemoryRollupForceAfter: a closed day whose recall usage is still not
	// final this long after its end is written anyway (the usage job is
	// behind or off); the delay is logged.
	MemoryRollupForceAfter = 12 * time.Hour
	// MemoryRollupLookback bounds the catch-up: older days have lost raw rows
	// to the 90-day journal purge and are never rolled up.
	MemoryRollupLookback = 89 * 24 * time.Hour
	// MemoryRollupMissSettle: a closed day is not written before this long
	// after its end. A miss of a late read is decided by what its session does
	// within domain.MemoryMissWindow (U7), and reads of that window must reach
	// the journal (15 s batches) before their usage counts as pending.
	MemoryRollupMissSettle = domain.MemoryMissWindow + 5*time.Minute
)

// memoryDailyRollupStore is what the job needs from postgres.MemoryDailyRollupRepo.
type memoryDailyRollupStore interface {
	JournalSince(ctx context.Context) (*time.Time, error)
	StatsWrittenDays(ctx context.Context, from, to time.Time) (map[time.Time]bool, error)
	PendingUsageReads(ctx context.Context, day time.Time) (int64, error)
	WriteStatsDay(ctx context.Context, day time.Time) (int64, error)
	WriteBaseDay(ctx context.Context, day time.Time, journalSince *time.Time) (int64, bool, error)
}

// MemoryDailyRollupJob is the nightly writer of the memory aggregates (U4) run
// inside mesh-api, like the recall usage job:
//   - memory_base_daily: the snapshot of yesterday, taken on the first pass
//     after midnight UTC, once. Older missing days are not backfilled: a
//     snapshot of current memories would not describe them.
//   - memory_stats_daily: every closed day since the journal starts (at most
//     MemoryRollupLookback back) that is not written yet, as soon as all its
//     recall reads and those of the MemoryMissWindow after it have final
//     usage (U3, U7), and no earlier than MemoryRollupMissSettle after its
//     end; with usage still pending, MemoryRollupForceAfter after its end.
//     A written day is not written again by the job.
//
// It runs only with MEMORY_READ_LOG on.
type MemoryDailyRollupJob struct {
	store    memoryDailyRollupStore
	interval time.Duration
	now      func() time.Time
}

// NewMemoryDailyRollupJob creates the job; interval <= 0 means MemoryRollupInterval.
func NewMemoryDailyRollupJob(repo *postgres.MemoryDailyRollupRepo, interval time.Duration) *MemoryDailyRollupJob {
	return newMemoryDailyRollupJob(repo, interval, time.Now)
}

func newMemoryDailyRollupJob(store memoryDailyRollupStore, interval time.Duration, now func() time.Time) *MemoryDailyRollupJob {
	if interval <= 0 {
		interval = MemoryRollupInterval
	}
	return &MemoryDailyRollupJob{store: store, interval: interval, now: now}
}

// RunOnce makes one pass. Errors are logged; the next pass retries.
func (j *MemoryDailyRollupJob) RunOnce(ctx context.Context) {
	now := j.now().UTC()
	today := postgres.UTCDay(now)
	yesterday := today.AddDate(0, 0, -1)

	since, err := j.store.JournalSince(ctx)
	if err != nil {
		log.Printf("[memory-rollup] %v", err)
		return
	}

	if n, wrote, berr := j.store.WriteBaseDay(ctx, yesterday, since); berr != nil {
		log.Printf("[memory-rollup] base %s: %v", yesterday.Format(time.DateOnly), berr)
	} else if wrote {
		log.Printf("[memory-rollup] base %s: %d row(s)", yesterday.Format(time.DateOnly), n)
	}

	if since == nil {
		return
	}
	first := postgres.UTCDay(*since)
	if floor := postgres.UTCDay(now.Add(-MemoryRollupLookback)); first.Before(floor) {
		first = floor
	}
	written, err := j.store.StatsWrittenDays(ctx, first, today)
	if err != nil {
		log.Printf("[memory-rollup] %v", err)
		return
	}
	for day := first; day.Before(today); day = day.AddDate(0, 0, 1) {
		if written[day] {
			continue
		}
		if now.Before(day.AddDate(0, 0, 1).Add(MemoryRollupMissSettle)) {
			continue
		}
		pending, err := j.store.PendingUsageReads(ctx, day)
		if err != nil {
			log.Printf("[memory-rollup] stats %s: %v", day.Format(time.DateOnly), err)
			return
		}
		if pending > 0 {
			if now.Before(day.AddDate(0, 0, 1).Add(MemoryRollupForceAfter)) {
				continue
			}
			log.Printf("[memory-rollup] stats %s: %d recall read(s) still without final usage after %s, writing anyway",
				day.Format(time.DateOnly), pending, MemoryRollupForceAfter)
		}
		n, err := j.store.WriteStatsDay(ctx, day)
		if err != nil {
			log.Printf("[memory-rollup] stats %s: %v", day.Format(time.DateOnly), err)
			return
		}
		log.Printf("[memory-rollup] stats %s: %d row(s)", day.Format(time.DateOnly), n)
	}
}

// Run blocks until ctx is done: one pass at start, then one every interval.
func (j *MemoryDailyRollupJob) Run(ctx context.Context) {
	j.RunOnce(ctx)
	t := time.NewTicker(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.RunOnce(ctx)
		}
	}
}
