package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/handler"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// registerTaskRoutes mounts task, dependency and comment routes.
func registerTaskRoutes(deps *apiDeps) {
	db := deps.db
	taskExternalRefRepo := deps.taskExternalRefRepo
	workspaceMemberRepo := deps.workspaceMemberRepo
	vcsLinkRepo := deps.vcsLinkRepo
	runtimeRepo := deps.runtimeRepo
	vcsIntegrationResolver := deps.vcsIntegrationResolver
	taskService := deps.taskService
	commentService := deps.commentService
	taskHandler := deps.taskHandler
	nativeReviewHandler := deps.nativeReviewHandler
	transitionReceiptHandler := deps.transitionReceiptHandler
	statusHandler := deps.statusHandler
	commentHandler := deps.commentHandler
	humanGateDecisionHandler := deps.humanGateDecisionHandler
	depHandler := deps.depHandler
	taskContextHandler := deps.taskContextHandler
	api := deps.api
	rbac := deps.rbac
	connectorRBAC := deps.connectorRBAC
	wsAccess := deps.wsAccess
	idempotent := deps.idempotent
	projAccess := deps.projAccess

	// Task routes.
	api.GET("/projects/:proj_id/tasks", taskHandler.List, projAccess)
	api.POST("/projects/:proj_id/tasks", taskHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.POST("/projects/:proj_id/tasks/bulk-update", taskHandler.BulkUpdate, projAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/by-short-id/:short", taskHandler.GetByShortID)
	api.GET("/tasks/:task_id", taskHandler.GetByID, wsAccess)
	api.PATCH("/tasks/:task_id", taskHandler.Update, wsAccess, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id", taskHandler.Delete, wsAccess, rbac(mw.PermDeleteTask))
	api.POST("/tasks/:task_id/move", taskHandler.MoveTask, wsAccess, rbac(mw.PermUpdateTask), idempotent)
	api.POST("/tasks/:task_id/transitions/product-done", transitionReceiptHandler.ProductDone, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/:task_id/transition-receipts", transitionReceiptHandler.ListReceipts, wsAccess)
	api.POST("/native-review/receipts", nativeReviewHandler.IssueReceipt)
	api.GET("/native-review/receipts/:receipt_id", nativeReviewHandler.GetReceipt)
	api.POST("/native-review/authorities", nativeReviewHandler.CreateAuthority)
	api.POST("/native-review/authorities/:authority_id/revoke", nativeReviewHandler.RevokeAuthority)
	api.GET("/native-review/authorities/:authority_id/reservation", nativeReviewHandler.GetReservation)
	api.POST("/native-review/deliveries", nativeReviewHandler.RecordDelivery)
	api.POST("/native-review/reservations", nativeReviewHandler.Reserve)
	api.POST("/tasks/:task_id/acceptance-intents", transitionReceiptHandler.RecordAcceptance, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/:task_id/acceptance-intents", transitionReceiptHandler.ListAcceptances, wsAccess)
	api.POST("/tasks/:task_id/acceptance-intents/:intent_id/revoke", transitionReceiptHandler.RevokeAcceptance, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/:task_id/product-done-acceptance", transitionReceiptHandler.ProductDoneAcceptance, wsAccess)
	api.GET("/projects/:proj_id/product-done-capabilities", transitionReceiptHandler.Capabilities, projAccess)
	api.GET("/projects/:proj_id/transition-receipt-policy", transitionReceiptHandler.GetPolicy, projAccess)
	api.PUT("/projects/:proj_id/transition-receipt-policy", transitionReceiptHandler.SetPolicy, projAccess, rbac(mw.PermManageProject))
	api.POST("/tasks/:task_id/parked-waits", taskHandler.RegisterParkedWait, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/parked-waits/release", taskHandler.ReleaseParkedWait, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/start-after/compare-clear", taskHandler.CompareClearStartAfter, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/move-to-project", taskHandler.MoveToProject, wsAccess, rbac(mw.PermUpdateTask))
	// Workspace comes from the caller's auth; the service refuses any task or
	// destination project outside it (source and destination are both checked).
	api.POST("/tasks/move-to-project-batch", taskHandler.MoveToProjectBatch, wsAccess, rbac(mw.PermUpdateTask))
	// Statuses of the task's project, under the SAME gate as /tasks/:task_id/move.
	// Resolving a status slug is a precondition of the move; gating the lookup more
	// strictly than the move itself makes the move unreachable and reports the refusal
	// against the wrong resource. Project-scoped status routes above keep projAccess.
	api.GET("/tasks/:task_id/statuses", statusHandler.ListByTask, wsAccess)
	api.GET("/tasks/:task_id/subtasks", taskHandler.ListSubtasks, wsAccess)
	api.POST("/tasks/:task_id/subtasks", taskHandler.CreateSubtask, wsAccess, rbac(mw.PermCreateTask))
	api.POST("/tasks/:task_id/assign", taskHandler.AssignTask, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/checkout", taskHandler.Checkout, wsAccess, connectorRBAC(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id/checkout", taskHandler.ReleaseCheckout, wsAccess, connectorRBAC(mw.PermUpdateTask), mw.RequireCheckoutRecoveryPermission(workspaceMemberRepo))
	api.PATCH("/tasks/:task_id/checkout", taskHandler.ExtendCheckout, wsAccess, connectorRBAC(mw.PermUpdateTask))
	api.PATCH("/tasks/:task_id/ship", taskHandler.ShipTask, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/tasks/:task_id/context", taskContextHandler.GetTaskContext, wsAccess)
	api.GET("/tasks/:task_id/cost-summary", taskHandler.GetCostSummary, wsAccess)
	api.PATCH("/tasks/:task_id/dod-check", taskHandler.SetDodCheck, wsAccess, rbac(mw.PermUpdateTask))
	// The single arming path for "this card is waiting on a human".
	// Arm records WHO asked (from the authenticated identity, not the body), WHAT was
	// asked, WHAT happens by default and BY WHEN, so a client's is_human_gated collapses
	// to reading task.human_gate instead of re-deriving it from comment text.
	// Clear enforces the same user-only rule as PATCH {human_gate:false}, inside the
	// handler (mirroring the human-gate-decisions revoke route above).
	api.POST("/tasks/:task_id/human-gate", taskHandler.ArmHumanGate, wsAccess, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id/human-gate", taskHandler.ClearHumanGate, wsAccess, rbac(mw.PermUpdateTask))
	// Operational state of a card (working/waiting/queued/stopped), orthogonal to its status.
	api.PUT("/tasks/:task_id/runtime-state", taskHandler.PutRuntimeState, wsAccess, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id/runtime-state", taskHandler.DeleteRuntimeState, wsAccess, rbac(mw.PermUpdateTask))
	// Binding of a card to an object in an external system; external_hash is write-once.
	taskExternalRefHandler := handler.NewTaskExternalRefHandler(service.NewTaskExternalRefService(taskExternalRefRepo), taskService)
	api.GET("/tasks/:task_id/external-ref", taskExternalRefHandler.Get, wsAccess)
	api.PUT("/tasks/:task_id/external-ref", taskExternalRefHandler.Put, wsAccess, rbac(mw.PermUpdateTask))
	// Runner reads of typed turn outcomes: latest outcomes of a session, the active wait.
	typedOutcomeHandler := handler.NewTypedOutcomeHandler(
		service.NewTypedOutcomeService(postgres.NewTaskWaitRepo(db), postgres.NewTurnOutcomeRepo(db),
			service.WithControllerCheck(func(ctx context.Context, ws uuid.UUID, by service.WaitResolver) (bool, error) {
				return runtimeRepo.IsController(ctx, ws, postgres.RuntimeActor{
					AgentID: by.AgentID, AuthWorkspaceID: by.AuthWorkspaceID, Connector: by.Connector})
			})), taskService)
	api.GET("/tasks/:task_id/turn-outcomes", typedOutcomeHandler.ListTurnOutcomes, wsAccess)
	api.GET("/tasks/:task_id/wait", typedOutcomeHandler.GetActiveWait, wsAccess)
	// The runner (the wait's own agent, or the workspace's controller) marks a
	// wait ended by the event it observed.
	api.POST("/tasks/:task_id/waits/:wait_id/resolve", typedOutcomeHandler.ResolveWait, wsAccess, rbac(mw.PermUpdateTask))
	// Turn-ending commands: ask an agent, answer it, finish a card with a typed result.
	if cmds, ok := taskService.(service.TypedOutcomeCommands); ok {
		typedCommandHandler := handler.NewTypedOutcomeCommandHandler(cmds, taskService)
		api.POST("/tasks/:task_id/decision-requests", typedCommandHandler.AskAgent, wsAccess, rbac(mw.PermUpdateTask))
		api.POST("/tasks/:task_id/finish", typedCommandHandler.FinishTask, wsAccess, rbac(mw.PermUpdateTask), idempotent)
		api.POST("/decision-requests/:decision_request_id/answer", typedCommandHandler.AnswerDecision, wsAccess, rbac(mw.PermAddComment))
	}
	// Turn-ending commands: each validates its target and writes the wait/outcome,
	// the comment and the checkout change in one transaction.
	turnCommandHandler := handler.NewTurnCommandHandler(service.NewTurnCommandService(
		postgres.NewTurnCommandRepo(db), vcsLinkRepo, service.TurnHooksFor(taskService),
		func(ctx context.Context, ws uuid.UUID) (gitlabapi.MergeRequestChecker, bool) {
			cfg, _, ok := vcsIntegrationResolver.ResolveGitLab(ctx, ws)
			if !ok || cfg.BaseURL == "" || cfg.Token == "" {
				return nil, false
			}
			return gitlabapi.NewClient(cfg.BaseURL, cfg.Token), true
		}).WithGitHub(func(ctx context.Context, ws uuid.UUID) (githubapi.PullRequestChecker, bool) {
		cfg, _, ok := vcsIntegrationResolver.ResolveGitHub(ctx, ws)
		if !ok || cfg.Token == "" {
			return nil, false
		}
		return githubapi.NewClient(cfg.Token), true
	}).WithHumanGate(commentService, taskService), taskService)
	api.POST("/tasks/:task_id/wait", turnCommandHandler.WaitFor, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/next", turnCommandHandler.ContinueNext, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/handoff", turnCommandHandler.HandOff, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/notes", turnCommandHandler.AddNote, wsAccess, rbac(mw.PermAddComment))
	api.POST("/tasks/:task_id/ask-human", turnCommandHandler.AskHuman, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/workspaces/:ws_id/tasks", taskHandler.SearchGlobal, wsAccess)

	// Dependency routes.
	api.GET("/tasks/:task_id/dependencies", depHandler.List, wsAccess)
	api.GET("/tasks/:task_id/next-action", depHandler.NextAction, wsAccess)
	api.POST("/tasks/:task_id/dependencies/:dep_id/binding-proposals", depHandler.ProposeBinding, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/dependencies/:dep_id/binding-proposals/:proposal_id/confirm", depHandler.ConfirmBinding, wsAccess, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/dependencies", depHandler.Create, wsAccess, rbac(mw.PermUpdateTask))
	api.PATCH("/tasks/:task_id/dependencies/:dep_id", depHandler.Update, wsAccess, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id/dependencies/:dep_id", depHandler.Delete, wsAccess, rbac(mw.PermUpdateTask))
	api.GET("/projects/:proj_id/dependency-graph", depHandler.DependencyGraph, projAccess)
	api.GET("/projects/:proj_id/dependency-findings", depHandler.DependencyFindings, projAccess)

	// Comment routes.
	api.GET("/tasks/:task_id/comments", commentHandler.List, wsAccess)
	api.POST("/tasks/:task_id/comments", commentHandler.Create, wsAccess, rbac(mw.PermAddComment), idempotent)
	api.PATCH("/comments/:comment_id", commentHandler.Update, rbac(mw.PermAddComment))
	api.DELETE("/comments/:comment_id", commentHandler.Delete, rbac(mw.PermAddComment))

	// Third human_gate exit — "decision recorded" (an internal ticket, contract
	// docs/human-gate-decision-recorded.md). Revoke enforces user-only inside
	// the handler itself (mirrors task_handler.go's PATCH human_gate 403), not
	// via rbac, since an agent can legitimately hold PermAddComment.
	api.GET("/tasks/:task_id/human-gate-decisions", humanGateDecisionHandler.List, wsAccess)
	api.POST("/tasks/:task_id/human-gate-decisions", humanGateDecisionHandler.Create, wsAccess, rbac(mw.PermAddComment))
	api.POST("/human-gate-decisions/:decision_id/revoke", humanGateDecisionHandler.Revoke, rbac(mw.PermAddComment))
}
