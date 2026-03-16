package ws

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

const (
	// writeWait is the time allowed to write a message to the client.
	writeWait = 10 * time.Second

	// pingInterval is the interval at which pings are sent. Must be less than pongWait.
	pingInterval = 30 * time.Second

	// maxMessageSize is the maximum message size allowed from the client.
	maxMessageSize = 4096

	// sendBufferSize is the size of the client send channel buffer.
	sendBufferSize = 256
)

// IncomingMessage represents a message sent from the client to the server.
type IncomingMessage struct {
	Action  string `json:"action"`  // "subscribe" or "unsubscribe"
	Channel string `json:"channel"` // e.g. "project:UUID" or "ws:slug"
}

// OutgoingMessage represents a message sent from the server to the client.
type OutgoingMessage struct {
	Type      string          `json:"type"`
	Channel   string          `json:"channel"`
	Data      json.RawMessage `json:"data"`
	Timestamp string          `json:"timestamp"`
}

// Client represents a single WebSocket connection.
type Client struct {
	ID            string
	Conn          *websocket.Conn
	Hub           *Hub
	WorkspaceSlug string
	UserID        uuid.UUID
	AgentID       uuid.UUID
	Subscriptions map[string]bool // channel names: "project:{id}", "ws:{slug}"
	Send          chan []byte
	mu            sync.RWMutex
}

// NewClient creates a new WebSocket client.
func NewClient(conn *websocket.Conn, hub *Hub, userID uuid.UUID, workspaceSlug string) *Client {
	clientID := uuid.New().String()
	return &Client{
		ID:            clientID,
		Conn:          conn,
		Hub:           hub,
		UserID:        userID,
		WorkspaceSlug: workspaceSlug,
		Subscriptions: make(map[string]bool),
		Send:          make(chan []byte, sendBufferSize),
	}
}

// ReadPump reads messages from the WebSocket connection.
// It handles subscribe/unsubscribe commands from the client.
// This method blocks and should be called in a goroutine.
func (c *Client) ReadPump(ctx context.Context) {
	defer func() {
		c.Hub.unregister <- c
		c.Conn.Close(websocket.StatusNormalClosure, "closing")
	}()

	c.Conn.SetReadLimit(maxMessageSize)

	for {
		_, data, err := c.Conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != -1 {
				log.Printf("[ws-client] %s closed: %v", c.ID, err)
			}
			return
		}

		var msg IncomingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[ws-client] %s invalid message: %v", c.ID, err)
			continue
		}

		switch msg.Action {
		case "subscribe":
			c.mu.Lock()
			c.Subscriptions[msg.Channel] = true
			c.mu.Unlock()
			log.Printf("[ws-client] %s subscribed to %s", c.ID, msg.Channel)

		case "unsubscribe":
			c.mu.Lock()
			delete(c.Subscriptions, msg.Channel)
			c.mu.Unlock()
			log.Printf("[ws-client] %s unsubscribed from %s", c.ID, msg.Channel)

		default:
			log.Printf("[ws-client] %s unknown action: %s", c.ID, msg.Action)
		}
	}
}

// WritePump sends messages to the WebSocket connection.
// It also sends periodic pings to keep the connection alive.
// This method blocks and should be called in a goroutine.
func (c *Client) WritePump(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		c.Conn.Close(websocket.StatusNormalClosure, "closing")
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case message, ok := <-c.Send:
			if !ok {
				// Hub closed the channel.
				_ = c.Conn.Close(websocket.StatusNormalClosure, "hub closed")
				return
			}

			writeCtx, cancel := context.WithTimeout(ctx, writeWait)
			err := c.Conn.Write(writeCtx, websocket.MessageText, message)
			cancel()

			if err != nil {
				log.Printf("[ws-client] %s write error: %v", c.ID, err)
				return
			}

		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, writeWait)
			err := c.Conn.Ping(pingCtx)
			cancel()

			if err != nil {
				log.Printf("[ws-client] %s ping failed: %v", c.ID, err)
				return
			}
		}
	}
}
