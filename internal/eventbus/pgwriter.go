package eventbus

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// runPGWriter is a background goroutine that reads events from the NATS
// MESH_EVENTS stream via a durable pull consumer and upserts them into
// PostgreSQL. This ensures all events are persisted even if the inline
// write in PublishEvent failed.
func (eb *EventBus) runPGWriter(ctx context.Context) {
	log.Println("[eventbus/pgwriter] Starting PG writer worker")

	// Create (or attach to) the pg-writer durable consumer.
	consumer, err := eb.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       PGWriterConsumer,
		FilterSubject: SubjectWildcard,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    10,
	})
	if err != nil {
		log.Printf("[eventbus/pgwriter] ERROR: failed to create consumer: %v", err)
		return
	}

	batchSize := eb.cfg.PGWriterBatchSize
	fetchWait := eb.cfg.PGWriterInterval

	for {
		select {
		case <-ctx.Done():
			log.Println("[eventbus/pgwriter] Shutting down")
			return
		default:
		}

		// Fetch a batch of messages from NATS.
		msgs, err := consumer.Fetch(batchSize,
			jetstream.FetchMaxWait(fetchWait),
		)
		if err != nil {
			// Context cancelled during shutdown is expected.
			if ctx.Err() != nil {
				return
			}
			log.Printf("[eventbus/pgwriter] Fetch error: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		for natsMsg := range msgs.Messages() {
			eb.processPGWriterMessage(ctx, natsMsg)
		}

		if fetchErr := msgs.Error(); fetchErr != nil {
			if ctx.Err() != nil {
				return
			}
			// Log non-timeout errors. Timeouts are normal when no messages are available.
			log.Printf("[eventbus/pgwriter] Batch error: %v", fetchErr)
		}
	}
}

// processPGWriterMessage deserializes a NATS message into an EventBusMessage
// and upserts it into PostgreSQL. On success, the message is acked.
func (eb *EventBus) processPGWriterMessage(ctx context.Context, natsMsg jetstream.Msg) {
	var msg domain.EventBusMessage
	if err := json.Unmarshal(natsMsg.Data(), &msg); err != nil {
		log.Printf("[eventbus/pgwriter] ERROR: failed to unmarshal message: %v", err)
		// Ack the message to avoid redelivery of malformed data.
		_ = natsMsg.Ack()
		return
	}

	if err := eb.repo.Create(ctx, &msg); err != nil {
		// The Create method may fail with a unique constraint violation if the event
		// was already persisted inline. That's fine - we just log and ack.
		log.Printf("[eventbus/pgwriter] Event %s persist (may be duplicate): %v", msg.ID, err)
	}

	if err := natsMsg.Ack(); err != nil {
		log.Printf("[eventbus/pgwriter] ERROR: failed to ack message: %v", err)
	}
}
