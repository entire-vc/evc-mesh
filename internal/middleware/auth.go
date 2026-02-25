package middleware

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Context keys used by auth middleware.
const (
	ContextKeyUserID      = "user_id"
	ContextKeyWorkspaceID = "workspace_id"
	ContextKeyAgentID     = "agent_id"
	ContextKeyAuthType    = "auth_type"
	ContextKeyEmail       = "email"
)

// Auth types set in the Echo context.
const (
	AuthTypeUser  = "user"
	AuthTypeAgent = "agent"
)

// JWTAuth returns middleware that requires a valid JWT Bearer token.
// On success it sets user_id, email, and auth_type in the Echo context.
func JWTAuth(authService *auth.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tokenString, err := extractBearerToken(c)
			if err != nil {
				return unauthorizedJSON(c, "Authentication required")
			}

			claims, err := authService.ValidateAccessToken(tokenString)
			if err != nil {
				return unauthorizedJSON(c, "Invalid or expired token")
			}

			userID, err := uuid.Parse(claims.Subject)
			if err != nil {
				return unauthorizedJSON(c, "Invalid token subject")
			}

			c.Set(ContextKeyAuthType, AuthTypeUser)
			c.Set(ContextKeyUserID, userID)
			c.Set(ContextKeyEmail, claims.Email)

			// Propagate actor into Go context for service layer.
			goCtx := actorctx.WithActor(c.Request().Context(), userID, domain.ActorTypeUser)
			c.SetRequest(c.Request().WithContext(goCtx))

			return next(c)
		}
	}
}

// AgentKeyAuth returns middleware that requires a valid agent API key
// in the X-Agent-Key header. On success it sets agent_id, workspace_id,
// and auth_type in the Echo context.
func AgentKeyAuth(agentService service.AgentService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			apiKey := c.Request().Header.Get("X-Agent-Key")
			if apiKey == "" {
				return unauthorizedJSON(c, "Agent API key required")
			}

			// Parse workspace slug from key format: agk_{workspace_slug}_{random}
			workspaceSlug, err := parseWorkspaceSlugFromKey(apiKey)
			if err != nil {
				return unauthorizedJSON(c, "Invalid agent API key format")
			}

			agent, err := agentService.Authenticate(c.Request().Context(), workspaceSlug, apiKey)
			if err != nil {
				return unauthorizedJSON(c, "Invalid agent API key")
			}

			c.Set(ContextKeyAuthType, AuthTypeAgent)
			c.Set(ContextKeyAgentID, agent.ID)
			c.Set(ContextKeyWorkspaceID, agent.WorkspaceID)

			// Propagate actor into Go context for service layer.
			goCtx := actorctx.WithActor(c.Request().Context(), agent.ID, domain.ActorTypeAgent)
			c.SetRequest(c.Request().WithContext(goCtx))

			return next(c)
		}
	}
}

// DualAuth requires either a valid JWT Bearer token or a valid agent API key.
// Returns 401 if neither is present or valid.
func DualAuth(authService *auth.Service, agentService service.AgentService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Try JWT Bearer token first.
			if tokenString, err := extractBearerToken(c); err == nil {
				if claims, err := authService.ValidateAccessToken(tokenString); err == nil {
					if userID, err := uuid.Parse(claims.Subject); err == nil {
						c.Set(ContextKeyAuthType, AuthTypeUser)
						c.Set(ContextKeyUserID, userID)
						c.Set(ContextKeyEmail, claims.Email)
						// Propagate actor into Go context for service layer.
						goCtx := actorctx.WithActor(c.Request().Context(), userID, domain.ActorTypeUser)
						c.SetRequest(c.Request().WithContext(goCtx))
						return next(c)
					}
				}
			}

			// Try Agent Key.
			if apiKey := c.Request().Header.Get("X-Agent-Key"); apiKey != "" {
				if slug, err := parseWorkspaceSlugFromKey(apiKey); err == nil {
					if agent, err := agentService.Authenticate(c.Request().Context(), slug, apiKey); err == nil {
						c.Set(ContextKeyAuthType, AuthTypeAgent)
						c.Set(ContextKeyAgentID, agent.ID)
						c.Set(ContextKeyWorkspaceID, agent.WorkspaceID)
						// Propagate actor into Go context for service layer.
						goCtx := actorctx.WithActor(c.Request().Context(), agent.ID, domain.ActorTypeAgent)
						c.SetRequest(c.Request().WithContext(goCtx))
						return next(c)
					}
				}
				return unauthorizedJSON(c, "Invalid agent API key")
			}

			return unauthorizedJSON(c, "Authentication required")
		}
	}
}

