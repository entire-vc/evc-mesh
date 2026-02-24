package ws

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// parseWorkspaceSlugFromKey extracts the workspace slug from an agent key.
// Key format: agk_{workspace_slug}_{random_part}
func parseWorkspaceSlugFromKey(key string) (string, error) {
	if !strings.HasPrefix(key, "agk_") {
		return "", errors.New("invalid agent key prefix")
	}

	// Remove "agk_" prefix.
	rest := key[4:]

	// Find the last underscore to separate slug from random part.
	lastUnderscore := strings.LastIndex(rest, "_")
	if lastUnderscore <= 0 {
		return "", errors.New("invalid agent key format")
	}

	slug := rest[:lastUnderscore]
	if slug == "" {
		return "", errors.New("empty workspace slug in agent key")
	}

	return slug, nil
}

// Handler returns an Echo handler that upgrades HTTP connections to WebSocket.
// Authentication is performed via:
//   - Authorization header: "Bearer <JWT>"
//   - Query parameter: "token=<JWT>"
//   - Query parameter: "agent_key=<AgentKey>" (fallback for agent connections)
//
// The workspace slug can be provided via the "workspace" query parameter.
func Handler(hub *Hub, authService *auth.Service, agentService service.AgentService) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Extract JWT token from Authorization header or query param.
		tokenString := ""

		// Try Authorization header first.
		authHeader := c.Request().Header.Get("Authorization")
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			tokenString = authHeader[7:]
		}

		// Fall back to query param.
		if tokenString == "" {
			tokenString = c.QueryParam("token")
		}

		// If no JWT token, try agent key authentication.
		if tokenString == "" {
			agentKey := c.QueryParam("agent_key")
			if agentKey != "" {
				return handleAgentAuth(c, hub, agentService, agentKey)
			}

			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "Authentication required",
			})
		}

		// Validate the JWT token.
		claims, err := authService.ValidateAccessToken(tokenString)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "Invalid or expired token",
			})
		}

		userID, err := parseUUID(claims.Subject)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "Invalid token subject",
			})
		}

		// Get workspace slug from query param.
		workspaceSlug := c.QueryParam("workspace")

		// Upgrade to WebSocket.
		conn, err := websocket.Accept(c.Response().Writer, c.Request(), &websocket.AcceptOptions{
			InsecureSkipVerify: true, // Allow all origins (CORS is handled at the HTTP level).
		})
		if err != nil {
			log.Printf("[ws-handler] Failed to accept WebSocket: %v", err)
			return nil // websocket.Accept already wrote the error response.
		}

		// Create client and register with hub.
		client := NewClient(conn, hub, userID, workspaceSlug)

		// Auto-subscribe to workspace channel if workspace slug was provided.
		if workspaceSlug != "" {
			client.mu.Lock()
			client.Subscriptions["ws:"+workspaceSlug] = true
			client.mu.Unlock()
		}

		hub.register <- client

		// Use the request context for the client pumps.
		ctx := c.Request().Context()

		// Start read and write pumps.
		go client.WritePump(ctx)
		client.ReadPump(ctx)

		return nil
	}
}

// handleAgentAuth handles WebSocket authentication via agent API key.
func handleAgentAuth(c echo.Context, hub *Hub, agentService service.AgentService, agentKey string) error {
	// Parse workspace slug from key format: agk_{slug}_{random}
	slug, err := parseWorkspaceSlugFromKey(agentKey)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "Invalid agent key format",
		})
	}

	ctx := c.Request().Context()
	agent, err := agentService.Authenticate(ctx, slug, agentKey)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "Invalid agent key",
		})
	}

	// Upgrade to WebSocket.
	conn, err := websocket.Accept(c.Response().Writer, c.Request(), &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("[ws-handler] Failed to accept WebSocket for agent: %v", err)
		return nil
	}

	// Create client with agent identity (no user ID).
	client := NewClient(conn, hub, uuid.Nil, slug)
	client.AgentID = agent.ID

	// Auto-subscribe to workspace channel.
	client.mu.Lock()
	client.Subscriptions["ws:"+slug] = true
	client.mu.Unlock()

	hub.register <- client

	// Start read and write pumps.
	go client.WritePump(ctx)
	client.ReadPump(ctx)

	return nil
}
