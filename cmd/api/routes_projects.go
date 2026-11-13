package main

import (
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// registerProjectRoutes mounts project routes and builds the workspace,
// project and body-scoped guards, then project members, statuses and
// custom fields.
func registerProjectRoutes(deps *apiDeps) {
	db := deps.db
	projectHandler := deps.projectHandler
	statusHandler := deps.statusHandler
	customFieldHandler := deps.customFieldHandler
	chatAgentPickerHandler := deps.chatAgentPickerHandler
	chatAttachmentHandler := deps.chatAttachmentHandler
	chatThreadDeleteHandler := deps.chatThreadDeleteHandler
	agentChatHandler := deps.agentChatHandler
	chatAuditHandler := deps.chatAuditHandler
	chatRunnerHandler := deps.chatRunnerHandler
	existingSessionHandler := deps.existingSessionHandler
	hostLocalQAHandler := deps.hostLocalQAHandler
	chatClaimHandler := deps.chatClaimHandler
	chatRunnerEventsHandler := deps.chatRunnerEventsHandler
	chatTurnStartHandler := deps.chatTurnStartHandler
	projectMemberHandler := deps.projectMemberHandler
	api := deps.api
	rbac := deps.rbac
	connectorRBAC := deps.connectorRBAC

	// Project routes.
	api.GET("/workspaces/:ws_id/projects", projectHandler.List)
	api.POST("/workspaces/:ws_id/projects", projectHandler.Create, rbac(mw.PermCreateProject))

	// Workspace membership guard — enforces the caller is a member of the requested workspace.
	wsAccess := mw.RequireWorkspaceMember(db)
	// Idempotency-Key on retried creates (docs/idempotency.md); mounted last on a
	// route so the scope sees the authenticated actor.
	idempotencyRepo := postgres.NewIdempotencyRepo(db)
	idempotent := mw.Idempotency(idempotencyRepo, 16<<20)

	// Project-scoped routes — RequireProjectMember enforces membership for :proj_id routes.
	projAccess := mw.RequireProjectMember(db)

	// Body-scoped guard — for the routes whose tenant is named in the request body
	// rather than the path, where RequireWorkspaceMemberScoped has nothing to see.
	bodyWS := mw.RequireBodyWorkspace(db)
	api.GET("/projects/:proj_id", projectHandler.GetByID, projAccess)
	api.PATCH("/projects/:proj_id", projectHandler.Update, projAccess, connectorRBAC(mw.PermManageProject))
	api.DELETE("/projects/:proj_id", projectHandler.Delete, projAccess, rbac(mw.PermDeleteProject))
	// Archive/unarchive carry the same permission DELETE did while it was
	// (wrongly) an archive — owners/admins, never plain members.
	api.POST("/projects/:proj_id/archive", projectHandler.Archive, projAccess, rbac(mw.PermDeleteProject))
	api.POST("/projects/:proj_id/unarchive", projectHandler.Unarchive, projAccess, rbac(mw.PermDeleteProject))

	// Project member routes.
	api.GET("/projects/:proj_id/members", projectMemberHandler.List, projAccess)
	api.GET("/projects/:proj_id/chat/agents", chatAgentPickerHandler.List, projAccess)
	api.POST("/projects/:proj_id/chat/attachments", chatAttachmentHandler.Presign, projAccess)
	api.GET("/projects/:proj_id/chat/attachments/:attachment_id/download", chatAttachmentHandler.Download, projAccess)
	api.DELETE("/projects/:proj_id/chat/threads/:thread_id", chatThreadDeleteHandler.Delete, projAccess, mw.RequireUserAuth())
	// Chat with Agent (agent-chat): threads are private to their author; the
	// handler resolves every thread by the caller's user id, so there is no
	// workspace-role bypass here beyond what the gate itself decides.
	api.GET("/projects/:proj_id/chat/threads", agentChatHandler.ListThreads, projAccess)
	api.POST("/projects/:proj_id/chat/threads", agentChatHandler.CreateThread, projAccess)
	api.GET("/projects/:proj_id/chat/threads/:thread_id", agentChatHandler.GetThread, projAccess)
	api.PATCH("/projects/:proj_id/chat/threads/:thread_id", agentChatHandler.RenameThread, projAccess)
	api.GET("/projects/:proj_id/chat/threads/:thread_id/messages", agentChatHandler.ListMessages, projAccess)
	api.POST("/projects/:proj_id/chat/threads/:thread_id/messages", agentChatHandler.SendMessage, projAccess)
	api.POST("/projects/:proj_id/chat/threads/:thread_id/stop", agentChatHandler.StopTurn, projAccess)
	// Read-only runner runtime (runner-chat-protocol §12): the latest chat-session
	// snapshot a runner sent with its controller announce.
	api.GET("/projects/:proj_id/chat/threads/:thread_id/runtime", agentChatHandler.ThreadRuntime, projAccess)
	api.GET("/projects/:proj_id/chat/runners/:runner_id/sessions", agentChatHandler.RunnerSessions, projAccess)
	api.GET("/projects/:proj_id/chat/settings", agentChatHandler.GetProjectSettings, projAccess, mw.RequireUserAuth())
	api.PUT("/projects/:proj_id/chat/settings", agentChatHandler.PutProjectSettings, projAccess, mw.RequireUserAuth(), rbac(mw.PermManageWebhooks))
	api.GET("/workspaces/:ws_id/chat/settings", agentChatHandler.GetWorkspaceSettings, mw.RequireUserAuth(), rbac(mw.PermManageWebhooks))
	api.PUT("/workspaces/:ws_id/chat/settings", agentChatHandler.PutWorkspaceSettings, mw.RequireUserAuth(), rbac(mw.PermManageWebhooks))
	// Read-only QA audit of chat (E6 setup): no route guard on purpose, the
	// repository admits only a human owner/admin of the workspace in the path
	// (listed in auditReadRoutes, cmd/api/rbac_routes_audit_test.go).
	api.GET("/workspaces/:ws_id/chat/audit/threads/:thread_id", chatAuditHandler.Thread)
	api.GET("/workspaces/:ws_id/chat/audit/threads/:thread_id/events", chatAuditHandler.ThreadEvents)
	api.GET("/workspaces/:ws_id/chat/audit/agents/:agent_id/sessions", chatAuditHandler.AgentSessions)
	api.GET("/workspaces/:ws_id/chat/audit/memory-writes", chatAuditHandler.MemoryWrites)
	// Claim long-poll and lease of the chat runner (agent key, protocol §4).
	api.GET("/runner/chat/claim", chatClaimHandler.Claim)
	api.GET("/runner/chat/turns/active", chatClaimHandler.Active)
	api.POST("/runner/chat/turns/:id/start", chatTurnStartHandler.Start)
	api.POST("/runner/chat/turns/:id/lease", chatClaimHandler.RenewLease)
	api.GET("/runner/chat/turns/:id/attachments/:attachment_id/download-url", chatClaimHandler.AttachmentDownloadURL)
	api.POST("/runner/chat/turns/:id/session-token", chatTurnStartHandler.SessionToken)
	api.POST("/runner/chat/turns/:id/events", chatRunnerEventsHandler.Events)
	// Orders and recovery history for the chat runner (agent key, protocol §8, §9).
	api.POST("/runner/chat/runners", chatRunnerHandler.RegisterRunner)
	api.DELETE("/runner/chat/runners/:runner_id", chatRunnerHandler.RevokeRunner)
	api.GET("/runner/chat/commands", chatRunnerHandler.Commands)
	api.POST("/runner/chat/commands/:command_id/ack", chatRunnerHandler.AckCommand)
	api.GET("/runner/chat/threads/:thread_id/history", chatRunnerHandler.History)
	// Strict existing-session metadata delivery (agent key; receiver routes also need the runner bearer).
	api.POST("/runner/existing-session/receivers", existingSessionHandler.RegisterReceiver)
	api.DELETE("/runner/existing-session/receivers/:receiver_id", existingSessionHandler.RevokeReceiver)
	api.GET("/runner/existing-session/deliveries", existingSessionHandler.ListDeliveries)
	api.POST("/runner/existing-session/deliveries/:delivery_id/ack", existingSessionHandler.AckDelivery)
	api.POST("/existing-session/deliveries", existingSessionHandler.SendDelivery)
	api.GET("/existing-session/deliveries/:delivery_id", existingSessionHandler.GetDelivery)
	// Host-local QA assignment v1: separate capability, routes and admission; default OFF.
	api.POST("/runner/host-local-qa/v1/receivers", hostLocalQAHandler.RegisterReceiver)
	api.DELETE("/runner/host-local-qa/v1/receivers/:receiver_id", hostLocalQAHandler.RevokeReceiver)
	api.GET("/runner/host-local-qa/v1/deliveries", hostLocalQAHandler.ListDeliveries)
	api.POST("/runner/host-local-qa/v1/deliveries/:delivery_id/ack", hostLocalQAHandler.AckDelivery)
	api.POST("/runner/host-local-qa/v1/deliveries/:delivery_id/check", hostLocalQAHandler.CheckDelivery)
	api.POST("/host-local-qa/v1/deliveries", hostLocalQAHandler.SendDelivery)
	api.GET("/host-local-qa/v1/deliveries/:delivery_id", hostLocalQAHandler.GetDelivery)
	api.POST("/projects/:proj_id/members", projectMemberHandler.Add, projAccess, rbac(mw.PermManageMembers))
	api.POST("/projects/:proj_id/members/agents", projectMemberHandler.AddAgent, projAccess, rbac(mw.PermManageMembers))
	api.PATCH("/projects/:proj_id/members/:user_id", projectMemberHandler.UpdateRole, projAccess, rbac(mw.PermManageMembers))
	api.DELETE("/projects/:proj_id/members/:user_id", projectMemberHandler.Remove, projAccess, rbac(mw.PermManageMembers))
	api.DELETE("/projects/:proj_id/members/agents/:member_agent_id", projectMemberHandler.RemoveAgent, projAccess, rbac(mw.PermManageMembers))

	// Task status routes.
	api.GET("/projects/:proj_id/statuses", statusHandler.List, projAccess)
	api.POST("/projects/:proj_id/statuses", statusHandler.Create, projAccess, connectorRBAC(mw.PermManageProject))
	api.PATCH("/projects/:proj_id/statuses/:status_id", statusHandler.Update, projAccess, connectorRBAC(mw.PermManageProject))
	api.PUT("/projects/:proj_id/statuses/reorder", statusHandler.Reorder, projAccess, connectorRBAC(mw.PermManageProject))

	// Custom field routes.
	api.GET("/projects/:proj_id/custom-fields", customFieldHandler.List, projAccess)
	api.POST("/projects/:proj_id/custom-fields", customFieldHandler.Create, projAccess, rbac(mw.PermManageCF))
	api.GET("/custom-fields/:field_id", customFieldHandler.GetByID)
	api.PATCH("/custom-fields/:field_id", customFieldHandler.Update, rbac(mw.PermManageCF))
	api.DELETE("/custom-fields/:field_id", customFieldHandler.Delete, rbac(mw.PermManageCF))
	api.PUT("/projects/:proj_id/custom-fields/reorder", customFieldHandler.Reorder, projAccess, rbac(mw.PermManageCF))

	deps.wsAccess = wsAccess
	deps.idempotencyRepo = idempotencyRepo
	deps.idempotent = idempotent
	deps.projAccess = projAccess
	deps.bodyWS = bodyWS
}
