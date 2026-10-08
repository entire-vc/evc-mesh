package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type taskOutboxDelivery interface {
	DeliverNext(context.Context, func(context.Context, *domain.EventBusMessage) error) (bool, error)
}

// RunTaskOutbox processes at most 32 rows per tick, with a ten-second bound for
// each delivery. The scheduler context stops it on API shutdown.
func RunTaskOutbox(ctx context.Context, repo taskOutboxDelivery, publish func(context.Context, *domain.EventBusMessage) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for range 32 {
			deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			found, err := repo.DeliverNext(deliveryCtx, publish)
			cancel()
			if err != nil {
				log.Printf("[task-outbox] delivery remains pending: %v", err)
				break
			}
			if !found {
				break
			}
		}
	}
}

// PublishCommitted never generates a new ID or falls back to success on a
// transport error. The feed row already committed with the task mutation.
func (s *eventBusService) PublishCommitted(ctx context.Context, msg *domain.EventBusMessage) error {
	publisher, ok := s.publisher.(interface {
		PublishCommittedEvent(context.Context, *domain.EventBusMessage, string, string) error
	})
	if !ok {
		return fmt.Errorf("committed event transport unavailable")
	}
	ws, project, err := s.resolveSlugs(ctx, msg.WorkspaceID, msg.ProjectID)
	if err != nil {
		return err
	}
	return publisher.PublishCommittedEvent(ctx, msg, ws, project)
}
