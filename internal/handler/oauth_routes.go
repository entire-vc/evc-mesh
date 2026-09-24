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
	// IPTrusted is true only when the client IP can be verified through the
	// reverse-proxy chain (MESH_TRUSTED_PROXIES configured — the same gate as
	// the /auth/login per-IP limiter). The zero value is the safe one: with
	// an unverifiable IP, c.RealIP() is either attacker-controlled (a random
	// X-Forwarded-For per request resets every per-IP budget) or one shared
	// address for the whole internet (a per-IP budget becomes a global DoS
	// switch), so the per-IP limiters are not mounted at all; see limiter.
	IPTrusted bool
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

// globalKey is the constant key the resource-cost limiters use when the
// client IP cannot be trusted. Each limiter still has its own Name, so DCR and
// first-seen CIMD each get ONE bucket of their own (not one between them). The
// "global:" prefix keeps its rate-limit hits distinguishable from per-IP ones
// in metrics (see keyTypeFromKey).
func globalKey(echo.Context) string { return "global:oauth" }

// limiter is a per-IP budget. It exists only when the IP is trustworthy:
// untrusted, it is disabled outright rather than run as a bucket keyed on a
// spoofable or shared address — the same call /auth/login makes.
func (l OAuthRateLimits) limiter(name string, rpm int) mw.RateLimitConfig {
	return mw.RateLimitConfig{
		Enabled:     l.Enabled && l.IPTrusted,
		RPM:         rpm,
		KeyFunc:     mw.RateLimitKeyByIP,
		RedisClient: l.Redis,
		Name:        name,
	}
}

// costLimiter guards the two endpoints whose every call costs this server a
// DB row (DCR) or an outbound fetch plus a row (first-seen CIMD client_id).
// Trusted IP: a per-IP budget, like limiter. Untrusted IP: those calls must
// still be bounded, so each gets ONE bucket of the same size, independent of
// any header. The trade-off is deliberate and confined to the untrusted
// topology: a caller that keeps a bucket drained (a few requests a minute)
// blocks onboarding of NEW clients for as long as it keeps at it — there is no
// per-account fallback here — and failed CIMD fetches, malformed requests and
// a failing known-client lookup (which counts as "new") also spend it. It
// cannot drive unbounded fetches/rows, and clients already in oauth_clients,
// token, revoke and the general authorize budget are untouched.
//
// Accepted residual: with no trustworthy IP there is also no ceiling on
// token, revoke or the client lookup authorize does for an https client_id.
// They are not spoofable-limited any more; they are unlimited. Set
// MESH_TRUSTED_PROXIES to get per-IP limits back.
func (l OAuthRateLimits) costLimiter(name string, rpm int) mw.RateLimitConfig {
	cfg := l.limiter(name, rpm)
	if !l.IPTrusted {
		cfg.Enabled = l.Enabled
		cfg.KeyFunc = globalKey
	}
	return cfg
}

// RegisterOAuthPublicRoutes mounts the unauthenticated, spec-mandated OAuth
// endpoints (RFC 8414 metadata, RFC 7591 DCR, RFC 6749 authorize/token,
// RFC 7009 revoke) directly on e — not under /api/v1 — each behind its own
// per-IP limiter. One function for both cmd/api and the tests, so the routes
// the tests exercise are the routes production serves, limiters included.
func RegisterOAuthPublicRoutes(e *echo.Echo, h *OAuthHandler, repo repository.OAuthRepository, limits OAuthRateLimits) {
	e.GET("/.well-known/oauth-authorization-server", h.ServerMetadata)
	e.POST("/oauth/register", h.Register, mw.RateLimit(limits.costLimiter("oauth-register", limits.Register)))

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
		mw.RateLimitWhen(isNewCIMDClient, limits.costLimiter("oauth-authorize-new-cimd-client", limits.AuthorizeNewClient)),
	)

	e.POST("/oauth/token", h.Token, mw.RateLimit(limits.limiter("oauth-token", limits.Token)))
	e.POST("/oauth/revoke", h.Revoke, mw.RateLimit(limits.limiter("oauth-revoke", limits.Token)))
}
