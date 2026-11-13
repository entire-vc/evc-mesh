package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/handler"
)

// Injected at build time via -ldflags. See docs/deploy.md.
var (
	BuildSHA     = "dev"
	BuildTime    = "unknown"
	BuildVersion = "dev"
	BuildEnv     = "dev"
)

// reviewTriageInitialWait computes how long a freshly-started process should wait before its
// FIRST review-triage run, given the persisted last-run watermark (nil if the job has never
// run on this workspace) and the current time. This is the fix for an internal ticket: a bare
// time.NewTicker(interval) always waits a fresh full interval from process start, so a service
// that restarts more often than the interval elapses almost never reaches a tick. Here, a
// restart occurring after lastRun+interval has already passed waits ~0 (catches up on the
// overdue run immediately); a restart occurring before that waits only the remaining time.
func reviewTriageInitialWait(lastRun *time.Time, now time.Time, interval time.Duration) time.Duration {
	if lastRun == nil {
		// Never run before (fresh install / migration just applied) — run once on startup
		// rather than waiting a full interval for the first-ever pass.
		return 0
	}
	if elapsed := now.Sub(*lastRun); elapsed < interval {
		return interval - elapsed
	}
	return 0
}

// oauthRateLimits derives the public OAuth endpoints' per-IP budgets from
// config. A function of its own so a test can pin which setting feeds which
// endpoint — in particular that token/revoke read OAuthTokenRPM, not APIRPM.
func oauthRateLimits(cfg *config.Config, ipTrusted bool, redisClient *redis.Client) handler.OAuthRateLimits {
	return handler.OAuthRateLimits{
		Enabled:            cfg.RateLimit.Enabled,
		IPTrusted:          ipTrusted,
		Redis:              redisClient,
		Register:           cfg.RateLimit.AuthRPM,
		Authorize:          cfg.RateLimit.RefreshRPM,
		AuthorizeNewClient: cfg.RateLimit.AuthRPM,
		Token:              cfg.RateLimit.OAuthTokenRPM,
	}
}

func main() {
	deps := &apiDeps{}
	defer deps.runAtExit()
	for _, register := range apiModules {
		register(deps)
	}
	serve(deps)
}

