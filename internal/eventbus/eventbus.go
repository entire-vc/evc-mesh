// Package eventbus provides NATS JetStream integration for the inter-agent
// event bus. It handles publishing, subscribing, and persistence of events.
package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Publisher is the interface exposed by EventBus for publishing events.
// This is used by the service layer to avoid a hard dependency on the
// concrete EventBus type.
type Publisher interface {
	PublishEvent(ctx context.Context, msg *domain.EventBusMessage, workspaceSlug, projectSlug string) error
}

// EventBus manages the NATS JetStream event bus, Redis pub/sub broadcast,
// and PostgreSQL persistence for events.
type EventBus struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	stream   jetstream.Stream
	rdb      *redis.Client
	repo     repository.EventBusMessageRepository
	cfg      EventBusConfig
	cancelFn context.CancelFunc
	wg       sync.WaitGroup
}

// Verify EventBus implements Publisher at compile time.
var _ Publisher = (*EventBus)(nil)

// New creates a new EventBus. It connects to NATS and Redis, ensures
// the JetStream stream exists, but does NOT start background workers.
// Call Start() to begin the PG writer and cleanup workers.
func New(ctx context.Context, cfg EventBusConfig, repo repository.EventBusMessageRepository) (*EventBus, error) {
	cfg.applyDefaults()

	// Connect to NATS.
	nc, err := nats.Connect(cfg.NATSUrl,
		nats.Name("evc-mesh-eventbus"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Printf("[eventbus] NATS disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.Println("[eventbus] NATS reconnected")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS at %s: %w", cfg.NATSUrl, err)
	}
	log.Printf("[eventbus] Connected to NATS at %s", cfg.NATSUrl)

	// Create JetStream context.
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}

	// Ensure stream exists.
	stream, err := ensureStream(ctx, js, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}

	// Connect to Redis.
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to connect to Redis at %s: %w", cfg.RedisAddr, err)
	}
	log.Printf("[eventbus] Connected to Redis at %s", cfg.RedisAddr)

	return &EventBus{
		nc:     nc,
		js:     js,
		stream: stream,
		rdb:    rdb,
		repo:   repo,
		cfg:    cfg,
	}, nil
}

// PublishEvent publishes an event to NATS JetStream, persists it to PostgreSQL,
// and broadcasts it to Redis pub/sub for WebSocket distribution.
//
// workspaceSlug and projectSlug are used to construct the NATS subject.
// The msg must have a valid ID (used as Nats-Msg-Id for deduplication).
func (eb *EventBus) PublishEvent(ctx context.Context, msg *domain.EventBusMessage, workspaceSlug, projectSlug string) error {
	// 1. Serialize the event for NATS.
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// 2. Publish to NATS JetStream with dedup ID.
	subject := BuildSubject(workspaceSlug, projectSlug, string(msg.EventType))
	ack, err := eb.js.Publish(ctx, subject, data,
		jetstream.WithMsgID(msg.ID.String()),
	)
	if err != nil {
		return fmt.Errorf("failed to publish to NATS subject %s: %w", subject, err)
	}
	log.Printf("[eventbus] Published event %s to %s (seq=%d)", msg.ID, subject, ack.Sequence)

	// 3. Persist to PostgreSQL (best-effort: log error but don't fail the publish).
	if err := eb.repo.Create(ctx, msg); err != nil {
		log.Printf("[eventbus] WARNING: failed to persist event %s to PostgreSQL: %v", msg.ID, err)
	}

	// 4. Broadcast to Redis pub/sub for WebSocket consumers.
	redisChannel := fmt.Sprintf("ws:%s", workspaceSlug)
	if err := eb.rdb.Publish(ctx, redisChannel, data).Err(); err != nil {
		log.Printf("[eventbus] WARNING: failed to broadcast event %s to Redis channel %s: %v", msg.ID, redisChannel, err)
	}

	return nil
}

// GetEvents retrieves events from PostgreSQL using the repository.
func (eb *EventBus) GetEvents(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, limit int) ([]domain.EventBusMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	pg := pagination.Params{
		Page:     1,
		PageSize: limit,
		SortBy:   "created_at",
		SortDir:  "desc",
	}

	page, err := eb.repo.List(ctx, projectID, filter, pg)
	if err != nil {
		return nil, err
	}

	return page.Items, nil
}

// Subscribe creates a durable pull consumer for an agent on the MESH_EVENTS stream.
// The consumer name is derived from the agent ID for durability.
// The caller receives a jetstream.Consumer that can be used to fetch messages.
func (eb *EventBus) Subscribe(ctx context.Context, agentID uuid.UUID, filterSubject string) (jetstream.Consumer, error) {
	consumerName := fmt.Sprintf("agent-%s", agentID.String())

	if filterSubject == "" {
		filterSubject = SubjectWildcard
	}

	consumer, err := eb.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: filterSubject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer %s: %w", consumerName, err)
	}

	log.Printf("[eventbus] Created durable consumer %s (filter=%s)", consumerName, filterSubject)
	return consumer, nil
}

// Start launches background workers (PG writer and cleanup).
func (eb *EventBus) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	eb.cancelFn = cancel

	eb.wg.Add(2)

	go func() {
		defer eb.wg.Done()
		eb.runPGWriter(ctx)
	}()

	go func() {
		defer eb.wg.Done()
		eb.runCleanup(ctx)
	}()

	log.Println("[eventbus] Background workers started")
}

// Close gracefully shuts down the event bus, stopping background workers
// and closing NATS and Redis connections.
func (eb *EventBus) Close() error {
	log.Println("[eventbus] Shutting down...")

	if eb.cancelFn != nil {
		eb.cancelFn()
	}
	eb.wg.Wait()

	if eb.rdb != nil {
		if err := eb.rdb.Close(); err != nil {
			log.Printf("[eventbus] Error closing Redis: %v", err)
		}
	}

	if eb.nc != nil {
		eb.nc.Close()
	}

	log.Println("[eventbus] Shutdown complete")
	return nil
}
