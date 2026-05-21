package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// AgentNotification is the payload sent to an agent via push mechanisms.
// It follows the spec format with a full task snapshot and change diff.
type AgentNotification struct {
	EventID     uuid.UUID      `json:"event_id,omitempty"`   // UUID v7, set by dispatch() for durable SSE replay
	EventType   string         `json:"event_type"`           // task.assigned, task.created, task.status_changed, task.commented
	Timestamp   time.Time      `json:"timestamp"`            //nolint:all
	WorkspaceID uuid.UUID      `json:"workspace_id"`         //nolint:all
	Task        map[string]any `json:"task"`                 // Full task snapshot
	AgentID     uuid.UUID      `json:"agent_id"`             // Target agent
	ActorID     uuid.UUID      `json:"actor_id"`             // Who initiated the action
	ActorType   string         `json:"actor_type"`           // "agent" | "user"
	ActorName   string         `json:"actor_name"`           // Display name of actor
	Changes     map[string]any `json:"changes,omitempty"`    // {field: {old, new}}
	Comment     map[string]any `json:"comment,omitempty"`    // For task.commented events
	TaskID      uuid.UUID      `json:"task_id,omitempty"`    // Kept for Redis/SSE consumers
	ProjectID   uuid.UUID      `json:"project_id,omitempty"` // Kept for Redis/SSE consumers
	Payload     map[string]any `json:"payload,omitempty"`    // Deprecated — kept for backwards compat
}

// criticalSSEEventTypes receive a 7-day retention window in agent_events instead of the default 24h.
var criticalSSEEventTypes = map[string]bool{
	"task.assigned":  true,
	"task.created":   true,
	"task.mentioned": true,
}

