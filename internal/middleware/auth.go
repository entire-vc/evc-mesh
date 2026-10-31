package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
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

// ContextKeyAgentAuthWorkspaceID stores the workspace the PRESENTED agent key
// actually authenticated into — agent.WorkspaceID as agentService.Authenticate
// resolved it (the matching grant's workspace, or the legacy home row's own
// workspace), set once here and never touched again.
//
// It exists because ContextKeyWorkspaceID does not stay that value: WorkspaceRLS
// (internal/middleware/workspace.go) overwrites it with whatever workspace the
// route's OWN path parameter names, for every route that has one — that is a
// different question ("which tenant is this request ABOUT") from the one this
// key answers ("which tenant did this key PROVE it belongs to"). A multi-
// workspace agent's key is scoped to exactly one workspace per Authenticate()
// call (see authenticateViaGrant's resolved.WorkspaceID doc comment); collapsing
// both questions onto one context key made every :ws_id route ask the wrong one
// — see RequireWorkspaceMember, which is the actual consumer of this value.
const ContextKeyAgentAuthWorkspaceID = "agent_auth_workspace_id"

// ContextKeyOAuthConnectorUserID stores domain.Agent.OAuthConnectorUserID
// when the current request authenticated via a mot_ OAuth token (MCP-OAuth
// 1/5) — unset for a trusted X-Agent-Key agent. RequirePermission
// (rbac.go) checks this to clamp an OAuth connector's permissions to the
// consenting user's live workspace role instead of the full agentPerms set.
const ContextKeyOAuthConnectorUserID = "oauth_connector_user_id"

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
			goCtx = actorctx.WithActorName(goCtx, claims.Name)
			c.SetRequest(c.Request().WithContext(goCtx))

			return next(c)
		}
	}
}

// OAuthTokenAuthenticator is the narrow slice of service.OAuthService the
// auth middleware needs: verify a raw mot_ bearer token (MCP-OAuth 1/5) and
// resolve it to the connector agent it authenticates as, with WorkspaceID/
// WorkspaceRole already set — the same shape agentService.Authenticate
// returns for an agk_ key. Kept separate from the full service.OAuthService
// interface, same reasoning as service.CheckoutHeartbeatExtender: this
// package should not need to know that interface's other dozen methods to
// use this one. A nil value (never wired) makes every function below skip
// the mot_ branch entirely and behave exactly as it did before MCP-OAuth
// 1/5 — the same "unwired optional dependency is a no-op" contract
// AgentWorkspaceGrantRepository's nil case uses in agentService.
type OAuthTokenAuthenticator interface {
	AuthenticateAccessToken(ctx context.Context, rawToken string) (*domain.Agent, error)
}

// oauthBearerToken extracts a Bearer token from Authorization specifically
// when it carries the OAuth access-token prefix (service.OAuthAccessToken-
// Prefix) — distinct from extractBearerToken (used for the JWT path), which
// takes any Bearer value regardless of prefix.
func oauthBearerToken(c echo.Context) (string, bool) {
	header := c.Request().Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(token, service.OAuthAccessTokenPrefix) {
		return "", false
	}
	return token, true
}

// setAgentAuthContext is the context-setting + actor-propagation block every
// agent-authenticated path (X-Agent-Key, and now a mot_ Bearer token) needs
// to run identically, factored out so AgentKeyAuth/DualAuth/OptionalAuth
// cannot drift between their agk_ and mot_ branches the way three inlined
// copies eventually would.
func setAgentAuthContext(c echo.Context, agent *domain.Agent) {
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agent.ID)
	c.Set(ContextKeyWorkspaceID, agent.WorkspaceID)
	c.Set(ContextKeyAgentAuthWorkspaceID, agent.WorkspaceID)
	c.Set(ContextKeyWorkspaceRole, agent.WorkspaceRole)
	if agent.OAuthConnectorUserID != nil {
		c.Set(ContextKeyOAuthConnectorUserID, *agent.OAuthConnectorUserID)
	}

	goCtx := actorctx.WithActor(c.Request().Context(), agent.ID, domain.ActorTypeAgent)
	goCtx = actorctx.WithActorName(goCtx, agent.Name)
	c.SetRequest(c.Request().WithContext(goCtx))
}

// AgentKeyAuth returns middleware that requires either a valid agent API key
// in the X-Agent-Key header, or (when oauthAuth is wired) a valid mot_
// Bearer access token. On success it sets agent_id, workspace_id, and
// auth_type in the Echo context.
func AgentKeyAuth(agentService service.AgentService, oauthAuth OAuthTokenAuthenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if oauthAuth != nil {
				if token, ok := oauthBearerToken(c); ok {
					agent, err := oauthAuth.AuthenticateAccessToken(c.Request().Context(), token)
					if err != nil {
						return unauthorizedJSON(c, "Invalid access token")
					}
					setAgentAuthContext(c, agent)
					return next(c)
				}
			}

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
				if !isAgentKeyDenial(err) {
					return authBackendUnavailableJSON(c, "AgentKeyAuth", workspaceSlug, apiKey, err)
				}
				return unauthorizedJSON(c, "Invalid agent API key")
			}

			setAgentAuthContext(c, agent)
			return next(c)
		}
	}
}

