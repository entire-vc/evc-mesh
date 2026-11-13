package main

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	wsHub "github.com/entire-vc/evc-mesh/internal/ws"
)

// startBackgroundWorkers creates the shared Redis client and the WebSocket hub
// and starts the trackers, sweepers and pollers that run beside the API.
func startBackgroundWorkers(deps *apiDeps) {
	cfg := deps.cfg
	projectRepo := deps.projectRepo
	taskRepo := deps.taskRepo
	taskStatusRepo := deps.taskStatusRepo
	taskDependencyRepo := deps.taskDependencyRepo
	commentRepo := deps.commentRepo
	agentRepo := deps.agentRepo
	activityLogRepo := deps.activityLogRepo
	agentNotifySvc := deps.agentNotifySvc
	rulesService := deps.rulesService
	telegramBotManager := deps.telegramBotManager
	taskService := deps.taskService
	documentWatchService := deps.documentWatchService
	teamRelayMountService := deps.teamRelayMountService
	authHandler := deps.authHandler
	sessionRepo := deps.sessionRepo
	chatThreadDeleteRepo := deps.chatThreadDeleteRepo
	chatTurnEventsService := deps.chatTurnEventsService
	workspaceMemberHandler := deps.workspaceMemberHandler

	// 8a. Shared Redis client used by the WebSocket hub and the rate limiter.
	// A single client is created here so all consumers share the same connection
	// pool rather than each opening independent connections.
	sharedRedis := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// Per-account failed-login lockout for /auth/login — the primary
	// brute-force defense (see mw.LoginLockout's doc comment). Wired
	// unconditionally: unlike the IP-based limiter below it does not depend
	// on trusting the reverse-proxy chain at all.
	loginLockout := mw.NewLoginLockout(
		sharedRedis, cfg.RateLimit.AuthMaxFailures, cfg.RateLimit.AuthLockoutWindow,
	)
	authHandler.WithLoginLockout(loginLockout)
	// An admin-set password clears the same per-account budget.
	workspaceMemberHandler.WithLoginLockout(loginLockout)

	// WebSocket Hub for real-time event streaming.
	wsRedis := sharedRedis
	hub := wsHub.NewHub(wsRedis)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		hub.Run(hubCtx)
	}()
	log.Println("WebSocket hub started")

	// Activity tracker: batches last_heartbeat updates for API-calling agents.
	activityTracker := mw.NewActivityTracker(agentRepo)
	activityCtx, activityCancel := context.WithCancel(context.Background())
	go activityTracker.Run(activityCtx)
	log.Println("Agent activity tracker started")

	// Tool-breakdown tracker: batches agent_sessions.tool_breakdown/tool_calls
	// increments for API-calling agents (§1.6 audit — tool_breakdown was empty
	// across all 5,595 sessions because nothing wrote to it). Same batching
	// shape as activityTracker above, for the same reason: this middleware
	// runs on the hottest path in the system (every MCP tool call), so counts
	// are accumulated in memory and flushed periodically rather than written
	// synchronously per call.
	toolBreakdownTracker := mw.NewToolBreakdownTracker(sessionRepo)
	toolBreakdownCtx, toolBreakdownCancel := context.WithCancel(context.Background())
	go toolBreakdownTracker.Run(toolBreakdownCtx)
	log.Println("Tool-breakdown tracker started")

	// Document-watch sweeper: turns pending change-notices into notifications
	// once their author has stopped typing for the quiet window.
	//
	// A 60-second tick against a 5-minute default window: the tick is the
	// granularity of the delay, not the delay itself, and a notice is never sent
	// early because the quiet test is a timestamp comparison inside the claim,
	// not a property of when the sweeper happened to run. Two replicas running
	// this at once is safe — the claim is atomic (FOR UPDATE SKIP LOCKED).
	watchSweepCtx, watchSweepCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if n, err := documentWatchService.SweepPendingNotices(watchSweepCtx); err != nil {
					log.Printf("[doc-watch] ERROR sweeping pending change notices: %v", err)
				} else if n > 0 {
					log.Printf("[doc-watch] dispatched %d coalesced document change notice(s)", n)
				}
			case <-watchSweepCtx.Done():
				return
			}
		}
	}()
	deps.atExit(watchSweepCancel)

	// Team Relay key-expiry sweep: recordSyncOutcome only ever
	// runs as a REACTION to a document open or an explicit "Sync now" — an
	// integration nobody touches keeps key_expires_at NULL until the moment
	// someone finally opens it and the key has already died, the exact
	// "warning arrives with the breakage" failure AC4 was built
	// to prevent, just on a trigger that never fires for a sleeping project.
	// This sweep is the third, PASSIVE producer: it asks every enabled
	// integration's own describe-key endpoint on a schedule, independent of
	// any user action, and only ever writes on success — see
	// RefreshSleepingKeyExpiries's doc comment for why a failure here must
	// never be recorded as a status.
	trKeyRefreshCtx, trKeyRefreshCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(service.DefaultTeamRelayKeyExpiryRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				checked, updated, err := teamRelayMountService.RefreshSleepingKeyExpiries(trKeyRefreshCtx)
				if err != nil {
					log.Printf("[tr-key-refresh] ERROR: %v", err)
				} else {
					log.Printf("[tr-key-refresh] checked %d integration(s), updated %d", checked, updated)
				}
			case <-trKeyRefreshCtx.Done():
				return
			}
		}
	}()
	deps.atExit(trKeyRefreshCancel)

	// Scheduled Team Relay mount sync: "Sync now" and document opens are the
	// two reactive sync producers; without a third, a share nobody opens never
	// changes in Docs no matter how long its source moves on. This sweep asks
	// each enabled integration whose configured interval elapsed to re-run the
	// mount (SyncDueMounts); per-project locks keep it from ever overlapping a
	// concurrent "Sync now". Failures pace themselves at the project's own
	// interval — see SyncDueMounts's doc comment.
	trSyncCtx, trSyncCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(service.TeamRelaySyncSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				synced, skipped, err := teamRelayMountService.SyncDueMounts(trSyncCtx)
				if err != nil {
					log.Printf("[tr-sync] ERROR: %v", err)
				} else if synced > 0 {
					log.Printf("[tr-sync] synced %d project(s), skipped %d", synced, skipped)
				}
			case <-trSyncCtx.Done():
				return
			}
		}
	}()
	deps.atExit(trSyncCancel)

	// Checkout reaper: periodically releases orphan task-locks whose TTL has expired.
	// Complements the lazy expiry in AtomicCheckout — handles tasks that no agent
	// retries after the original holder's session dies.
	// leaseReaper additionally moves expired in_progress tasks back to todo so that
	// the capacity slot (max_in_progress) is freed for other agents.
	leaseReaper := service.NewCheckoutLeaseReaper(taskRepo, taskStatusRepo, commentRepo, taskService, agentNotifySvc, rulesService, activityLogRepo, projectRepo, taskDependencyRepo)
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	// Chat history retention + 30-day physical purge. The purge half skips itself
	// without object storage; the retention soft delete runs either way.
	go service.NewChatRetentionJob(chatThreadDeleteRepo, time.Hour).Run(reaperCtx)
	// Offline chat turns: a queued turn nobody claimed within its TTL expires (not delivered).
	go chatTurnEventsService.RunExpiry(reaperCtx, time.Minute)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Phase 1: move expired in_progress tasks back to todo (frees capacity).
				if n, err := leaseReaper.SweepExpiredLeases(reaperCtx); err != nil {
					log.Printf("[lease-reaper] ERROR sweeping expired leases: %v", err)
				} else if n > 0 {
					log.Printf("[lease-reaper] moved %d expired lease(s) back to todo", n)
				}
				// Phase 2: clear expired non-in-progress leases only. An in-progress
				// lease is released by the same guarded write that moves its task;
				// a failed phase 1 retains its evidence for the next tick.
				if n, err := taskRepo.ReleaseExpiredCheckouts(reaperCtx); err != nil {
					log.Printf("[checkout-reaper] ERROR releasing expired locks: %v", err)
				} else if n > 0 {
					log.Printf("[checkout-reaper] released %d expired checkout lock(s)", n)
				}
				// Phase 3: return in_progress tasks that hold NO checkout at all and
				// have gone quiet. Phase 1 cannot see them (it needs a non-null
				// checkout_expires) and neither can the agent feed (it polls
				// status_category=todo), so without this they sit in_progress forever.
				if n, err := leaseReaper.SweepUnleasedInProgress(reaperCtx, service.DefaultUnleasedGrace); err != nil {
					log.Printf("[lease-reaper] ERROR sweeping unleased in_progress tasks: %v", err)
				} else if n > 0 {
					log.Printf("[lease-reaper] returned %d unleased in_progress task(s) to todo", n)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	// Telegram bot manager: long-polls getUpdates for every workspace with an
	// active bot, so a fresh /start lands within one poll cycle of being sent
	// rather than waiting for the next deploy/restart.
	telegramCtx, telegramCancel := context.WithCancel(context.Background())
	if telegramBotManager != nil {
		go telegramBotManager.Start(telegramCtx)
		log.Println("Telegram bot manager started")
	}
	log.Println("Checkout reaper started (interval: 60s)")

	deps.sharedRedis = sharedRedis
	deps.hub = hub
	deps.hubCancel = hubCancel
	deps.hubDone = hubDone
	deps.activityTracker = activityTracker
	deps.activityCancel = activityCancel
	deps.toolBreakdownTracker = toolBreakdownTracker
	deps.toolBreakdownCancel = toolBreakdownCancel
	deps.reaperCancel = reaperCancel
	deps.telegramCancel = telegramCancel
}
