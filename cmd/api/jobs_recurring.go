package main

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// startRecurringScheduler starts the recurring task scheduler and the task
// outbox.
func startRecurringScheduler(deps *apiDeps) {
	db := deps.db
	eventBusService := deps.eventBusService
	recurringService := deps.recurringService

	// 10. Start recurring task scheduler.
	schedulerShutdownCh := make(chan struct{})
	outboxCtx, cancelOutbox := context.WithCancel(context.Background())
	deps.atExit(cancelOutbox)
	outboxDone := make(chan struct{})
	if committedPublisher, ok := eventBusService.(interface {
		PublishCommitted(context.Context, *domain.EventBusMessage) error
	}); ok {
		go func() {
			defer close(outboxDone)
			service.RunTaskOutbox(outboxCtx, postgres.NewTaskOutboxRepo(db), committedPublisher.PublishCommitted)
		}()
	} else {
		close(outboxDone)
	}
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// actorctx.WithActor is required here, not optional: RunDue ->
				// createInstance -> taskSvc.Create writes the task.created
				// activity_log row atomically with the task itself, reading the
				// actor from this ctx. Without it, actor_type arrives empty at a
				// NOT NULL enum column with no DEFAULT — Postgres rejects the
				// INSERT (22P02), and the whole transaction rolls back, so EVERY
				// recurring schedule fails on its next tick. TaskService.Create
				// now also falls back to ActorTypeSystem on an empty actor type
				// (defense in depth for any other background caller), but this
				// ticker should still identify itself explicitly rather than
				// rely on that fallback.
				ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
				ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				count, err := recurringService.RunDue(ctx)
				cancel()
				if err != nil {
					log.Printf("[scheduler] ERROR running due schedules: %v", err)
				} else if count > 0 {
					log.Printf("[scheduler] Created %d recurring task instances", count)
				}
			case <-schedulerShutdownCh:
				log.Println("[scheduler] Shutting down")
				return
			}
		}
	}()
	log.Println("Recurring task scheduler started (60s interval)")

	deps.schedulerShutdownCh = schedulerShutdownCh
	deps.cancelOutbox = cancelOutbox
	deps.outboxDone = outboxDone
}
