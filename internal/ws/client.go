package ws

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

const (
	// writeWait is the time allowed to write a message to the client.
	writeWait = 10 * time.Second

	// pingInterval is the interval at which pings are sent.
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

// Channel name prefixes. The personal prefix is a longer form of the workspace
// one, so it must always be tested first.
const (
	personalChannelPrefix  = "ws:user:"
	workspaceChannelPrefix = "ws:"
	projectChannelPrefix   = "project:"
)

// Principal is the authenticated identity behind a connection together with the
// single workspace that identity was authorised for during the handshake.
// Everything the connection is later allowed to subscribe to derives from it.
type Principal struct {
	// UserID is uuid.Nil on agent connections.
	UserID uuid.UUID
	// AgentID is uuid.Nil on user connections.
	AgentID uuid.UUID
	// WorkspaceID and WorkspaceSlug are the workspace the handshake verified the
	// principal belongs to; both are zero when the connection named none.
	WorkspaceID   uuid.UUID
	WorkspaceSlug string
}

// Client represents a single WebSocket connection.
type Client struct {
	ID            string
	Conn          *websocket.Conn
	Hub           *Hub
	WorkspaceSlug string
	WorkspaceID   uuid.UUID
	UserID        uuid.UUID
	AgentID       uuid.UUID
	Subscriptions map[string]bool // channel names: "project:{id}", "ws:{slug}"
	Send          chan []byte
	authz         Authorizer
	mu            sync.RWMutex
}

// NewClient creates a new WebSocket client for an already-authenticated principal.
// The authorizer is consulted on every later subscribe request; a nil one denies
// them rather than allowing them, so a miswired caller loses live updates instead
// of leaking another tenant's.
func NewClient(conn *websocket.Conn, hub *Hub, p Principal, authz Authorizer) *Client {
	clientID := uuid.New().String()
	return &Client{
		ID:            clientID,
		Conn:          conn,
		Hub:           hub,
		UserID:        p.UserID,
		AgentID:       p.AgentID,
		WorkspaceID:   p.WorkspaceID,
		WorkspaceSlug: p.WorkspaceSlug,
		Subscriptions: make(map[string]bool),
		Send:          make(chan []byte, sendBufferSize),
		authz:         authz,
	}
}

// CanSubscribe reports whether this connection may receive a channel's traffic.
// The handshake establishes who the principal is and which workspace they belong
// to; this decides what that entitles them to hear.
//
//   - "ws:user:<uuid>" — personal mention feed: only the user it names.
//   - "ws:<slug>" — workspace event feed: only the workspace the handshake
//     authorised. Slugs are ws-<8 hex> and appear in every /w/<slug>/... URL, so
//     accepting whichever one the client asked for was the whole hole.
//   - "project:<uuid>" — project event feed: only projects inside that workspace.
//
// Anything else is refused. The hub fans out on exact channel names, so a name
// that matches none of these can only be a guess at someone else's.
func (c *Client) CanSubscribe(ctx context.Context, channel string) bool {
	switch {
	case strings.HasPrefix(channel, personalChannelPrefix):
		if c.UserID == uuid.Nil {
			return false
		}
		id, err := uuid.Parse(strings.TrimPrefix(channel, personalChannelPrefix))
		return err == nil && id == c.UserID

	case strings.HasPrefix(channel, workspaceChannelPrefix):
		return c.WorkspaceSlug != "" && channel == workspaceChannelPrefix+c.WorkspaceSlug

	case strings.HasPrefix(channel, projectChannelPrefix):
		if c.WorkspaceID == uuid.Nil || c.authz == nil {
			return false
		}
		projectID, err := uuid.Parse(strings.TrimPrefix(channel, projectChannelPrefix))
		if err != nil {
			return false
		}
		wsID, err := c.authz.ProjectWorkspaceID(ctx, projectID)
		return err == nil && wsID == c.WorkspaceID

	default:
		return false
	}
}

// sendDenial tells the client its subscribe request was refused. The reason is
// deliberately constant: "no such project" and "not your project" have to look
// alike, or the socket becomes an enumeration tool for ids it cannot read.
func (c *Client) sendDenial(channel string) {
	msg, err := json.Marshal(OutgoingMessage{
		Type:      "subscribe.denied",
		Channel:   channel,
		Data:      json.RawMessage(`{"error":"forbidden"}`),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	select {
	case c.Send <- msg:
	default:
	}
}

// ReadPump reads messages from the WebSocket connection.
// It handles subscribe/unsubscribe commands from the client.
// This method blocks and should be called in a goroutine.
func (c *Client) ReadPump(ctx context.Context) {
	defer func() {
		c.Hub.unregister <- c
		_ = c.Conn.Close(websocket.StatusNormalClosure, "closing")
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
			// Authorise every channel, not just the one named at handshake time.
			// The handshake used to be the only check, and this branch let any
			// connected client add "ws:<victim-slug>" or "project:<victim-uuid>"
			// afterwards and receive that tenant's events — the same leak through
			// a second door.
			if !c.CanSubscribe(ctx, msg.Channel) {
				log.Printf("[ws-client] %s denied subscribe to %s", c.ID, msg.Channel)
				c.sendDenial(msg.Channel)
				continue
			}
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
		_ = c.Conn.Close(websocket.StatusNormalClosure, "closing")
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
