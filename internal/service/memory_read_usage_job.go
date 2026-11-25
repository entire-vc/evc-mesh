package service

import (
	"context"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// MemoryReadUsageJob attributes recall answers to their use
// (memory_read_usage, U3) off the request path: every interval it runs one
// MemoryReadUsageRepo.ProcessOnce. It runs only with MEMORY_READ_LOG on.
type MemoryReadUsageJob struct {
	repo     *postgres.MemoryReadUsageRepo
	interval time.Duration
	now      func() time.Time
}

// NewMemoryReadUsageJob creates the job; interval <= 0 means 5 minutes.
func NewMemoryReadUsageJob(repo *postgres.MemoryReadUsageRepo, interval time.Duration) *MemoryReadUsageJob {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &MemoryReadUsageJob{repo: repo, interval: interval, now: time.Now}
}

// RunOnce runs one attribution pass; an error is logged and the next pass
// retries the same reads (nothing is finalized on a failed batch).
func (j *MemoryReadUsageJob) RunOnce(ctx context.Context) {
	res, err := j.repo.ProcessOnce(ctx, j.now())
	if err != nil {
		log.Printf("memory read usage: %v", err)
	}
	if res.Inserted > 0 || res.Finalized > 0 {
		log.Printf("memory read usage: %d read(s) evaluated, %d usage row(s) added, %d read(s) finalized",
			res.Reads, res.Inserted, res.Finalized)
	}
}

// Run blocks until ctx is done: one pass at start, then one every interval.
func (j *MemoryReadUsageJob) Run(ctx context.Context) {
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