// serve runs the HTTP server until SIGINT/SIGTERM and then shuts the process
// down: scheduler, outbox, long-lived connections, Echo, trackers, Redis
// clients and the event bus, in that order.
func serve(deps *apiDeps) {
	cfg := deps.cfg
	agentNotifyRedis := deps.agentNotifyRedis
	ctxCacheRedis := deps.ctxCacheRedis
	wsBadgeRedis := deps.wsBadgeRedis
	httpShutdownCh := deps.httpShutdownCh
	e := deps.e
	sharedRedis := deps.sharedRedis
	hubCancel := deps.hubCancel
	hubDone := deps.hubDone
	activityTracker := deps.activityTracker
	activityCancel := deps.activityCancel
	toolBreakdownTracker := deps.toolBreakdownTracker
	toolBreakdownCancel := deps.toolBreakdownCancel
	reaperCancel := deps.reaperCancel
	telegramCancel := deps.telegramCancel
	schedulerShutdownCh := deps.schedulerShutdownCh
	cancelOutbox := deps.cancelOutbox
	outboxDone := deps.outboxDone

	// 11. Start server with graceful shutdown.
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting evc-mesh API server on %s", addr)

	// Only the header phase is bounded: cfg.Server.ReadTimeout/WriteTimeout
	// would also cut SSE, /ws and the runner chat long-poll.
	e.Server.ReadHeaderTimeout = cfg.Server.ReadHeaderTimeout

	// Start server in a goroutine.
	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Stop the scheduler.
	close(schedulerShutdownCh)
	cancelOutbox()
	select {
	case <-outboxDone:
	case <-time.After(10 * time.Second):
		log.Println("[task-outbox] shutdown timed out")
	}

	// End long-lived connections first: SSE/long-poll handlers return, WS
	// clients are closed by the hub. e.Shutdown does not track hijacked WS
	// connections and blocks on in-flight SSE handlers until its deadline.
	close(httpShutdownCh)
	hubCancel()
	select {
	case <-hubDone:
	case <-time.After(time.Second):
		log.Println("[ws-hub] did not stop within 1s, continuing shutdown")
	}

	// Graceful shutdown with timeout (safety net, normally finishes in ms).
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	// Stop checkout reaper.
	reaperCancel()

	// Stop every Telegram bot poller.
	telegramCancel()

	// Flush activity tracker and cancel its background loop.
	activityCancel()
	activityTracker.Flush(shutdownCtx)

	// Flush tool-breakdown tracker and cancel its background loop.
	toolBreakdownCancel()
	toolBreakdownTracker.Flush(shutdownCtx)

	// Shared Redis (also used by the rate limiter) — the WS hub was stopped before e.Shutdown.
	if err := sharedRedis.Close(); err != nil {
		log.Printf("Error closing shared Redis: %v", err)
	}
	if err := agentNotifyRedis.Close(); err != nil {
		log.Printf("Error closing agent-notify Redis: %v", err)
	}
	if err := ctxCacheRedis.Close(); err != nil {
		log.Printf("Error closing context-cache Redis: %v", err)
	}
	if err := wsBadgeRedis.Close(); err != nil {
		log.Printf("Error closing ws-badge Redis: %v", err)
	}

	// Close event bus.
	if deps.eb != nil {
		if err := deps.eb.Close(); err != nil {
			log.Printf("Error closing event bus: %v", err)
		}
	}

	log.Println("Server stopped")
}

// redactInviteToken strips the invite token out of a request URI before the
// access log sees it.
//
// GET /api/v1/invites/:token and its /accept sibling are public routes whose
// path segment IS the credential: anyone holding it can join the workspace. The
// access logger printed the raw URI, so every time an invitee opened their link
// the token was written to the log in full — and logs get shipped to collectors
// that far more people can read than the workspace has members.
//
// Only the public top-level route is redacted. The admin route
// /api/v1/workspaces/:ws_id/invites/:invite_id carries a database id, not a
// secret, and stays readable.
func redactInviteToken(uri string) string {
	const prefix = "/api/v1/invites/"
	if !strings.HasPrefix(uri, prefix) {
		return uri
	}
	rest := uri[len(prefix):]
	if rest == "" {
		return uri
	}
	// Preserve any trailing path ("/accept") and query string so the log line
	// still says which operation it was.
	if end := strings.IndexAny(rest, "/?"); end != -1 {
		return prefix + "<redacted>" + rest[end:]
	}
	return prefix + "<redacted>"
}

// documentWatchQuietWindow reads DOCUMENT_WATCH_QUIET_WINDOW, the idle period an
// editor must leave before their edits are announced to a document's watchers.
//
// It exists as a knob because the right value is a judgement about how people
// work rather than a property of the system. The bounds are not: a window at or
// below the editor's 2-second autosave debounce coalesces nothing and restores
// the notification storm the feature was built to prevent, so a value that small
// is refused rather than honoured. An unparseable or out-of-range value falls
// back to the default and says so — a typo here would otherwise be discovered as
// "why is everyone getting a hundred emails".
func documentWatchQuietWindow() time.Duration {
	const (
		minWindow = 30 * time.Second
		maxWindow = 24 * time.Hour
	)
	raw := strings.TrimSpace(os.Getenv("DOCUMENT_WATCH_QUIET_WINDOW"))
	if raw == "" {
		return 0 // service applies its own default
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("[doc-watch] DOCUMENT_WATCH_QUIET_WINDOW=%q is not a duration (e.g. \"5m\") — using the default", raw)
		return 0
	}
	if d < minWindow || d > maxWindow {
		log.Printf("[doc-watch] DOCUMENT_WATCH_QUIET_WINDOW=%s is outside [%s, %s] — using the default", d, minWindow, maxWindow)
		return 0
	}
	return d
}

