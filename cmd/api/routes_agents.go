package main

import (
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// registerAgentRoutes mounts agent routes.
func registerAgentRoutes(deps *apiDeps) {
	db := deps.db
	workspaceMemberRepo := deps.workspaceMemberRepo
	agentKeyHandler := deps.agentKeyHandler
	agentHandler := deps.agentHandler
	agentControllerHandler := deps.agentControllerHandler
	agentRepositoryAccessHandler := deps.agentRepositoryAccessHandler
	userRuntimeHandler := deps.userRuntimeHandler
	agentWorkspaceGrantHandler := deps.agentWorkspaceGrantHandler
	api := deps.api
	rbac := deps.rbac
	connectorSelfOrRBAC := deps.connectorSelfOrRBAC
	idempotent := deps.idempotent

	// Agent routes.
	// NOTE: /agents/me/* routes MUST be registered before /agents/:agent_id to avoid
	// "me" being parsed as a UUID parameter.
	api.GET("/workspaces/:ws_id/agents", agentHandler.List)
	api.POST("/workspaces/:ws_id/agents", agentHandler.Register, rbac(mw.PermRegisterAgent))
	api.GET("/agents/me", agentHandler.Me)
	api.PATCH("/agents/me", agentHandler.UpdateMe)
	api.GET("/agents/me/tasks", agentHandler.GetMyTasks)
	api.GET("/agents/me/events/stream", agentHandler.EventStream)
	api.GET("/agents/me/tasks/poll", agentHandler.PollTasks)
	api.POST("/agents/me/sessions/report", agentHandler.ReportSession, idempotent)
	api.POST("/agents/me/controller", agentControllerHandler.Announce)
	// GET /agents/me/workspaces — the calling agent's OWN connections, self
	// only, no permission check (mirrors the rest of /agents/me/*). Must
	// stay registered ahead of /agents/:agent_id/workspaces below for the
	// same "me" != UUID reason as every other /agents/me/* route (task
	// an internal ticket — this route was missing entirely; a guest agent's own key
	// got a 403 asking where it's connected).
	api.GET("/agents/me/workspaces", agentWorkspaceGrantHandler.ListMyWorkspaces)
	// GET /agents/me/decision-requests — the open questions other agents asked
	// the caller (ask_agent), in the workspace its key authenticated into; the
	// runner answers each with answer_decision on the asking card.
	api.GET("/agents/me/decision-requests",
		handler.NewDecisionRequestInboxHandler(postgres.NewDecisionRequestRepo(db)).ListMine)
	// Operator-level runtime ("My Runner"): the agent reads its settings by key; an
	// agent holding the runtime_manager assignment edits its operator's document.
	api.GET("/agents/me/runtime", userRuntimeHandler.AgentRuntime)
	api.GET("/agents/me/operator-runtime", userRuntimeHandler.GetAsManager)
	api.GET("/agents/me/operator-runtime/history", userRuntimeHandler.HistoryAsManager)
	api.GET("/agents/me/operator-runtime/agents", userRuntimeHandler.OperatorAgentsAsManager)
	api.GET("/agents/me/operator-runtime/runners", userRuntimeHandler.OperatorRunnersAsManager)
	api.PUT("/agents/me/operator-runtime", userRuntimeHandler.SaveAsManager)
	api.PUT("/agents/me/operator-runtime/agents/:agent_id/limits", userRuntimeHandler.SetLimitsAsManager)
	api.POST("/agents/heartbeat", agentHandler.Heartbeat)
	api.GET("/agents/:agent_id", agentHandler.GetByID)
	api.PATCH("/agents/:agent_id", agentHandler.Update, rbac(mw.PermDeleteAgent))
	// The only writer of repository_access. No route-level
	// rbac: the repository authorizes in the write transaction (operator,
	// workspace owner/admin, or another agent's live runtime_manager).
	api.PUT("/agents/:agent_id/repository-access", agentRepositoryAccessHandler.Put)
	// Audit of those writes for the agent editor, newest first; same readers as GET /agents/:agent_id.
	api.GET("/agents/:agent_id/repository-access/history", agentRepositoryAccessHandler.History)
	api.DELETE("/agents/:agent_id", agentHandler.Delete, rbac(mw.PermDeleteAgent))
	api.POST("/agents/:agent_id/regenerate-key", agentHandler.RegenerateKey, rbac(mw.PermDeleteAgent))
	api.GET("/agents/:agent_id/keys", agentKeyHandler.List, rbac(mw.PermDeleteAgent))
	api.POST("/agents/:agent_id/keys", agentKeyHandler.Issue, rbac(mw.PermDeleteAgent))
	api.DELETE("/agents/:agent_id/keys/:key_id", agentKeyHandler.Revoke, rbac(mw.PermDeleteAgent))
	api.GET("/agents/:agent_id/sub-agents", agentHandler.ListSubAgents)
	api.GET("/agents/:agent_id/heartbeat", agentHandler.GetAgentHeartbeat)
	api.GET("/agents/:agent_id/activity", agentHandler.ListAgentActivity)
	api.POST("/agents/:agent_id/activity", agentHandler.CreateAgentActivity, connectorSelfOrRBAC("agent_id", mw.PermDeleteAgent))
	api.GET("/workspaces/:ws_id/agents/status", agentHandler.GetAgentsStatus)
	// GET /agents/:agent_id/workspaces (task U3) — where this agent is
	// connected, home workspace not included (that's GET /agents/:agent_id).
	// :agent_id resolves to the TARGET agent's home workspace
	// (workspaceParamResolvers, agents.workspace_id) regardless of which
	// workspace the caller is currently authenticated into — so
	// RequireSelfOrPermission's fallback checks manage_members THERE, same
	// self-or-manage-members bar as PUT /agents/:agent_id/profile above.
	api.GET("/agents/:agent_id/workspaces", agentWorkspaceGrantHandler.ListAgentWorkspaces, mw.RequireSelfOrPermission("agent_id", mw.PermManageMembers, workspaceMemberRepo))
}
