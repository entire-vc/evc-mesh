package handler

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// OAuthRateLimits is the per-IP request budget (requests per minute) of each
// public OAuth endpoint. Zero-value RPMs are not defaulted here: cmd/api
// derives them from config, and tests pick deliberately tiny ones.
type OAuthRateLimits struct {
	// Enabled mirrors cfg.RateLimit.Enabled.
	Enabled bool
	// Redis is optional; nil selects the in-memory limiter.
	Redis *redis.Client
	// Register is POST /oauth/register. DCR is unauthenticated and writes a
	// row per call, so it gets the credential-endpoint budget.
	Register int
	// Authorize is GET /oauth/authorize for every request — a browser
	// navigation, so a human-sized budget.
	Authorize int
	// AuthorizeNewClient is the much tighter extra budget for a first-seen
	// https client_id: that request shape makes this server fetch an
	// attacker-named URL (holding a goroutine for up to 10 s) and insert an
	// oauth_clients row — the DCR cost with none of DCR's limiter in front of
	// it. A client already in oauth_clients does not count against it.
	AuthorizeNewClient int
	// Token is POST /oauth/token and POST /oauth/revoke. Server-to-server
	// calls: a hosted MCP client exchanges and refreshes on behalf of every
	// one of its users from a handful of egress IPs, so this is API-sized, not
	// credential-endpoint-sized. Codes and tokens are 256-bit random values;
	// what this bounds is load, not guessing.
	Token int
}

func (l OAuthRateLimits) limiter(name string, rpm int) mw.RateLimitConfig {
	return mw.RateLimitConfig{
		Enabled:     l.Enabled,
		RPM:         rpm,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: l.Redis,
		Name:        name,
	}
}

// RegisterOAuthPublicRoutes mounts the unauthenticated, spec-mandated OAuth
// endpoints (RFC 8414 metadata, RFC 7591 DCR, RFC 6749 authorize/token,
// RFC 7009 revoke) directly on e — not under /api/v1 — each behind its own
// per-IP limiter. One function for both cmd/api and the tests, so the routes
// the tests exercise are the routes production serves, limiters included.
func RegisterOAuthPublicRoutes(e *echo.Echo, h *OAuthHandler, repo repository.OAuthRepository, limits OAuthRateLimits) {
	e.GET("/.well-known/oauth-authorization-server", h.ServerMetadata)
	e.POST("/oauth/register", h.Register, mw.RateLimit(limits.limiter("oauth-register", limits.Register)))

	// A lookup failure counts as "new client": fail toward the stricter limit.
	isNewCIMDClient := func(c echo.Context) bool {
		clientID := c.QueryParam("client_id")
		if !strings.HasPrefix(clientID, "https://") {
			return false
		}
		known, err := repo.GetClientByClientID(c.Request().Context(), clientID)
		return err != nil || known == nil
	}
	e.GET("/oauth/authorize", h.Authorize,
		mw.RateLimit(limits.limiter("oauth-authorize", limits.Authorize)),
		mw.RateLimitWhen(isNewCIMDClient, limits.limiter("oauth-authorize-new-cimd-client", limits.AuthorizeNewClient)),
	)

	e.POST("/oauth/token", h.Token, mw.RateLimit(limits.limiter("oauth-token", limits.Token)))
	e.POST("/oauth/revoke", h.Revoke, mw.RateLimit(limits.limiter("oauth-revoke", limits.Token)))
}
