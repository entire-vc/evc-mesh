// Package ws implements a WebSocket hub that bridges Redis pub/sub events
// to connected browser clients for real-time UI updates.
package ws

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Hub manages WebSocket client connections and fans out events from
// Redis pub/sub to the appropriate clients based on their subscriptions.
type Hub struct {
	clients    sync.Map // map[string]*Client (clientID -> Client)
	register   chan *Client
	unregister chan *Client
	rdb        *redis.Client
}

// NewHub creates a new Hub with the given Redis client.
func NewHub(rdb *redis.Client) *Hub {
	return &Hub{
		register:   make(chan *Client, 64),
		unregister: make(chan *Client, 64),
		rdb:        rdb,
	}
}

// Run starts the hub's main loop. It subscribes to Redis pub/sub pattern
// ws:* and dispatches incoming events to connected clients.
// This method blocks and should be called in a goroutine.
func (h *Hub) Run(ctx context.Context) {
	// Subscribe to Redis pub/sub pattern for all workspace channels.
	pubsub := h.rdb.PSubscribe(ctx, "ws:*")
	defer func() {
		if err := pubsub.Close(); err != nil {
			log.Printf("[ws-hub] Error closing Redis pub/sub: %v", err)
		}
	}()

	// Wait for subscription confirmation.
	_, err := pubsub.Receive(ctx)
	if err != nil {
		log.Printf("[ws-hub] Failed to subscribe to Redis pub/sub: %v", err)
		return
	}
	log.Println("[ws-hub] Subscribed to Redis pub/sub pattern ws:*")

	redisCh := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			log.Println("[ws-hub] Shutting down")
			// Close all client connections.
			h.clients.Range(func(_, value any) bool {
				client := value.(*Client)
				close(client.Send)
				return true
			})
			return

		case client := <-h.register:
			h.clients.Store(client.ID, client)
			log.Printf("[ws-hub] Client %s registered (user=%s)", client.ID, client.UserID)

		case client := <-h.unregister:
			if _, loaded := h.clients.LoadAndDelete(client.ID); loaded {
				close(client.Send)
				log.Printf("[ws-hub] Client %s unregistered", client.ID)
			}

		case msg := <-redisCh:
			h.fanOut(msg)
		}
	}
}

// fanOut dispatches a Redis pub/sub message to all clients subscribed
// to the matching workspace or project channel.
func (h *Hub) fanOut(msg *redis.Message) {
	// msg.Channel is e.g. "ws:my-workspace"
	// msg.Payload is the JSON event data.

	// Parse the event to determine project_id for channel matching.
	var event struct {
		ProjectID   string `json:"project_id"`
		WorkspaceID string `json:"workspace_id"`
		EventType   string `json:"event_type"`
		Subject     string `json:"subject"`
	}

	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		log.Printf("[ws-hub] Failed to parse event payload: %v", err)
		return
	}

	// Build the outgoing WebSocket message.
	outMsg := OutgoingMessage{
		Type:      event.EventType,
		Channel:   msg.Channel,
		Data:      json.RawMessage(msg.Payload),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}

	outBytes, err := json.Marshal(outMsg)
	if err != nil {
		log.Printf("[ws-hub] Failed to marshal outgoing message: %v", err)
		return
	}

	// Derive workspace channel from Redis channel (ws:slug -> workspace:slug).
	wsChannel := msg.Channel // e.g. "ws:my-workspace"
	projectChannel := ""
	if event.ProjectID != "" {
		projectChannel = "project:" + event.ProjectID
	}

	h.clients.Range(func(_, value any) bool {
		client := value.(*Client)

		// Check if client is subscribed to this workspace or project.
		client.mu.RLock()
		subscribedToWS := client.Subscriptions[wsChannel]
		subscribedToProject := projectChannel != "" && client.Subscriptions[projectChannel]
		client.mu.RUnlock()

		if subscribedToWS || subscribedToProject {
			select {
			case client.Send <- outBytes:
			default:
				// Client send buffer is full; drop the message.
				log.Printf("[ws-hub] Dropping message for slow client %s", client.ID)
			}
		}

		return true
	})
}

// ClientCount returns the number of currently connected clients.
func (h *Hub) ClientCount() int {
	count := 0
	h.clients.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