// DualAuth requires a valid JWT Bearer token, a valid agent API key, or
// (when oauthAuth is wired) a valid mot_ Bearer access token. Returns 401 if
// none is present or valid. This is the middleware gating the whole
// /api/v1 group (cmd/api/main.go), so it is what MCP-OAuth 1/5's "existing
// endpoints work under mot_ unmodified" acceptance criterion actually runs
// through.
func DualAuth(authService *auth.Service, agentService service.AgentService, oauthAuth OAuthTokenAuthenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Already authenticated as an extension actor by ExtensionAuth, which
			// runs ahead of this middleware on the /api/v1 group.
			if IsExtension(c) {
				return next(c)
			}
			// Likewise a cst_ chat-session token, resolved by ChatSessionAuth.
			if IsChatSession(c) {
				return next(c)
			}

			// Try a mot_ OAuth access token first — it lives in the same
			// Authorization: Bearer header a JWT would, but ValidateAccessToken
			// will simply fail to parse it as a JWT and fall through, so trying
			// this first (cheap prefix check) avoids paying that failed parse
			// on every OAuth-authenticated request.
			if oauthAuth != nil {
				if token, ok := oauthBearerToken(c); ok {
					if agent, err := oauthAuth.AuthenticateAccessToken(c.Request().Context(), token); err == nil {
						setAgentAuthContext(c, agent)
						return next(c)
					}
					return unauthorizedJSON(c, "Invalid access token")
				}
			}

			// Try JWT Bearer token.
			if tokenString, err := extractBearerToken(c); err == nil {
				if claims, err := authService.ValidateAccessToken(tokenString); err == nil {
					if userID, err := uuid.Parse(claims.Subject); err == nil {
						c.Set(ContextKeyAuthType, AuthTypeUser)
						c.Set(ContextKeyUserID, userID)
						c.Set(ContextKeyEmail, claims.Email)
						// Propagate actor into Go context for service layer.
						goCtx := actorctx.WithActor(c.Request().Context(), userID, domain.ActorTypeUser)
						goCtx = actorctx.WithActorName(goCtx, claims.Name)
						c.SetRequest(c.Request().WithContext(goCtx))
						return next(c)
					}
				}
			}

			// Try Agent Key.
			if apiKey := c.Request().Header.Get("X-Agent-Key"); apiKey != "" {
				if slug, err := parseWorkspaceSlugFromKey(apiKey); err == nil {
					agent, err := agentService.Authenticate(c.Request().Context(), slug, apiKey)
					if err == nil {
						setAgentAuthContext(c, agent)
						return next(c)
					}
					if !isAgentKeyDenial(err) {
						return authBackendUnavailableJSON(c, "DualAuth", slug, apiKey, err)
					}
				}
				return unauthorizedJSON(c, "Invalid agent API key")
			}

			return unauthorizedJSON(c, "Authentication required")
		}
	}
}

// OptionalAuth tries a mot_ OAuth token (if oauthAuth is wired), then JWT,
// then agent key. If none is present, the request passes through without
// authentication context.
func OptionalAuth(authService *auth.Service, agentService service.AgentService, oauthAuth OAuthTokenAuthenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if oauthAuth != nil {
				if token, ok := oauthBearerToken(c); ok {
					if agent, err := oauthAuth.AuthenticateAccessToken(c.Request().Context(), token); err == nil {
						setAgentAuthContext(c, agent)
						return next(c)
					}
				}
			}

			// Try JWT Bearer token.
			if tokenString, err := extractBearerToken(c); err == nil {
				if claims, err := authService.ValidateAccessToken(tokenString); err == nil {
					if userID, err := uuid.Parse(claims.Subject); err == nil {
						c.Set(ContextKeyAuthType, AuthTypeUser)
						c.Set(ContextKeyUserID, userID)
						c.Set(ContextKeyEmail, claims.Email)
						// Propagate actor into Go context for service layer.
						goCtx := actorctx.WithActor(c.Request().Context(), userID, domain.ActorTypeUser)
						goCtx = actorctx.WithActorName(goCtx, claims.Name)
						c.SetRequest(c.Request().WithContext(goCtx))
						return next(c)
					}
				}
			}

			// Try Agent Key.
			if apiKey := c.Request().Header.Get("X-Agent-Key"); apiKey != "" {
				if slug, err := parseWorkspaceSlugFromKey(apiKey); err == nil {
					agent, err := agentService.Authenticate(c.Request().Context(), slug, apiKey)
					if err == nil {
						setAgentAuthContext(c, agent)
						return next(c)
					}
					// A rejected key still passes through anonymously, but a
					// failed lookup must not silently downgrade a valid caller.
					if !isAgentKeyDenial(err) {
						return authBackendUnavailableJSON(c, "OptionalAuth", slug, apiKey, err)
					}
				}
			}

			// Nothing present: pass through unauthenticated.
			return next(c)
		}
	}
}