func sseEventTTL(eventType string) time.Duration {
	if criticalSSEEventTypes[eventType] {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// AgentNotifyService sends push notifications to agents via callback URL and Redis pub/sub.
type AgentNotifyService interface {
	// NotifyAgent sends a push notification to a specific agent about a task event.
	// It dispatches to callback_url (if configured) and publishes to Redis for SSE/long-poll.
	// This method never blocks the caller.
	NotifyAgent(ctx context.Context, agentID uuid.UUID, event AgentNotification)
}

// agentNotifyService implements AgentNotifyService.
type agentNotifyService struct {
	agentSvc       AgentService
	rdb            *redis.Client
	agentEventsRepo repository.AgentEventsRepository // nil = persistence disabled
	client         *http.Client
}

// Retry backoff intervals for 5xx / timeout failures.
var callbackRetryBackoffs = []time.Duration{10 * time.Second, 60 * time.Second, 300 * time.Second}

// NewAgentNotifyService returns a new AgentNotifyService.
// Pass a non-nil agentEventsRepo to enable durable SSE event persistence (Last-Event-ID replay).
// Passing nil disables persistence — events are still delivered via Redis pub/sub.
func NewAgentNotifyService(agentSvc AgentService, rdb *redis.Client, agentEventsRepo repository.AgentEventsRepository) AgentNotifyService {
	return &agentNotifyService{
		agentSvc:        agentSvc,
		rdb:             rdb,
		agentEventsRepo: agentEventsRepo,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// agentNotifyRedisChannel returns the Redis pub/sub channel name for an agent.
func agentNotifyRedisChannel(agentID uuid.UUID) string {
	return fmt.Sprintf("agent-notify:%s", agentID.String())
}

// NotifyAgent dispatches a notification to the agent via callback URL (if set) and
// publishes to the Redis channel so SSE/long-poll consumers are woken up.
// It never blocks — all I/O is done in goroutines.
func (s *agentNotifyService) NotifyAgent(ctx context.Context, agentID uuid.UUID, event AgentNotification) {
	go s.dispatch(agentID, event)
}

func (s *agentNotifyService) dispatch(agentID uuid.UUID, event AgentNotification) {
	bgCtx := context.Background()

	// Look up the agent to get its callback_url.
	agent, err := s.agentSvc.GetByID(bgCtx, agentID)
	if err != nil {
		log.Printf("[agent-notify] failed to look up agent %s: %v", agentID, err)
		return
	}

	// Assign a UUID v7 event ID so the event can be used as a Last-Event-ID cursor.
	// v7 UUIDs are time-ordered (monotonic), so cursor comparison via byte ordering is correct.
	if eventID, v7Err := uuid.NewV7(); v7Err == nil {
		event.EventID = eventID
	} else {
		// Fallback to random v4 if the clock is misconfigured — events still deliver but
		// ordering guarantees for replay are weakened (effectively replay may skip events).
		log.Printf("[agent-notify] uuid.NewV7 failed for agent %s: %v — falling back to v4", agentID, v7Err)
		event.EventID = uuid.New()
	}

	payloadBytes, err := json.Marshal(event)
	if err != nil {
		log.Printf("[agent-notify] failed to marshal notification for agent %s: %v", agentID, err)
		return
	}

	// 1. Persist to agent_events for durable SSE replay (if repo is wired).
	// Persist BEFORE Redis publish so the event is in DB before any subscriber could
	// query it on reconnect.
	if s.agentEventsRepo != nil {
		now := time.Now()
		agentEvent := &domain.AgentEvent{
			EventID:     event.EventID,
			AgentID:     agentID,
			WorkspaceID: event.WorkspaceID,
			EventType:   event.EventType,
			Payload:     json.RawMessage(payloadBytes),
			EmittedAt:   now,
			ExpiresAt:   now.Add(sseEventTTL(event.EventType)),
		}
		if persistErr := s.agentEventsRepo.Create(bgCtx, agentEvent); persistErr != nil {
			// Non-fatal: log and continue. Redis pub/sub still delivers live.
			log.Printf("[agent-notify] failed to persist event %s for agent %s: %v", event.EventID, agentID, persistErr)
		}
	}

	// 2. Publish to Redis pub/sub so SSE and long-poll consumers wake up.
	channel := agentNotifyRedisChannel(agentID)
	if pubErr := s.rdb.Publish(bgCtx, channel, string(payloadBytes)).Err(); pubErr != nil {
		log.Printf("[agent-notify] failed to publish to Redis channel %s: %v", channel, pubErr)
	}

	// 3. If the agent has a callback_url, fire an HTTP POST with retry.
	if strings.TrimSpace(agent.CallbackURL) == "" {
		return
	}

	deliveryID := uuid.New().String()
	s.deliverWithRetry(agent.CallbackURL, agentID, event.EventType, deliveryID, payloadBytes)
}

// deliverWithRetry POSTs the payload to callbackURL. On 5xx or timeout it retries
// up to 3 times with backoff (10s, 60s, 300s). 4xx errors are not retried.
func (s *agentNotifyService) deliverWithRetry(callbackURL string, agentID uuid.UUID, eventType, deliveryID string, body []byte) {
	for attempt := 0; attempt <= len(callbackRetryBackoffs); attempt++ {
		if attempt > 0 {
			time.Sleep(callbackRetryBackoffs[attempt-1])
		}

		req, err := http.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(body))
		if err != nil {
			log.Printf("[agent-notify] failed to build callback request for agent %s: %v", agentID, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Mesh-Event", eventType)
		req.Header.Set("X-Mesh-Delivery", deliveryID)
		req.Header.Set("X-Mesh-Agent", agentID.String())
		req.Header.Set("User-Agent", "evc-mesh-agent-notify/1.0")

		resp, err := s.client.Do(req)
		if err != nil {
			log.Printf("[agent-notify] callback POST failed for agent %s (attempt %d, url: %s): %v", agentID, attempt+1, callbackURL, err)
			continue // timeout or network error — retry
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck // drain body to enable connection reuse
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Printf("[agent-notify] callback delivered for agent %s (attempt %d, url: %s)", agentID, attempt+1, callbackURL)
			return // success
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			log.Printf("[agent-notify] callback POST for agent %s returned %d (4xx) — not retrying", agentID, resp.StatusCode)
			return // 4xx — do not retry
		}

		log.Printf("[agent-notify] callback POST for agent %s returned %d (attempt %d/%d)", agentID, resp.StatusCode, attempt+1, len(callbackRetryBackoffs)+1)
	}

	log.Printf("[agent-notify] callback delivery exhausted for agent %s (delivery: %s)", agentID, deliveryID)
}

// Ensure agentNotifyService satisfies the AgentNotifyService interface.
var _ AgentNotifyService = (*agentNotifyService)(nil)
