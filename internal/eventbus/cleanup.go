package eventbus

import (
	"context"
	"log"
	"time"
)

// maxCleanupIterations is the maximum number of batch deletions per cleanup cycle
// to prevent unbounded work in a single run.
const maxCleanupIterations = 100

// runCleanup is a background goroutine that periodically deletes expired events
// from PostgreSQL in batches.
func (eb *EventBus) runCleanup(ctx context.Context) {
	log.Printf("[eventbus/cleanup] Starting cleanup worker (interval=%s)", eb.cfg.CleanupInterval)

	ticker := time.NewTicker(eb.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[eventbus/cleanup] Shutting down")
			return
		case <-ticker.C:
			eb.cleanupExpired(ctx)
		}
	}
}

// cleanupExpired deletes expired events from PostgreSQL.
// It runs DeleteExpired in a loop until no more expired events are found
// or maxCleanupIterations is reached.
func (eb *EventBus) cleanupExpired(ctx context.Context) {
	var totalDeleted int64

	for i := 0; i < maxCleanupIterations; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		count, err := eb.repo.DeleteExpired(ctx)
		if err != nil {
			log.Printf("[eventbus/cleanup] ERROR: delete expired failed: %v", err)
			return
		}

		totalDeleted += count

		// If no more expired records were found, stop.
		if count == 0 {
			break
		}
	}

	if totalDeleted > 0 {
		log.Printf("[eventbus/cleanup] Deleted %d expired events", totalDeleted)
	}
}