// RequireUserAuth rejects any request not authenticated as a real user
// (JWT) — for routes only a human acting for THEMSELVES should reach, never
// an agent key or a mot_ OAuth token acting on a workspace's behalf. Used on
// the OAuth consent/grants API (MCP-OAuth 1/5): consenting to, or revoking,
// a connector agent's access is a decision only the resource owner makes,
// not something that should be reachable via the very token the flow is
// about to mint. Must run after DualAuth (or JWTAuth), which is what sets
// ContextKeyAuthType in the first place.
func RequireUserAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if v, _ := c.Get(ContextKeyAuthType).(string); v != AuthTypeUser {
				return unauthorizedJSON(c, "User authentication required")
			}
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

// GetAgentAuthWorkspaceID extracts the workspace the current request's agent
// key actually authenticated into — see ContextKeyAgentAuthWorkspaceID. Only
// meaningful when IsAgent(c); unset (and this returns an error) for user auth.
func GetAgentAuthWorkspaceID(c echo.Context) (uuid.UUID, error) {
	v := c.Get(ContextKeyAgentAuthWorkspaceID)
	if v == nil {
		return uuid.Nil, errors.New("agent_auth_workspace_id not found in context")
	}
	id, ok := v.(uuid.UUID)
	if !ok {
		return uuid.Nil, errors.New("agent_auth_workspace_id has invalid type in context")
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

// GetOAuthConnectorUserID returns the consenting user's ID and true when the
// current request authenticated via a mot_ OAuth token — see
// ContextKeyOAuthConnectorUserID. False (zero value, no error) for a
// trusted X-Agent-Key agent or any non-agent auth, which is the common case
// callers should fall through on rather than treat as failure.
func GetOAuthConnectorUserID(c echo.Context) (uuid.UUID, bool) {
	v := c.Get(ContextKeyOAuthConnectorUserID)
	if v == nil {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok
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

// isAgentKeyDenial reports whether an agentService.Authenticate error is a
// verdict on the key itself (unknown, revoked, expired). Every denial in
// Authenticate is built with apierror.Unauthorized; anything else — a dropped
// DB connection, an exhausted pool, a canceled context — says nothing about
// the key, and answering 401 for it makes clients treat a valid key as
// revoked (card an internal ticket: a postgres restart produced 11 false 401s).
func isAgentKeyDenial(err error) bool {
	var apiErr *apierror.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusUnauthorized
}

// authBackendUnavailableJSON answers a transient agent-key verification
// failure with a retryable 503. Nothing parsed from the key is logged, not
// even the workspace slug: the request id correlates the line with the access
// log instead. The error text comes from repositories that may quote their
// query arguments, so it is redacted before it reaches the log.
func authBackendUnavailableJSON(c echo.Context, middleware, workspaceSlug, apiKey string, err error) error {
	log.Printf("%s: agent key verification failed (request_id=%s): %s",
		middleware, c.Response().Header().Get(echo.HeaderXRequestID),
		redactAgentKey(err.Error(), workspaceSlug, apiKey))
	c.Response().Header().Set("Retry-After", "2")
	return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("authentication backend unavailable"))
}

// redactKeyPrefixLen matches the stored key prefix length (see
// service.extractPrefix): repositories look keys up by that prefix, so it is
// what their errors are most likely to quote.
const redactKeyPrefixLen = 8

// redactAgentKey masks apiKey as a whole, then the slug parsed from it, then
// the secret part of it
// (everything after "agk_<slug>_") and its stored prefix, wherever they
// appear in msg. The whole key goes first so a quoted key leaves nothing
// parsed from it, the slug included. Repository errors may use %q or %+q,
// so each value's escaped forms are masked before its raw form.
func redactAgentKey(msg, workspaceSlug, apiKey string) string {
	if apiKey == "" {
		return msg
	}
	msg = redactKeyMaterial(msg, apiKey)
	msg = redactKeyMaterial(msg, workspaceSlug)
	secret := strings.TrimPrefix(apiKey, "agk_"+workspaceSlug+"_")
	if secret == "" {
		return msg
	}
	msg = redactKeyMaterial(msg, secret)
	if len(secret) > redactKeyPrefixLen {
		msg = redactKeyMaterial(msg, secret[:redactKeyPrefixLen])
	}
	return msg
}

func redactKeyMaterial(msg, value string) string {
	if value == "" {
		return msg
	}
	for _, quoted := range []string{strconv.Quote(value), strconv.QuoteToASCII(value)} {
		msg = strings.ReplaceAll(msg, quoted[1:len(quoted)-1], "[redacted]")
	}
	return strings.ReplaceAll(msg, value, "[redacted]")
}
