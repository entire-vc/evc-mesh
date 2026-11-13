package main

import (
	"context"
	"log"
	"time"
)

// startMemoryJobs starts memory decay and cleanup, OAuth housekeeping,
// connector membership resync and the nightly memory review triage.
func startMemoryJobs(deps *apiDeps) {
	memoryRepo := deps.memoryRepo
	schedulerStateRepo := deps.schedulerStateRepo
	memoryEdgesRepo := deps.memoryEdgesRepo
	memReconciler := deps.memReconciler
	oauthService := deps.oauthService
	chatSessionService := deps.chatSessionService
	schedulerShutdownCh := deps.schedulerShutdownCh

	// 10a. Memory decay + cleanup scheduler (every 6 hours).
	// DecayRelevance, CleanExpired, DecayWeights, and PruneDeadEdges are all idempotent.
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if n, decayErr := memoryRepo.DecayRelevance(ctx); decayErr != nil {
					log.Printf("[memory-decay] ERROR: %v", decayErr)
				} else if n > 0 {
					log.Printf("[memory-decay] Decayed %d stale memories", n)
				}
				if n, cleanErr := memoryRepo.CleanExpired(ctx); cleanErr != nil {
					log.Printf("[memory-cleanup] ERROR: %v", cleanErr)
				} else if n > 0 {
					log.Printf("[memory-cleanup] Cleaned %d expired memories", n)
				}
				if n, archErr := memoryRepo.ArchiveStaleWorkspaceCheckpoints(ctx, 30*24*time.Hour, 0.40); archErr != nil {
					log.Printf("[memory-archive] ERROR: %v", archErr)
				} else if n > 0 {
					log.Printf("[memory-archive] Archived %d stale workspace checkpoints", n)
				}
				if n, edgeDecayErr := memoryEdgesRepo.DecayWeights(ctx); edgeDecayErr != nil {
					log.Printf("[edge-decay] ERROR: %v", edgeDecayErr)
				} else if n > 0 {
					log.Printf("[edge-decay] Decayed %d stale edges", n)
				}
				if n, pruneErr := memoryEdgesRepo.PruneDeadEdges(ctx); pruneErr != nil {
					log.Printf("[edge-prune] ERROR: %v", pruneErr)
				} else if n > 0 {
					log.Printf("[edge-prune] Pruned %d dead edges", n)
				}
				cancel()

				// Memory reconciler (P1-C): monitor (expire/stale) + linker (supersede).
				// Given a longer timeout since the linker may issue embedding API calls.
				recCtx, recCancel := context.WithTimeout(context.Background(), 5*time.Minute)
				if err := memReconciler.Run(recCtx); err != nil {
					log.Printf("[memory-reconciler] ERROR: %v", err)
				}
				recCancel()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Memory decay scheduler started (6h interval)")

	// 10a-ter. OAuth housekeeping: expired authorization codes and
	// tokens are dead weight — the auth path already rejects them — but nothing
	// deleted them, so oauth_tokens grew by two rows per connector refresh
	// forever. Hourly, with the first pass a minute after start so a service
	// that restarts more often than the interval (every deploy) still purges.
	// PurgeExpired is idempotent and only removes rows past their retention.
	go func() {
		purge := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			codes, tokens, err := oauthService.PurgeExpired(ctx)
			if err != nil {
				log.Printf("[oauth-purge] ERROR: %v", err)
			}
			if codes > 0 || tokens > 0 {
				log.Printf("[oauth-purge] Removed %d expired authorization codes, %d expired tokens", codes, tokens)
			}
			// Chat-session tokens live minutes; rows a day past expiry are only history.
			if n, cerr := chatSessionService.PurgeExpired(ctx); cerr != nil {
				log.Printf("[chat-token-purge] ERROR: %v", cerr)
			} else if n > 0 {
				log.Printf("[chat-token-purge] Removed %d expired chat session tokens", n)
			}
		}
		first := time.NewTimer(time.Minute)
		defer first.Stop()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-first.C:
				purge()
			case <-ticker.C:
				purge()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("OAuth expired-token purge started (1h interval)")

	// 10a-quater. Connector-membership resync: mirrorProjectMemberships
	// only ever runs once, at connector-agent registration — an
	// existing grant's reuse path never re-mirrors. A human later removed from a
	// project (or demoted) keeps their connector agent holding the stale access
	// until something re-syncs it; the OAuth re-consent path can't be relied on for
	// this given the 30-day refresh-token TTL. Same hourly cadence and 1-minute
	// first-run offset as the purge job above, deliberately: this is the same class
	// of best-effort, no-request-in-flight background job.
	go func() {
		resync := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := oauthService.ResyncConnectorMemberships(ctx)
			if err != nil {
				log.Printf("[oauth-membership-resync] ERROR: %v", err)
			}
			if result.Added > 0 || result.Removed > 0 || result.Updated > 0 {
				log.Printf("[oauth-membership-resync] %d connector agents checked: %d memberships added, %d removed, %d roles corrected",
					result.Agents, result.Added, result.Removed, result.Updated)
			}
		}
		first := time.NewTimer(time.Minute)
		defer first.Stop()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-first.C:
				resync()
			case <-ticker.C:
				resync()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("OAuth connector-membership resync started (1h interval)")

	// 10a-bis. Memory review-triage nightly job (an internal ticket, plan:1.11):
	// disposes of the review_needed backlog that the 6h reconciler's linker phase
	// above never revisits (it only ever compares memories created in the last 24h
	// against similar history — once a memory is parked at review_needed, that
	// phase never looks at it again). Deliberately its own ~24h ticker rather than
	// folded into the 6h loop above — see RunReviewTriage's doc.
	//
	// an internal ticket: a bare time.NewTicker(24h) is keyed off THIS PROCESS'S OWN
	// START, not wall-clock time since the job last actually ran. Measured on
	// prod-host over 6 days: 70 process restarts, only 2 real triage runs — the
	// service almost never stays up 24h straight, so the ticker almost never
	// fires. Fixed by persisting last_run_at (schedulerStateRepo,
	// scheduler_job_runs table) and computing the FIRST wait from it at startup:
	// a restart 3h after the last real run now waits the remaining 21h, not a
	// fresh 24h. Every tick re-persists last_run_at before rearming for the next
	// interval, so the watermark keeps surviving future restarts too.
	go func() {
		const reviewTriageInterval = 24 * time.Hour
		const reviewTriageJobName = "memory-review-triage"

		initialWait := reviewTriageInterval
		lastRun, err := schedulerStateRepo.GetLastRun(context.Background(), reviewTriageJobName)
		if err != nil {
			log.Printf("[memory-review-triage] ERROR reading last_run_at, falling back to full %v wait: %v", reviewTriageInterval, err)
		} else {
			initialWait = reviewTriageInitialWait(lastRun, time.Now(), reviewTriageInterval)
		}

		timer := time.NewTimer(initialWait)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				runAt := time.Now()
				recCtx, recCancel := context.WithTimeout(context.Background(), 10*time.Minute)
				stats, err := memReconciler.RunReviewTriage(recCtx)
				recCancel()
				if err != nil {
					log.Printf("[memory-review-triage] ERROR: %v", err)
				} else {
					log.Printf("[memory-review-triage] considered=%d superseded=%d stale=%d active=%d errored=%d",
						stats.Considered, stats.Superseded, stats.Stale, stats.Active, stats.Errored)
				}
				// Persisted regardless of err: matches the old ticker's behavior of not
				// retrying before the next full interval on failure, and prevents a
				// persistently-failing run from turning into a tight restart-retry loop.
				if setErr := schedulerStateRepo.SetLastRun(context.Background(), reviewTriageJobName, runAt); setErr != nil {
					log.Printf("[memory-review-triage] ERROR persisting last_run_at: %v", setErr)
				}
				timer.Reset(reviewTriageInterval)
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Memory review-triage scheduler started (24h interval, wall-clock persisted)")
}
