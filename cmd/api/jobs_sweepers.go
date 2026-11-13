package main

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// startSweepers starts the agent event, backlog, close_on_merge, human_gate,
// stale session and closed-card follow-up sweepers.
func startSweepers(deps *apiDeps) {
	projectRepo := deps.projectRepo
	taskRepo := deps.taskRepo
	taskStatusRepo := deps.taskStatusRepo
	taskDependencyRepo := deps.taskDependencyRepo
	commentRepo := deps.commentRepo
	agentRepo := deps.agentRepo
	activityLogRepo := deps.activityLogRepo
	vcsLinkRepo := deps.vcsLinkRepo
	agentEventsRepo := deps.agentEventsRepo
	rulesService := deps.rulesService
	notificationService := deps.notificationService
	taskService := deps.taskService
	commentService := deps.commentService
	sessionRepo := deps.sessionRepo
	schedulerShutdownCh := deps.schedulerShutdownCh

	// 10b. Agent events sweeper — delete expired rows from agent_events every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if n, sweepErr := agentEventsRepo.DeleteExpired(ctx); sweepErr != nil {
					log.Printf("[agent-events-sweeper] ERROR: %v", sweepErr)
				} else if n > 0 {
					log.Printf("[agent-events-sweeper] Deleted %d expired events", n)
				}
				cancel()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Agent events sweeper started (5m interval)")

	// 10b-bis. Backlog promotion sweeper — auto-unparks ANY backlog task whose due_date
	// has passed, moving it back to todo (the team workflow guide §0m passive-wait
	// pattern; complements the event-driven dependency-unblock auto-transition, which
	// only fires on task completion, with a time-based trigger).
	//
	// Scope widened from backlog+kind:monitor to every dated backlog card by an internal ticket:
	// the label filter meant a due_date only woke cards parked by the lease reaper (the
	// one thing that writes that label), so 8 cards sat with an already-passed date and
	// no promotion path. The freeze/human_gate/shipped/open-blocks guards that decide
	// which candidates actually move live in the service, not the query.
	monitorPromotionSvc := service.NewMonitorPromotionService(
		taskRepo, taskStatusRepo, commentRepo, taskDependencyRepo, activityLogRepo, taskService,
	)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				n, err := monitorPromotionSvc.SweepDueBacklogTasks(ctx)
				cancel()
				if err != nil {
					log.Printf("[monitor-promotion] ERROR sweeping due backlog tasks: %v", err)
				} else if n > 0 {
					log.Printf("[monitor-promotion] promoted %d due backlog task(s) to todo", n)
				}
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Backlog due-date promotion sweeper started (60s interval)")

	// 10b-quater. Backlog promotion ADVISORY sweeper (an internal ticket, parent an internal ticket,
	// unit 1 of 4). Mirrors <ops-repo>/scripts/mesh-intake-sweep.py's backlog→todo decision
	// server-side, log-only — it NEVER calls MoveTask. Runs alongside the live Python
	// sweep for a 7-day parallel comparison; only after that comparison
	// shows zero unexplained divergence does a LATER task flip this to
	// enforcing and retire the sweep. Same 60s cadence as the monitor
	// sweeper above so a day of logs lines up 1:1 with a day of sweep runs.
	backlogPromotionAdvisorySvc := service.NewBacklogPromotionAdvisoryService(
		taskRepo, taskStatusRepo, taskDependencyRepo, activityLogRepo, projectRepo,
		service.WithLiftBlockersShadow(rulesService),
		service.WithLaneAvailability(agentRepo),
		service.WithCapacityBrake(4), // FLEET_BALANCE_DEV_CAP default in fleet-balancer.py
	)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				decisions, err := backlogPromotionAdvisorySvc.SweepAdvisory(ctx)
				cancel()
				if err != nil {
					log.Printf("[backlog-promotion-advisory] ERROR sweeping backlog tasks: %v", err)
					continue
				}
				var promotable int
				for _, d := range decisions {
					if d.Promote {
						promotable++
					}
				}
				log.Printf("[backlog-promotion-advisory] evaluated %d backlog task(s), %d advisory-promotable (advisory only, no moves made)",
					len(decisions), promotable)
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Backlog promotion advisory sweeper started (60s interval, advisory-only)")

	// 10b-quater-bis. close_on_merge SHADOW sweeper: journals the
	// close board-services would do for a merged-but-open card; never moves anything.
	closeOnMergeShadowSvc := service.NewCloseOnMergeShadowService(vcsLinkRepo, activityLogRepo, projectRepo, rulesService)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				n, err := closeOnMergeShadowSvc.Sweep(ctx)
				cancel()
				if err != nil {
					log.Printf("[close-on-merge-shadow] ERROR: %v", err)
				} else if n > 0 {
					log.Printf("[close-on-merge-shadow] journaled %d card(s) (shadow, no moves)", n)
				}
			case <-schedulerShutdownCh:
				return
			}
		}
	}()

	// 10b-ter. human_gate soft-timeout sweeper (an internal ticket, contract
	// docs/human-gate-decision-recorded.md §5). Releases soft-classified gates armed
	// past DefaultHumanGateSoftTimeout; a hard-classified gate is structurally
	// unreachable from this sweep (see FindSoftTimedOutGates in task_repo.go) — no
	// interval here can accidentally release one, so a short poll interval is safe.
	humanGateSoftWindow := humanGateSoftTimeoutWindow()
	humanGateSoftTimeoutSvc := service.NewHumanGateSoftTimeoutService(taskRepo, commentRepo, humanGateSoftWindow)
	if humanGateSoftWindow > 0 {
		log.Printf("[human-gate-soft-timeout] window overridden via HUMAN_GATE_SOFT_TIMEOUT=%s", humanGateSoftWindow)
	} else {
		log.Printf("[human-gate-soft-timeout] window = default %s", service.DefaultHumanGateSoftTimeout)
	}
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				n, err := humanGateSoftTimeoutSvc.SweepExpiredSoftGates(ctx)
				cancel()
				if err != nil {
					log.Printf("[human-gate-soft-timeout] ERROR sweeping expired soft gates: %v", err)
				} else if n > 0 {
					log.Printf("[human-gate-soft-timeout] released %d soft-classified gate(s)", n)
				}
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("human_gate soft-timeout sweeper started (15m interval)")

	// 10b-quater. human_gate default-on-timeout sweeper. Applies each
	// expired gate's own stated recommended_default, records it as a decision, releases
	// the gate, and returns the task to todo. Deliberately a separate sweep from the one
	// above rather than a parameter on it — see FindSoftTimedOutGates' doc for why the
	// two must not both act on the same row, and human_gate_default_timeout.go for the
	// rest of the contract. Same 15-minute cadence as the sweep above; a hard-classified
	// gate is structurally unreachable here too (FindExpiredDefaultGates' class literal).
	humanGateDefaultTimeoutSvc := service.NewHumanGateDefaultTimeoutService(
		taskRepo, taskStatusRepo, projectRepo, commentService, taskService, notificationService,
	)
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				// Regression: first give marker gates armed hard under the old
				// tag-only rule their current class, so a stated default that is
				// already past its deadline is applied in this same pass.
				if rn, rerr := commentService.ReconcileMarkerGateClasses(ctx); rerr != nil {
					log.Printf("[human-gate-reclassify] ERROR: %v", rerr)
				} else if rn > 0 {
					log.Printf("[human-gate-reclassify] reclassified %d marker gate(s) soft", rn)
				}
				n, err := humanGateDefaultTimeoutSvc.SweepExpiredDefaultGates(ctx)
				cancel()
				if err != nil {
					log.Printf("[human-gate-default-timeout] ERROR sweeping expired default gates: %v", err)
				} else if n > 0 {
					log.Printf("[human-gate-default-timeout] applied %d recommended default(s)", n)
				}
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("human_gate default-timeout sweeper started (15m interval)")

	// 10c. Stale session sweeper — end agent sessions left active longer than 6h every hour.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if n, sweepErr := sessionRepo.EndStale(ctx, 6*time.Hour); sweepErr != nil {
					log.Printf("[session-sweeper] ERROR: %v", sweepErr)
				} else if n > 0 {
					log.Printf("[session-sweeper] Ended %d stale sessions", n)
				}
				cancel()
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Println("Stale session sweeper started (1h interval)")

	// 10z. Closed-card follow-up pending reconciler: drains
	// closed_followup_pending — findings whose delivery survived the inline
	// ladder undelivered. Same delivery function as the live path, idempotent
	// on the finding's P1 identity. The pass line logs on EVERY tick, empty
	// queue included: a silent job cannot be told from a dead one.
	go func() {
		ticker := time.NewTicker(service.ClosedFollowUpReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// actorctx.WithActor for the same reason as the recurring
				// scheduler above: claimAndCreateRoot may reach taskSvc.Create,
				// whose task.created activity row reads the actor from this
				// ctx. Reconcile acts as the system, never as a past commenter.
				ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
				ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				delivered, retried, escalated, rerr := commentService.ReconcileClosedFollowUpPending(ctx)
				cancel()
				if rerr != nil {
					log.Printf("[closed-followup-reconcile] ERROR: %v", rerr)
				} else {
					log.Printf("[closed-followup-reconcile] pass: delivered=%d retried=%d escalated=%d",
						delivered, retried, escalated)
				}
			case <-schedulerShutdownCh:
				return
			}
		}
	}()
	log.Printf("Closed-card follow-up reconciler started (%s interval)", service.ClosedFollowUpReconcileInterval)
}
