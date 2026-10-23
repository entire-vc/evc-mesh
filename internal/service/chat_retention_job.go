package service

import (
	"context"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// chatRetentionBatch bounds one pass so a large backlog never holds a worker.
const chatRetentionBatch = 200

// ChatRetentionJob runs the two periodic halves of chat deletion: threads past
// their workspace's history retention are soft-deleted, and threads soft-deleted
// more than 30 days ago are purged (rows and S3 objects).
type ChatRetentionJob struct {
	repo     *postgres.ChatThreadDeleteRepo
	interval time.Duration
}

func NewChatRetentionJob(repo *postgres.ChatThreadDeleteRepo, interval time.Duration) *ChatRetentionJob {
	if interval <= 0 {
		interval = time.Hour
	}
	return &ChatRetentionJob{repo: repo, interval: interval}
}

// RunOnce does one expire + purge pass; errors are logged and the other half
// still runs.
func (j *ChatRetentionJob) RunOnce(ctx context.Context) {
	if n, err := j.repo.ExpireHistory(ctx, chatRetentionBatch); err != nil {
		log.Printf("chat retention: expire history: %v", err)
	} else if n > 0 {
		log.Printf("chat retention: soft-deleted %d thread(s) past history retention", n)
	}
	if res, err := j.repo.PurgeDeleted(ctx, chatRetentionBatch); err != nil {
		log.Printf("chat retention: purge: %v", err)
	} else if res.Threads > 0 {
		log.Printf("chat retention: purged %d thread(s), %d object(s)", res.Threads, res.Objects)
	}
}

// Run blocks until ctx is done, running a pass every interval.
func (j *ChatRetentionJob) Run(ctx context.Context) {
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
