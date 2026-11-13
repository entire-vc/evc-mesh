package main

import (
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerPlanningRoutes mounts analytics, project updates, initiatives,
// triage, recurring schedules, templates, team directory, assignment and
// workflow rules and config import/export.
func registerPlanningRoutes(deps *apiDeps) {
	workspaceMemberRepo := deps.workspaceMemberRepo
	analyticsHandler := deps.analyticsHandler
	projectUpdateHandler := deps.projectUpdateHandler
	initiativeHandler := deps.initiativeHandler
	triageHandler := deps.triageHandler
	rulesHandler := deps.rulesHandler
	recurringHandler := deps.recurringHandler
	taskTemplateHandler := deps.taskTemplateHandler
	api := deps.api
	rbac := deps.rbac
	connectorRBAC := deps.connectorRBAC
	projAccess := deps.projAccess

	// Analytics routes.
	api.GET("/workspaces/:ws_id/analytics", analyticsHandler.GetMetrics)
	api.GET("/workspaces/:ws_id/analytics/export", analyticsHandler.ExportMetrics)

	// Project update routes.
	api.POST("/projects/:proj_id/updates", projectUpdateHandler.Create, projAccess, connectorRBAC(mw.PermAddComment))
	api.GET("/projects/:proj_id/updates", projectUpdateHandler.List, projAccess)
	api.GET("/projects/:proj_id/updates/latest", projectUpdateHandler.GetLatest, projAccess)

	// Initiative routes.
	api.POST("/workspaces/:ws_id/initiatives", initiativeHandler.Create, rbac(mw.PermCreateProject))
	api.GET("/workspaces/:ws_id/initiatives", initiativeHandler.List)
	api.GET("/initiatives/:init_id", initiativeHandler.GetByID)
	api.PATCH("/initiatives/:init_id", initiativeHandler.Update, rbac(mw.PermCreateProject))
	api.DELETE("/initiatives/:init_id", initiativeHandler.Delete, rbac(mw.PermDeleteProject))
	api.POST("/initiatives/:init_id/projects", initiativeHandler.LinkProject, rbac(mw.PermCreateProject))
	api.DELETE("/initiatives/:init_id/projects/:proj_id", initiativeHandler.UnlinkProject, rbac(mw.PermCreateProject))

	// Triage inbox route.
	api.GET("/workspaces/:ws_id/triage", triageHandler.List)

	// Recurring task schedule routes.
	//
	// The parameter is spelled :recurring_id rather than :id so WorkspaceRLS can
	// resolve the schedule's workspace from it and the membership guard applies.
	// The URLs are unchanged. :id is also what /memories/:id is spelled with, and
	// one central resolver cannot serve two different tables.
	api.POST("/projects/:proj_id/recurring", recurringHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.GET("/projects/:proj_id/recurring", recurringHandler.List, projAccess)
	api.GET("/recurring/:recurring_id", recurringHandler.GetByID)
	api.PATCH("/recurring/:recurring_id", recurringHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/recurring/:recurring_id", recurringHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/recurring/:recurring_id/trigger", recurringHandler.Trigger, rbac(mw.PermCreateTask))
	api.GET("/recurring/:recurring_id/history", recurringHandler.History)

	// Task template routes.
	api.POST("/projects/:proj_id/templates", taskTemplateHandler.Create, projAccess, rbac(mw.PermCreateTask))
	api.GET("/projects/:proj_id/templates", taskTemplateHandler.List, projAccess)
	api.GET("/templates/:tmpl_id", taskTemplateHandler.GetByID)
	api.PATCH("/templates/:tmpl_id", taskTemplateHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/templates/:tmpl_id", taskTemplateHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/templates/:tmpl_id/create-task", taskTemplateHandler.CreateTask, rbac(mw.PermCreateTask))

	// Team Directory routes (Sprint 20).
	api.GET("/workspaces/:ws_id/team", rulesHandler.GetTeamDirectory)
	// Self-service (an agent updating its own profile via X-Agent-Key) is always
	// allowed; rewriting another agent's profile requires PermDeleteAgent, same
	// bar as PATCH /agents/:agent_id above.
	api.PUT("/agents/:agent_id/profile", rulesHandler.UpdateAgentProfile, mw.RequireSelfOrPermission("agent_id", mw.PermDeleteAgent, workspaceMemberRepo))

	// Assignment Rules routes (Sprint 20).
	api.GET("/workspaces/:ws_id/rules/assignment", rulesHandler.GetWorkspaceAssignmentRules)
	api.PUT("/workspaces/:ws_id/rules/assignment", rulesHandler.SetWorkspaceAssignmentRules, rbac(mw.PermManageMembers))
	api.GET("/projects/:proj_id/rules/assignment", rulesHandler.GetEffectiveAssignmentRules, projAccess)
	api.PUT("/projects/:proj_id/rules/assignment", rulesHandler.SetProjectAssignmentRules, projAccess, rbac(mw.PermManageMembers))

	// Workflow Rules routes (Sprint 20).
	api.GET("/projects/:proj_id/rules/workflow", rulesHandler.GetProjectWorkflowRules, projAccess)
	api.PUT("/projects/:proj_id/rules/workflow", rulesHandler.SetProjectWorkflowRules, projAccess, rbac(mw.PermManageMembers))
	api.POST("/projects/:proj_id/rules/handoff/decide", rulesHandler.DecideHandoff, projAccess)

	// Violation Log routes (Sprint 20).
	api.GET("/workspaces/:ws_id/violations", rulesHandler.ListViolations)

	// Config Import/Export routes (Sprint 21).
	api.POST("/workspaces/:ws_id/config/import", rulesHandler.ImportConfig, rbac(mw.PermManageMembers))
	api.GET("/workspaces/:ws_id/config/export", rulesHandler.ExportConfig)
	api.POST("/workspaces/:ws_id/team/import", rulesHandler.ImportTeam, rbac(mw.PermManageMembers))

	// Workflow Templates routes (Sprint 21).
	api.GET("/workspaces/:ws_id/rules/workflow-templates", rulesHandler.GetWorkflowTemplates)
	api.PUT("/workspaces/:ws_id/rules/workflow-templates", rulesHandler.SetWorkflowTemplates, rbac(mw.PermManageMembers))
}