// OptionalAuth tries JWT first, then agent key. If neither is present,
// the request passes through without authentication context.
func OptionalAuth(authService *auth.Service, agentService service.AgentService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Try JWT Bearer token first.
			if tokenString, err := extractBearerToken(c); err == nil {
				if claims, err := authService.ValidateAccessToken(tokenString); err == nil {
					if userID, err := uuid.Parse(claims.Subject); err == nil {
						c.Set(ContextKeyAuthType, AuthTypeUser)
						c.Set(ContextKeyUserID, userID)
						c.Set(ContextKeyEmail, claims.Email)
						// Propagate actor into Go context for service layer.
						goCtx := actorctx.WithActor(c.Request().Context(), userID, domain.ActorTypeUser)
						c.SetRequest(c.Request().WithContext(goCtx))
						return next(c)
					}
				}
			}

			// Try Agent Key.
			if apiKey := c.Request().Header.Get("X-Agent-Key"); apiKey != "" {
				if slug, err := parseWorkspaceSlugFromKey(apiKey); err == nil {
					if agent, err := agentService.Authenticate(c.Request().Context(), slug, apiKey); err == nil {
						c.Set(ContextKeyAuthType, AuthTypeAgent)
						c.Set(ContextKeyAgentID, agent.ID)
						c.Set(ContextKeyWorkspaceID, agent.WorkspaceID)
						// Propagate actor into Go context for service layer.
						goCtx := actorctx.WithActor(c.Request().Context(), agent.ID, domain.ActorTypeAgent)
						c.SetRequest(c.Request().WithContext(goCtx))
						return next(c)
					}
				}
			}

			// Neither present: pass through unauthenticated.
			return next(c)
		}
	}
}

// --- Context helper functions ---

// GetUserID extracts the user_id from the Echo context.
func GetUserID(c echo.Context) (uuid.UUID, error) {
	v := c.Get(ContextKeyUserID)
	if v == nil {
		return uuid.Nil, errors.New("user_id not found in context")
	}
	id, ok := v.(uuid.UUID)
	if !ok {
		return uuid.Nil, errors.New("user_id has invalid type in context")
	}
	return id, nil
}

// GetWorkspaceID extracts the workspace_id from the Echo context.
func GetWorkspaceID(c echo.Context) (uuid.UUID, error) {
	v := c.Get(ContextKeyWorkspaceID)
	if v == nil {
		return uuid.Nil, errors.New("workspace_id not found in context")
	}
	id, ok := v.(uuid.UUID)
	if !ok {
		return uuid.Nil, errors.New("workspace_id has invalid type in context")
	}
	return id, nil
}

// GetAgentID extracts the agent_id from the Echo context.
func GetAgentID(c echo.Context) (uuid.UUID, error) {
	v := c.Get(ContextKeyAgentID)
	if v == nil {
		return uuid.Nil, errors.New("agent_id not found in context")
	}
	id, ok := v.(uuid.UUID)
	if !ok {
		return uuid.Nil, errors.New("agent_id has invalid type in context")
	}
	return id, nil
}

// IsAgent returns true if the current request was authenticated with an agent API key.
func IsAgent(c echo.Context) bool {
	v := c.Get(ContextKeyAuthType)
	if v == nil {
		return false
	}
	authType, ok := v.(string)
	return ok && authType == AuthTypeAgent
}

// --- Internal helpers ---

// extractBearerToken extracts the token from the "Authorization: Bearer <token>" header.
func extractBearerToken(c echo.Context) (string, error) {
	header := c.Request().Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		return "", errors.New("missing or invalid Authorization header")
	}
	return strings.TrimPrefix(header, "Bearer "), nil
}

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

// unauthorizedJSON returns a 401 JSON response using the project's error format.
func unauthorizedJSON(c echo.Context, message string) error {
	return c.JSON(401, apierror.Unauthorized(message))
}
