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
//
// Authorisation is this handler's own job. /ws is registered on the root Echo
// instance, before the /api/v1 group that carries WorkspaceRLS and
// RequireWorkspaceMemberScoped, so no middleware guard can ever run on it — which
// is why the "workspace" query parameter used to be taken at face value and any
// logged-in stranger could subscribe to another tenant's live event feed with
// nothing but its slug, which is public in every /w/<slug>/... URL.
func Handler(hub *Hub, authService *auth.Service, agentService service.AgentService, authz Authorizer) echo.HandlerFunc {
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
				return handleAgentAuth(c, hub, agentService, agentKey, authz)
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

		// Get workspace slug from query param and prove the caller belongs to it
		// BEFORE upgrading, so a refusal is an ordinary HTTP handshake failure the
		// client sees immediately rather than a socket that opens and stays silent.
		workspaceSlug := c.QueryParam("workspace")
		var workspaceID uuid.UUID
		if workspaceSlug != "" {
			if authz == nil {
				log.Printf("[ws-handler] no authorizer configured; refusing workspace subscription")
				return c.JSON(http.StatusForbidden, map[string]string{
					"error": "Workspace access denied",
				})
			}
			wsID, wsErr := authz.WorkspaceIDBySlug(c.Request().Context(), workspaceSlug)
			// One answer for "no such workspace" and "not a member of it". Slugs
			// are ws-<8 hex>; distinguishing the two would turn this endpoint into
			// a cheap oracle for which workspaces exist.
			if wsErr != nil || !authz.UserIsWorkspaceMember(c.Request().Context(), wsID, userID) {
				return c.JSON(http.StatusForbidden, map[string]string{
					"error": "Workspace access denied",
				})
			}
			workspaceID = wsID
		}

		// Upgrade to WebSocket.
		conn, err := websocket.Accept(c.Response().Writer, c.Request(), &websocket.AcceptOptions{
			InsecureSkipVerify: true, // Allow all origins (CORS is handled at the HTTP level).
		})
		if err != nil {
			log.Printf("[ws-handler] Failed to accept WebSocket: %v", err)
			return nil // websocket.Accept already wrote the error response.
		}

		// Create client and register with hub.
		client := NewClient(conn, hub, Principal{
			UserID:        userID,
			WorkspaceID:   workspaceID,
			WorkspaceSlug: workspaceSlug,
		}, authz)

		// Auto-subscribe to the workspace channel just verified above.
		if workspaceSlug != "" {
			client.mu.Lock()
			client.Subscriptions[workspaceChannelPrefix+workspaceSlug] = true
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
//
// The workspace comes from the key itself (agk_{slug}_{random}) and the key is
// then authenticated against that workspace, so an agent cannot reach a workspace
// other than its own here. The "workspace" query parameter is ignored on this
// path on purpose — honouring it would reintroduce the caller-controlled
// workspace that the user path had to be fixed for.
func handleAgentAuth(c echo.Context, hub *Hub, agentService service.AgentService, agentKey string, authz Authorizer) error {
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
	client := NewClient(conn, hub, Principal{
		AgentID:       agent.ID,
		WorkspaceID:   agent.WorkspaceID,
		WorkspaceSlug: slug,
	}, authz)

	// Auto-subscribe to the agent's own workspace channel.
	client.mu.Lock()
	client.Subscriptions[workspaceChannelPrefix+slug] = true
	client.mu.Unlock()

	hub.register <- client

	// Start read and write pumps.
	go client.WritePump(ctx)
	client.ReadPump(ctx)

	return nil
}
