package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// AgentNotification is the payload sent to an agent via push mechanisms.
type AgentNotification struct {
	EventType string         `json:"event_type"` // task.assigned, task.created, task.status_changed
	TaskID    uuid.UUID      `json:"task_id"`
	ProjectID uuid.UUID      `json:"project_id"`
	Payload   map[string]any `json:"payload"`
	Timestamp time.Time      `json:"timestamp"`
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
	agentSvc AgentService
	rdb      *redis.Client
	client   *http.Client
}

// NewAgentNotifyService returns a new AgentNotifyService.
func NewAgentNotifyService(agentSvc AgentService, rdb *redis.Client) AgentNotifyService {
	return &agentNotifyService{
		agentSvc: agentSvc,
		rdb:      rdb,
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
	// Use a background context: the original request context may be cancelled
	// before we finish dispatching.
	bgCtx := context.Background()

	// Look up the agent to get its callback_url.
	agent, err := s.agentSvc.GetByID(bgCtx, agentID)
	if err != nil {
		log.Printf("[agent-notify] failed to look up agent %s: %v", agentID, err)
		return
	}

	payloadBytes, err := json.Marshal(event)
	if err != nil {
		log.Printf("[agent-notify] failed to marshal notification for agent %s: %v", agentID, err)
		return
	}

	// 1. Publish to Redis pub/sub so SSE and long-poll consumers wake up.
	channel := agentNotifyRedisChannel(agentID)
	if pubErr := s.rdb.Publish(bgCtx, channel, string(payloadBytes)).Err(); pubErr != nil {
		log.Printf("[agent-notify] failed to publish to Redis channel %s: %v", channel, pubErr)
	}

	// 2. If the agent has a callback_url, fire an HTTP POST.
	if strings.TrimSpace(agent.CallbackURL) == "" {
		return
	}

	req, err := http.NewRequest(http.MethodPost, agent.CallbackURL, bytes.NewReader(payloadBytes))
	if err != nil {
		log.Printf("[agent-notify] failed to build callback request for agent %s (url: %s): %v", agentID, agent.CallbackURL, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mesh-Event", event.EventType)
	req.Header.Set("X-Mesh-Agent", agentID.String())
	req.Header.Set("User-Agent", "evc-mesh-agent-notify/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("[agent-notify] callback POST failed for agent %s (url: %s): %v", agentID, agent.CallbackURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[agent-notify] callback POST for agent %s returned non-2xx status: %d", agentID, resp.StatusCode)
	}
}

// Ensure agentNotifyService satisfies the AgentNotifyService interface.
var _ AgentNotifyService = (*agentNotifyService)(nil)
