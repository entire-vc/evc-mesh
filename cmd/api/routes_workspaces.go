package main

import (
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerWorkspaceRoutes mounts workspace, member, agent-grant and invite routes.
func registerWorkspaceRoutes(deps *apiDeps) {
	workspaceMemberRepo := deps.workspaceMemberRepo
	workspaceExportHandler := deps.workspaceExportHandler
	workspaceHandler := deps.workspaceHandler
	runtimeHandler := deps.runtimeHandler
	workspaceMemberHandler := deps.workspaceMemberHandler
	agentWorkspaceGrantHandler := deps.agentWorkspaceGrantHandler
	inviteHandler := deps.inviteHandler
	themeHandler := deps.themeHandler
	api := deps.api
	rbac := deps.rbac
	runtimeCatalogRBAC := deps.runtimeCatalogRBAC

	// Workspace routes.
	api.GET("/workspaces", workspaceHandler.List)
	api.POST("/workspaces", workspaceHandler.Create)
	api.GET("/workspaces/:ws_id", workspaceHandler.GetByID)
	// Update carries the workspace slug, and changing a slug rewrites every
	// /w/<slug>/... URL the team has bookmarked or pasted into a doc. Same bar as
	// DELETE and icon upload: owner/admin, not any member who happens to be in.
	api.PATCH("/workspaces/:ws_id", workspaceHandler.Update, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id", workspaceHandler.Delete, rbac(mw.PermDeleteWorkspace))
	// GET /workspaces/:ws_id/icon is registered on the public group above —
	// browsers fetch it as a plain <img>/<link rel=icon> subresource.
	api.PUT("/workspaces/:ws_id/icon", workspaceHandler.UploadIcon, rbac(mw.PermManageMembers))
	// Theme choice of a workspace: the same bar as the icon and the slug.
	api.GET("/workspaces/:ws_id/theme", themeHandler.GetWorkspace, rbac(mw.PermManageMembers))
	api.PUT("/workspaces/:ws_id/theme", themeHandler.PutWorkspace, rbac(mw.PermManageMembers))
	// Installed themes with their settings schemas, for the picker form.
	api.GET("/themes", themeHandler.List)
	// Instance theme and installing themes: instance administrator only (a theme's
	// views are code that runs in every member's browser, ADR D11).
	api.GET("/instance/theme", themeHandler.GetInstance, mw.RequireUserAuth())
	api.PUT("/instance/theme", themeHandler.PutInstance, mw.RequireUserAuth())
	api.POST("/instance/themes", themeHandler.Install, mw.RequireUserAuth())
	api.DELETE("/instance/themes/:id", themeHandler.Delete, mw.RequireUserAuth())

	// Workspace member routes.
	// NOTE: /members/me MUST be registered before /members/:user_id to avoid "me" being parsed as UUID.
	api.GET("/workspaces/:ws_id/members", workspaceMemberHandler.List)
	// Full hierarchy for an extension's periodic reconciliation. Humans need the
	// member-management right (it lists agent key metadata); an extension is
	// authorized by the capability guard plus the handler's four-capability check.
	api.GET("/workspaces/:ws_id/export/hierarchy", workspaceExportHandler.Hierarchy, rbac(mw.PermManageMembers))
	api.GET("/workspaces/:ws_id/members/me", workspaceMemberHandler.Me)
	api.POST("/workspaces/:ws_id/members", workspaceMemberHandler.Add, rbac(mw.PermManageMembers))
	api.PATCH("/workspaces/:ws_id/members/:user_id", workspaceMemberHandler.UpdateRole, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id/members/:user_id", workspaceMemberHandler.Remove, rbac(mw.PermManageMembers))
	// A person edits their own team profile; someone else's needs manage-members.
	api.PUT("/workspaces/:ws_id/members/:user_id/profile", workspaceMemberHandler.UpdateProfile, mw.RequireSelfUserOrPermission("user_id", mw.PermManageMembers, workspaceMemberRepo))
	// SearchUsers cannot be narrowed to the workspace in the path — its entire
	// job is to surface people who are NOT in it yet, which is how one account
	// joins a second workspace instead of becoming a second account.
	//
	// It is bounded by the CALLER instead: an exact address match anywhere on the
	// instance (knowing the address is the credential for inviting somebody),
	// plus loose name matching restricted to people the caller already shares a
	// workspace with — data they can already read off a member list. See
	// UserRepo.SearchAddableUsers.
	//
	// Restricting the route to manage-members, which is all it had before, is not
	// by itself a tenant boundary: creating a workspace is open to every
	// authenticated user and makes them its owner, so anyone could hold that
	// permission and page the whole instance directory, addresses included, with
	// ?q=a. Invite-by-email (POST .../invites) remains the path that needs no
	// directory at all.
	api.GET("/workspaces/:ws_id/users/search", workspaceMemberHandler.SearchUsers, rbac(mw.PermManageMembers))

	// Agent-workspace grant routes (task U3) — connecting an agent to a
	// workspace other than its home one. Same rbac bar as human members:
	// only PermManageMembers can invite/revoke. Listing is open to any
	// workspace member, same as GET .../members above — agentPerms has no
	// PermManageMembers entry (see internal/middleware/rbac.go), so an
	// X-Agent-Key caller 403s on Invite/Revoke unconditionally; nothing extra
	// needed here to keep "the workspace side always initiates" true.
	api.GET("/workspaces/:ws_id/agent-grants", agentWorkspaceGrantHandler.List)
	api.GET("/workspaces/:ws_id/runtime", runtimeHandler.Inventory, runtimeCatalogRBAC)
	api.PUT("/workspaces/:ws_id/runtime", runtimeHandler.Save, runtimeCatalogRBAC)
	api.POST("/workspaces/:ws_id/runtime/restore", runtimeHandler.Restore, runtimeCatalogRBAC)
	api.GET("/workspaces/:ws_id/runtime/revisions", runtimeHandler.Revisions, runtimeCatalogRBAC)
	api.GET("/workspaces/:ws_id/runtime/capacity", runtimeHandler.Capacity)
	api.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/report", runtimeHandler.Report)
	api.GET("/workspaces/:ws_id/runtime/controllers/:controller_ref/desired", runtimeHandler.Desired)
	api.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/quota-observations", runtimeHandler.ReportQuota)
	api.GET("/workspaces/:ws_id/runtime/controllers/:controller_ref/quota-observations", runtimeHandler.QuotaObservations)
	api.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/execution-instances", runtimeHandler.RegisterExecutionInstance)
	api.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/execution-reports", runtimeHandler.ReportExecutions)
	api.GET("/workspaces/:ws_id/agent-executions", runtimeHandler.AgentExecutions)
	api.POST("/workspaces/:ws_id/runtime/artifacts/:provenance_artifact_id/provenance", runtimeHandler.Provenance)
	api.GET("/workspaces/:ws_id/runtime/bindings/:binding_id", runtimeHandler.Binding)
	api.PUT("/workspaces/:ws_id/runtime/bindings/:binding_id/admission", runtimeHandler.Admit, rbac(mw.PermManageMembers))
	// :ws_id is the receiver; the resource owner comes from the query and its admin
	// check runs in the repo transaction, so there is no receiver-side role gate.
	api.PUT("/workspaces/:ws_id/runtime/bindings/:binding_id/policy", runtimeHandler.SetPolicy)
	api.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/preview", runtimeHandler.Preview)
	api.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations", runtimeHandler.AcquireReservation)
	api.GET("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id", runtimeHandler.GetReservation)
	api.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/consume", runtimeHandler.ConsumeReservation)
	api.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/renew", runtimeHandler.RenewReservation)
	api.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/release", runtimeHandler.ReleaseReservation)
	api.POST("/workspaces/:ws_id/agent-grants", agentWorkspaceGrantHandler.Invite, rbac(mw.PermManageMembers))
	api.PATCH("/workspaces/:ws_id/agent-grants/:grant_id", agentWorkspaceGrantHandler.UpdateRole, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id/agent-grants/:grant_id", agentWorkspaceGrantHandler.Revoke, rbac(mw.PermManageMembers))

	// Workspace invite routes (email-link flow).
	api.POST("/workspaces/:ws_id/invites", inviteHandler.Create, rbac(mw.PermManageMembers))
	api.GET("/workspaces/:ws_id/invites", inviteHandler.List, rbac(mw.PermManageMembers))
	api.POST("/workspaces/:ws_id/invites/:invite_id/resend", inviteHandler.Resend, rbac(mw.PermManageMembers))
	api.DELETE("/workspaces/:ws_id/invites/:invite_id", inviteHandler.Revoke, rbac(mw.PermManageMembers))
}
