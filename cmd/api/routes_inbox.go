package main

import (
	"net/http"

	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// registerInboxRoutes mounts the mention inbox, the operator runtime, the
// caller's active tasks and the activity feed.
func registerInboxRoutes(deps *apiDeps) {
	taskHandler := deps.taskHandler
	commentHandler := deps.commentHandler
	userRuntimeHandler := deps.userRuntimeHandler
	mentionHandler := deps.mentionHandler
	documentMentionHandler := deps.documentMentionHandler
	userUIStateHandler := deps.userUIStateHandler
	mentionablesHandler := deps.mentionablesHandler
	api := deps.api
	wsAccess := deps.wsAccess

	// @mention inbox (REST).
	api.GET("/me/mentions", mentionHandler.List)
	api.GET("/me/mentions/unseen_count", mentionHandler.UnseenCount)
	api.POST("/me/mentions/:comment_id/seen", mentionHandler.MarkSeen)
	// The same inbox for @-mentions inside document comments. Separate routes
	// rather than a widened /me/mentions: the rows name a document and no task,
	// and folding them in would put a nullable task_id on a response shape three
	// existing screens read as non-null.
	//
	// :dcom_id is resolved to a workspace by workspaceParamResolvers, so
	// RequireWorkspaceMemberScoped (group-level, above) refuses another tenant's
	// comment id here exactly as it does on /document-comments/:dcom_id.
	// Per-user remembered UI state (Docs tree expansion). Human sessions only —
	// the handler refuses agent keys itself.
	api.GET("/me/ui-state/:scope", userUIStateHandler.Get)
	api.PUT("/me/ui-state/:scope", userUIStateHandler.Put)
	api.GET("/me/document-mentions", documentMentionHandler.List)
	api.GET("/me/document-mentions/unseen_count", documentMentionHandler.UnseenCount)
	api.POST("/me/document-mentions/:dcom_id/seen", documentMentionHandler.MarkSeen)
	api.GET("/workspaces/:ws_id/mentionables", mentionablesHandler.Search)

	// Operator's own runtime document, history, limits and runtime_manager assignments.
	// An agent key gets 403 naming /agents/me/operator-runtime instead of a bare 401.
	agentToOperatorRuntime := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if _, connector := mw.GetOAuthConnectorUserID(c); mw.IsAgent(c) && !connector {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("operator runtime requires a signed-in user; an agent with Can manage My Runner uses /agents/me/operator-runtime"))
			}
			return next(c)
		}
	}
	api.GET("/me/runtime", userRuntimeHandler.Get, agentToOperatorRuntime, mw.RequireUserAuth())
	api.PUT("/me/runtime", userRuntimeHandler.Save, agentToOperatorRuntime, mw.RequireUserAuth())
	api.GET("/me/runtime/history", userRuntimeHandler.History, agentToOperatorRuntime, mw.RequireUserAuth())
	// No RequireUserAuth here: it answers 401 to agent keys, while these two must
	// answer 403 to them (UserRuntimeRepo.operatorOnly refuses agents and OAuth
	// connectors); unauthenticated requests still get 401 from DualAuth.
	api.GET("/me/runtime/agents", userRuntimeHandler.OperatorAgents)
	api.GET("/me/runtime/runners", userRuntimeHandler.OperatorRunners)
	api.PUT("/me/runtime/agents/:agent_id/limits", userRuntimeHandler.SetLimits, agentToOperatorRuntime, mw.RequireUserAuth())
	api.GET("/me/runtime/managers", userRuntimeHandler.Managers, agentToOperatorRuntime, mw.RequireUserAuth())
	api.POST("/me/runtime/managers", userRuntimeHandler.GrantManager, agentToOperatorRuntime, mw.RequireUserAuth())
	api.POST("/me/runtime/managers/revoke", userRuntimeHandler.RevokeManager, agentToOperatorRuntime, mw.RequireUserAuth())

	// Current user's active tasks (excludes done/cancelled).
	api.GET("/me/tasks", taskHandler.GetCurrentUserTasks)

	// Activity feed — my comments + workspace-wide recent comments.
	api.GET("/me/comments", commentHandler.GetMyComments)
	api.GET("/workspaces/:ws_id/comments/recent", commentHandler.GetRecentByWorkspace, wsAccess)
}
