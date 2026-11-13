package main

import (
	"log"
	"net"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
)

// wireEcho creates the Echo instance with its global middleware and the
// metrics, health and version endpoints.
func wireEcho(deps *apiDeps) {
	cfg := deps.cfg
	sparkIntegrationResolver := deps.sparkIntegrationResolver

	// 8. Create Echo instance with global middleware.
	e := echo.New()
	e.HideBanner = true

	// Echo's own DefaultHTTPErrorHandler only recognizes *echo.HTTPError;
	// every handler in this codebase returns *apierror.Error instead, which
	// fell through to a bare 500 for what were actually 400s/404s/etc — see
	// handler.NewHTTPErrorHandler's doc comment for the incident this fixes.
	e.HTTPErrorHandler = handler.NewHTTPErrorHandler(e.HTTPErrorHandler)

	// MESH_TRUSTED_PROXIES gates whether c.RealIP() (used by
	// mw.RateLimitKeyByIP, and therefore by the /auth/login per-IP limiter
	// registered below) can be trusted. See RateLimitKeyByIP's doc comment
	// (internal/middleware/ratelimit.go) for the incident this exists to
	// prevent: a reverse-proxy hop that does not itself trust its own
	// upstream overwrites X-Forwarded-For with its own peer address, so
	// EVERY external client resolves to the SAME IP and a per-IP limiter
	// becomes one shared bucket for the whole internet — the opposite of
	// its purpose, and a built-in DoS.
	//
	// Configuring this does NOT fix a misconfigured upstream proxy — our own
	// Caddy config on prod-host is deploy/caddy/mesh.Caddyfile, tracked
	// separately (a PR). It only tells mesh-api which additional network
	// ranges, beyond the loopback/link-local/private-net Echo trusts by
	// default, are allowed to relay a client IP via X-Forwarded-For.
	//
	// Left unset, mesh-api does not pretend to have per-client IP
	// granularity it cannot verify: e.IPExtractor stays at Echo's untouched
	// default, and the /auth/login per-IP limiter below is disabled outright
	// (see loginGroup) rather than silently operating as one shared bucket.
	var trustedProxyNets []*net.IPNet
	for _, cidr := range cfg.RateLimit.TrustedProxies {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Printf("[config] MESH_TRUSTED_PROXIES: ignoring invalid CIDR %q: %v", cidr, err)
			continue
		}
		trustedProxyNets = append(trustedProxyNets, ipNet)
	}
	ipTrusted := len(trustedProxyNets) > 0
	if ipTrusted {
		trustOpts := make([]echo.TrustOption, 0, len(trustedProxyNets))
		for _, ipNet := range trustedProxyNets {
			trustOpts = append(trustOpts, echo.TrustIPRange(ipNet))
		}
		e.IPExtractor = echo.ExtractIPFromXFFHeader(trustOpts...)
		log.Printf("[config] MESH_TRUSTED_PROXIES configured (%d range(s)) — per-IP rate "+
			"limiting on /auth/login is ACTIVE", len(trustedProxyNets))
	} else {
		// Loud degradation, not silent — per this file's own deep-verify
		// discipline, a control nobody can see failing gets trusted and
		// stops being checked. Surfaced three ways: this WARN, the /health
		// field below, and the mesh_client_ip_trusted gauge on /metrics.
		log.Printf("[config] MESH_TRUSTED_PROXIES is unset — client IP cannot be verified " +
			"through the reverse-proxy chain, so /auth/login has NO per-IP rate-limit " +
			"granularity (it is disabled, not silently shared across every client); the " +
			"public OAuth endpoints likewise lose per-IP limits, and DCR / first-seen client " +
			"metadata fetches fall back to one global bucket each. " +
			"Brute-force protection for /auth/login now relies solely on the per-account " +
			"failed-login lockout (MESH_RATE_LIMIT_AUTH_MAX_FAILURES, default 10/15min). " +
			"Set MESH_TRUSTED_PROXIES to the CIDR(s) of your trusted reverse-proxy hop(s) " +
			"to restore per-IP granularity.")
	}
	metrics.SetClientIPTrusted(ipTrusted)

	// Prometheus metrics — registered early so every request is counted.
	e.Use(mw.Metrics())

	// Deletes upload temp files that net/http misses once auth has swapped
	// the request for a WithContext copy.
	e.Use(mw.MultipartCleanup())

	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:     true,
		LogStatus:  true,
		LogLatency: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			for _, line := range accessLogLines(c.Request().Method, redactInviteToken(v.URI), v.Status, v.Latency) {
				log.Print(line)
			}
			return nil
		},
	}))
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())

	// Body-read timings for slow requests and the cause of every failed Bind
	// POSTs that hung 20–114 s and ended in 400/500 left no trace
	// of whether the time went into receiving the body or into the handler.
	e.Use(mw.SlowBody(mw.SlowBodyConfig{Redact: redactInviteToken}))
	e.Binder = &mw.BindErrorLogger{Inner: e.Binder, Redact: redactInviteToken}
	// AllowCredentials lets the browser store/send the httpOnly refresh
	// cookie on cross-origin requests (credentials: "include" — see
	// web/src/lib/api.ts). It is a no-op for the common same-origin
	// deployment (frontend and API behind one reverse proxy), where cookies
	// already flow regardless of any CORS header. It only matters once
	// MESH_CORS_ORIGINS names actual origins — the echo middleware refuses
	// to combine AllowCredentials with a literal "*" origin (that combination
	// is invalid per the Fetch spec and browsers reject it), so leaving
	// MESH_CORS_ORIGINS at its default wildcard silently blocks credentialed
	// cross-origin login, not same-origin login.
	if len(cfg.CORS.AllowOrigins) == 1 && cfg.CORS.AllowOrigins[0] == "*" {
		log.Printf("[config] MESH_CORS_ORIGINS is unset (default \"*\") — cross-origin login/refresh " +
			"will not work (browsers refuse credentialed requests against a wildcard origin). " +
			"Same-origin deployments (frontend served from the API's own host) are unaffected.")
	}
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:     cfg.CORS.AllowOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "X-Agent-Key", "X-Request-ID", "Idempotency-Key"},
		ExposeHeaders:    []string{"Idempotent-Replayed", "Retry-After"},
		AllowCredentials: true,
	}))

	// Prometheus metrics scrape endpoint. Gated by MESH_METRICS_TOKEN when
	// set (mw.MetricsAuth is a no-op otherwise) — the self-host
	// docker-compose.prod.yml requires the var and configures Prometheus's
	// scrape config with the matching bearer_token_file, since that compose
	// stack publishes this port and has no front proxy of its own. An
	// unauthenticated deployment (e.g. internal prod, gated by Caddy
	// upstream) is unaffected by leaving the var unset.
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()), mw.MetricsAuth(cfg.Server.MetricsToken))

	// Health check.
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]any{
			"status":            "ok",
			"service":           "evc-mesh-api",
			"client_ip_trusted": ipTrusted,
			"event_bus_enabled": deps.eb != nil,
		})
	})

	// Build version — public, no auth. All three paths route through Caddy's /api/* block.
	// spark_enabled/spark_url ride along here rather than a dedicated endpoint: they're
	// the instance capabilities the frontend needs to know before the user picks a
	// workspace (the Spark Catalog nav link, and the "View on Spark" links, which must
	// point at whatever catalog this deployment configured rather than a hardcoded
	// vendor domain). This reflects the instance level only — it has no workspace
	// context to check a per-workspace override against (that check happens
	// per-request via sparkIntegrationResolver, once a workspace is known); a
	// workspace that has disabled Spark still sees this nav link, and gets refused
	// with a named reason if it actually tries to browse the catalog.
	versionHandler := newVersionHandler(sparkIntegrationResolver.InstanceURL)
	e.GET("/api/version", versionHandler)
	e.GET("/api/v1/version", versionHandler)
	e.GET("/api/v1/healthz/version", versionHandler)

	deps.e = e
	deps.ipTrusted = ipTrusted
}