// humanGateSoftTimeoutWindow reads HUMAN_GATE_SOFT_TIMEOUT, the arm-age past which a
// soft-classified human_gate is eligible for the 15-minute sweeper's release (contract
// docs/human-gate-decision-recorded.md §5, an internal ticket).
//
// It exists as a knob for the same reason DefaultHumanGateSoftTimeout (24h) was a
// hardcoded constant until now: proving the sweeper actually releases a gate at prod
// scale costs one full window of real wait time, which makes the mechanism cheaper to
// believe than to verify. A lower bound still applies — a window at or below the
// sweeper's own 15-minute tick would race the first tick after arming and make a
// release look flaky when it isn't, so a value that small is refused rather than
// honoured. An unparseable or out-of-range value falls back to the default and says so.
func humanGateSoftTimeoutWindow() time.Duration {
	const (
		minWindow = 15 * time.Minute
		maxWindow = 30 * 24 * time.Hour
	)
	raw := strings.TrimSpace(os.Getenv("HUMAN_GATE_SOFT_TIMEOUT"))
	if raw == "" {
		return 0 // service applies its own default (DefaultHumanGateSoftTimeout)
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("[human-gate-soft-timeout] HUMAN_GATE_SOFT_TIMEOUT=%q is not a duration (e.g. \"2h\") — using the default", raw)
		return 0
	}
	if d < minWindow || d > maxWindow {
		log.Printf("[human-gate-soft-timeout] HUMAN_GATE_SOFT_TIMEOUT=%s is outside [%s, %s] — using the default", d, minWindow, maxWindow)
		return 0
	}
	return d
}

// slowRequestThreshold is the latency above which the access log also emits a
// WARN line, so slow requests can be grepped without parsing every duration.
const slowRequestThreshold = 2 * time.Second

// accessLogLines renders the access-log entry for one request: the
// "METHOD uri -> status Nms" line, plus a WARN line when it ran past
// slowRequestThreshold.
func accessLogLines(method, uri string, status int, latency time.Duration) []string {
	ms := latency.Milliseconds()
	lines := []string{fmt.Sprintf("%s %s -> %d %dms", method, uri, status, ms)}
	if latency > slowRequestThreshold {
		lines = append(lines, fmt.Sprintf("WARN slow request: %s %s -> %d %dms (> %s)",
			method, uri, status, ms, slowRequestThreshold))
	}
	return lines
}

// maxArtifactBodyBytes bounds the artifact size fed into the idempotency body
// limit; far above any real upload cap, small enough that the base64 math
// below cannot overflow int64.
const maxArtifactBodyBytes int64 = 1 << 40

// artifactIdempotencyBodyLimit returns the body size the idempotency
// middleware may buffer for an artifact upload: the base64-encoded payload
// plus 2 MiB of envelope. It stays in int64 so a large ARTIFACT_MAX_BYTES
// cannot be truncated by an int conversion on a 32-bit build.
func artifactIdempotencyBodyLimit(maxBytes int64) int64 {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if maxBytes > maxArtifactBodyBytes {
		maxBytes = maxArtifactBodyBytes
	}
	return (maxBytes+2)/3*4 + (2 << 20)
}

// keepMetadataReader is the metadata-only view of the process Keep client, nil
// when Keep is not configured. It is the narrow interface on purpose: the probe
// route is handed nothing that can read or write a value.
func keepMetadataReader(r *secretstore.Router) secretstore.KeepMetadataReader {
	if r == nil {
		return nil
	}
	k, ok := r.Keep.(secretstore.Keep)
	if !ok || k.Client == nil {
		return nil
	}
	m, _ := k.Client.(secretstore.KeepMetadataReader)
	return m
}
