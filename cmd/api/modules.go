package main

// apiModules builds the API process: main runs each module in this order and
// then serve() starts the HTTP server. Each module lives in its own file and
// registers its own repositories, services, handlers, background jobs and
// routes on the deps it is handed.
//
// The order is the order the old single main() ran in, and it still matters:
// a module reads the deps published by the modules above it, and the route
// groups and their middleware (registerProtectedGroup) exist before any
// module adds routes to them.
//
// A new feature adds its own file with a func(deps *apiDeps) and appends one
// line at the END of this list; it does not edit main.go or another module.
var apiModules = []func(*apiDeps){
	wireStorage,
	wireCoreServices,
	wireDomainServices,
	wireEventBus,
	wireHandlers,
	wireEcho,
	startBackgroundWorkers,
	registerPublicRoutes,
	registerProtectedGroup,
	registerWorkspaceRoutes,
	registerProjectRoutes,
	registerTaskRoutes,
	registerDocumentRoutes,
	registerAgentRoutes,
	registerEventRoutes,
	registerOAuthRoutes,
	registerExtensionHookRoutes,
	registerSecretRoutes,
	registerExtensionRoutes,
	registerTeamRelayRoutes,
	registerPlanningRoutes,
	registerGovernanceRoutes,
	registerInboxRoutes,
	registerMemoryRoutes,
	startRecurringScheduler,
	startMemoryJobs,
	startSweepers,
}
